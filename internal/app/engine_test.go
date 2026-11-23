package app

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/mrpoundsign/PoundControl/pkg/builtin/core"
	"github.com/mrpoundsign/PoundControl/pkg/deck"
	"github.com/mrpoundsign/PoundControl/pkg/plugin"
	"github.com/mrpoundsign/PoundControl/pkg/secrets"
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

type testLifecycleProvider struct {
	mu       sync.Mutex
	recorded []plugin.ActionEvent
}

func (p *testLifecycleProvider) Kind() plugin.ProviderKind { return plugin.ProviderBuiltin }
func (p *testLifecycleProvider) Name() string              { return "Test Lifecycle" }

func (p *testLifecycleProvider) Manifests() []plugin.Manifest {
	return []plugin.Manifest{{
		UUID:     "com.test.lifecycle",
		Name:     "Test Lifecycle",
		Version:  "1.0.0",
		Provider: plugin.ProviderBuiltin,
		Actions:  p.Actions(),
	}}
}

func (p *testLifecycleProvider) Actions() []plugin.ActionDescriptor {
	return []plugin.ActionDescriptor{
		{
			UUID:        "test.lifecycle.action",
			PluginUUID:  "com.test.lifecycle",
			Name:        "Lifecycle Test",
			Category:    "Test",
			Controllers: []deck.ControllerType{deck.ControllerKeypad, deck.ControllerEncoder},
			Provider:    plugin.ProviderBuiltin,
		},
		{
			UUID:        "test.keypad.only",
			PluginUUID:  "com.test.lifecycle",
			Name:        "Keypad Only",
			Category:    "Test",
			Controllers: []deck.ControllerType{deck.ControllerKeypad},
			Provider:    plugin.ProviderBuiltin,
		},
	}
}

func (p *testLifecycleProvider) BindHost(plugin.HostAPI) {}

func (p *testLifecycleProvider) Dispatch(_ context.Context, ev plugin.ActionEvent) error {
	p.mu.Lock()
	p.recorded = append(p.recorded, ev)
	p.mu.Unlock()
	return nil
}

func (p *testLifecycleProvider) Close() error { return nil }

func (p *testLifecycleProvider) reset() {
	p.mu.Lock()
	p.recorded = nil
	p.mu.Unlock()
}

func (p *testLifecycleProvider) events() []plugin.ActionEvent {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.recorded)
}

func newMoveTestEngine(t *testing.T) (*Engine, *testLifecycleProvider) {
	t.Helper()
	reg := plugin.NewRegistry()
	if err := reg.RegisterProvider(core.New()); err != nil {
		t.Fatalf("RegisterProvider(core) error = %v", err)
	}
	rec := &testLifecycleProvider{}
	if err := reg.RegisterProvider(rec); err != nil {
		t.Fatalf("RegisterProvider(rec) error = %v", err)
	}
	eng, err := NewEngine(deck.NewMemoryStore(nil), reg)
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	return eng, rec
}

