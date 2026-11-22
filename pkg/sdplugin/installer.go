package sdplugin

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	maxPluginArchiveBytes      = 100 * 1024 * 1024
	maxPluginUncompressedBytes = 256 * 1024 * 1024
	maxPluginArchiveFiles      = 10000
)

// IsUserManagedPlugin reports whether pluginDir resides directly inside DefaultPluginsDir
// and can therefore be safely upgraded or uninstalled by PoundControl.
func (p *Provider) IsUserManagedPlugin(pluginDir string) bool {
	baseDir := strings.TrimSpace(p.opts.DefaultPluginsDir)
	if baseDir == "" || strings.TrimSpace(pluginDir) == "" {
		return false
	}
	return isSubpathOf(baseDir, pluginDir)
}

// InstallArchive validates and installs a local .streamDeckPlugin or .zip archive into DefaultPluginsDir,
// stopping any running instance of the same plugin UUID before swapping atomically and reloading.
func (p *Provider) InstallArchive(archivePath string) (*LoadedBundle, error) {
	baseDir := strings.TrimSpace(p.opts.DefaultPluginsDir)
	if baseDir == "" {
		return nil, errors.New("default plugins directory is not configured")
	}

	p.reloadMu.Lock()
	defer p.reloadMu.Unlock()

	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		return nil, fmt.Errorf("create plugins directory: %w", err)
	}

	stageRoot, err := os.MkdirTemp(baseDir, ".stage-*")
	if err != nil {
		return nil, fmt.Errorf("create staging directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(stageRoot) }()

	if err := extractZipSafely(archivePath, stageRoot); err != nil {
		return nil, err
	}

	bundleRoot, err := locateExtractedBundleRoot(stageRoot)
	if err != nil {
		return nil, err
	}

	loaded, err := LoadBundle(bundleRoot)
	if err != nil {
		return nil, fmt.Errorf("validate extracted plugin bundle: %w", err)
	}

	targetName := sanitizeBundleFolderName(loaded.UUID) + ".sdPlugin"
	targetDir := filepath.Join(baseDir, targetName)
	if !isSubpathOf(baseDir, targetDir) {
		return nil, fmt.Errorf("invalid plugin target directory for UUID %q", loaded.UUID)
	}

	// Stop running plugins cleanly so Windows executable handles are released before swap.
	p.stopAllPlugins()

	if err := swapDirectoryAtomically(bundleRoot, targetDir, baseDir); err != nil {
		_ = p.reloadLocked()
		return nil, err
	}

	if err := p.reloadLocked(); err != nil {
		return nil, err
	}

	finalLoaded, err := LoadBundle(targetDir)
	if err != nil {
		return loaded, nil
	}
	return finalLoaded, nil
}

// InstallFromCatalogue downloads, optionally SHA-256 verifies, and installs a plugin from the OpenAction Marketplace.
func (p *Provider) InstallFromCatalogue(
	ctx context.Context,
	client *CatalogueClient,
	entry CatalogueEntry,
	onProgress func(downloaded, total int64, stage string),
) (*LoadedBundle, error) {
	baseDir := strings.TrimSpace(p.opts.DefaultPluginsDir)
	if baseDir == "" {
		return nil, errors.New("default plugins directory is not configured")
	}
	if client == nil {
		client = NewCatalogueClient()
	}

	if onProgress != nil {
		onProgress(0, 0, "Resolving download URL...")
	}
	dlURL, expectedSHA, err := client.ResolveDownload(ctx, entry)
	if err != nil {
		return nil, err
	}

	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		return nil, fmt.Errorf("create plugins directory: %w", err)
	}
	tmpFile, err := os.CreateTemp(baseDir, ".download-*.streamDeckPlugin")
	if err != nil {
		return nil, fmt.Errorf("create temporary download file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, dlURL, nil)
	if err != nil {
		_ = tmpFile.Close()
		return nil, fmt.Errorf("create plugin download request: %w", err)
	}
	req.Header.Set("User-Agent", "PoundControl-OpenAction-Client")

	resp, err := client.httpClient().Do(req)
	if err != nil {
		_ = tmpFile.Close()
		return nil, fmt.Errorf("download plugin %q: %w", entry.Name, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		_ = tmpFile.Close()
		return nil, fmt.Errorf("download plugin %q returned HTTP %d", entry.Name, resp.StatusCode)
	}

	hasher := sha256.New()
	total := resp.ContentLength
	var downloaded int64
	buf := make([]byte, 32*1024)
	limited := io.LimitReader(resp.Body, maxPluginArchiveBytes+1)
	for {
		n, readErr := limited.Read(buf)
		if n > 0 {
			downloaded += int64(n)
			if downloaded > maxPluginArchiveBytes {
				_ = tmpFile.Close()
				return nil, fmt.Errorf("plugin archive exceeds %d MiB limit", maxPluginArchiveBytes/(1024*1024))
			}
			if _, wErr := tmpFile.Write(buf[:n]); wErr != nil {
				_ = tmpFile.Close()
				return nil, fmt.Errorf("write downloaded plugin archive: %w", wErr)
			}
			_, _ = hasher.Write(buf[:n])
			if onProgress != nil {
				onProgress(downloaded, total, "Downloading...")
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			_ = tmpFile.Close()
			return nil, fmt.Errorf("read downloaded plugin archive: %w", readErr)
		}
	}
	if err := tmpFile.Close(); err != nil {
		return nil, fmt.Errorf("close downloaded plugin archive: %w", err)
	}

	if expectedSHA != "" {
		if onProgress != nil {
			onProgress(downloaded, total, "Verifying SHA-256...")
		}
		actualSHA := hex.EncodeToString(hasher.Sum(nil))
		if !strings.EqualFold(actualSHA, expectedSHA) {
			return nil, fmt.Errorf("SHA-256 mismatch for %q: got %s, want %s", entry.ID, actualSHA, expectedSHA)
		}
	}

	if onProgress != nil {
		onProgress(downloaded, total, "Installing plugin...")
	}
	return p.InstallArchive(tmpPath)
}

// UninstallPlugin removes an installed plugin bundle strictly from DefaultPluginsDir,
// stopping its process first and reloading the provider.
func (p *Provider) UninstallPlugin(pluginUUID string) error {
	baseDir := strings.TrimSpace(p.opts.DefaultPluginsDir)
	if baseDir == "" {
		return errors.New("default plugins directory is not configured")
	}
	targetUUID := strings.TrimSpace(pluginUUID)
	if targetUUID == "" {
		return errors.New("plugin UUID is required")
	}

	p.reloadMu.Lock()
	defer p.reloadMu.Unlock()

	p.mu.RLock()
	inst := p.plugins[targetUUID]
	var dir string
	if inst != nil && inst.bundle != nil {
		dir = inst.bundle.Dir
	}
	p.mu.RUnlock()

	if dir == "" {
		candidate := filepath.Join(baseDir, sanitizeBundleFolderName(targetUUID)+".sdPlugin")
		if st, err := os.Stat(candidate); err == nil && st.IsDir() {
			dir = candidate
		}
	}
	if dir == "" {
		return fmt.Errorf("plugin %q is not installed", targetUUID)
	}
	if !isSubpathOf(baseDir, dir) {
		return fmt.Errorf("refusing to uninstall external plugin %q outside %s", targetUUID, baseDir)
	}

	p.stopAllPlugins()
	if err := os.RemoveAll(dir); err != nil {
		_ = p.reloadLocked()
		return fmt.Errorf("remove plugin directory %s: %w", dir, err)
	}
	return p.reloadLocked()
}

func extractZipSafely(archivePath, dstRoot string) error {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("open plugin archive: %w", err)
	}
	defer func() { _ = zr.Close() }()

	if len(zr.File) > maxPluginArchiveFiles {
		return fmt.Errorf("plugin archive contains too many files (%d > %d)", len(zr.File), maxPluginArchiveFiles)
	}

	absRoot, err := filepath.Abs(dstRoot)
	if err != nil {
		return fmt.Errorf("resolve staging directory: %w", err)
	}

	var totalBytes int64
	for _, f := range zr.File {
		if f.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("plugin archive contains disallowed symlink %q", f.Name)
		}

		cleanRel, ok := cleanArchiveMemberPath(f.Name)
		if !ok {
			return fmt.Errorf("plugin archive contains unsafe path %q", f.Name)
		}
		if cleanRel == "" {
			continue
		}

		dstPath := filepath.Clean(filepath.Join(absRoot, filepath.FromSlash(cleanRel)))
		if !isSubpathOf(absRoot, dstPath) {
			return fmt.Errorf("plugin archive entry %q escapes extraction root", f.Name)
		}

		if isZipDir(f) {
			if err := os.MkdirAll(dstPath, 0o755); err != nil {
				return fmt.Errorf("create directory %q: %w", cleanRel, err)
			}
			continue
		}

		if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
			return fmt.Errorf("create parent directory for %q: %w", cleanRel, err)
		}

		perm := os.FileMode(0o644)
		if f.Mode().Perm()&0o111 != 0 {
			perm = 0o755
		}

		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("open archive member %q: %w", cleanRel, err)
		}

		outFile, err := os.OpenFile(dstPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
		if err != nil {
			_ = rc.Close()
			return fmt.Errorf("create extracted file %q: %w", cleanRel, err)
		}

		remaining := maxPluginUncompressedBytes - totalBytes
		n, copyErr := io.Copy(outFile, io.LimitReader(rc, remaining+1))
		closeErr := outFile.Close()
		_ = rc.Close()
		if copyErr != nil {
			return fmt.Errorf("extract %q: %w", cleanRel, copyErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close extracted %q: %w", cleanRel, closeErr)
		}
		totalBytes += n
		if totalBytes > maxPluginUncompressedBytes {
			return fmt.Errorf("plugin archive exceeds maximum uncompressed size (%d MiB)", maxPluginUncompressedBytes/(1024*1024))
		}
	}
	return nil
}

func isZipDir(f *zip.File) bool {
	if f.FileInfo().IsDir() {
		return true
	}
	if strings.HasSuffix(f.Name, "/") || strings.HasSuffix(f.Name, "\\") {
		return true
	}
	// MS-DOS directory attribute bit (0x10) in lower 16 bits of ExternalAttrs.
	return f.ExternalAttrs&0x10 != 0 && f.UncompressedSize64 == 0
}

func cleanArchiveMemberPath(raw string) (string, bool) {
	norm := strings.ReplaceAll(raw, "\\", "/")
	if strings.HasPrefix(norm, "/") {
		return "", false
	}
	// Reject Windows drive prefixes like "C:/..."
	if len(norm) >= 2 && norm[1] == ':' {
		return "", false
	}
	cleaned := path.Clean(norm)
	if cleaned == "." {
		return "", true
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.Contains(cleaned, "/../") {
		return "", false
	}
	return cleaned, true
}

func locateExtractedBundleRoot(stageRoot string) (string, error) {
	if _, err := os.Stat(filepath.Join(stageRoot, "manifest.json")); err == nil {
		return stageRoot, nil
	}

	entries, err := os.ReadDir(stageRoot)
	if err != nil {
		return "", fmt.Errorf("inspect extracted archive: %w", err)
	}
	var candidates []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.EqualFold(name, "__MACOSX") {
			continue
		}
		dir := filepath.Join(stageRoot, name)
		if _, statErr := os.Stat(filepath.Join(dir, "manifest.json")); statErr == nil {
			candidates = append(candidates, dir)
		}
	}
	if len(candidates) == 1 {
		return candidates[0], nil
	}
	for _, c := range candidates {
		if strings.HasSuffix(strings.ToLower(filepath.Base(c)), ".sdplugin") {
			return c, nil
		}
	}
	if len(candidates) > 1 {
		return candidates[0], nil
	}
	return "", errors.New("archive does not contain a valid .sdPlugin folder with manifest.json")
}

func swapDirectoryAtomically(srcDir, targetDir, parentDir string) error {
	var backupDir string
	if _, err := os.Stat(targetDir); err == nil {
		backupDir = filepath.Join(parentDir, ".backup-"+filepath.Base(targetDir))
		_ = os.RemoveAll(backupDir)
		if err := os.Rename(targetDir, backupDir); err != nil {
			return fmt.Errorf("prepare upgrade for %s: %w", filepath.Base(targetDir), err)
		}
	}

	if err := os.Rename(srcDir, targetDir); err != nil {
		if backupDir != "" {
			_ = os.Rename(backupDir, targetDir)
		}
		return fmt.Errorf("install plugin into %s: %w", targetDir, err)
	}
	if backupDir != "" {
		_ = os.RemoveAll(backupDir)
	}
	return nil
}

func sanitizeBundleFolderName(uuid string) string {
	trimmed := strings.TrimSpace(uuid)
	trimmed = strings.TrimSuffix(trimmed, ".sdPlugin")
	trimmed = strings.TrimSuffix(trimmed, ".sdplugin")
	var b strings.Builder
	b.Grow(len(trimmed))
	for _, r := range trimmed {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	out := strings.Trim(b.String(), ".")
	if out == "" {
		return "plugin"
	}
	return out
}

func isSubpathOf(rootDir, targetPath string) bool {
	absRoot, err := filepath.Abs(rootDir)
	if err != nil {
		return false
	}
	absTarget, err := filepath.Abs(targetPath)
	if err != nil {
		return false
	}
	if runtime.GOOS == "windows" {
		absRoot = strings.ToLower(absRoot)
		absTarget = strings.ToLower(absTarget)
	}
	rel, err := filepath.Rel(absRoot, absTarget)
	if err != nil {
		return false
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return false
	}
	return true
}
