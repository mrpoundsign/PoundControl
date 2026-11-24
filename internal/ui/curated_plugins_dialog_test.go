package ui

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"github.com/mrpoundsign/PoundControl/internal/app"
	"github.com/mrpoundsign/PoundControl/pkg/builtin/core"
	"github.com/mrpoundsign/PoundControl/pkg/deck"
	"github.com/mrpoundsign/PoundControl/pkg/plugin"
	"github.com/mrpoundsign/PoundControl/pkg/pluginmgr"
	"github.com/mrpoundsign/PoundControl/pkg/sdplugin"
	"github.com/mrpoundsign/PoundControl/pkg/wasmplugin"
)

func TestDecodeCatalogIconSVGAndRaster(t *testing.T) {
	svgRaw := []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><rect width="24" height="24" fill="#9146FF"/></svg>`)
	svgImg, err := decodeCatalogIcon(svgRaw)
	if err != nil || svgImg == nil || svgImg.Bounds().Dx() == 0 {
		t.Fatalf("decodeCatalogIcon(svg) = (%v, %v)", svgImg, err)
	}

	rgba := image.NewRGBA(image.Rect(0, 0, 8, 8))
	rgba.Set(0, 0, color.RGBA{R: 56, G: 189, B: 248, A: 255})
	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, rgba); err != nil {
		t.Fatalf("png.Encode() error = %v", err)
	}
	pngImg, err := decodeCatalogIcon(pngBuf.Bytes())
	if err != nil || pngImg == nil || pngImg.Bounds().Dx() != 8 {
		t.Fatalf("decodeCatalogIcon(png) = (%v, %v)", pngImg, err)
	}

	if _, err := decodeCatalogIcon([]byte("invalid-image")); err == nil {
		t.Fatal("expected error decoding invalid icon payload")
	}
	if _, err := decodeCatalogIcon(nil); err == nil {
		t.Fatal("expected error decoding empty icon payload")
	}
}

func walkUIObjects(root fyne.CanvasObject, visit func(fyne.CanvasObject)) {
	seen := make(map[fyne.CanvasObject]struct{})
	var walk func(fyne.CanvasObject)
	walk = func(obj fyne.CanvasObject) {
		if obj == nil {
			return
		}
		if _, ok := seen[obj]; ok {
			return
		}
		seen[obj] = struct{}{}
		visit(obj)
		switch v := obj.(type) {
		case *fyne.Container:
			for _, child := range v.Objects {
				walk(child)
			}
		case *container.Scroll:
			walk(v.Content)
		case *container.AppTabs:
			for _, item := range v.Items {
				walk(item.Content)
			}
		default:
			if w, ok := obj.(fyne.Widget); ok {
				if r := test.WidgetRenderer(w); r != nil {
					for _, child := range r.Objects() {
						walk(child)
					}
				}
			}
		}
	}
	walk(root)
}

func findUIButton(root fyne.CanvasObject, prefix string) *widget.Button {
	var found *widget.Button
	walkUIObjects(root, func(obj fyne.CanvasObject) {
		if found != nil {
			return
		}
		if b, ok := obj.(*widget.Button); ok && strings.HasPrefix(b.Text, prefix) {
			found = b
		}
	})
	return found
}

func findUICheck(root fyne.CanvasObject, label string) *widget.Check {
	var found *widget.Check
	walkUIObjects(root, func(obj fyne.CanvasObject) {
		if found != nil {
			return
		}
		if c, ok := obj.(*widget.Check); ok && c.Text == label {
			found = c
		}
	})
	return found
}

func findUIEntry(root fyne.CanvasObject, placeholderSubstr string) *widget.Entry {
	var found *widget.Entry
	walkUIObjects(root, func(obj fyne.CanvasObject) {
		if found != nil {
			return
		}
		if e, ok := obj.(*widget.Entry); ok && strings.Contains(e.PlaceHolder, placeholderSubstr) {
			found = e
		}
	})
	return found
}

func countUILabelsContaining(root fyne.CanvasObject, substr string) int {
	cnt := 0
	walkUIObjects(root, func(obj fyne.CanvasObject) {
		if l, ok := obj.(*widget.Label); ok && strings.Contains(l.Text, substr) {
			cnt++
		}
	})
	return cnt
}

func waitForUIConditionLong(t *testing.T, timeout time.Duration, desc string, overlay fyne.CanvasObject, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		drainUI()
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	drainUI()
	if !cond() {
		var labels []string
		if overlay != nil {
			walkUIObjects(overlay, func(obj fyne.CanvasObject) {
				if l, ok := obj.(*widget.Label); ok && strings.TrimSpace(l.Text) != "" {
					labels = append(labels, l.Text)
				}
			})
		}
		t.Fatalf("timed out waiting for %s (labels=%q)", desc, labels)
	}
}

func TestMainWindowPluginUpdateBadgeAndStartupPreference(t *testing.T) {
	_, _, mw := newTestStudio(t)

	if mw.pluginsTabItem.Text != "Plugins" {
		t.Fatalf("initial Plugins tab title = %q, want Plugins", mw.pluginsTabItem.Text)
	}

	mw.applyPluginUpdateBadge(pluginmgr.UpdateSummary{
		Updates: []pluginmgr.PluginUpdate{
			{UUID: "com.example.a", Name: "Alpha", LatestVersion: "1.1.0"},
		},
	})
	if mw.pluginsTabItem.Text != "Plugins (1 update)" {
		t.Fatalf("1 update tab title = %q, want %q", mw.pluginsTabItem.Text, "Plugins (1 update)")
	}
	if !strings.Contains(mw.statusNote, "1 plugin update(s) available") {
		t.Fatalf("expected status bar notice for 1 update, got %q", mw.statusNote)
	}

	mw.applyPluginUpdateBadge(pluginmgr.UpdateSummary{
		Updates: []pluginmgr.PluginUpdate{
			{UUID: "com.example.a", Name: "Alpha", LatestVersion: "1.1.0"},
			{UUID: "com.example.b", Name: "Beta", LatestVersion: "2.0.0"},
		},
	})
	if mw.pluginsTabItem.Text != "Plugins (2 updates)" {
		t.Fatalf("2 updates tab title = %q, want %q", mw.pluginsTabItem.Text, "Plugins (2 updates)")
	}

	mw.applyPluginUpdateBadge(pluginmgr.UpdateSummary{})
	if mw.pluginsTabItem.Text != "Plugins" {
		t.Fatalf("cleared tab title = %q, want Plugins", mw.pluginsTabItem.Text)
	}

	// Verify startup plugin update checkbox in General Settings updates preference.
	mw.ShowSettingsTab(settingsTabGeneral)
	drainUI()
	chk := findUICheck(mw.settingsTabItem.Content, "Check for plugin updates on startup")
	if chk == nil {
		t.Fatal("expected 'Check for plugin updates on startup' checkbox in General Settings")
	}
	if !chk.Checked {
		t.Fatal("expected startup plugin update check enabled by default")
	}
	chk.SetChecked(false)
	if mw.app.Preferences().BoolWithFallback(prefCheckPluginUpdatesOnStartup, true) {
		t.Fatal("expected startup plugin update preference saved as false")
	}
}

func buildWasmTestBinary(t *testing.T, uuid, name, version string) []byte {
	t.Helper()
	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s) error = %v", srcDir, err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "go.mod"), []byte("module uitestwasm\n\ngo 1.27\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(go.mod) error = %v", err)
	}
	code := fmt.Sprintf(`package main

import (
	"encoding/json"
	"unsafe"
)

var allocations = map[uint32][]byte{}

//go:wasmexport pc_alloc
func pcAlloc(size uint32) uint32 {
	if size == 0 {
		size = 1
	}
	buf := make([]byte, size)
	ptr := uint32(uintptr(unsafe.Pointer(&buf[0])))
	allocations[ptr] = buf
	return ptr
}

//go:wasmexport pc_free
func pcFree(ptr uint32) {
	delete(allocations, ptr)
}

func returnHostBytes(b []byte) uint64 {
	ptr := pcAlloc(uint32(len(b)))
	copy(allocations[ptr], b)
	return (uint64(ptr) << 32) | uint64(len(b))
}

//go:wasmexport pc_manifest
func pcManifest() uint64 {
	manifest := map[string]any{
		"uuid":    %q,
		"name":    %q,
		"version": %q,
		"author":  "PoundControl",
		"permissions": map[string]any{
			"allowedHosts": []string{"api.twitch.tv"},
		},
		"actions": []map[string]any{
			{"uuid": %q, "name": "Curated Action", "category": %q},
		},
	}
	raw, _ := json.Marshal(manifest)
	return returnHostBytes(raw)
}

//go:wasmexport pc_handle_event
func pcHandleEvent(ptr, size uint32) int32 {
	return 0
}

func main() {}
`, uuid, name, version, uuid+".action", name)
	if err := os.WriteFile(filepath.Join(srcDir, "main.go"), []byte(code), 0o600); err != nil {
		t.Fatalf("WriteFile(main.go) error = %v", err)
	}
	outPath := filepath.Join(tmp, "plugin.wasm")
	cmd := exec.Command("go", "build", "-buildmode=c-shared", "-o", outPath, ".")
	cmd.Dir = srcDir
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm", "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("compile wasip1 fixture failed: %v\n%s", err, string(out))
	}
	raw, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", outPath, err)
	}
	return raw
}

func TestCuratedPluginsDialogSearchInstallAndUpdate(t *testing.T) {
	drainUI()
	testApp := test.NewApp()
	t.Cleanup(func() {
		drainUI()
		testApp.Quit()
	})
	testApp.Settings().SetTheme(&StudioTheme{})

	const twitchUUID = "com.github.mrpoundsign.poundcontrol.twitch"
	v1Bytes := buildWasmTestBinary(t, twitchUUID, "Twitch", "0.1.0")
	v2Bytes := buildWasmTestBinary(t, twitchUUID, "Twitch", "0.2.0")
	v1Sum := sha256.Sum256(v1Bytes)
	v2Sum := sha256.Sum256(v2Bytes)
	v1Hex := hex.EncodeToString(v1Sum[:])
	v2Hex := hex.EncodeToString(v2Sum[:])

	var (
		mu         sync.Mutex
		curTag     = "v0.1.0"
		curAsset   = v1Bytes
		curHex     = v1Hex
		srvURL     string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		tag := curTag
		asset := curAsset
		sumHex := curHex
		baseURL := srvURL
		mu.Unlock()

		switch r.URL.Path {
		case "/catalog.json":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"version": 1,
				"plugins": []map[string]any{
					{
						"id":           twitchUUID,
						"name":         "Twitch",
						"kind":         "wasm",
						"author":       "PoundControl",
						"category":     "Streaming",
						"description":  "Twitch controls",
						"repository":   "https://github.com/mrpoundsign/PoundControl-Twitch",
						"assetName":    "poundcontrol-twitch.wasm",
						"icon":         baseURL + "/logo.svg",
						"allowedHosts": []string{"id.twitch.tv", "api.twitch.tv"},
						"featured":     true,
					},
					{
						"id":          "com.example.other",
						"name":        "Audio Helper",
						"kind":        "sdplugin",
						"author":      "AudioDev",
						"category":    "Audio",
						"description": "Extra audio helper",
						"repository":  "https://github.com/example/audio-helper",
					},
				},
			})
		case "/logo.svg":
			w.Header().Set("Content-Type", "image/svg+xml")
			_, _ = w.Write([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><rect width="24" height="24" fill="#9146FF"/></svg>`))
		case "/repos/mrpoundsign/PoundControl-Twitch/releases/latest":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tag_name": tag,
				"name":     tag,
				"body":     "- Release " + tag,
				"assets": []map[string]any{
					{
						"name":                 "poundcontrol-twitch.wasm",
						"browser_download_url": baseURL + "/downloads/poundcontrol-twitch.wasm",
						"size":                 len(asset),
					},
					{
						"name":                 "checksums.txt",
						"browser_download_url": baseURL + "/downloads/checksums.txt",
					},
				},
			})
		case "/downloads/poundcontrol-twitch.wasm":
			_, _ = w.Write(asset)
		case "/downloads/checksums.txt":
			_, _ = w.Write([]byte(sumHex + "  poundcontrol-twitch.wasm\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	mu.Lock()
	srvURL = srv.URL
	mu.Unlock()

	tmp := t.TempDir()
	reg := plugin.NewRegistry()
	if err := reg.RegisterProvider(core.New()); err != nil {
		t.Fatalf("RegisterProvider(core) error = %v", err)
	}
	wasmHost := wasmplugin.NewProvider(wasmplugin.Options{
		DefaultPluginsDir: filepath.Join(tmp, "wasm"),
	})
	if err := reg.RegisterProvider(wasmHost); err != nil {
		t.Fatalf("RegisterProvider(wasmHost) error = %v", err)
	}
	sdHost := sdplugin.NewProvider(sdplugin.Options{
		DefaultPluginsDir: filepath.Join(tmp, "plugins"),
	})
	if err := reg.RegisterProvider(sdHost); err != nil {
		t.Fatalf("RegisterProvider(sdHost) error = %v", err)
	}

	eng, err := app.NewEngine(deck.NewMemoryStore(nil), reg)
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	t.Cleanup(func() {
		_ = wasmHost.Close()
		_ = sdHost.Close()
		_ = eng.Close()
		drainUI()
	})

	curatedCli := &pluginmgr.CuratedClient{
		CatalogURL:       srv.URL + "/catalog.json",
		GitHubAPIBaseURL: srv.URL,
		HTTPClient:       srv.Client(),
	}
	oaCli := &sdplugin.CatalogueClient{
		CatalogueURL:     srv.URL + "/openaction.json",
		GitHubAPIBaseURL: srv.URL,
		HTTPClient:       srv.Client(),
	}
	mgr := pluginmgr.NewManager(curatedCli, oaCli, wasmHost, sdHost)
	mgr.GitHubAPIBaseURL = srv.URL
	mgr.HTTPClient = srv.Client()

	mw := NewMainWindow(testApp, eng)
	mw.SetWasmPluginController(wasmHost)
	mw.SetSDPluginController(sdHost)
	mw.SetPluginManager(mgr)
	mw.ShowSettingsTab(settingsTabPlugins)
	drainUI()

	// Open Curated Plugins dialog via Plugins tab button.
	browseBtn := findUIButton(mw.pluginsTabItem.Content, "Browse Curated Plugins")
	if browseBtn == nil {
		t.Fatal("expected 'Browse Curated Plugins...' button on Plugins tab")
	}
	test.Tap(browseBtn)
	drainUI()

	topOverlay := mw.win.Canvas().Overlays().Top()
	if topOverlay == nil {
		t.Fatal("expected Curated Plugins modal overlay")
	}
	t.Cleanup(func() {
		for _, ov := range mw.win.Canvas().Overlays().List() {
			ov.Hide()
		}
	})

	waitForUIConditionLong(t, 6*time.Second, "curated plugins list load", topOverlay, func() bool {
		return countUILabelsContaining(topOverlay, "Twitch [wasm] [Featured]") == 1 &&
			countUILabelsContaining(topOverlay, "Audio Helper [sdplugin]") == 1
	})

	// Search filters list down to Twitch only.
	searchEnt := findUIEntry(topOverlay, "Search curated plugins")
	if searchEnt == nil {
		t.Fatal("expected search entry in Curated Plugins dialog")
	}
	searchEnt.SetText("twitch")
	drainUI()
	if countUILabelsContaining(topOverlay, "Audio Helper") != 0 || countUILabelsContaining(topOverlay, "Twitch") == 0 {
		t.Fatal("expected search filter to show only Twitch")
	}

	// Tap Install on Twitch -> installs v0.1.0 and refreshes Action Catalog.
	installBtn := findUIButton(topOverlay, "Install")
	if installBtn == nil {
		t.Fatal("expected Install button for Twitch")
	}
	test.Tap(installBtn)
	waitForUIConditionLong(t, 12*time.Second, "Twitch v0.1.0 install", topOverlay, func() bool {
		return countUILabelsContaining(topOverlay, "[Installed]") == 1 &&
			mw.catalogButtons[twitchUUID+".action"] != nil
	})

	// Close modal, publish v0.2.0 on mock server, trigger Check Updates -> badge updates to "(1 update)".
	topOverlay.Hide()
	drainUI()

	mu.Lock()
	curTag = "v0.2.0"
	curAsset = v2Bytes
	curHex = v2Hex
	mu.Unlock()

	checkBtn := findUIButton(mw.pluginsTabItem.Content, "Check for Plugin Updates")
	if checkBtn == nil {
		t.Fatal("expected Check for Plugin Updates button on Plugins tab")
	}
	test.Tap(checkBtn)
	waitForUIConditionLong(t, 12*time.Second, "plugin update detected", mw.pluginsTabItem.Content, func() bool {
		return mw.pluginsTabItem.Text == "Plugins (1 update)" &&
			findUIButton(mw.pluginsTabItem.Content, "Update All (1)") != nil
	})

	// Tap Update All (1) -> upgrades Twitch to 0.2.0 and clears badge back to "Plugins".
	updAllBtn := findUIButton(mw.pluginsTabItem.Content, "Update All (1)")
	test.Tap(updAllBtn)
	waitForUIConditionLong(t, 12*time.Second, "plugin update applied", mw.pluginsTabItem.Content, func() bool {
		if mw.pluginsTabItem.Text != "Plugins" {
			return false
		}
		for _, st := range wasmHost.Plugins() {
			if st.UUID == twitchUUID && st.Version == "0.2.0" {
				return true
			}
		}
		return false
	})
}
