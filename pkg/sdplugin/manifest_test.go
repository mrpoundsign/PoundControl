package sdplugin

import (
	"os"
	"path/filepath"
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
