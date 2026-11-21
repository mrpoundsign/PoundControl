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
	if err := eng.SetKeypadHotkey(deck.Coordinates{Row: 0, Col: 0}, "Ctrl+Shift+F1"); err != nil {
		t.Fatalf("SetKeypadHotkey() error = %v", err)
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

	// Save plugin global settings and verify persistence across reload.
	if err := eng.SaveGlobalSettings("com.example.obs", map[string]any{
		"host": "127.0.0.1",
		"port": float64(4455),
	}); err != nil {
		t.Fatalf("SaveGlobalSettings() error = %v", err)
	}

	// Reload from FileStore in a brand-new Engine to prove disk persistence.
	_ = eng.Close()
	eng2 := newTestEngine(t, deck.NewFileStore(cfgPath))
	snap2 := eng2.Snapshot()
	if snap2.PageCount != 2 {
		t.Fatalf("reloaded PageCount = %d, want 2", snap2.PageCount)
	}
	slot0 := snap2.Slots["0,0"]
	if slot0.Config == nil || slot0.Config.Title != "Kills" || slot0.Visual.Subtitle != "1" || slot0.Config.Hotkey != "Ctrl+Shift+F1" {
		t.Fatalf("unexpected reloaded slot 0,0: %+v", slot0)
	}
	if gs := eng2.GlobalSettings("com.example.obs"); gs["host"] != "127.0.0.1" {
		t.Fatalf("reloaded GlobalSettings = %+v, want host=127.0.0.1", gs)
	}
}

func TestEngineUndoRedoAssignEditClearAndPersistence(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "undo-config.json")
	store := deck.NewFileStore(cfgPath)
	eng := newTestEngine(t, store)

	snap0 := eng.Snapshot()
	if snap0.CanUndo || snap0.CanRedo || snap0.HistoryRevision != 0 {
		t.Fatalf("unexpected initial history state: %+v", snap0)
	}

	coord := deck.Coordinates{Row: 0, Col: 0}
	if _, err := eng.AssignKeypadAction(coord, core.ActionCounter); err != nil {
		t.Fatalf("AssignKeypadAction(Counter) error = %v", err)
	}
	if err := eng.UpdateKeypadSlotWithHotkey(coord, "My Counter", "#22C55E", "Ctrl+Shift+C", map[string]any{
		"initial": 10,
		"step":    5,
	}); err != nil {
		t.Fatalf("UpdateKeypadSlotWithHotkey() error = %v", err)
	}
	if _, err := eng.AssignKeypadAction(coord, core.ActionDelay); err != nil {
		t.Fatalf("AssignKeypadAction(Delay) error = %v", err)
	}
	if err := eng.ClearKeypadSlot(coord); err != nil {
		t.Fatalf("ClearKeypadSlot() error = %v", err)
	}

	snap := eng.Snapshot()
	if !snap.CanUndo || snap.CanRedo || snap.UndoLabel != "Clear slot" || snap.HistoryRevision != 4 {
		t.Fatalf("unexpected history state after 4 mutations: canUndo=%v canRedo=%v label=%q rev=%d",
			snap.CanUndo, snap.CanRedo, snap.UndoLabel, snap.HistoryRevision)
	}

	// 1) Undo clear -> ActionDelay restored.
	label, err := eng.Undo()
	if err != nil || label != "Clear slot" {
		t.Fatalf("Undo 1 = (%q, %v), want Clear slot", label, err)
	}
	snap = eng.Snapshot()
	if slot := snap.Slots[coord.SlotKey()]; slot.Config == nil || slot.Config.ActionUUID != core.ActionDelay {
		t.Fatalf("expected ActionDelay after Undo 1, got %+v", slot.Config)
	}
	if !snap.CanRedo || snap.RedoLabel != "Clear slot" || snap.HistoryRevision != 5 {
		t.Fatalf("unexpected redo state after Undo 1: %+v", snap)
	}

	// 2) Undo overwrite -> ActionCounter with edited title, hotkey, and settings restored.
	label, err = eng.Undo()
	if err != nil || label == "" {
		t.Fatalf("Undo 2 = (%q, %v)", label, err)
	}
	snap = eng.Snapshot()
	slot := snap.Slots[coord.SlotKey()]
	if slot.Config == nil || slot.Config.ActionUUID != core.ActionCounter ||
		slot.Config.Title != "My Counter" || slot.Config.Color != "#22C55E" ||
		slot.Config.Hotkey != "Ctrl+Shift+C" || slot.Visual.Subtitle != "10" {
		t.Fatalf("expected restored customized Counter after Undo 2, got cfg=%+v vis=%+v", slot.Config, slot.Visual)
	}

	// Verify disk persistence on FileStore reflects the undone state right away.
	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("store.Load() error = %v", err)
	}
	persisted := loaded.Profiles[0].Pages[0].Slots[coord.SlotKey()]
	if persisted == nil || persisted.ActionUUID != core.ActionCounter || persisted.Title != "My Counter" || persisted.Hotkey != "Ctrl+Shift+C" {
		t.Fatalf("disk config after Undo mismatch: %+v", persisted)
	}

	// 3) Single-step Undo of Edit slot reverts title, color, hotkey, and settings together.
	if _, err := eng.Undo(); err != nil {
		t.Fatalf("Undo 3 error = %v", err)
	}
	snap = eng.Snapshot()
	slot = snap.Slots[coord.SlotKey()]
	if slot.Config == nil || slot.Config.Title != "Count" || slot.Config.Hotkey != "" || slot.Visual.Subtitle != "0" {
		t.Fatalf("expected default Counter after Undo 3, got cfg=%+v vis=%+v", slot.Config, slot.Visual)
	}

	// 4) Undo initial Assign -> slot is empty, CanUndo is false.
	if _, err := eng.Undo(); err != nil {
		t.Fatalf("Undo 4 error = %v", err)
	}
	snap = eng.Snapshot()
	if _, exists := snap.Slots[coord.SlotKey()]; exists || snap.CanUndo {
		t.Fatalf("expected empty slot and CanUndo=false after Undo 4, exists=%v canUndo=%v", exists, snap.CanUndo)
	}
	if extraLabel, err := eng.Undo(); err != nil || extraLabel != "" {
		t.Fatalf("empty Undo() = (%q, %v), want ('', nil)", extraLabel, err)
	}

	// Redo forward twice -> Counter restored with custom settings, then a new edit clears redo stack.
	if _, err := eng.Redo(); err != nil {
		t.Fatalf("Redo 1 error = %v", err)
	}
	if _, err := eng.Redo(); err != nil {
		t.Fatalf("Redo 2 error = %v", err)
	}
	snap = eng.Snapshot()
	if s := snap.Slots[coord.SlotKey()]; s.Config == nil || s.Config.Title != "My Counter" || !snap.CanRedo {
		t.Fatalf("expected My Counter with CanRedo=true after Redo 2, got %+v canRedo=%v", s.Config, snap.CanRedo)
	}

	if err := eng.SetKeypadHotkey(coord, "Alt+F9"); err != nil {
		t.Fatalf("SetKeypadHotkey() error = %v", err)
	}
	if eng.Snapshot().CanRedo {
		t.Fatal("expected CanRedo=false after branching with a new mutation")
	}
}
