package wasmplugin

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/mrpoundsign/appinstaller/pkg/updater"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"

	"github.com/mrpoundsign/PoundControl/pkg/plugin"
)

const (
	maxWasmFileBytes     = 32 * 1024 * 1024
	installMetaFileName  = ".installed.json"
	wasmPluginSourceRepo = "github"
)

// InstallRecord tracks provenance metadata for a user-managed .wasm plugin installed into DefaultPluginsDir.
type InstallRecord struct {
	UUID             string    `json:"uuid"`
	Filename         string    `json:"filename"`
	Source           string    `json:"source"` // "github" or "local"
	Repository       string    `json:"repository,omitempty"`
	AssetName        string    `json:"assetName,omitempty"`
	InstalledVersion string    `json:"installedVersion,omitempty"`
	InstalledAt      time.Time `json:"installedAt"`
}

type installMetadataStore struct {
	Plugins map[string]InstallRecord `json:"plugins"`
}

// IsUserManagedPlugin reports whether pluginPath resides strictly inside DefaultPluginsDir
// and can therefore be safely upgraded or uninstalled by PoundControl.
func (p *Provider) IsUserManagedPlugin(pluginPath string) bool {
	baseDir := strings.TrimSpace(p.opts.DefaultPluginsDir)
	if baseDir == "" || strings.TrimSpace(pluginPath) == "" {
		return false
	}
	return isSubpathOf(baseDir, pluginPath)
}

// InstalledMetadata returns a copy of persisted installation records from DefaultPluginsDir/.installed.json.
func (p *Provider) InstalledMetadata() map[string]InstallRecord {
	baseDir := strings.TrimSpace(p.opts.DefaultPluginsDir)
	if baseDir == "" {
		return nil
	}
	p.metaMu.Lock()
	records, err := loadInstallMetadata(baseDir)
	p.metaMu.Unlock()
	if err != nil {
		return nil
	}
	return maps.Clone(records)
}

