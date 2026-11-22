package sdplugin

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// DefaultManagedNodeVersion is the pinned LTS Node.js version offered by the on-demand downloader.
const DefaultManagedNodeVersion = "v22.14.0"

// NodeRuntimeInfo describes a discovered or managed Node.js executable and whether it meets version requirements.
type NodeRuntimeInfo struct {
	Found   bool   `json:"found"`
	Usable  bool   `json:"usable"`
	Path    string `json:"path,omitempty"`
	Version string `json:"version,omitempty"`
	Major   int    `json:"major,omitempty"`
	Source  string `json:"source,omitempty"` // "custom", "managed", "path", "elgato"
	Error   string `json:"error,omitempty"`
}

// NodeManager resolves installed Node.js runtimes and optionally downloads a standalone
// SHA-256 verified Node.js binary into ManagedDir.
type NodeManager struct {
	managedDir   string
	nodeVersion  string
	baseURL      string
	goos         string
	goarch       string
	httpClient   *http.Client
	lookPath     func(string) (string, error)
	probeVersion func(string) (string, error)
	elgatoPaths  []string
}

// NewNodeManager creates a NodeManager storing downloaded binaries inside managedDir.
func NewNodeManager(managedDir string) *NodeManager {
	return &NodeManager{
		managedDir:   managedDir,
		nodeVersion:  DefaultManagedNodeVersion,
		baseURL:      "https://nodejs.org/dist/" + DefaultManagedNodeVersion,
		goos:         runtime.GOOS,
		goarch:       runtime.GOARCH,
		httpClient:   &http.Client{},
		lookPath:     exec.LookPath,
		probeVersion: execProbeNodeVersion,
		elgatoPaths:  defaultElgatoNodePaths(),
	}
}

// ManagedDir returns the root directory used for PoundControl's managed Node.js binary.
func (m *NodeManager) ManagedDir() string {
	return m.managedDir
}

// Resolve finds a usable Node.js binary meeting minMajor (default 20) according to priority:
// custom override -> PoundControl managed binary -> system PATH -> Elgato bundled node.
func (m *NodeManager) Resolve(customPath string, minMajor int) NodeRuntimeInfo {
	if minMajor <= 0 {
		minMajor = 20
	}

	type candidate struct {
		source string
		path   string
	}
	var candidates []candidate
	if cp := strings.TrimSpace(customPath); cp != "" {
		candidates = append(candidates, candidate{source: "custom", path: cp})
	}
	if managed := m.managedBinaryPath(); managed != "" {
		candidates = append(candidates, candidate{source: "managed", path: managed})
	}
	if m.lookPath != nil {
		if sysPath, err := m.lookPath("node"); err == nil && sysPath != "" {
			candidates = append(candidates, candidate{source: "path", path: sysPath})
		}
	}
	for _, ep := range m.elgatoPaths {
		if ep != "" {
			candidates = append(candidates, candidate{source: "elgato", path: ep})
		}
	}

	var tooOld *NodeRuntimeInfo
	for _, cand := range candidates {
		infoSt, err := os.Stat(cand.path)
		if err != nil || infoSt.IsDir() {
			continue
		}
		rawVer, err := m.probeVersion(cand.path)
		if err != nil {
			continue
		}
		ver, major, ok := parseNodeVersion(rawVer)
		if !ok {
			continue
		}
		info := NodeRuntimeInfo{
			Found:   true,
			Path:    cand.path,
			Version: ver,
			Major:   major,
			Source:  cand.source,
		}
		if major >= minMajor {
			info.Usable = true
			return info
		}
		if tooOld == nil {
			info.Error = fmt.Sprintf("Node.js %s at %s is older than required v%d", ver, cand.path, minMajor)
			cp := info
			tooOld = &cp
		}
	}

	if tooOld != nil {
		return *tooOld
	}
	return NodeRuntimeInfo{
		Found:  false,
		Usable: false,
		Error:  fmt.Sprintf("Node.js >= v%d runtime not found", minMajor),
	}
}

