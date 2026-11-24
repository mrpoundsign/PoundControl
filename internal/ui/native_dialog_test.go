package ui

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"

	"github.com/mrpoundsign/PoundControl/pkg/sdplugin"
	"github.com/mrpoundsign/PoundControl/pkg/wasmplugin"
)

func stubNativeFileDialog(t *testing.T, fn func(win fyne.Window, attach any, req fileDialogRequest) (string, error)) {
	t.Helper()
	prev := nativeFileDialogRunner
	nativeFileDialogOpen.Store(false)
	nativeFileDialogRunner = fn
	t.Cleanup(func() {
		nativeFileDialogRunner = prev
		nativeFileDialogOpen.Store(false)
	})
}

func TestResolveInitialDialogPath(t *testing.T) {
	tmp := t.TempDir()
	existingDir := filepath.Join(tmp, "clips")
	if err := os.MkdirAll(existingDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	existingFile := filepath.Join(existingDir, "airhorn.wav")
	if err := os.WriteFile(existingFile, []byte("RIFF"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if got := resolveInitialDialogPath(""); got != "" {
		t.Fatalf("resolveInitialDialogPath(\"\") = %q, want empty", got)
	}
	if got := resolveInitialDialogPath("asset://builtin/icon.png"); got != "" {
		t.Fatalf("resolveInitialDialogPath(asset URI) = %q, want empty", got)
	}
	if got := resolveInitialDialogPath(existingDir); got != existingDir {
		t.Fatalf("resolveInitialDialogPath(dir) = %q, want %q", got, existingDir)
	}
	if got := resolveInitialDialogPath(existingFile); got != existingDir {
		t.Fatalf("resolveInitialDialogPath(file) = %q, want %q", got, existingDir)
	}
	fileURI := "file:///" + strings.TrimPrefix(filepath.ToSlash(existingFile), "/")
	if got := resolveInitialDialogPath(fileURI); got != existingDir {
		t.Fatalf("resolveInitialDialogPath(%q) = %q, want %q", fileURI, got, existingDir)
	}

	missingInExistingDir := filepath.Join(existingDir, "not-yet-created.png")
	if got := resolveInitialDialogPath(missingInExistingDir); got != existingDir {
		t.Fatalf("resolveInitialDialogPath(missing file in existing dir) = %q, want %q", got, existingDir)
	}

	completelyMissing := filepath.Join(tmp, "does-not-exist", "nested", "file.png")
	if got := resolveInitialDialogPath(completelyMissing); got != "" {
		t.Fatalf("resolveInitialDialogPath(missing tree) = %q, want empty", got)
	}
}

func TestBuildZenityOptionsAndFilters(t *testing.T) {
	filters := buildZenityFilters([]fileDialogFilter{
		{
			Name:       "Stream Deck Plugin Archives",
			Extensions: []string{"streamDeckPlugin", ".streamdeckplugin", "zip"},
		},
	})
	if len(filters) != 2 {
		t.Fatalf("len(buildZenityFilters) = %d, want 2", len(filters))
	}
	if filters[0].Name != "Stream Deck Plugin Archives (*.streamDeckPlugin, *.zip)" {
		t.Fatalf("primary filter name = %q", filters[0].Name)
	}
	if !slices.Equal(filters[0].Patterns, []string{"*.streamDeckPlugin", "*.zip"}) {
		t.Fatalf("primary filter patterns = %v", filters[0].Patterns)
	}
	if !filters[0].CaseFold {
		t.Fatal("expected CaseFold = true on filter")
	}
	if filters[1].Name != "All Files (*.*)" || !slices.Equal(filters[1].Patterns, []string{"*"}) {
		t.Fatalf("fallback filter = %+v", filters[1])
	}

	fileOpts := buildZenityOptions(nil, fileDialogRequest{
		Title:       "Open Clip",
		InitialPath: t.TempDir(),
		Filters: []fileDialogFilter{
			{Name: "Audio", Extensions: []string{".wav", ".mp3"}},
		},
	})
	// Title + Filename + FileFilters = 3 options.
	if len(fileOpts) != 3 {
		t.Fatalf("len(fileOpts) = %d, want 3", len(fileOpts))
	}

	dirOpts := buildZenityOptions(nil, fileDialogRequest{
		Title:     "Pick Folder",
		Directory: true,
		Filters: []fileDialogFilter{
			{Name: "Ignored", Extensions: []string{".txt"}},
		},
	})
	// Directory mode omits filters: Title + Directory = 2 options.
	if len(dirOpts) != 2 {
		t.Fatalf("len(dirOpts) = %d, want 2 (filters should be skipped in directory mode)", len(dirOpts))
	}
}

func TestOpenNativeFileDialogReentrancyAndCancel(t *testing.T) {
	drainUI()
	testApp := test.NewApp()
	t.Cleanup(func() {
		drainUI()
		testApp.Quit()
	})
	win := testApp.NewWindow("dialog-reentrancy")
	t.Cleanup(win.Close)

	entered := make(chan struct{})
	release := make(chan struct{})
	var calls int
	var mu sync.Mutex

	stubNativeFileDialog(t, func(_ fyne.Window, _ any, _ fileDialogRequest) (string, error) {
		mu.Lock()
		calls++
		callNum := calls
		mu.Unlock()
		if callNum == 1 {
			close(entered)
			<-release
			return "", errFileDialogCanceled
		}
		return "/tmp/selected.txt", nil
	})

	doneCount := 0
	var gotPath string
	onDone := func(path string, err error) {
		if err != nil {
			t.Fatalf("unexpected dialog error: %v", err)
		}
		doneCount++
		gotPath = path
	}

	openNativeFileDialog(win, fileDialogRequest{Title: "First"}, onDone)
	<-entered

	// Concurrent trigger while the first dialog is active must be dropped.
	openNativeFileDialog(win, fileDialogRequest{Title: "Duplicate"}, onDone)
	close(release)

	waitForCondition(t, 2*time.Second, func() bool {
		return !nativeFileDialogOpen.Load()
	})
	drainUI()

	mu.Lock()
	gotCalls := calls
	mu.Unlock()
	if gotCalls != 1 {
		t.Fatalf("nativeFileDialogRunner calls = %d, want 1 (duplicate call should be ignored)", gotCalls)
	}
	if doneCount != 0 {
		t.Fatalf("canceled dialog called onDone %d times, want 0", doneCount)
	}

	// Next dialog call after cancel succeeds and dispatches result.
	openNativeFileDialog(win, fileDialogRequest{Title: "Second"}, onDone)
	waitForCondition(t, 2*time.Second, func() bool {
		return doneCount == 1
	})
	if gotPath != "/tmp/selected.txt" {
		t.Fatalf("gotPath = %q, want /tmp/selected.txt", gotPath)
	}
}

func TestFileFieldControlBrowseUsesNativeDialog(t *testing.T) {
	drainUI()
	testApp := test.NewApp()
	t.Cleanup(func() {
		drainUI()
		testApp.Quit()
	})
	win := testApp.NewWindow("file-field")
	t.Cleanup(win.Close)

	tmp := t.TempDir()
	existingFile := filepath.Join(tmp, "previous.wav")
	if err := os.WriteFile(existingFile, []byte("RIFF"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	chosenFile := filepath.Join(tmp, "next.mp3")

	var gotReq fileDialogRequest
	stubNativeFileDialog(t, func(_ fyne.Window, _ any, req fileDialogRequest) (string, error) {
		gotReq = req
		return chosenFile, nil
	})

	ctrl := newFileFieldControl(win, existingFile, "Audio file", []string{"wav", ".mp3"})
	test.Tap(ctrl.browseBtn)

	waitForCondition(t, 2*time.Second, func() bool {
		return ctrl.Value() == chosenFile
	})

	if gotReq.Title != "Select File" {
		t.Fatalf("req.Title = %q, want Select File", gotReq.Title)
	}
	if gotReq.InitialPath != tmp {
		t.Fatalf("req.InitialPath = %q, want %q", gotReq.InitialPath, tmp)
	}
	if gotReq.Directory {
		t.Fatal("expected Directory = false for file field")
	}
	if len(gotReq.Filters) != 1 || !slices.Equal(gotReq.Filters[0].Extensions, []string{".wav", ".mp3"}) {
		t.Fatalf("unexpected req.Filters: %+v", gotReq.Filters)
	}
}

func TestImageFieldControlBrowseImportsViaNativeDialog(t *testing.T) {
	drainUI()
	testApp := test.NewApp()
	t.Cleanup(func() {
		drainUI()
		testApp.Quit()
	})
	win := testApp.NewWindow("image-field")
	t.Cleanup(win.Close)

	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "src")
	assetsDir := filepath.Join(tmp, "assets")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	srcPNG := filepath.Join(srcDir, "tile.png")

	rgba := image.NewRGBA(image.Rect(0, 0, 4, 4))
	rgba.Set(0, 0, color.RGBA{R: 56, G: 189, B: 248, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, rgba); err != nil {
		t.Fatalf("png.Encode() error = %v", err)
	}
	if err := os.WriteFile(srcPNG, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	var gotReq fileDialogRequest
	stubNativeFileDialog(t, func(_ fyne.Window, _ any, req fileDialogRequest) (string, error) {
		gotReq = req
		return srcPNG, nil
	})

	var lastStatus string
	ctrl := newImageFieldControl(win, "", func() string { return assetsDir }, func(msg string) {
		lastStatus = msg
	})
	test.Tap(ctrl.browseBtn)

	waitForCondition(t, 2*time.Second, func() bool {
		return ctrl.Value() != "" && strings.Contains(lastStatus, "Imported button image")
	})

	if gotReq.Title != "Select Button Image" {
		t.Fatalf("req.Title = %q, want Select Button Image", gotReq.Title)
	}
	if len(gotReq.Filters) != 1 || !slices.Equal(gotReq.Filters[0].Extensions, []string{".png", ".jpg", ".jpeg", ".svg"}) {
		t.Fatalf("unexpected image filters: %+v", gotReq.Filters)
	}
	if !strings.HasPrefix(ctrl.Value(), "asset://") && !strings.Contains(ctrl.Value(), assetsDir) {
		if _, err := os.Stat(ctrl.Value()); err != nil && !strings.HasPrefix(ctrl.Value(), "assets/") {
			t.Fatalf("unexpected imported image reference %q", ctrl.Value())
		}
	}
	entries, err := os.ReadDir(assetsDir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected 1 imported file in %s, got entries=%v err=%v", assetsDir, entries, err)
	}
}

type fakeSDPluginCtrl struct {
	defaultDir    string
	hostCfg       sdplugin.HostConfig
	installCalled chan string
	installGate   chan struct{}
}

func (f *fakeSDPluginCtrl) HostConfig() sdplugin.HostConfig     { return f.hostCfg }
func (f *fakeSDPluginCtrl) SaveHostConfig(sdplugin.HostConfig) error { return nil }
func (f *fakeSDPluginCtrl) Reload() error                         { return nil }
func (f *fakeSDPluginCtrl) Plugins() []sdplugin.PluginStatus      { return nil }
func (f *fakeSDPluginCtrl) DefaultPluginsDir() string             { return f.defaultDir }
func (f *fakeSDPluginCtrl) ElgatoPluginsDir() string              { return "" }
func (f *fakeSDPluginCtrl) OpenDeckPluginsDir() string            { return "" }
func (f *fakeSDPluginCtrl) IsUserManagedPlugin(string) bool       { return true }
func (f *fakeSDPluginCtrl) InstallArchive(archivePath string) (*sdplugin.LoadedBundle, error) {
	f.installCalled <- archivePath
	<-f.installGate
	return &sdplugin.LoadedBundle{
		UUID: "com.example.archive",
		Manifest: sdplugin.SDManifest{
			Name:    "Archive Plugin",
			Version: "1.2.3",
		},
	}, nil
}
func (f *fakeSDPluginCtrl) InstallFromCatalogue(context.Context, *sdplugin.CatalogueClient, sdplugin.CatalogueEntry, func(int64, int64, string)) (*sdplugin.LoadedBundle, error) {
	return nil, errors.New("not implemented")
}
func (f *fakeSDPluginCtrl) UninstallPlugin(string) error { return nil }
func (f *fakeSDPluginCtrl) RegisterMarketplaceDetailPage(string) (string, bool) {
	return "", false
}
func (f *fakeSDPluginCtrl) NodeStatus(int) sdplugin.NodeRuntimeInfo {
	return sdplugin.NodeRuntimeInfo{Found: true, Usable: true, Version: "v22.0.0", Source: "PATH", Path: "node"}
}
func (f *fakeSDPluginCtrl) DownloadNode(context.Context, func(int64, int64, string)) (sdplugin.NodeRuntimeInfo, error) {
	return sdplugin.NodeRuntimeInfo{}, nil
}
func (f *fakeSDPluginCtrl) NeedsNodeForAction(string) (int, bool) { return 0, false }

type fakeWasmPluginCtrl struct {
	defaultDir    string
	hostCfg       wasmplugin.HostConfig
	installCalled chan string
	installGate   chan struct{}
}

func (f *fakeWasmPluginCtrl) HostConfig() wasmplugin.HostConfig          { return f.hostCfg }
func (f *fakeWasmPluginCtrl) SaveHostConfig(wasmplugin.HostConfig) error { return nil }
func (f *fakeWasmPluginCtrl) Reload() error                              { return nil }
func (f *fakeWasmPluginCtrl) Plugins() []wasmplugin.PluginStatus         { return nil }
func (f *fakeWasmPluginCtrl) DefaultPluginsDir() string                  { return f.defaultDir }
func (f *fakeWasmPluginCtrl) IsUserManagedPlugin(string) bool            { return true }
func (f *fakeWasmPluginCtrl) InstalledMetadata() map[string]wasmplugin.InstallRecord {
	return nil
}
func (f *fakeWasmPluginCtrl) InstallFile(srcPath string, _ *wasmplugin.InstallRecord) (wasmplugin.PluginStatus, error) {
	f.installCalled <- srcPath
	<-f.installGate
	return wasmplugin.PluginStatus{
		UUID:    "com.example.wasm",
		Name:    "Local Wasm",
		Version: "0.4.0",
	}, nil
}
func (f *fakeWasmPluginCtrl) InstallFromGitHubRelease(context.Context, string, string, string, string, *http.Client, func(int64, int64, string)) (wasmplugin.PluginStatus, error) {
	return wasmplugin.PluginStatus{}, errors.New("not implemented")
}
func (f *fakeWasmPluginCtrl) UninstallPlugin(string) error { return nil }

func TestSettingsPluginInstallRunsOffUIThread(t *testing.T) {
	drainUI()
	testApp := test.NewApp()
	t.Cleanup(func() {
		drainUI()
		testApp.Quit()
	})
	testApp.Settings().SetTheme(&StudioTheme{})
	win := testApp.NewWindow("settings-native-dialogs")
	t.Cleanup(win.Close)

	tmp := t.TempDir()
	archivePath := filepath.Join(tmp, "demo.streamDeckPlugin")
	wasmPath := filepath.Join(tmp, "demo.wasm")
	folderPath := filepath.Join(tmp, "custom-plugins")

	var (
		mu         sync.Mutex
		nextResult string
		nextErr    error
		lastReq    fileDialogRequest
	)
	stubNativeFileDialog(t, func(_ fyne.Window, _ any, req fileDialogRequest) (string, error) {
		mu.Lock()
		lastReq = req
		res := nextResult
		err := nextErr
		mu.Unlock()
		return res, err
	})

	sdCtrl := &fakeSDPluginCtrl{
		defaultDir:    filepath.Join(tmp, "sd-default"),
		installCalled: make(chan string, 1),
		installGate:   make(chan struct{}),
	}
	wasmCtrl := &fakeWasmPluginCtrl{
		defaultDir:    filepath.Join(tmp, "wasm-default"),
		installCalled: make(chan string, 1),
		installGate:   make(chan struct{}),
	}

	var (
		statusMu     sync.Mutex
		statuses     []string
		syncedReason string
	)
	onStatus := func(msg string) {
		statusMu.Lock()
		statuses = append(statuses, msg)
		statusMu.Unlock()
	}
	syncAll := func(reason string) {
		statusMu.Lock()
		syncedReason = reason
		statusMu.Unlock()
	}

	sdObj, _, sdCleanup := buildSDPluginSection(win, sdCtrl, nil, func(string) {}, syncAll, onStatus)
	t.Cleanup(sdCleanup)

	installArchiveBtn := findUIButton(sdObj, "Install Archive")
	if installArchiveBtn == nil {
		t.Fatal("expected Install Archive button")
	}

	mu.Lock()
	nextResult = archivePath
	nextErr = nil
	mu.Unlock()

	test.Tap(installArchiveBtn)
	waitForCondition(t, 2*time.Second, func() bool {
		return len(sdCtrl.installCalled) == 1 && installArchiveBtn.Disabled()
	})
	if got := <-sdCtrl.installCalled; got != archivePath {
		t.Fatalf("InstallArchive(%q), want %q", got, archivePath)
	}
	close(sdCtrl.installGate)

	waitForCondition(t, 2*time.Second, func() bool {
		statusMu.Lock()
		defer statusMu.Unlock()
		return !installArchiveBtn.Disabled() && strings.Contains(syncedReason, "Installed plugin Archive Plugin")
	})

	// Verify SD folder Browse button opens a directory picker and populates the entry.
	sdBrowseBtn := findUIButton(sdObj, "Browse...")
	sdPathEnt := findUIEntry(sdObj, ".sdPlugin bundle")
	if sdBrowseBtn == nil || sdPathEnt == nil {
		t.Fatal("expected SD Browse button and path entry")
	}
	mu.Lock()
	nextResult = folderPath
	mu.Unlock()
	test.Tap(sdBrowseBtn)
	waitForCondition(t, 2*time.Second, func() bool {
		return sdPathEnt.Text == folderPath
	})
	mu.Lock()
	sdReq := lastReq
	mu.Unlock()
	if !sdReq.Directory {
		t.Fatal("expected SD Browse dialog to set Directory = true")
	}

	// Verify Wasm local install runs off UI thread and folder Browse populates entry.
	wasmObj, _, wasmCleanup := buildWasmPluginSection(win, wasmCtrl, nil, func(string) {}, syncAll, nil, onStatus)
	t.Cleanup(wasmCleanup)

	installWasmBtn := findUIButton(wasmObj, "Install Local .wasm")
	if installWasmBtn == nil {
		t.Fatal("expected Install Local .wasm button")
	}

	mu.Lock()
	nextResult = wasmPath
	mu.Unlock()
	statusMu.Lock()
	syncedReason = ""
	statusMu.Unlock()

	test.Tap(installWasmBtn)
	waitForCondition(t, 2*time.Second, func() bool {
		return len(wasmCtrl.installCalled) == 1 && installWasmBtn.Disabled()
	})
	if got := <-wasmCtrl.installCalled; got != wasmPath {
		t.Fatalf("InstallFile(%q), want %q", got, wasmPath)
	}
	close(wasmCtrl.installGate)

	waitForCondition(t, 2*time.Second, func() bool {
		statusMu.Lock()
		defer statusMu.Unlock()
		return !installWasmBtn.Disabled() && strings.Contains(syncedReason, "Installed WebAssembly plugin Local Wasm")
	})

	wasmBrowseBtn := findUIButton(wasmObj, "Browse...")
	wasmPathEnt := findUIEntry(wasmObj, ".wasm plugin file")
	if wasmBrowseBtn == nil || wasmPathEnt == nil {
		t.Fatal("expected Wasm Browse button and path entry")
	}
	mu.Lock()
	nextResult = folderPath
	mu.Unlock()
	test.Tap(wasmBrowseBtn)
	waitForCondition(t, 2*time.Second, func() bool {
		return wasmPathEnt.Text == folderPath
	})
	mu.Lock()
	wasmReq := lastReq
	mu.Unlock()
	if !wasmReq.Directory {
		t.Fatal("expected Wasm Browse dialog to set Directory = true")
	}

	// Verify installing status messages were reported.
	statusMu.Lock()
	gotStatuses := slices.Clone(statuses)
	statusMu.Unlock()
	if !slices.Contains(gotStatuses, "Installing plugin archive...") || !slices.Contains(gotStatuses, "Installing WebAssembly plugin...") {
		t.Fatalf("expected installing status messages, got %q", gotStatuses)
	}
}
