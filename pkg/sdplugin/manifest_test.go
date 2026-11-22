package sdplugin

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/mrpoundsign/PoundControl/pkg/deck"
	"github.com/mrpoundsign/PoundControl/pkg/plugin"
)

func createSyntheticBundle(t *testing.T, parentDir, folderName, manifestJSON, htmlPI string) string {
	t.Helper()
	bundleDir := filepath.Join(parentDir, folderName)
	for _, sub := range []string{"bin", "imgs", "property-inspector"} {
		if err := os.MkdirAll(filepath.Join(bundleDir, sub), 0o755); err != nil {
			t.Fatalf("MkdirAll(%s) error = %v", sub, err)
		}
	}
	if err := os.WriteFile(filepath.Join(bundleDir, "manifest.json"), []byte(manifestJSON), 0o600); err != nil {
		t.Fatalf("WriteFile(manifest.json) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(bundleDir, "bin", "plugin.js"), []byte("// plugin"), 0o600); err != nil {
		t.Fatalf("WriteFile(plugin.js) error = %v", err)
	}
	if htmlPI != "" {
		if err := os.WriteFile(filepath.Join(bundleDir, "property-inspector", "index.html"), []byte(htmlPI), 0o600); err != nil {
			t.Fatalf("WriteFile(index.html) error = %v", err)
		}
	}
	return bundleDir
}

func TestLoadBundleAndDiscoverBundles(t *testing.T) {
	root := t.TempDir()
	manifest := `{
		"SDKVersion": 2,
		"Name": "Demo Plugin",
		"Author": "Tester",
		"Version": "1.2.3",
		"Category": "Demo",
		"CodePath": "bin/plugin.js",
		"PropertyInspectorPath": "property-inspector/index.html",
		"Nodejs": {"Version": "20"},
		"Actions": [
			{
				"UUID": "com.example.demo.action",
				"Name": "Demo Action",
				"Tooltip": "Run demo",
				"Controllers": ["Keypad", "Encoder"],
				"States": [{"Image": "imgs/state0", "ShowTitle": false}]
			},
			{
				"UUID": "com.example.demo.hidden",
				"Name": "Hidden Action",
				"VisibleInActionsList": false
			}
		]
	}`
	piHTML := `<div data-action="com.example.demo.action"><label>Mode</label><select data-setting="mode"><option value="a">A</option><option value="b">B</option></select></div>`
	bundleDir := createSyntheticBundle(t, root, "com.example.demo.sdPlugin", manifest, piHTML)

	found := DiscoverBundles([]string{root, bundleDir})
	if len(found) != 1 || found[0] != bundleDir {
		t.Fatalf("DiscoverBundles() = %v, want [%s]", found, bundleDir)
	}

	loaded, err := LoadBundle(bundleDir)
	if err != nil {
		t.Fatalf("LoadBundle() error = %v", err)
	}
	if loaded.UUID != "com.example.demo" {
		t.Fatalf("derived UUID = %q, want com.example.demo", loaded.UUID)
	}
	if !loaded.IsNode || loaded.MinNodeMajor != 20 {
		t.Fatalf("expected IsNode=true MinNodeMajor=20, got %v/%d", loaded.IsNode, loaded.MinNodeMajor)
	}
	if len(loaded.Actions) != 1 {
		t.Fatalf("expected 1 visible action, got %d", len(loaded.Actions))
	}
	act := loaded.Actions[0]
	if !act.HideTitle || !act.SupportsController(deck.ControllerEncoder) || act.Provider != plugin.ProviderStreamDeck {
		t.Fatalf("unexpected action metadata: %+v", act)
	}
	if len(act.SettingsSchema) != 1 || act.SettingsSchema[0].Key != "mode" || act.SettingsSchema[0].Type != plugin.FieldSelect {
		t.Fatalf("expected HTML-extracted select field 'mode', got %+v", act.SettingsSchema)
	}

	// Sidecar poundcontrol.schema.json overrides HTML extraction cleanly.
	sidecar := `{"com.example.demo.action":[{"key":"token","label":"API Token","type":"password","required":true}]}`
	if err := os.WriteFile(filepath.Join(bundleDir, "poundcontrol.schema.json"), []byte(sidecar), 0o600); err != nil {
		t.Fatalf("WriteFile(poundcontrol.schema.json) error = %v", err)
	}
	loaded2, err := LoadBundle(bundleDir)
	if err != nil {
		t.Fatalf("LoadBundle(sidecar) error = %v", err)
	}
	if len(loaded2.Actions[0].SettingsSchema) != 1 || loaded2.Actions[0].SettingsSchema[0].Type != plugin.FieldPassword {
		t.Fatalf("expected sidecar schema override, got %+v", loaded2.Actions[0].SettingsSchema)
	}

	if _, safe := ResolveAssetPath(bundleDir, "../outside.txt"); safe {
		t.Fatal("expected traversal path '../outside.txt' to be rejected")
	}
}

func TestLoadBundleEncryptedManifestDetection(t *testing.T) {
	root := t.TempDir()
	bundleDir := filepath.Join(root, "com.elgato.twitch.sdPlugin")
	if err := os.MkdirAll(bundleDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	payload := append([]byte("ELGATO\x01\x00"), 0xDE, 0xAD, 0xBE, 0xEF)
	if err := os.WriteFile(filepath.Join(bundleDir, "manifest.json"), payload, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, err := LoadBundle(bundleDir)
	if !errors.Is(err, ErrEncryptedManifest) {
		t.Fatalf("LoadBundle() error = %v, want ErrEncryptedManifest", err)
	}
}

func TestResolveCodePathPrecedence(t *testing.T) {
	m := SDManifest{
		CodePath:      "bin/universal.js",
		CodePathWin:   "bin/win.exe",
		CodePathMac:   "bin/mac",
		CodePathLin:   "bin/lin",
		CodePathLinux: "bin/linux",
		CodePaths: map[string]string{
			"x86_64-pc-windows-msvc":    "bin/win-x64.exe",
			"aarch64-apple-darwin":      "bin/mac-arm64",
			"aarch64-unknown-linux-gnu": "bin/linux-arm64",
		},
	}

	if got := m.ResolveCodePathFor("windows", "amd64"); got != "bin/win-x64.exe" {
		t.Fatalf("windows/amd64 = %q, want bin/win-x64.exe", got)
	}
	if got := m.ResolveCodePathFor("windows", "arm64"); got != "bin/win.exe" {
		t.Fatalf("windows/arm64 = %q, want bin/win.exe fallback", got)
	}
	if got := m.ResolveCodePathFor("darwin", "arm64"); got != "bin/mac-arm64" {
		t.Fatalf("darwin/arm64 = %q, want bin/mac-arm64", got)
	}
	if got := m.ResolveCodePathFor("darwin", "amd64"); got != "bin/mac" {
		t.Fatalf("darwin/amd64 = %q, want bin/mac fallback", got)
	}
	if got := m.ResolveCodePathFor("linux", "arm64"); got != "bin/linux-arm64" {
		t.Fatalf("linux/arm64 = %q, want bin/linux-arm64", got)
	}
	if got := m.ResolveCodePathFor("linux", "amd64"); got != "bin/lin" {
		t.Fatalf("linux/amd64 = %q, want bin/lin", got)
	}

	m2 := SDManifest{CodePath: "bin/universal.js", CodePathLinux: "bin/linux"}
	if got := m2.ResolveCodePathFor("linux", "amd64"); got != "bin/linux" {
		t.Fatalf("linux/amd64 without CodePathLin = %q, want bin/linux", got)
	}
	if got := m2.ResolveCodePathFor("windows", "amd64"); got != "bin/universal.js" {
		t.Fatalf("windows/amd64 universal fallback = %q, want bin/universal.js", got)
	}
}

func TestManifestOverlayAndLocalization(t *testing.T) {
	root := t.TempDir()
	baseManifest := `{
		"SDKVersion": 2,
		"Name": "Base Plugin",
		"Category": "BaseCat",
		"Icon": "imgs/plugin",
		"CategoryIcon": "imgs/cat",
		"CodePath": "bin/plugin.js",
		"Actions": [
			{
				"UUID": "com.example.oa.toggle",
				"Name": "Raw Toggle",
				"Tooltip": "Raw tip",
				"Icon": "imgs/action",
				"States": [{"Image": "imgs/state0", "Title": "RawTitle"}]
			}
		]
	}`
	bundleDir := createSyntheticBundle(t, root, "com.example.oa.sdPlugin", baseManifest, "")
	if err := os.WriteFile(filepath.Join(bundleDir, "bin", "override.js"), []byte("// override"), 0o600); err != nil {
		t.Fatalf("WriteFile(override.js) error = %v", err)
	}

	overlayName := manifestOverlayFilename(runtime.GOOS)
	if overlayName == "" {
		t.Fatalf("expected non-empty overlay filename for GOOS=%s", runtime.GOOS)
	}
	overlayJSON := `{"CodePath": "bin/override.js", "Version": "2.5.0"}`
	if err := os.WriteFile(filepath.Join(bundleDir, overlayName), []byte(overlayJSON), 0o600); err != nil {
		t.Fatalf("WriteFile(overlay) error = %v", err)
	}

	enJSON := `{
		"Name": "Localized Plugin",
		"Category": "Localized Cat",
		"com.example.oa.toggle": {
			"Name": "Localized Toggle",
			"Tooltip": "Localized tooltip",
			"States": [{"Title": "Localized Title"}]
		}
	}`
	if err := os.WriteFile(filepath.Join(bundleDir, "en.json"), []byte(enJSON), 0o600); err != nil {
		t.Fatalf("WriteFile(en.json) error = %v", err)
	}

	loaded, err := LoadBundle(bundleDir)
	if err != nil {
		t.Fatalf("LoadBundle() error = %v", err)
	}
	if loaded.CodePath != "bin/override.js" {
		t.Fatalf("CodePath = %q, want bin/override.js from OS overlay", loaded.CodePath)
	}
	if loaded.Manifest.Version != "2.5.0" {
		t.Fatalf("Version = %q, want 2.5.0 from OS overlay", loaded.Manifest.Version)
	}
	if loaded.Manifest.Name != "Localized Plugin" {
		t.Fatalf("Name = %q, want Localized Plugin", loaded.Manifest.Name)
	}
	if len(loaded.Actions) != 1 {
		t.Fatalf("expected 1 action, got %d", len(loaded.Actions))
	}
	act := loaded.Actions[0]
	if act.Name != "Localized Toggle" || act.Tooltip != "Localized tooltip" || act.DefaultTitle != "Localized Title" || act.Category != "Localized Cat" {
		t.Fatalf("unexpected localized action: %+v", act)
	}
	if act.Icon != filepath.Join(bundleDir, "imgs", "action") {
		t.Fatalf("act.Icon = %q, want imgs/action", act.Icon)
	}
	pm := loaded.ToPluginManifest()
	if pm.Icon != filepath.Join(bundleDir, "imgs", "plugin") || pm.CategoryIcon != filepath.Join(bundleDir, "imgs", "cat") {
		t.Fatalf("unexpected manifest icons: Icon=%q CategoryIcon=%q", pm.Icon, pm.CategoryIcon)
	}
}

func TestLoadBundleWindowsExeFallback(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows-specific .exe CodePath fallback test")
	}
	root := t.TempDir()
	bundleDir := filepath.Join(root, "com.example.dotnet.sdPlugin")
	if err := os.MkdirAll(bundleDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	manifest := `{
		"SDKVersion": 2,
		"UUID": "com.example.dotnet",
		"Name": "DotNet Plugin",
		"CodePath": "StreamDeck.ColorPicker",
		"Actions": [{"UUID": "com.example.dotnet.pick", "Name": "Pick"}]
	}`
	if err := os.WriteFile(filepath.Join(bundleDir, "manifest.json"), []byte(manifest), 0o600); err != nil {
		t.Fatalf("WriteFile(manifest.json) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(bundleDir, "StreamDeck.ColorPicker.exe"), []byte("MZ"), 0o600); err != nil {
		t.Fatalf("WriteFile(exe) error = %v", err)
	}

	loaded, err := LoadBundle(bundleDir)
	if err != nil {
		t.Fatalf("LoadBundle() error = %v", err)
	}
	if loaded.CodePath != "StreamDeck.ColorPicker.exe" {
		t.Fatalf("CodePath = %q, want StreamDeck.ColorPicker.exe", loaded.CodePath)
	}
}