// InstallFile validates a local .wasm plugin module, atomically installs or upgrades it inside
// DefaultPluginsDir, persists optional install metadata, and reloads the provider.
func (p *Provider) InstallFile(srcPath string, meta *InstallRecord) (PluginStatus, error) {
	baseDir := strings.TrimSpace(p.opts.DefaultPluginsDir)
	if baseDir == "" {
		return PluginStatus{}, errors.New("default wasm plugins directory is not configured")
	}

	wasmBytes, err := readBoundedWasmFile(srcPath)
	if err != nil {
		return PluginStatus{}, err
	}

	manifest, err := validateWasmBytes(wasmBytes, baseDir, p.callTimeout)
	if err != nil {
		return PluginStatus{}, fmt.Errorf("validate wasm plugin: %w", err)
	}

	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		return PluginStatus{}, fmt.Errorf("create wasm plugins directory: %w", err)
	}

	targetName := sanitizeWasmFileName(manifest.UUID) + ".wasm"
	targetPath := filepath.Join(baseDir, targetName)
	if !isSubpathOf(baseDir, targetPath) {
		return PluginStatus{}, fmt.Errorf("invalid target path for wasm plugin %q", manifest.UUID)
	}

	p.reloadMu.Lock()
	defer p.reloadMu.Unlock()

	p.mu.RLock()
	closed := p.closed
	var oldManagedPath string
	if existing := p.instances[manifest.UUID]; existing != nil && p.IsUserManagedPlugin(existing.path) {
		oldManagedPath = existing.path
	}
	p.mu.RUnlock()
	if closed {
		return PluginStatus{}, errors.New("wasm plugin provider is closed")
	}

	p.metaMu.Lock()
	records, _ := loadInstallMetadata(baseDir)
	p.metaMu.Unlock()
	if oldManagedPath == "" {
		if prevRec, ok := records[manifest.UUID]; ok && strings.TrimSpace(prevRec.Filename) != "" {
			cand := filepath.Join(baseDir, filepath.Base(prevRec.Filename))
			if isSubpathOf(baseDir, cand) {
				oldManagedPath = cand
			}
		}
	}

	p.stopAllInstances()

	if err := writeWasmAtomically(wasmBytes, targetPath, baseDir); err != nil {
		_ = p.reloadLocked()
		return PluginStatus{}, err
	}

	if oldManagedPath != "" && !sameLocalPath(oldManagedPath, targetPath) && isSubpathOf(baseDir, oldManagedPath) {
		_ = os.Remove(oldManagedPath)
	}

	rec := InstallRecord{
		UUID:             manifest.UUID,
		Filename:         targetName,
		Source:           "local",
		InstalledVersion: manifest.Version,
		InstalledAt:      time.Now().UTC(),
	}
	if meta != nil {
		if s := strings.TrimSpace(meta.Source); s != "" {
			rec.Source = s
		}
		rec.Repository = strings.TrimSpace(meta.Repository)
		rec.AssetName = strings.TrimSpace(meta.AssetName)
		if v := strings.TrimSpace(meta.InstalledVersion); v != "" {
			rec.InstalledVersion = v
		}
	}
	p.metaMu.Lock()
	latestRecords, _ := loadInstallMetadata(baseDir)
	if latestRecords == nil {
		latestRecords = make(map[string]InstallRecord)
	}
	latestRecords[manifest.UUID] = rec
	saveErr := saveInstallMetadata(baseDir, latestRecords)
	p.metaMu.Unlock()
	if saveErr != nil {
		_ = p.reloadLocked()
		return PluginStatus{}, fmt.Errorf("save wasm install metadata: %w", saveErr)
	}

	if err := p.reloadLocked(); err != nil {
		return PluginStatus{}, err
	}

	for _, st := range p.Plugins() {
		if st.UUID == manifest.UUID {
			if st.State == StatusError {
				return st, fmt.Errorf("installed plugin %q failed to start: %s", st.Name, st.Error)
			}
			return st, nil
		}
	}
	return PluginStatus{}, fmt.Errorf("installed wasm plugin %q did not appear after reload", manifest.UUID)
}

// InstallFromGitHubRelease fetches the latest GitHub Release for owner/repo, verifies the downloaded
// .wasm asset's SHA-256 against the release checksums file, and installs it into DefaultPluginsDir.
func (p *Provider) InstallFromGitHubRelease(
	ctx context.Context,
	owner, repo, assetName, apiBaseURL string,
	httpClient *http.Client,
	onProgress func(downloaded, total int64, stage string),
) (PluginStatus, error) {
	baseDir := strings.TrimSpace(p.opts.DefaultPluginsDir)
	if baseDir == "" {
		return PluginStatus{}, errors.New("default wasm plugins directory is not configured")
	}
	owner = strings.TrimSpace(owner)
	repo = strings.TrimSpace(repo)
	if owner == "" || repo == "" {
		return PluginStatus{}, errors.New("github owner and repository are required")
	}

	checker := updater.NewChecker(owner, repo)
	checker.UserAgent = "PoundControl-Plugin-Client"
	if base := strings.TrimSpace(apiBaseURL); base != "" {
		checker.APIBaseURL = base
	}
	if httpClient != nil {
		checker.HTTPClient = httpClient
	}

	if onProgress != nil {
		onProgress(0, 0, "Checking latest GitHub release...")
	}
	rel, _, err := checker.CheckLatest(ctx, "0.0.0")
	if err != nil {
		return PluginStatus{}, fmt.Errorf("check release for %s/%s: %w", owner, repo, err)
	}

	targetAsset, err := selectWasmReleaseAsset(rel, assetName)
	if err != nil {
		return PluginStatus{}, fmt.Errorf("release %s for %s/%s: %w", rel.TagName, owner, repo, err)
	}

	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		return PluginStatus{}, fmt.Errorf("create wasm plugins directory: %w", err)
	}
	tmpDir, err := os.MkdirTemp(baseDir, ".dl-*")
	if err != nil {
		return PluginStatus{}, fmt.Errorf("create temporary download directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	verifiedPath, err := checker.DownloadAndVerify(ctx, rel, targetAsset, tmpDir, func(downloaded, total int64) {
		if onProgress != nil {
			onProgress(downloaded, total, "Downloading & verifying SHA-256...")
		}
	})
	if err != nil {
		return PluginStatus{}, fmt.Errorf("download %s: %w", targetAsset.Name, err)
	}

	if onProgress != nil {
		onProgress(targetAsset.Size, targetAsset.Size, "Validating & installing plugin...")
	}

	ver := updater.NormalizeVersion(rel.TagName)
	return p.InstallFile(verifiedPath, &InstallRecord{
		Source:           wasmPluginSourceRepo,
		Repository:       fmt.Sprintf("https://github.com/%s/%s", owner, repo),
		AssetName:        targetAsset.Name,
		InstalledVersion: ver,
	})
}