// DownloadManaged downloads the official Node.js archive for the host OS/arch, verifies its SHA-256 digest
// against SHASUMS256.txt, and extracts only the standalone node binary into ManagedDir.
func (m *NodeManager) DownloadManaged(
	ctx context.Context,
	onProgress func(downloaded, total int64, stage string),
) (NodeRuntimeInfo, error) {
	if strings.TrimSpace(m.managedDir) == "" {
		return NodeRuntimeInfo{}, errors.New("managed Node.js directory is not configured")
	}

	archiveName, innerPath, destRel, isZip, err := nodeArchiveSpec(m.nodeVersion, m.goos, m.goarch)
	if err != nil {
		return NodeRuntimeInfo{}, err
	}

	report := func(done, total int64, stage string) {
		if onProgress != nil {
			onProgress(done, total, stage)
		}
	}

	report(0, 0, "Fetching SHA-256 checksums…")
	sumsURL := strings.TrimRight(m.baseURL, "/") + "/SHASUMS256.txt"
	expectedHash, err := m.fetchExpectedSHA256(ctx, sumsURL, archiveName)
	if err != nil {
		return NodeRuntimeInfo{}, fmt.Errorf("fetch Node.js checksums: %w", err)
	}

	tmpDir, err := os.MkdirTemp("", "poundcontrol-node-*")
	if err != nil {
		return NodeRuntimeInfo{}, fmt.Errorf("create temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	archivePath := filepath.Join(tmpDir, archiveName)
	archiveURL := strings.TrimRight(m.baseURL, "/") + "/" + archiveName
	actualHash, err := m.downloadAndHash(ctx, archiveURL, archivePath, func(done, total int64) {
		report(done, total, "Downloading "+archiveName+"…")
	})
	if err != nil {
		return NodeRuntimeInfo{}, fmt.Errorf("download %s: %w", archiveName, err)
	}
	if !strings.EqualFold(actualHash, expectedHash) {
		return NodeRuntimeInfo{}, fmt.Errorf("SHA-256 verification failed for %s: got %s, want %s", archiveName, actualHash, expectedHash)
	}

	report(0, 0, "Extracting Node.js binary…")
	destPath := filepath.Join(m.managedDir, filepath.FromSlash(destRel))
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return NodeRuntimeInfo{}, fmt.Errorf("create runtime directory: %w", err)
	}
	tmpDest := destPath + ".tmp"
	if isZip {
		err = extractZipMember(archivePath, innerPath, tmpDest)
	} else {
		err = extractTarGzMember(archivePath, innerPath, tmpDest)
	}
	if err != nil {
		_ = os.Remove(tmpDest)
		return NodeRuntimeInfo{}, fmt.Errorf("extract %s from %s: %w", innerPath, archiveName, err)
	}
	if err := os.Chmod(tmpDest, 0o755); err != nil {
		_ = os.Remove(tmpDest)
		return NodeRuntimeInfo{}, err
	}
	if err := os.Rename(tmpDest, destPath); err != nil {
		_ = os.Remove(tmpDest)
		return NodeRuntimeInfo{}, fmt.Errorf("install Node.js binary: %w", err)
	}

	report(0, 0, "Verifying Node.js runtime…")
	resolved := m.Resolve("", 1)
	if !resolved.Found {
		return NodeRuntimeInfo{}, errors.New("downloaded Node.js binary failed version check")
	}
	return resolved, nil
}

func (m *NodeManager) managedBinaryPath() string {
	if strings.TrimSpace(m.managedDir) == "" {
		return ""
	}
	if m.goos == "windows" {
		return filepath.Join(m.managedDir, "node.exe")
	}
	return filepath.Join(m.managedDir, "bin", "node")
}

func (m *NodeManager) fetchExpectedSHA256(ctx context.Context, sumsURL, archiveName string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sumsURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := m.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("HTTP status %d", resp.StatusCode)
	}

	scanner := bufio.NewScanner(io.LimitReader(resp.Body, 512*1024))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 2 && fields[1] == archiveName && len(fields[0]) == 64 {
			return strings.ToLower(fields[0]), nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("checksum entry for %s not found in SHASUMS256.txt", archiveName)
}

func (m *NodeManager) downloadAndHash(
	ctx context.Context,
	srcURL string,
	dstPath string,
	onProgress func(done, total int64),
) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srcURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := m.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("HTTP status %d", resp.StatusCode)
	}

	out, err := os.Create(dstPath)
	if err != nil {
		return "", err
	}
	defer func() { _ = out.Close() }()

	hasher := sha256.New()
	total := resp.ContentLength
	var downloaded int64
	buf := make([]byte, 32*1024)

	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			if _, writeErr := out.Write(chunk); writeErr != nil {
				return "", writeErr
			}
			_, _ = hasher.Write(chunk)
			downloaded += int64(n)
			if onProgress != nil {
				onProgress(downloaded, total)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return "", readErr
		}
	}
	if err := out.Close(); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func extractZipMember(zipPath, targetName, dstPath string) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer func() { _ = zr.Close() }()

	want := strings.TrimLeft(strings.ReplaceAll(targetName, "\\", "/"), "/")
	for _, f := range zr.File {
		name := strings.TrimLeft(strings.ReplaceAll(f.Name, "\\", "/"), "/")
		if name != want {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		writeErr := writeFileFromReader(dstPath, rc)
		_ = rc.Close()
		return writeErr
	}
	return fmt.Errorf("entry %s missing from zip archive", targetName)
}

