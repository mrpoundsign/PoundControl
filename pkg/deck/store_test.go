package deck

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMemoryStoreIsolation(t *testing.T) {
	store := NewMemoryStore(nil)
	cfg1, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	cfg1.Profiles[0].Name = "MutatedOutsideSave"

	cfg2, err := store.Load()
	if err != nil {
		t.Fatalf("Load() second error = %v", err)
	}
	if cfg2.Profiles[0].Name != "Default" {
		t.Fatalf("MemoryStore leaked un-saved mutation: got %q", cfg2.Profiles[0].Name)
	}

	cfg2.Profiles[0].Name = "Streaming"
	if err := store.Save(cfg2); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	cfg3, err := store.Load()
	if err != nil {
		t.Fatalf("Load() after Save error = %v", err)
	}
	if cfg3.Profiles[0].Name != "Streaming" {
		t.Fatalf("expected persisted profile name Streaming, got %q", cfg3.Profiles[0].Name)
	}

	if err := store.Save(nil); err == nil {
		t.Error("expected error saving nil config")
	}
}

func TestFileStoreAtomicSaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "config.json")
	store := NewFileStore(path)

	// Missing file returns valid DefaultConfig without creating file yet.
	initial, err := store.Load()
	if err != nil {
		t.Fatalf("Load() on missing file error = %v", err)
	}
	if err := initial.Validate(); err != nil {
		t.Fatalf("initial DefaultConfig invalid: %v", err)
	}

	// Save custom profile + slot.
	initial.Profiles[0].Name = "Studio"
	initial.Profiles[0].Pages[0].Slots["1,2"] = &SlotConfig{
		Context:    NewUUID(),
		ActionUUID: "com.github.mrpoundsign.pounddeck.core.counter",
		Title:      "Clicks",
		Settings:   map[string]any{"initial": float64(5)},
	}
	if err := store.Save(initial); err != nil {
		t.Fatalf("first Save() error = %v", err)
	}

	// Second save exercises atomic replacement on existing destination file.
	initial.Profiles[0].Pages[0].Slots["1,2"].Title = "Clicks Updated"
	if err := store.Save(initial); err != nil {
		t.Fatalf("second Save() error = %v", err)
	}

	reloaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load() after Save error = %v", err)
	}
	if reloaded.Profiles[0].Name != "Studio" {
		t.Errorf("reloaded profile name = %q, want Studio", reloaded.Profiles[0].Name)
	}
	gotSlot := reloaded.Profiles[0].Pages[0].Slots["1,2"]
	if gotSlot == nil || gotSlot.Title != "Clicks Updated" {
		t.Fatalf("unexpected reloaded slot: %+v", gotSlot)
	}

	// Ensure no leftover temp files remain.
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("ReadDir error = %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "config.json" {
		t.Errorf("expected only config.json in dir, got %d entries", len(entries))
	}

	// Corrupted JSON returns an error.
	if err := os.WriteFile(path, []byte("{broken json"), 0o600); err != nil {
		t.Fatalf("WriteFile error = %v", err)
	}
	if _, err := store.Load(); err == nil {
		t.Error("expected error loading corrupted JSON")
	}
}