// UninstallPlugin removes a user-managed .wasm plugin strictly from DefaultPluginsDir,
// stopping its runtime first and reloading the provider.
func (p *Provider) UninstallPlugin(pluginUUID string) error {
	baseDir := strings.TrimSpace(p.opts.DefaultPluginsDir)
	if baseDir == "" {
		return errors.New("default wasm plugins directory is not configured")
	}
	targetUUID := strings.TrimSpace(pluginUUID)
	if targetUUID == "" {
		return errors.New("plugin UUID is required")
	}

	p.reloadMu.Lock()
	defer p.reloadMu.Unlock()

	p.mu.RLock()
	inst := p.instances[targetUUID]
	var targetPath string
	if inst != nil {
		targetPath = inst.path
	}
	p.mu.RUnlock()

	p.metaMu.Lock()
	records, _ := loadInstallMetadata(baseDir)
	p.metaMu.Unlock()
	if targetPath == "" {
		if rec, ok := records[targetUUID]; ok && strings.TrimSpace(rec.Filename) != "" {
			cand := filepath.Join(baseDir, filepath.Base(rec.Filename))
			if st, err := os.Stat(cand); err == nil && !st.IsDir() {
				targetPath = cand
			}
		}
	}
	if targetPath == "" {
		cand := filepath.Join(baseDir, sanitizeWasmFileName(targetUUID)+".wasm")
		if st, err := os.Stat(cand); err == nil && !st.IsDir() {
			targetPath = cand
		}
	}
	if targetPath == "" {
		return fmt.Errorf("wasm plugin %q is not installed", targetUUID)
	}
	if !p.IsUserManagedPlugin(targetPath) {
		return fmt.Errorf("refusing to uninstall external wasm plugin %q outside %s", targetUUID, baseDir)
	}

	p.stopAllInstances()
	if err := os.Remove(targetPath); err != nil {
		_ = p.reloadLocked()
		return fmt.Errorf("remove wasm plugin %s: %w", targetPath, err)
	}
	p.metaMu.Lock()
	if latestRecords, err := loadInstallMetadata(baseDir); err == nil {
		if _, exists := latestRecords[targetUUID]; exists {
			delete(latestRecords, targetUUID)
			_ = saveInstallMetadata(baseDir, latestRecords)
		}
	}
	p.metaMu.Unlock()
	return p.reloadLocked()
}

func selectWasmReleaseAsset(rel *updater.ReleaseInfo, preferredName string) (*updater.Asset, error) {
	if rel == nil || len(rel.Assets) == 0 {
		return nil, errors.New("release contains no downloadable assets")
	}
	pref := strings.TrimSpace(preferredName)
	for i := range rel.Assets {
		a := &rel.Assets[i]
		name := strings.TrimSpace(a.Name)
		if name == "" || filepath.Base(name) != name || strings.ContainsAny(name, "/\\") || name == ".." || name == "." {
			continue
		}
		if pref != "" {
			if strings.EqualFold(name, pref) {
				return a, nil
			}
			continue
		}
		if strings.HasSuffix(strings.ToLower(name), ".wasm") {
			return a, nil
		}
	}
	if pref != "" {
		return nil, fmt.Errorf("release asset %q not found", pref)
	}
	return nil, errors.New("no .wasm asset found in release")
}

