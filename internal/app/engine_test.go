package app

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/mrpoundsign/PoundDeck/pkg/builtin/core"
	"github.com/mrpoundsign/PoundDeck/pkg/deck"
	"github.com/mrpoundsign/PoundDeck/pkg/plugin"
)

func newTestEngine(t *testing.T, store deck.Store) *Engine {
	t.Helper()
	reg := plugin.NewRegistry()
	if err := reg.RegisterProvider(core.New()); err != nil {
		t.Fatalf("RegisterProvider(core) error = %v", err)
	}
	eng, err := NewEngine(store, reg)
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	return eng
}

func TestEngineProfilesPagesFoldersAndAutoBack(t *testing.T) {
	eng := newTestEngine(t, deck.NewMemoryStore(nil))
	ctx := context.Background()

	var notifyCount atomic.Int32
	unsub := eng.Subscribe(func(Snapshot) {
		notifyCount.Add(1)
	})
	defer unsub()

	// Create second profile with 3x2 grid and 2 dials.
	prof2, err := eng.CreateProfile("Stream", deck.GridSize{Cols: 3, Rows: 2}, 2)
	if err != nil {
		t.Fatalf("CreateProfile() error = %v", err)
	}
	snap := eng.Snapshot()
	if snap.ActiveProfileID != prof2.ID || snap.Grid != (deck.GridSize{Cols: 3, Rows: 2}) || snap.Dials != 2 {
		t.Fatalf("unexpected snapshot after CreateProfile: %+v", snap)
	}

	// Add a second page and test wrapping navigation.
	if _, err := eng.AddPage("Scenes"); err != nil {
		t.Fatalf("AddPage() error = %v", err)
	}
	if snap = eng.Snapshot(); snap.PageIndex != 1 || snap.PageCount != 2 {
		t.Fatalf("unexpected page state: idx=%d count=%d", snap.PageIndex, snap.PageCount)
	}
	if err := eng.NextPage(true); err != nil {
		t.Fatalf("NextPage(wrap=true) error = %v", err)
	}
	if snap = eng.Snapshot(); snap.PageIndex != 0 {
		t.Fatalf("expected wrapped PageIndex 0, got %d", snap.PageIndex)
	}
	if err := eng.PrevPage(true); err != nil {
		t.Fatalf("PrevPage(wrap=true) error = %v", err)
	}
	if snap = eng.Snapshot(); snap.PageIndex != 1 {
		t.Fatalf("expected wrapped PageIndex 1, got %d", snap.PageIndex)
	}

	// Assign OpenFolder at (1, 1) on Page 2 and press it.
	if _, err := eng.AssignKeypadAction(deck.Coordinates{Row: 1, Col: 1}, core.ActionFolderOpen); err != nil {
		t.Fatalf("AssignKeypadAction(FolderOpen) error = %v", err)
	}
	if err := eng.UpdateKeypadSlot(deck.Coordinates{Row: 1, Col: 1}, "OBS Folder", "#FBBF24", map[string]any{
		"folderName": "OBS",
	}); err != nil {
		t.Fatalf("UpdateKeypadSlot() error = %v", err)
	}
	if err := eng.PressKey(ctx, deck.Coordinates{Row: 1, Col: 1}); err != nil {
		t.Fatalf("PressKey(FolderOpen) error = %v", err)
	}

	snap = eng.Snapshot()
	if !snap.InFolder || len(snap.FolderPath) != 1 || snap.FolderPath[0] != "OBS" {
		t.Fatalf("expected inside folder OBS, got InFolder=%v path=%v", snap.InFolder, snap.FolderPath)
	}
	autoBack := snap.Slots[(deck.Coordinates{Row: 0, Col: 0}).SlotKey()]
	if !autoBack.AutoBack || autoBack.Visual.Title != "← Back" {
		t.Fatalf("expected AutoBack tile at (0,0), got %+v", autoBack)
	}

	// Press AutoBack at (0, 0) to pop back out.
	if err := eng.PressKey(ctx, deck.Coordinates{Row: 0, Col: 0}); err != nil {
		t.Fatalf("PressKey(AutoBack) error = %v", err)
	}
	if snap = eng.Snapshot(); snap.InFolder || len(snap.FolderPath) != 0 {
		t.Fatalf("expected folder closed after AutoBack, got %+v", snap.FolderPath)
	}

	// Delete page 1 and delete Stream profile; cannot delete final profile.
	if err := eng.DeletePage(1); err != nil {
		t.Fatalf("DeletePage(1) error = %v", err)
	}
	if err := eng.DeleteProfile(prof2.ID); err != nil {
		t.Fatalf("DeleteProfile(Stream) error = %v", err)
	}
	if err := eng.DeleteProfile(eng.Snapshot().ActiveProfileID); err == nil {
		t.Error("expected error deleting last remaining profile")
	}
	if notifyCount.Load() == 0 {
		t.Error("expected subscriber notifications during mutations")
	}
}