func TestEngineMoveSwapAcrossPagesFoldersAndTargetedLifecycle(t *testing.T) {
	eng, rec := newMoveTestEngine(t)
	ctx := context.Background()

	c00 := deck.Coordinates{Row: 0, Col: 0}
	c01 := deck.Coordinates{Row: 0, Col: 1}
	c02 := deck.Coordinates{Row: 0, Col: 2}
	c10 := deck.Coordinates{Row: 1, Col: 0}
	c11 := deck.Coordinates{Row: 1, Col: 1}
	c12 := deck.Coordinates{Row: 1, Col: 2}

	slotA, err := eng.AssignKeypadAction(c00, "test.lifecycle.action")
	if err != nil {
		t.Fatalf("AssignKeypadAction(c00) error = %v", err)
	}
	if err := eng.UpdateKeypadSlotWithHotkey(c00, "Title A", "#38BDF8", "Ctrl+Alt+1", map[string]any{"tag": "alpha"}); err != nil {
		t.Fatalf("UpdateKeypadSlotWithHotkey(c00) error = %v", err)
	}
	slotB, err := eng.AssignKeypadAction(c01, "test.lifecycle.action")
	if err != nil {
		t.Fatalf("AssignKeypadAction(c01) error = %v", err)
	}
	if err := eng.UpdateKeypadSlotWithHotkey(c01, "Title B", "#22C55E", "Ctrl+Alt+2", map[string]any{"tag": "beta"}); err != nil {
		t.Fatalf("UpdateKeypadSlotWithHotkey(c01) error = %v", err)
	}
	untouched, err := eng.AssignKeypadAction(c10, "test.lifecycle.action")
	if err != nil {
		t.Fatalf("AssignKeypadAction(c10) error = %v", err)
	}

	profID := eng.Snapshot().ActiveProfileID
	rec.reset()

	// 1. Move (0,0) -> empty (0,2) with targeted lifecycle events (c10 untouched).
	loc00 := SlotLocation{ProfileID: profID, PageIndex: 0, Controller: deck.ControllerKeypad, Coords: c00}
	loc01 := SlotLocation{ProfileID: profID, PageIndex: 0, Controller: deck.ControllerKeypad, Coords: c01}
	loc02 := SlotLocation{ProfileID: profID, PageIndex: 0, Controller: deck.ControllerKeypad, Coords: c02}
	swapped, err := eng.MoveSlot(loc00, loc02)
	if err != nil || swapped {
		t.Fatalf("MoveSlot(c00->c02) = swapped:%v err:%v, want false/nil", swapped, err)
	}

	snap := eng.Snapshot()
	if snap.Slots[c00.SlotKey()].Config != nil {
		t.Fatal("expected source (0,0) cleared after move")
	}
	got02 := snap.Slots[c02.SlotKey()].Config
	if got02 == nil || got02.Context != slotA.Context || got02.Title != "Title A" || got02.Color != "#38BDF8" || got02.Hotkey != "Ctrl+Alt+1" || got02.Settings["tag"] != "alpha" {
		t.Fatalf("moved slot at (0,2) mismatch: %+v", got02)
	}
	for _, ev := range rec.events() {
		if ev.Context == untouched.Context {
			t.Fatalf("untouched slot (1,0) received unexpected lifecycle event: %+v", ev)
		}
	}
	if evs := rec.events(); len(evs) != 2 || evs[0].Type != plugin.EventWillDisappear || evs[0].Coordinates != c00 || evs[1].Type != plugin.EventWillAppear || evs[1].Coordinates != c02 {
		t.Fatalf("unexpected targeted lifecycle events on move: %+v", evs)
	}

	// 2. Move occupied (0,2) -> occupied (0,1) -> atomic auto-swap + Undo/Redo.
	rec.reset()
	swapped, err = eng.MoveSlot(loc02, loc01)
	if err != nil || !swapped {
		t.Fatalf("MoveSlot(c02->c01) = swapped:%v err:%v, want true/nil", swapped, err)
	}
	snap = eng.Snapshot()
	if snap.Slots[c01.SlotKey()].Config.Context != slotA.Context || snap.Slots[c02.SlotKey()].Config.Context != slotB.Context {
		t.Fatalf("swap failed: c01=%+v c02=%+v", snap.Slots[c01.SlotKey()].Config, snap.Slots[c02.SlotKey()].Config)
	}
	if snap.Slots[c02.SlotKey()].Config.Title != "Title B" || snap.Slots[c02.SlotKey()].Config.Hotkey != "Ctrl+Alt+2" {
		t.Fatalf("swapped slot B lost fields: %+v", snap.Slots[c02.SlotKey()].Config)
	}
	for _, ev := range rec.events() {
		if ev.Context == untouched.Context {
			t.Fatalf("untouched slot (1,0) received unexpected lifecycle event during swap: %+v", ev)
		}
	}

	if _, err := eng.Undo(); err != nil {
		t.Fatalf("Undo(swap) error = %v", err)
	}
	snap = eng.Snapshot()
	if snap.Slots[c02.SlotKey()].Config.Context != slotA.Context || snap.Slots[c01.SlotKey()].Config.Context != slotB.Context {
		t.Fatalf("expected Undo to revert swap atomically")
	}
	if _, err := eng.Redo(); err != nil {
		t.Fatalf("Redo(swap) error = %v", err)
	}
	snap = eng.Snapshot()
	if snap.Slots[c01.SlotKey()].Config.Context != slotA.Context || snap.Slots[c02.SlotKey()].Config.Context != slotB.Context {
		t.Fatalf("expected Redo to reapply swap atomically")
	}

	// 3. Cross-page move & cross-page auto-swap.
	if _, err := eng.AddPage("Page 2"); err != nil {
		t.Fatalf("AddPage() error = %v", err)
	}
	if err := eng.GoToPage(1); err != nil {
		t.Fatalf("GoToPage(1) error = %v", err)
	}
	p2Loc11 := SlotLocation{ProfileID: profID, PageIndex: 1, Controller: deck.ControllerKeypad, Coords: c11}
	if swapped, err := eng.MoveSlot(loc01, p2Loc11); err != nil || swapped {
		t.Fatalf("cross-page MoveSlot(empty) = swapped:%v err:%v, want false/nil", swapped, err)
	}
	if swapped, err := eng.MoveSlot(loc02, p2Loc11); err != nil || !swapped {
		t.Fatalf("cross-page MoveSlot(swap) = swapped:%v err:%v, want true/nil", swapped, err)
	}
	if err := eng.GoToPage(2); err != nil {
		t.Fatalf("GoToPage(2) error = %v", err)
	}
	if got := eng.Snapshot().Slots[c11.SlotKey()].Config; got == nil || got.Context != slotB.Context {
		t.Fatalf("expected Page 2 (1,1) to hold slotB after cross-page swap, got %+v", got)
	}
	if err := eng.GoToPage(1); err != nil {
		t.Fatalf("GoToPage(1) error = %v", err)
	}
	if got := eng.Snapshot().Slots[c02.SlotKey()].Config; got == nil || got.Context != slotA.Context {
		t.Fatalf("expected Page 1 (0,2) to hold slotA after cross-page swap, got %+v", got)
	}

	// 4. MoveSlotToNewPage + single Undo removes new page and restores source slot.
	newPageIdx, dstNewCoord, err := eng.MoveSlotToNewPage(loc02)
	if err != nil {
		t.Fatalf("MoveSlotToNewPage() error = %v", err)
	}
	if newPageIdx != 2 || dstNewCoord != c00 {
		t.Fatalf("MoveSlotToNewPage() = page %d coord %+v, want page 2 (0,0)", newPageIdx, dstNewCoord)
	}
	snap = eng.Snapshot()
	if snap.PageCount != 3 || snap.Slots[c02.SlotKey()].Config != nil {
		t.Fatalf("expected PageCount=3 and (0,2) cleared, got count=%d slot=%+v", snap.PageCount, snap.Slots[c02.SlotKey()].Config)
	}
	if err := eng.GoToPage(3); err != nil {
		t.Fatalf("GoToPage(3) error = %v", err)
	}
	if got := eng.Snapshot().Slots[c00.SlotKey()].Config; got == nil || got.Context != slotA.Context {
		t.Fatalf("expected Page 3 (0,0) to hold slotA, got %+v", got)
	}
	if _, err := eng.Undo(); err != nil {
		t.Fatalf("Undo(MoveSlotToNewPage) error = %v", err)
	}
	snap = eng.Snapshot()
	if snap.PageCount != 2 {
		t.Fatalf("expected Undo(MoveSlotToNewPage) to remove Page 3, got PageCount=%d", snap.PageCount)
	}
	_ = eng.GoToPage(1)
	if got := eng.Snapshot().Slots[c02.SlotKey()].Config; got == nil || got.Context != slotA.Context {
		t.Fatalf("expected Undo(MoveSlotToNewPage) to restore slotA at (0,2), got %+v", got)
	}

	// 5. MoveSlotToFolder + AutoBack (0,0) & self-folder guard.
	folderBtn, err := eng.AssignKeypadAction(c12, core.ActionFolderOpen)
	if err != nil {
		t.Fatalf("AssignKeypadAction(FolderOpen) error = %v", err)
	}
	if err := eng.PressKey(ctx, c12); err != nil {
		t.Fatalf("open folder error = %v", err)
	}
	folderID := eng.Snapshot().ActiveFolderID
	if folderID == "" {
		t.Fatal("expected non-empty ActiveFolderID after opening folder")
	}
	_ = eng.CloseFolder()

	if _, err := eng.MoveSlotToFolder(SlotLocation{ProfileID: profID, PageIndex: 0, Controller: deck.ControllerKeypad, Coords: c12}, folderID); err == nil {
		t.Fatal("expected MoveSlotToFolder to reject moving a folder button into itself")
	}
	if _, err := eng.MoveSlot(
		SlotLocation{ProfileID: profID, PageIndex: 0, Controller: deck.ControllerKeypad, Coords: c12},
		SlotLocation{ProfileID: profID, FolderID: folderID, Controller: deck.ControllerKeypad, Coords: c02},
	); err == nil {
		t.Fatal("expected MoveSlot to reject moving a folder button into itself")
	}
	if _, err := eng.MoveSlot(
		loc02,
		SlotLocation{ProfileID: profID, FolderID: folderID, Controller: deck.ControllerKeypad, Coords: c00},
	); err == nil {
		t.Fatal("expected MoveSlot to protect folder AutoBack slot (0,0)")
	}
	movedToFolderCoord, err := eng.MoveSlotToFolder(loc02, folderID)
	if err != nil {
		t.Fatalf("MoveSlotToFolder(c02) error = %v", err)
	}
	if movedToFolderCoord != c01 {
		t.Fatalf("MoveSlotToFolder landed at %+v, want first open (0,1) skipping AutoBack (0,0)", movedToFolderCoord)
	}
	if err := eng.PressKey(ctx, c12); err != nil {
		t.Fatalf("re-open folder error = %v", err)
	}
	if got := eng.Snapshot().Slots[c01.SlotKey()].Config; got == nil || got.Context != slotA.Context || folderBtn == nil {
		t.Fatalf("expected moved button inside folder at (0,1), got %+v", got)
	}

	// 6. MoveSlotToPage on a compact 2x1 profile -> first open slot and "is full" error.
	tiny, err := eng.CreateProfile("Tiny", deck.GridSize{Cols: 2, Rows: 1}, 0)
	if err != nil {
		t.Fatalf("CreateProfile(Tiny) error = %v", err)
	}
	if _, err := eng.AddPage("Tiny P2"); err != nil {
		t.Fatalf("AddPage(Tiny P2) error = %v", err)
	}
	_, _ = eng.AssignKeypadAction(c00, "test.keypad.only")
	_ = eng.GoToPage(1)
	_, _ = eng.AssignKeypadAction(c00, "test.keypad.only")
	_, _ = eng.AssignKeypadAction(c01, "test.keypad.only")

	tinyDst, err := eng.MoveSlotToPage(SlotLocation{ProfileID: tiny.ID, PageIndex: 0, Controller: deck.ControllerKeypad, Coords: c00}, 1)
	if err != nil {
		t.Fatalf("MoveSlotToPage(tiny -> P2) error = %v", err)
	}
	if tinyDst != c01 {
		t.Fatalf("MoveSlotToPage(tiny -> P2) = %+v, want (0,1)", tinyDst)
	}
	if _, err := eng.MoveSlotToPage(SlotLocation{ProfileID: tiny.ID, PageIndex: 0, Controller: deck.ControllerKeypad, Coords: c01}, 1); err == nil || !strings.Contains(err.Error(), "is full") {
		t.Fatalf("expected 'is full' error moving onto full Page 2, got %v", err)
	}
}

