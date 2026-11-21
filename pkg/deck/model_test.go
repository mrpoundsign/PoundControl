package deck

import "testing"

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

	cp := cfg.Clone()
	cp.Profiles[0].Pages[0].Slots["0,0"].Title = "Mutated"
	cp.Profiles[0].Pages[0].Slots["0,0"].Settings["nested"].(map[string]any)["count"] = 99
	cp.Profiles[0].Pages[0].Slots["0,0"].Settings["items"].([]any)[0] = "z"

	orig := cfg.Profiles[0].Pages[0].Slots["0,0"]
	if orig.Title != "Original" {
		t.Errorf("Clone leaked Title mutation: got %q", orig.Title)
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