func TestEngineCounterDialMultiActionAndFilePersistence(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.json")
	store := deck.NewFileStore(cfgPath)
	eng := newTestEngine(t, store)
	ctx := context.Background()

	// Bind counter to keypad (0, 0) and dial 0, plus multi-action at (0, 1).
	if _, err := eng.AssignKeypadAction(deck.Coordinates{Row: 0, Col: 0}, core.ActionCounter); err != nil {
		t.Fatalf("AssignKeypadAction(Counter) error = %v", err)
	}
	if err := eng.UpdateKeypadSlot(deck.Coordinates{Row: 0, Col: 0}, "Kills", "#38BDF8", map[string]any{
		"initial": 1,
		"step":    2,
	}); err != nil {
		t.Fatalf("UpdateKeypadSlot(Counter) error = %v", err)
	}
	if _, err := eng.AssignDialAction(0, core.ActionCounter); err != nil {
		t.Fatalf("AssignDialAction(Counter) error = %v", err)
	}
	if _, err := eng.AssignKeypadAction(deck.Coordinates{Row: 0, Col: 1}, core.ActionMultiAction); err != nil {
		t.Fatalf("AssignKeypadAction(MultiAction) error = %v", err)
	}
	if _, err := eng.AddPage("Second"); err != nil {
		t.Fatalf("AddPage() error = %v", err)
	}
	if err := eng.GoToPage(1); err != nil {
		t.Fatalf("GoToPage(1) error = %v", err)
	}
	if err := eng.UpdateKeypadSlot(deck.Coordinates{Row: 0, Col: 1}, "Macro", "#22C55E", map[string]any{
		"steps": `[{"actionUuid":"` + core.ActionDelay + `","settings":{"ms":1}},{"actionUuid":"` + core.ActionPageNext + `"}]`,
	}); err != nil {
		t.Fatalf("UpdateKeypadSlot(MultiAction) error = %v", err)
	}

	// Press counter twice (1 -> 3 -> 5) and rotate dial (+4).
	if err := eng.PressKey(ctx, deck.Coordinates{Row: 0, Col: 0}); err != nil {
		t.Fatalf("PressKey 1 error = %v", err)
	}
	if err := eng.PressKey(ctx, deck.Coordinates{Row: 0, Col: 0}); err != nil {
		t.Fatalf("PressKey 2 error = %v", err)
	}
	if err := eng.RotateDial(ctx, 0, 4); err != nil {
		t.Fatalf("RotateDial error = %v", err)
	}

	snap := eng.Snapshot()
	if got := snap.Slots["0,0"].Visual.Subtitle; got != "5" {
		t.Errorf("counter keypad subtitle = %q, want 5", got)
	}
	if got := snap.DialSlots[0].Visual.Subtitle; got != "4" {
		t.Errorf("counter dial subtitle = %q, want 4", got)
	}

	// Trigger Multi-Action to advance to Page 2.
	if err := eng.PressKey(ctx, deck.Coordinates{Row: 0, Col: 1}); err != nil {
		t.Fatalf("PressKey(MultiAction) error = %v", err)
	}
	if snap = eng.Snapshot(); snap.PageIndex != 1 {
		t.Errorf("expected MultiAction to advance to PageIndex 1, got %d", snap.PageIndex)
	}

	// Reload from FileStore in a brand-new Engine to prove disk persistence.
	_ = eng.Close()
	eng2 := newTestEngine(t, deck.NewFileStore(cfgPath))
	snap2 := eng2.Snapshot()
	if snap2.PageCount != 2 {
		t.Fatalf("reloaded PageCount = %d, want 2", snap2.PageCount)
	}
	slot0 := snap2.Slots["0,0"]
	if slot0.Config == nil || slot0.Config.Title != "Kills" || slot0.Visual.Subtitle != "1" {
		t.Fatalf("unexpected reloaded slot 0,0: %+v", slot0)
	}
}