func TestEngineDialMoveAndCrossProfileCopyPaste(t *testing.T) {
	eng, _ := newMoveTestEngine(t)
	ctx := context.Background()

	snap := eng.Snapshot()
	prof1ID := snap.ActiveProfileID
	c00 := deck.Coordinates{Row: 0, Col: 0}
	c01 := deck.Coordinates{Row: 0, Col: 1}

	// 1. Dial MoveSlot (empty + swap) and cross-controller guard.
	d0, err := eng.AssignDialAction(0, "test.lifecycle.action")
	if err != nil {
		t.Fatalf("AssignDialAction(0) error = %v", err)
	}
	if err := eng.UpdateDialSlot(0, "Dial Alpha", "#38BDF8", map[string]any{"k": 1}); err != nil {
		t.Fatalf("UpdateDialSlot(0) error = %v", err)
	}
	d1, err := eng.AssignDialAction(1, "test.lifecycle.action")
	if err != nil {
		t.Fatalf("AssignDialAction(1) error = %v", err)
	}

	if swapped, err := eng.MoveSlot(
		SlotLocation{ProfileID: prof1ID, PageIndex: 0, Controller: deck.ControllerEncoder, Dial: 0},
		SlotLocation{ProfileID: prof1ID, PageIndex: 0, Controller: deck.ControllerEncoder, Dial: 2},
	); err != nil || swapped {
		t.Fatalf("dial MoveSlot(0->2) = swapped:%v err:%v, want false/nil", swapped, err)
	}
	if swapped, err := eng.MoveSlot(
		SlotLocation{ProfileID: prof1ID, PageIndex: 0, Controller: deck.ControllerEncoder, Dial: 2},
		SlotLocation{ProfileID: prof1ID, PageIndex: 0, Controller: deck.ControllerEncoder, Dial: 1},
	); err != nil || !swapped {
		t.Fatalf("dial MoveSlot(2<->1) = swapped:%v err:%v, want true/nil", swapped, err)
	}
	snap = eng.Snapshot()
	if snap.DialSlots[0].Config != nil || snap.DialSlots[1].Config.Context != d0.Context || snap.DialSlots[2].Config.Context != d1.Context {
		t.Fatalf("unexpected dial positions after move+swap: %+v", snap.DialSlots)
	}
	if _, err := eng.MoveSlot(
		SlotLocation{ProfileID: prof1ID, PageIndex: 0, Controller: deck.ControllerKeypad, Coords: c00},
		SlotLocation{ProfileID: prof1ID, PageIndex: 0, Controller: deck.ControllerEncoder, Dial: 0},
	); err == nil {
		t.Fatal("expected cross-controller MoveSlot error")
	}

	// 2. Prepare customized keypad button + folder subtree in Profile 1.
	if _, err := eng.AssignKeypadAction(c00, "test.keypad.only"); err != nil {
		t.Fatalf("AssignKeypadAction(c00) error = %v", err)
	}
	if err := eng.UpdateKeypadSlotWithHotkey(c00, "CopyMe", "#F59E0B", "Ctrl+F8", map[string]any{"foo": "bar"}); err != nil {
		t.Fatalf("UpdateKeypadSlotWithHotkey(c00) error = %v", err)
	}
	srcKey := eng.Snapshot().Slots[c00.SlotKey()].Config

	if _, err := eng.AssignKeypadAction(c01, core.ActionFolderOpen); err != nil {
		t.Fatalf("AssignKeypadAction(FolderOpen) error = %v", err)
	}
	if err := eng.PressKey(ctx, c01); err != nil {
		t.Fatalf("open Profile 1 folder error = %v", err)
	}
	origFolderID := eng.Snapshot().ActiveFolderID
	origChild, err := eng.AssignKeypadAction(c01, "test.keypad.only")
	if err != nil {
		t.Fatalf("AssignKeypadAction(folder child) error = %v", err)
	}
	if err := eng.UpdateKeypadSlot(c01, "FolderChild", "#22C55E", map[string]any{"inside": true}); err != nil {
		t.Fatalf("UpdateKeypadSlot(folder child) error = %v", err)
	}
	_ = eng.CloseFolder()
	srcFolderSlot := eng.Snapshot().Slots[c01.SlotKey()].Config

	// 3. Switch to Profile 2 (different grid/dials) and test cross-profile PasteSlotConfig.
	prof2, err := eng.CreateProfile("Profile Two", deck.GridSize{Cols: 3, Rows: 2}, 2)
	if err != nil {
		t.Fatalf("CreateProfile() error = %v", err)
	}
	// Verify Controller=="" normalizes to Keypad and allocates fresh Context UUID.
	pastedKey, err := eng.PasteSlotConfig(srcKey, prof1ID, SlotLocation{ProfileID: prof2.ID, PageIndex: 0, Coords: c00})
	if err != nil {
		t.Fatalf("PasteSlotConfig(srcKey) error = %v", err)
	}
	if pastedKey.Context == srcKey.Context || pastedKey.Title != "CopyMe" || pastedKey.Hotkey != "Ctrl+F8" || pastedKey.Settings["foo"] != "bar" {
		t.Fatalf("pasted slot mismatch: got=%+v src=%+v", pastedKey, srcKey)
	}

	// Verify keypad-only action rejected when pasted onto Encoder dial.
	if _, err := eng.PasteSlotConfig(srcKey, prof1ID, SlotLocation{ProfileID: prof2.ID, PageIndex: 0, Controller: deck.ControllerEncoder, Dial: 0}); err == nil {
		t.Fatal("expected PasteSlotConfig to reject keypad-only action on Encoder dial")
	}

	// Verify folder subtree deep-cloned independently into Profile 2.
	pastedFolder, err := eng.PasteSlotConfig(srcFolderSlot, prof1ID, SlotLocation{ProfileID: prof2.ID, PageIndex: 0, Controller: deck.ControllerKeypad, Coords: c01})
	if err != nil {
		t.Fatalf("PasteSlotConfig(folder) error = %v", err)
	}
	clonedFolderID, _ := pastedFolder.Settings["folderId"].(string)
	if clonedFolderID == "" || clonedFolderID == origFolderID {
		t.Fatalf("expected new independent cloned folderId, got %q (orig=%q)", clonedFolderID, origFolderID)
	}
	if err := eng.PressKey(ctx, c01); err != nil {
		t.Fatalf("open cloned folder error = %v", err)
	}
	clonedChild := eng.Snapshot().Slots[c01.SlotKey()].Config
	if clonedChild == nil || clonedChild.Context == origChild.Context || clonedChild.Title != "FolderChild" {
		t.Fatalf("cloned folder child mismatch: got=%+v orig=%+v", clonedChild, origChild)
	}
	if err := eng.ClearKeypadSlot(c01); err != nil {
		t.Fatalf("ClearKeypadSlot(cloned folder child) error = %v", err)
	}
	_ = eng.CloseFolder()

	// Profile 1's folder child must remain completely untouched.
	if err := eng.SwitchProfile(prof1ID); err != nil {
		t.Fatalf("SwitchProfile(prof1) error = %v", err)
	}
	if err := eng.PressKey(ctx, c01); err != nil {
		t.Fatalf("open Profile 1 folder error = %v", err)
	}
	if got := eng.Snapshot().Slots[c01.SlotKey()].Config; got == nil || got.Context != origChild.Context {
		t.Fatalf("expected Profile 1 folder child untouched after deleting cloned copy, got %+v", got)
	}
}

