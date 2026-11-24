package deck

import (
	"encoding/json"
	"slices"
	"testing"
)

func TestNewUUIDAndDefaultConfig(t *testing.T) {
	u1 := NewUUID()
	u2 := NewUUID()
	if len(u1) != 36 || u1 == u2 {
		t.Fatalf("unexpected UUIDs: %q, %q", u1, u2)
	}

	cfg := DefaultConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("DefaultConfig().Validate() error = %v", err)
	}
	if cfg.SchemaVersion != CurrentSchemaVersion {
		t.Fatalf("SchemaVersion = %d, want %d", cfg.SchemaVersion, CurrentSchemaVersion)
	}
	if len(cfg.Profiles) != 1 || cfg.Profiles[0].Grid != DefaultGrid() {
		t.Fatalf("unexpected default profiles: %+v", cfg.Profiles)
	}
}

func TestConfigCloneIsolationAndValidate(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Profiles[0].Pages[0].Slots["0,0"] = &SlotConfig{
		Context:    NewUUID(),
		ActionUUID: "com.example.act",
		Title:      "Original",
		Settings: map[string]any{
			"nested": map[string]any{"count": 1},
			"items":  []any{"a", "b"},
		},
	}

	cfg.PluginSettings["com.example.obs"] = map[string]any{"host": "127.0.0.1", "port": float64(4455)}

	cp := cfg.Clone()
	cp.Profiles[0].Pages[0].Slots["0,0"].Title = "Mutated"
	cp.Profiles[0].Pages[0].Slots["0,0"].Settings["nested"].(map[string]any)["count"] = 99
	cp.Profiles[0].Pages[0].Slots["0,0"].Settings["items"].([]any)[0] = "z"
	cp.PluginSettings["com.example.obs"]["host"] = "mutated"

	orig := cfg.Profiles[0].Pages[0].Slots["0,0"]
	if orig.Title != "Original" {
		t.Errorf("Clone leaked Title mutation: got %q", orig.Title)
	}
	if got := cfg.PluginSettings["com.example.obs"]["host"]; got != "127.0.0.1" {
		t.Errorf("Clone leaked PluginSettings mutation: got %v", got)
	}
	if got := orig.Settings["nested"].(map[string]any)["count"]; got != 1 {
		t.Errorf("Clone leaked nested map mutation: got %v", got)
	}
	if got := orig.Settings["items"].([]any)[0]; got != "a" {
		t.Errorf("Clone leaked slice mutation: got %v", got)
	}

	// Validate catches invalid active profile and missing action UUID.
	bad := cfg.Clone()
	bad.ActiveProfileID = "missing"
	if err := bad.Validate(); err == nil {
		t.Error("expected error for missing activeProfileId")
	}

	badSlot := cfg.Clone()
	badSlot.Profiles[0].Pages[0].Slots["0,0"].ActionUUID = ""
	if err := badSlot.Validate(); err == nil {
		t.Error("expected error for empty slot ActionUUID")
	}
}

func TestColorPaletteNormalizeHexDedupeAndEmptyPreservation(t *testing.T) {
	if hex, ok := NormalizeHexColor("38bdf8"); !ok || hex != "#38BDF8" {
		t.Fatalf("NormalizeHexColor(38bdf8) = (%q, %v), want (#38BDF8, true)", hex, ok)
	}
	if hex, ok := NormalizeHexColor("#0fc"); !ok || hex != "#00FFCC" {
		t.Fatalf("NormalizeHexColor(#0fc) = (%q, %v), want (#00FFCC, true)", hex, ok)
	}
	if _, ok := NormalizeHexColor("#12345"); ok {
		t.Fatal("expected invalid 5-char hex rejected")
	}

	// Nil palette seeds built-in defaults; custom palette deduplicates and normalizes.
	norm := NormalizeColorPalette([]ColorSwatch{
		{Name: " Primary ", Hex: "38bdf8"},
		{Name: "Duplicate Lowercase", Hex: "#38BDF8"},
		{Name: "", Hex: "#abc"},
		{Name: "Bad", Hex: "not-hex"},
	})
	if len(norm) != 2 || norm[0] != (ColorSwatch{Name: "Primary", Hex: "#38BDF8"}) || norm[1] != (ColorSwatch{Name: "#AABBCC", Hex: "#AABBCC"}) {
		t.Fatalf("unexpected normalized palette: %+v", norm)
	}

	// Empty non-nil palette survives JSON roundtrip and Normalize().
	cfg := DefaultConfig()
	cfg.ColorPalette = []ColorSwatch{}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	var decoded Config
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	decoded.Normalize()
	if decoded.ColorPalette == nil || len(decoded.ColorPalette) != 0 {
		t.Fatalf("expected intentionally empty ColorPalette preserved across JSON roundtrip, got %#v", decoded.ColorPalette)
	}

	// SlotConfig Clone & Normalize preserve BackgroundColor, BackgroundImage, HideTitle.
	slot := &SlotConfig{
		ActionUUID:      "test.act",
		Color:           "38bdf8",
		BackgroundColor: "#0f0",
		BackgroundImage: " /tmp/bg.png ",
		HideTitle:       true,
	}
	cfg.Profiles[0].Pages[0].Slots["0,0"] = slot
	cfg.Normalize()
	cp := cfg.Clone()
	got := cp.Profiles[0].Pages[0].Slots["0,0"]
	if got.Color != "#38BDF8" || got.BackgroundColor != "#00FF00" || got.BackgroundImage != "/tmp/bg.png" || !got.HideTitle {
		t.Fatalf("unexpected normalized slot background fields: %+v", got)
	}
}

func TestConfigDisabledPluginsNormalizeCloneAndJSON(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DisabledPlugins = []string{"  com.example.b ", "", "com.example.a", "com.example.b"}
	cfg.Normalize()

	want := []string{"com.example.a", "com.example.b"}
	if !slices.Equal(cfg.DisabledPlugins, want) {
		t.Fatalf("DisabledPlugins = %v, want %v", cfg.DisabledPlugins, want)
	}

	cp := cfg.Clone()
	cp.DisabledPlugins[0] = "mutated"
	if cfg.DisabledPlugins[0] != "com.example.a" {
		t.Fatalf("Clone leaked DisabledPlugins mutation: %v", cfg.DisabledPlugins)
	}

	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	var decoded Config
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	decoded.Normalize()
	if !slices.Equal(decoded.DisabledPlugins, want) {
		t.Fatalf("DisabledPlugins after JSON roundtrip = %v, want %v", decoded.DisabledPlugins, want)
	}
}