func extractTarGzMember(tgzPath, targetName, dstPath string) error {
	f, err := os.Open(tgzPath)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer func() { _ = gz.Close() }()

	want := strings.TrimLeft(strings.ReplaceAll(targetName, "\\", "/"), "/")
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		name := strings.TrimLeft(strings.ReplaceAll(hdr.Name, "\\", "/"), "/")
		if name == want && hdr.Typeflag == tar.TypeReg {
			return writeFileFromReader(dstPath, tr)
		}
	}
	return fmt.Errorf("entry %s missing from tar.gz archive", targetName)
}

func writeFileFromReader(dstPath string, r io.Reader) error {
	out, err := os.OpenFile(dstPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, r); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func nodeArchiveSpec(version, goos, goarch string) (archiveName, innerPath, destRel string, isZip bool, err error) {
	archToken := ""
	switch goarch {
	case "amd64":
		archToken = "x64"
	case "arm64":
		archToken = "arm64"
	default:
		return "", "", "", false, fmt.Errorf("unsupported CPU architecture for Node.js download: %s", goarch)
	}

	switch goos {
	case "windows":
		base := fmt.Sprintf("node-%s-win-%s", version, archToken)
		return base + ".zip", base + "/node.exe", "node.exe", true, nil
	case "linux":
		base := fmt.Sprintf("node-%s-linux-%s", version, archToken)
		return base + ".tar.gz", base + "/bin/node", "bin/node", false, nil
	case "darwin":
		base := fmt.Sprintf("node-%s-darwin-%s", version, archToken)
		return base + ".tar.gz", base + "/bin/node", "bin/node", false, nil
	default:
		return "", "", "", false, fmt.Errorf("unsupported OS for Node.js download: %s", goos)
	}
}

func parseNodeVersion(raw string) (ver string, major int, ok bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", 0, false
	}
	line := strings.Fields(trimmed)[0]
	clean := strings.TrimPrefix(line, "v")
	majorStr, _, _ := strings.Cut(clean, ".")
	maj, err := strconv.Atoi(majorStr)
	if err != nil || maj <= 0 {
		return "", 0, false
	}
	return "v" + clean, maj, true
}

func execProbeNodeVersion(binPath string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binPath, "-v")
	configureBackgroundCmd(cmd)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func defaultElgatoNodePaths() []string {
	switch runtime.GOOS {
	case "windows":
		var out []string
		for _, pf := range []string{os.Getenv("ProgramFiles"), `C:\Program Files`} {
			if strings.TrimSpace(pf) == "" {
				continue
			}
			out = append(out, filepath.Join(pf, "Elgato", "StreamDeck", "nodejs", "node.exe"))
		}
		return out
	case "darwin":
		return []string{"/Applications/Elgato Stream Deck.app/Contents/Resources/nodejs/bin/node"}
	default:
		return nil
	}
}