func TestEngineAppearanceBackgroundsHideTitleAndGlobalPalette(t *testing.T) {
	eng, _ := newMoveTestEngine(t)

	// 1. Default global palette starts seeded; AddColorSwatch normalizes & deduplicates.
	if len(eng.ColorPalette()) != len(deck.DefaultColorPalette()) {
		t.Fatalf("expected default palette length %d, got %d", len(deck.DefaultColorPalette()), len(eng.ColorPalette()))
	}
	if err := eng.AddColorSwatch("Brand Teal", "14b8a6"); err != nil {
		t.Fatalf("AddColorSwatch() error = %v", err)
	}
	if err := eng.AddColorSwatch("Ocean Teal", "#14B8A6"); err != nil {
		t.Fatalf("AddColorSwatch(update) error = %v", err)
	}
	if err := eng.AddColorSwatch("Invalid", "not-hex"); err == nil {
		t.Fatal("expected AddColorSwatch to reject invalid hex")
	}
	pal := eng.Snapshot().ColorPalette
	if len(pal) != len(deck.DefaultColorPalette())+1 || pal[len(pal)-1] != (deck.ColorSwatch{Name: "Ocean Teal", Hex: "#14B8A6"}) {
		t.Fatalf("unexpected updated palette: %+v", pal)
	}

	// Empty custom palette persists across SaveColorPalette, then ResetColorPalette restores defaults.
	if err := eng.SaveColorPalette([]deck.ColorSwatch{}); err != nil {
		t.Fatalf("SaveColorPalette(empty) error = %v", err)
	}
	if len(eng.Snapshot().ColorPalette) != 0 {
		t.Fatalf("expected empty palette after SaveColorPalette([]), got %+v", eng.Snapshot().ColorPalette)
	}
	if err := eng.ResetColorPalette(); err != nil {
		t.Fatalf("ResetColorPalette() error = %v", err)
	}
	if len(eng.Snapshot().ColorPalette) != len(deck.DefaultColorPalette()) {
		t.Fatalf("expected restored default palette, got %+v", eng.Snapshot().ColorPalette)
	}
	if err := eng.AddColorSwatch("Brand Teal", "#14B8A6"); err != nil {
		t.Fatalf("re-AddColorSwatch() error = %v", err)
	}

	// 2. Keypad & dial background fill/image + HideTitle + validation + Undo preserving global palette.
	c00 := deck.Coordinates{Row: 0, Col: 0}
	if _, err := eng.AssignKeypadAction(c00, "test.lifecycle.action"); err != nil {
		t.Fatalf("AssignKeypadAction(c00) error = %v", err)
	}
	if err := eng.UpdateKeypadSlotAppearance(c00, SlotAppearance{
		Title:           "Hidden Label",
		Color:           "#38BDF8",
		BackgroundColor: "invalid-hex",
	}, nil); err == nil {
		t.Fatal("expected UpdateKeypadSlotAppearance to reject invalid background hex")
	}

	if err := eng.UpdateKeypadSlotAppearance(c00, SlotAppearance{
		Title:           "Hidden Label",
		Color:           "38bdf8",
		BackgroundColor: "fbbf24",
		BackgroundImage: "/tmp/bg.png",
		HideTitle:       true,
		SetHotkey:       true,
		Hotkey:          "Ctrl+Alt+9",
	}, map[string]any{"mode": "custom"}); err != nil {
		t.Fatalf("UpdateKeypadSlotAppearance() error = %v", err)
	}

	if _, err := eng.AssignDialAction(0, "test.lifecycle.action"); err != nil {
		t.Fatalf("AssignDialAction(0) error = %v", err)
	}
	if err := eng.UpdateDialSlotAppearance(0, SlotAppearance{
		Title:           "Encoder",
		Color:           "#22C55E",
		BackgroundColor: "#1E293B",
	}, nil); err != nil {
		t.Fatalf("UpdateDialSlotAppearance() error = %v", err)
	}

	snap := eng.Snapshot()
	rKey := snap.Slots[c00.SlotKey()]
	if rKey.Visual.BackgroundColor != "#FBBF24" || rKey.Visual.BackgroundImage != "/tmp/bg.png" || rKey.Visual.Title != "" {
		t.Fatalf("unexpected rendered keypad visual with background & HideTitle: %+v", rKey.Visual)
	}
	if rKey.Config.Color != "#38BDF8" || rKey.Config.Hotkey != "Ctrl+Alt+9" {
		t.Fatalf("unexpected normalized keypad config: %+v", rKey.Config)
	}
	rDial := snap.DialSlots[0]
	if rDial.Visual.BackgroundColor != "#1E293B" {
		t.Fatalf("expected dial BackgroundColor #1E293B, got %+v", rDial.Visual)
	}

	// Cross-profile PasteSlotConfig preserves BackgroundColor, BackgroundImage, and HideTitle.
	prof2, err := eng.CreateProfile("Second", deck.GridSize{Cols: 3, Rows: 2}, 1)
	if err != nil {
		t.Fatalf("CreateProfile() error = %v", err)
	}
	pasted, err := eng.PasteSlotConfig(rKey.Config, snap.ActiveProfileID, SlotLocation{
		ProfileID:  prof2.ID,
		PageIndex:  0,
		Controller: deck.ControllerKeypad,
		Coords:     c00,
	})
	if err != nil {
		t.Fatalf("PasteSlotConfig() error = %v", err)
	}
	if pasted.BackgroundColor != "#FBBF24" || pasted.BackgroundImage != "/tmp/bg.png" || !pasted.HideTitle {
		t.Fatalf("PasteSlotConfig lost background appearance fields: %+v", pasted)
	}

	// Undo preserves custom ColorPalette swatches even when reverting earlier slot mutations.
	if _, err := eng.Undo(); err != nil {
		t.Fatalf("Undo() error = %v", err)
	}
	if pal := eng.Snapshot().ColorPalette; len(pal) != len(deck.DefaultColorPalette())+1 {
		t.Fatalf("expected Undo to preserve global ColorPalette, got %+v", pal)
	}
}