func readBoundedWasmFile(srcPath string) ([]byte, error) {
	cleaned := filepath.Clean(strings.TrimSpace(srcPath))
	if cleaned == "" || cleaned == "." {
		return nil, errors.New("wasm file path is required")
	}
	info, err := os.Stat(cleaned)
	if err != nil {
		return nil, fmt.Errorf("stat wasm file: %w", err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("path %q is a directory", cleaned)
	}
	if info.Size() <= 0 {
		return nil, errors.New("wasm file is empty")
	}
	if info.Size() > maxWasmFileBytes {
		return nil, fmt.Errorf("wasm file exceeds %d MiB limit", maxWasmFileBytes/(1024*1024))
	}
	f, err := os.Open(cleaned)
	if err != nil {
		return nil, fmt.Errorf("open wasm file: %w", err)
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxWasmFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read wasm file: %w", err)
	}
	if int64(len(data)) > maxWasmFileBytes {
		return nil, fmt.Errorf("wasm file exceeds %d MiB limit", maxWasmFileBytes/(1024*1024))
	}
	return data, nil
}

func validateWasmBytes(wasmBytes []byte, baseDir string, callTimeout time.Duration) (plugin.Manifest, error) {
	if len(wasmBytes) == 0 {
		return plugin.Manifest{}, errors.New("wasm binary is empty")
	}
	if len(wasmBytes) > maxWasmFileBytes {
		return plugin.Manifest{}, fmt.Errorf("wasm binary exceeds %d MiB limit", maxWasmFileBytes/(1024*1024))
	}
	if callTimeout <= 0 {
		callTimeout = defaultCallTimeout
	}

	lifeCtx, lifeCancel := context.WithCancel(context.Background())
	defer lifeCancel()

	tmpProv := NewProvider(Options{CallTimeout: callTimeout})
	tmpInst := &wasmInstance{
		provider:   tmpProv,
		path:       filepath.Join(baseDir, "validate.wasm"),
		state:      StatusError,
		lifeCtx:    lifeCtx,
		lifeCancel: lifeCancel,
		timers:     make(map[string]timerHandle),
	}
	defer tmpInst.closeInstance()

	rtCfg := wazero.NewRuntimeConfig().
		WithMemoryLimitPages(maxMemoryPages).
		WithCloseOnContextDone(true)
	rt := wazero.NewRuntimeWithConfig(lifeCtx, rtCfg)
	tmpInst.rt = rt

	wasi_snapshot_preview1.MustInstantiate(lifeCtx, rt)
	if err := tmpProv.instantiateHostModule(lifeCtx, rt, tmpInst); err != nil {
		return plugin.Manifest{}, fmt.Errorf("host module init failed: %w", err)
	}

	compiled, err := rt.CompileModule(lifeCtx, wasmBytes)
	if err != nil {
		return plugin.Manifest{}, fmt.Errorf("compile wasm: %w", err)
	}

	modCfg := wazero.NewModuleConfig().
		WithName("validate.wasm").
		WithStartFunctions("_initialize").
		WithSysWalltime().
		WithSysNanotime().
		WithSysNanosleep().
		WithRandSource(rand.Reader)

	initCtx, initCancel := context.WithTimeout(lifeCtx, callTimeout)
	defer initCancel()

	mod, err := rt.InstantiateModule(initCtx, compiled, modCfg)
	if err != nil {
		return plugin.Manifest{}, fmt.Errorf("instantiate wasm: %w", err)
	}
	tmpInst.mod = mod
	if mod.ExportedFunction("_initialize") == nil && mod.ExportedFunction("_start") != nil {
		return plugin.Manifest{}, errors.New("wasm module exports WASI command _start instead of reactor _initialize (for Go plugins, build with: GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared)")
	}

	tmpInst.allocFn = mod.ExportedFunction("pc_alloc")
	tmpInst.freeFn = mod.ExportedFunction("pc_free")
	manifestFn := mod.ExportedFunction("pc_manifest")
	eventFn := mod.ExportedFunction("pc_handle_event")
	if manifestFn == nil || eventFn == nil {
		return plugin.Manifest{}, errors.New("missing required PoundControl exports (pc_manifest, pc_handle_event)")
	}

	gm, err := tmpInst.readManifest(manifestFn)
	if err != nil {
		return plugin.Manifest{}, err
	}
	pm := gm.toPluginManifest(baseDir)
	if err := pm.Validate(); err != nil {
		return plugin.Manifest{}, fmt.Errorf("invalid manifest: %w", err)
	}
	return pm, nil
}

func writeWasmAtomically(data []byte, targetPath, baseDir string) error {
	tmpFile, err := os.CreateTemp(baseDir, ".stage-*.tmp")
	if err != nil {
		return fmt.Errorf("create staging file: %w", err)
	}
	tmpPath := tmpFile.Name()
	cleanupTmp := true
	defer func() {
		if cleanupTmp {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("write staged wasm file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("close staged wasm file: %w", err)
	}

	var backupPath string
	if _, statErr := os.Stat(targetPath); statErr == nil {
		backupPath = filepath.Join(baseDir, ".backup-"+filepath.Base(targetPath)+".bak")
		_ = os.Remove(backupPath)
		if err := os.Rename(targetPath, backupPath); err != nil {
			return fmt.Errorf("prepare upgrade for %s: %w", filepath.Base(targetPath), err)
		}
	}

	if err := os.Rename(tmpPath, targetPath); err != nil {
		if backupPath != "" {
			_ = os.Rename(backupPath, targetPath)
		}
		return fmt.Errorf("install wasm file %s: %w", targetPath, err)
	}
	cleanupTmp = false
	if backupPath != "" {
		_ = os.Remove(backupPath)
	}
	return nil
}

func loadInstallMetadata(baseDir string) (map[string]InstallRecord, error) {
	metaPath := filepath.Join(baseDir, installMetaFileName)
	raw, err := os.ReadFile(metaPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]InstallRecord{}, nil
		}
		return nil, err
	}
	var store installMetadataStore
	if err := json.Unmarshal(raw, &store); err != nil {
		return nil, err
	}
	if store.Plugins == nil {
		return map[string]InstallRecord{}, nil
	}
	return store.Plugins, nil
}

func saveInstallMetadata(baseDir string, records map[string]InstallRecord) error {
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		return err
	}
	metaPath := filepath.Join(baseDir, installMetaFileName)
	encoded, err := json.MarshalIndent(installMetadataStore{Plugins: records}, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')

	tmpFile, err := os.CreateTemp(baseDir, ".installed-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmpFile.Name()
	cleanupTmp := true
	defer func() {
		if cleanupTmp {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmpFile.Write(encoded); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if err := tmpFile.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, metaPath); err != nil {
		return err
	}
	cleanupTmp = false
	return nil
}

func sanitizeWasmFileName(uuid string) string {
	trimmed := strings.TrimSpace(uuid)
	trimmed = strings.TrimSuffix(trimmed, ".wasm")
	trimmed = strings.TrimSuffix(trimmed, ".WASM")
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

func sameLocalPath(a, b string) bool {
	absA, errA := filepath.Abs(a)
	absB, errB := filepath.Abs(b)
	if errA != nil || errB != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	if runtime.GOOS == "windows" {
		return strings.EqualFold(absA, absB)
	}
	return absA == absB
}