type fakeGlobalConfigProvider struct {
	mu           sync.Mutex
	manifest     plugin.Manifest
	lastNotified map[string]any
	notifyCount  int
}

func (f *fakeGlobalConfigProvider) Kind() plugin.ProviderKind        { return plugin.ProviderWasm }
func (f *fakeGlobalConfigProvider) Name() string                     { return f.manifest.Name }
func (f *fakeGlobalConfigProvider) Manifests() []plugin.Manifest     { return []plugin.Manifest{f.manifest} }
func (f *fakeGlobalConfigProvider) Actions() []plugin.ActionDescriptor { return f.manifest.Actions }
func (f *fakeGlobalConfigProvider) BindHost(plugin.HostAPI)          {}
func (f *fakeGlobalConfigProvider) Dispatch(context.Context, plugin.ActionEvent) error {
	return nil
}
func (f *fakeGlobalConfigProvider) Close() error { return nil }
func (f *fakeGlobalConfigProvider) OnGlobalSettingsChanged(_ context.Context, _ string, settings map[string]any) error {
	f.mu.Lock()
	f.lastNotified = settings
	f.notifyCount++
	f.mu.Unlock()
	return nil
}

func TestEngineGlobalSettingsSecretsAndListener(t *testing.T) {
	const pluginUUID = "com.test.global.secrets"
	prov := &fakeGlobalConfigProvider{
		manifest: plugin.Manifest{
			UUID:     pluginUUID,
			Name:     "Secret Plugin",
			Version:  "1.0.0",
			Provider: plugin.ProviderWasm,
			GlobalSettingsSchema: []plugin.SettingField{
				{Key: "channel", Label: "Default Channel", Type: plugin.FieldText},
				{Key: "apiToken", Label: "API Token", Type: plugin.FieldPassword},
			},
			Actions: []plugin.ActionDescriptor{
				{UUID: pluginUUID + ".ping", PluginUUID: pluginUUID, Name: "Ping", Category: "Secret Plugin"},
			},
		},
	}

	reg := plugin.NewRegistry()
	if err := reg.RegisterProvider(prov); err != nil {
		t.Fatalf("RegisterProvider() error = %v", err)
	}
	memDeck := deck.NewMemoryStore(nil)
	memSecrets := secrets.NewMemoryStore()

	eng, err := NewEngine(memDeck, reg)
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	eng.SetSecretStore(memSecrets)
	t.Cleanup(func() { _ = eng.Close() })

	// Save both plain and password settings.
	if err := eng.SaveGlobalSettings(pluginUUID, map[string]any{
		"channel":  "mrpoundsign",
		"apiToken": "super-secret-token",
	}); err != nil {
		t.Fatalf("SaveGlobalSettings() error = %v", err)
	}

	// Verify password is NOT stored in deck config JSON, only in secret store.
	loadedCfg, err := memDeck.Load()
	if err != nil {
		t.Fatalf("memDeck.Load() error = %v", err)
	}
	if _, leaked := loadedCfg.PluginSettings[pluginUUID]["apiToken"]; leaked {
		t.Fatalf("expected apiToken to stay out of config.json, found %+v", loadedCfg.PluginSettings[pluginUUID])
	}
	if loadedCfg.PluginSettings[pluginUUID]["channel"] != "mrpoundsign" {
		t.Fatalf("expected channel in config.json, got %+v", loadedCfg.PluginSettings[pluginUUID])
	}
	if !eng.HasGlobalSecret(pluginUUID, "apiToken") {
		t.Fatal("expected HasGlobalSecret(apiToken) = true")
	}

	// Verify GlobalSettings transparently merges secret back in.
	gs := eng.GlobalSettings(pluginUUID)
	if gs["channel"] != "mrpoundsign" || gs["apiToken"] != "super-secret-token" {
		t.Fatalf("unexpected merged GlobalSettings: %+v", gs)
	}

	// Verify listener received merged settings.
	prov.mu.Lock()
	if prov.notifyCount != 1 || prov.lastNotified["apiToken"] != "super-secret-token" {
		t.Fatalf("unexpected listener notification: count=%d settings=%+v", prov.notifyCount, prov.lastNotified)
	}
	prov.mu.Unlock()

	// Updating plain setting while omitting apiToken preserves existing keyring secret.
	if err := eng.SaveGlobalSettings(pluginUUID, map[string]any{
		"channel": "newchannel",
	}); err != nil {
		t.Fatalf("SaveGlobalSettings(omit secret) error = %v", err)
	}
	gs2 := eng.GlobalSettings(pluginUUID)
	if gs2["channel"] != "newchannel" || gs2["apiToken"] != "super-secret-token" {
		t.Fatalf("expected existing secret to be preserved when omitted, got %+v", gs2)
	}
	prov.mu.Lock()
	if prov.notifyCount != 2 || prov.lastNotified["channel"] != "newchannel" || prov.lastNotified["apiToken"] != "super-secret-token" {
		t.Fatalf("expected listener to receive preserved secret, got count=%d settings=%+v", prov.notifyCount, prov.lastNotified)
	}
	prov.mu.Unlock()

	// Saving empty string deletes secret from keyring.
	if err := eng.SaveGlobalSettings(pluginUUID, map[string]any{
		"channel":  "newchannel",
		"apiToken": "",
	}); err != nil {
		t.Fatalf("SaveGlobalSettings(clear secret) error = %v", err)
	}
	if eng.HasGlobalSecret(pluginUUID, "apiToken") {
		t.Fatal("expected HasGlobalSecret(apiToken) = false after clearing")
	}
	if _, stillHas := eng.GlobalSettings(pluginUUID)["apiToken"]; stillHas {
		t.Fatalf("expected apiToken removed from GlobalSettings, got %+v", eng.GlobalSettings(pluginUUID))
	}
}
