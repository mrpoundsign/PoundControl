package app

import (
	"context"
	"maps"
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
	host     plugin.HostAPI
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

func (p *testLifecycleProvider) BindHost(host plugin.HostAPI) {
	p.mu.Lock()
	p.host = host
	p.mu.Unlock()
}

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
	mu               sync.Mutex
	manifest         plugin.Manifest
	lastNotified     map[string]any
	notifyCount      int
	lastActionKey    string
	lastActionArgs   map[string]any
	actionErr        error
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
func (f *fakeGlobalConfigProvider) OnConfigAction(_ context.Context, _ string, actionKey string, settings map[string]any) error {
	f.mu.Lock()
	f.lastActionKey = actionKey
	f.lastActionArgs = maps.Clone(settings)
	err := f.actionErr
	f.mu.Unlock()
	return err
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

func TestEngineInvokePluginConfigActionMergesSecretsAndRespectsDisabled(t *testing.T) {
	const pluginUUID = "com.test.global.actions"
	prov := &fakeGlobalConfigProvider{
		manifest: plugin.Manifest{
			UUID:     pluginUUID,
			Name:     "Twitch Sim",
			Version:  "1.0.0",
			Provider: plugin.ProviderWasm,
			GlobalSettingsSchema: []plugin.SettingField{
				{Key: "clientId", Label: "Client ID", Type: plugin.FieldText, Default: "default-cid"},
				{Key: "accessToken", Label: "Access Token", Type: plugin.FieldPassword},
				{Key: "authConnect", Label: "Sign In", Type: plugin.FieldButton},
			},
			Actions: []plugin.ActionDescriptor{
				{UUID: pluginUUID + ".act", PluginUUID: pluginUUID, Name: "Act", Category: "Twitch Sim"},
			},
		},
	}

	reg := plugin.NewRegistry()
	if err := reg.RegisterProvider(core.New()); err != nil {
		t.Fatalf("RegisterProvider(core) error = %v", err)
	}
	if err := reg.RegisterProvider(prov); err != nil {
		t.Fatalf("RegisterProvider(prov) error = %v", err)
	}

	memSecrets := secrets.NewMemoryStore()
	eng, err := NewEngine(deck.NewMemoryStore(nil), reg)
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	eng.SetSecretStore(memSecrets)
	t.Cleanup(func() { _ = eng.Close() })

	if err := eng.SaveGlobalSettings(pluginUUID, map[string]any{
		"clientId":    "saved-cid",
		"accessToken": "keyring-secret",
		"userId":      "42",
	}); err != nil {
		t.Fatalf("SaveGlobalSettings() error = %v", err)
	}

	// Unsaved form override for clientId overlays saved settings while preserving keyring secret & userId.
	if err := eng.InvokePluginConfigAction(context.Background(), pluginUUID, "authConnect", map[string]any{
		"clientId": "override-cid",
	}); err != nil {
		t.Fatalf("InvokePluginConfigAction() error = %v", err)
	}

	prov.mu.Lock()
	gotKey := prov.lastActionKey
	gotArgs := prov.lastActionArgs
	prov.mu.Unlock()
	if gotKey != "authConnect" || gotArgs["clientId"] != "override-cid" || gotArgs["accessToken"] != "keyring-secret" || gotArgs["userId"] != "42" {
		t.Fatalf("unexpected merged InvokePluginConfigAction payload: key=%q args=%+v", gotKey, gotArgs)
	}

	if err := eng.SetPluginEnabled(pluginUUID, false); err != nil {
		t.Fatalf("SetPluginEnabled(false) error = %v", err)
	}
	if err := eng.InvokePluginConfigAction(context.Background(), pluginUUID, "authConnect", nil); err == nil {
		t.Fatal("expected error invoking config action on disabled plugin")
	}
}

func TestEngineVisualPatchBackgroundColorOverrideAndRestore(t *testing.T) {
	eng, prov := newMoveTestEngine(t)

	c00 := deck.Coordinates{Row: 0, Col: 0}
	slot, err := eng.AssignKeypadAction(c00, "test.lifecycle.action")
	if err != nil {
		t.Fatalf("AssignKeypadAction(c00) error = %v", err)
	}
	if err := eng.UpdateKeypadSlotAppearance(c00, SlotAppearance{
		Title:           "Airhorn",
		Color:           "#A855F7",
		BackgroundColor: "#1E293B",
		BackgroundImage: "/tmp/custom-bg.png",
	}, nil); err != nil {
		t.Fatalf("UpdateKeypadSlotAppearance() error = %v", err)
	}

	prov.mu.Lock()
	host := prov.host
	prov.mu.Unlock()
	if host == nil {
		t.Fatal("expected HostAPI to be bound to provider")
	}

	// Non-empty VisualPatch.BackgroundColor overrides configured BackgroundColor and temporarily suppresses BackgroundImage.
	host.UpdateVisual(slot.Context, plugin.VisualPatch{
		Subtitle:        new("MISSING"),
		Color:           new("#EF4444"),
		BackgroundColor: new("#5C1922"),
		State:           new(0),
		Flash:           "alert",
	})
	vis := eng.Snapshot().Slots[c00.SlotKey()].Visual
	if vis.BackgroundColor != "#5C1922" || vis.BackgroundImage != "" || vis.Color != "#EF4444" || vis.Subtitle != "MISSING" {
		t.Fatalf("expected missing-file VisualPatch override, got %+v", vis)
	}

	// Clearing VisualPatch.BackgroundColor restores the configured BackgroundColor and BackgroundImage.
	host.UpdateVisual(slot.Context, plugin.VisualPatch{
		Subtitle:        new(""),
		Color:           new(""),
		BackgroundColor: new(""),
		State:           new(0),
	})
	visRestored := eng.Snapshot().Slots[c00.SlotKey()].Visual
	if visRestored.BackgroundColor != "#1E293B" || visRestored.BackgroundImage != "/tmp/custom-bg.png" || visRestored.Color != "#A855F7" || visRestored.Subtitle != "" {
		t.Fatalf("expected configured background & accent restored, got %+v", visRestored)
	}
}

type toggleableTestProvider struct {
	mu       sync.Mutex
	events   []plugin.ActionEvent
	enabled  bool
	host     plugin.HostAPI
}

func (p *toggleableTestProvider) Kind() plugin.ProviderKind { return plugin.ProviderBuiltin }
func (p *toggleableTestProvider) Name() string              { return "Optional Feature" }
func (p *toggleableTestProvider) Manifests() []plugin.Manifest {
	return []plugin.Manifest{{
		UUID:     "com.test.optional",
		Name:     "Optional Feature",
		Version:  "1.0.0",
		Provider: plugin.ProviderBuiltin,
		Actions: []plugin.ActionDescriptor{{
			UUID:         "com.test.optional.act",
			PluginUUID:   "com.test.optional",
			Name:         "Optional Act",
			Category:     "Optional",
			DefaultColor: "#22C55E",
			Provider:     plugin.ProviderBuiltin,
		}},
	}}
}
func (p *toggleableTestProvider) Actions() []plugin.ActionDescriptor {
	return p.Manifests()[0].Actions
}
func (p *toggleableTestProvider) BindHost(host plugin.HostAPI) {
	p.mu.Lock()
	p.host = host
	p.mu.Unlock()
}
func (p *toggleableTestProvider) Dispatch(_ context.Context, ev plugin.ActionEvent) error {
	p.mu.Lock()
	p.events = append(p.events, ev)
	host := p.host
	p.mu.Unlock()
	if ev.Type == plugin.EventWillAppear && host != nil {
		host.UpdateVisual(ev.Context, plugin.VisualPatch{Subtitle: new("READY"), State: new(1)})
	}
	return nil
}
func (p *toggleableTestProvider) SetPluginEnabled(_ context.Context, _ string, enabled bool) error {
	p.mu.Lock()
	p.enabled = enabled
	p.mu.Unlock()
	return nil
}
func (p *toggleableTestProvider) Close() error { return nil }

func TestEngineSetPluginEnabledLifecyclePersistenceAndUndo(t *testing.T) {
	reg := plugin.NewRegistry()
	if err := reg.RegisterProvider(core.New()); err != nil {
		t.Fatalf("RegisterProvider(core) error = %v", err)
	}
	opt := &toggleableTestProvider{enabled: true}
	if err := reg.RegisterProvider(opt); err != nil {
		t.Fatalf("RegisterProvider(opt) error = %v", err)
	}

	store := deck.NewMemoryStore(nil)
	eng, err := NewEngine(store, reg)
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	defer func() { _ = eng.Close() }()

	// Cannot disable required core plugin.
	if err := eng.SetPluginEnabled(core.PluginUUID, false); err == nil {
		t.Fatal("expected error disabling required core plugin")
	}

	c00 := deck.Coordinates{Row: 0, Col: 0}
	if _, err := eng.AssignKeypadAction(c00, "com.test.optional.act"); err != nil {
		t.Fatalf("AssignKeypadAction() error = %v", err)
	}
	if err := eng.UpdateKeypadSlot(c00, "My Opt", "#22C55E", map[string]any{"customKey": "keep-me"}); err != nil {
		t.Fatalf("UpdateKeypadSlot() error = %v", err)
	}

	// Add missing-plugin slot at (0,1) directly via loaded store later or inspect disabled behavior first.
	snapOn := eng.Snapshot()
	if snapOn.Slots[c00.SlotKey()].Visual.Subtitle != "READY" || snapOn.Slots[c00.SlotKey()].Visual.State != 1 {
		t.Fatalf("expected active visual READY on enabled slot, got %+v", snapOn.Slots[c00.SlotKey()].Visual)
	}

	// Disable optional plugin -> WillDisappear dispatched, listener notified, tile renders DISABLED, settings preserved.
	if err := eng.SetPluginEnabled("com.test.optional", false); err != nil {
		t.Fatalf("SetPluginEnabled(false) error = %v", err)
	}
	if eng.IsPluginEnabled("com.test.optional") {
		t.Fatal("expected IsPluginEnabled(false)")
	}
	opt.mu.Lock()
	sawDisappear := false
	for _, ev := range opt.events {
		if ev.Type == plugin.EventWillDisappear {
			sawDisappear = true
		}
	}
	stillEnabled := opt.enabled
	opt.mu.Unlock()
	if !sawDisappear || stillEnabled {
		t.Fatalf("expected WillDisappear and enabled=false, sawDisappear=%v stillEnabled=%v", sawDisappear, stillEnabled)
	}

	snapOff := eng.Snapshot()
	rOff := snapOff.Slots[c00.SlotKey()]
	if !rOff.Disabled || rOff.Visual.Subtitle != "DISABLED" || rOff.Config.Settings["customKey"] != "keep-me" {
		t.Fatalf("expected DISABLED visual with preserved settings, got %+v", rOff)
	}
	if err := eng.PressKey(context.Background(), c00); err == nil {
		t.Fatal("expected pressing disabled plugin key to return error")
	}

	// Perform another slot edit & undo -> global DisabledPlugins must stay disabled.
	c01 := deck.Coordinates{Row: 0, Col: 1}
	if _, err := eng.AssignKeypadAction(c01, core.ActionDelay); err != nil {
		t.Fatalf("AssignKeypadAction(delay) error = %v", err)
	}
	if _, err := eng.Undo(); err != nil {
		t.Fatalf("Undo() error = %v", err)
	}
	if eng.IsPluginEnabled("com.test.optional") {
		t.Fatal("expected Undo to preserve global DisabledPlugins state")
	}

	// Reload from persisted store -> optional plugin stays disabled and unavailable action renders MISSING PLUGIN.
	cfgReloaded, _ := store.Load()
	cfgReloaded.Profiles[0].Pages[0].Slots["0,1"] = &deck.SlotConfig{
		Context:    deck.NewUUID(),
		ActionUUID: "com.uninstalled.missing.act",
		Title:      "Old",
	}
	_ = store.Save(cfgReloaded)

	reg2 := plugin.NewRegistry()
	_ = reg2.RegisterProvider(core.New())
	opt2 := &toggleableTestProvider{enabled: true}
	_ = reg2.RegisterProvider(opt2)
	eng2, err := NewEngine(store, reg2)
	if err != nil {
		t.Fatalf("NewEngine(reload) error = %v", err)
	}
	defer func() { _ = eng2.Close() }()

	if eng2.IsPluginEnabled("com.test.optional") {
		t.Fatal("expected optional plugin disabled after reload")
	}
	snapReload := eng2.Snapshot()
	if !snapReload.Slots["0,1"].Unavailable || snapReload.Slots["0,1"].Visual.Subtitle != "MISSING PLUGIN" {
		t.Fatalf("expected MISSING PLUGIN visual on uninstalled slot, got %+v", snapReload.Slots["0,1"])
	}

	// Re-enable optional plugin -> listener notified, WillAppear dispatched, READY restored.
	if err := eng2.SetPluginEnabled("com.test.optional", true); err != nil {
		t.Fatalf("SetPluginEnabled(true) error = %v", err)
	}
	snapBack := eng2.Snapshot()
	if snapBack.Slots[c00.SlotKey()].Disabled || snapBack.Slots[c00.SlotKey()].Visual.Subtitle != "READY" {
		t.Fatalf("expected restored READY visual after re-enable, got %+v", snapBack.Slots[c00.SlotKey()])
	}
}

func TestEngineIndependentViewportsLifecycleAndClamping(t *testing.T) {
	eng, rec := newMoveTestEngine(t)
	ctx := context.Background()

	prof1ID := eng.Snapshot().ActiveProfileID
	c00 := deck.Coordinates{Row: 0, Col: 0}
	c01 := deck.Coordinates{Row: 0, Col: 1}
	c02 := deck.Coordinates{Row: 0, Col: 2}
	c10 := deck.Coordinates{Row: 1, Col: 0}
	c11 := deck.Coordinates{Row: 1, Col: 1}

	sharedSlot, err := eng.AssignKeypadAction(c00, "test.lifecycle.action")
	if err != nil {
		t.Fatalf("AssignKeypadAction(shared) error = %v", err)
	}
	if _, err := eng.AssignKeypadAction(c01, core.ActionPageNext); err != nil {
		t.Fatalf("AssignKeypadAction(pageNext) error = %v", err)
	}
	if _, err := eng.AssignKeypadAction(c02, core.ActionFolderOpen); err != nil {
		t.Fatalf("AssignKeypadAction(folderOpen) error = %v", err)
	}
	_ = eng.UpdateKeypadSlot(c02, "Sub", "#FBBF24", map[string]any{"folderName": "Sub"})
	if _, err := eng.AddPage("P1-Page2"); err != nil {
		t.Fatalf("AddPage(P1-Page2) error = %v", err)
	}
	p1Page2Slot, err := eng.AssignKeypadAction(c00, "test.lifecycle.action")
	if err != nil {
		t.Fatalf("AssignKeypadAction(p1Page2Slot) error = %v", err)
	}
	if err := eng.GoToPage(1); err != nil {
		t.Fatalf("GoToPage(1) error = %v", err)
	}

	// Create Profile 2 with 2 pages and MultiAction + ProfileSwitch actions.
	prof2, err := eng.CreateProfile("Watch", deck.GridSize{Cols: 2, Rows: 2}, 1)
	if err != nil {
		t.Fatalf("CreateProfile(Watch) error = %v", err)
	}
	p2Slot, err := eng.AssignKeypadAction(c00, "test.lifecycle.action")
	if err != nil {
		t.Fatalf("AssignKeypadAction(p2Slot) error = %v", err)
	}
	if _, err := eng.AssignKeypadAction(c10, core.ActionProfileSwitch); err != nil {
		t.Fatalf("AssignKeypadAction(profileSwitch) error = %v", err)
	}
	_ = eng.UpdateKeypadSlot(c10, "ToDefault", "#A855F7", map[string]any{"profile": prof1ID})
	if _, err := eng.AssignKeypadAction(c11, core.ActionMultiAction); err != nil {
		t.Fatalf("AssignKeypadAction(multi) error = %v", err)
	}
	_ = eng.UpdateKeypadSlot(c11, "MultiNav", "#22C55E", map[string]any{
		"steps": []core.MultiActionStep{{ActionUUID: core.ActionPageNext, Settings: map[string]any{"wrap": true}}},
	})
	if _, err := eng.AddPage("Watch-P2"); err != nil {
		t.Fatalf("AddPage(Watch-P2) error = %v", err)
	}
	if err := eng.GoToPage(1); err != nil {
		t.Fatalf("GoToPage(1) error = %v", err)
	}

	// Switch Studio back to Profile 1 Page 1.
	if err := eng.SwitchProfile(prof1ID); err != nil {
		t.Fatalf("SwitchProfile(prof1) error = %v", err)
	}

	// Register Viewport A on Profile 1 (independent) and Viewport B on Profile 2 (Watch).
	vpA, unregA := eng.RegisterViewport(Viewport{ProfileID: prof1ID})
	defer unregA()
	vpB, unregB := eng.RegisterViewport(Viewport{ProfileID: prof2.ID})
	defer unregB()

	var vpBSubCount atomic.Int32
	unsubB := eng.SubscribeViewport(vpB, func(Snapshot) {
		vpBSubCount.Add(1)
	})
	defer unsubB()

	// Ref-counting: Studio and vpA both view sharedSlot. Moving Studio to Profile 2 must NOT disappear sharedSlot,
	// while p2Slot was already appeared via vpB and must NOT double-appear when Studio moves to Profile 2.
	rec.reset()
	if err := eng.SwitchProfile(prof2.ID); err != nil {
		t.Fatalf("SwitchProfile(prof2) error = %v", err)
	}
	for _, ev := range rec.events() {
		if ev.Context == sharedSlot.Context && ev.Type == plugin.EventWillDisappear {
			t.Fatalf("sharedSlot disappeared while vpA was still viewing it: %+v", ev)
		}
		if ev.Context == p2Slot.Context && ev.Type == plugin.EventWillAppear {
			t.Fatalf("p2Slot double-appeared even though vpB already had it visible: %+v", ev)
		}
	}

	// Verify vpA stayed on Profile 1 Page 0 while Studio is on Profile 2.
	if sA := eng.ViewportSnapshot(vpA); sA.ActiveProfileID != prof1ID || sA.PageIndex != 0 {
		t.Fatalf("expected vpA isolated on prof1 page 0, got profile=%s page=%d", sA.ActiveProfileID, sA.PageIndex)
	}

	// Scoped Core navigation: pressing PageNext on vpA advances ONLY vpA to Page 2 (appearing p1Page2Slot and disappearing sharedSlot).
	rec.reset()
	if err := eng.ViewportPressKey(ctx, vpA, c01); err != nil {
		t.Fatalf("ViewportPressKey(vpA, PageNext) error = %v", err)
	}
	if sA := eng.ViewportSnapshot(vpA); sA.ActiveProfileID != prof1ID || sA.PageIndex != 1 {
		t.Fatalf("expected vpA at prof1 page 1, got profile=%s page=%d", sA.ActiveProfileID, sA.PageIndex)
	}
	if stStudio := eng.Snapshot(); stStudio.ActiveProfileID != prof2.ID || stStudio.PageIndex != 0 {
		t.Fatalf("Studio moved unexpectedly when vpA navigated: profile=%s page=%d", stStudio.ActiveProfileID, stStudio.PageIndex)
	}
	var sawSharedDisappear, sawPage2Appear bool
	for _, ev := range rec.events() {
		if ev.Context == sharedSlot.Context && ev.Type == plugin.EventWillDisappear {
			sawSharedDisappear = true
		}
		if ev.Context == p1Page2Slot.Context && ev.Type == plugin.EventWillAppear {
			sawPage2Appear = true
		}
	}
	if !sawSharedDisappear || !sawPage2Appear {
		t.Fatalf("expected sharedSlot WillDisappear and p1Page2Slot WillAppear when last viewer moved, got %+v", rec.events())
	}

	// Return vpA to Page 1, open Folder & AutoBack independently.
	if err := eng.ViewportPrevPage(vpA, true); err != nil {
		t.Fatalf("ViewportPrevPage(vpA) error = %v", err)
	}
	if err := eng.ViewportPressKey(ctx, vpA, c02); err != nil {
		t.Fatalf("ViewportPressKey(vpA, FolderOpen) error = %v", err)
	}
	if sA := eng.ViewportSnapshot(vpA); !sA.InFolder || len(sA.FolderPath) != 1 || sA.FolderPath[0] != "Sub" {
		t.Fatalf("expected vpA inside folder Sub, got %+v", sA.FolderPath)
	}
	if eng.Snapshot().InFolder {
		t.Fatal("expected Studio not inside folder when vpA opened folder")
	}
	if err := eng.ViewportPressKey(ctx, vpA, c00); err != nil {
		t.Fatalf("ViewportPressKey(vpA, AutoBack) error = %v", err)
	}
	if eng.ViewportSnapshot(vpA).InFolder {
		t.Fatal("expected vpA exited folder after AutoBack")
	}

	// Scoped Multi-Action on vpB advances vpB to Watch-P2 without affecting Studio.
	if err := eng.ViewportPressKey(ctx, vpB, c11); err != nil {
		t.Fatalf("ViewportPressKey(vpB, MultiAction) error = %v", err)
	}
	if sB := eng.ViewportSnapshot(vpB); sB.ActiveProfileID != prof2.ID || sB.PageIndex != 1 {
		t.Fatalf("expected vpB on Watch-P2, got profile=%s page=%d", sB.ActiveProfileID, sB.PageIndex)
	}
	if eng.Snapshot().PageIndex != 0 {
		t.Fatalf("expected Studio still on Watch Page 0, got %d", eng.Snapshot().PageIndex)
	}

	// Scoped Profile Switch button on vpB switches vpB to prof1ID without changing Studio.
	if err := eng.ViewportGoToPage(vpB, 1); err != nil {
		t.Fatalf("ViewportGoToPage(vpB, 1) error = %v", err)
	}
	if err := eng.ViewportPressKey(ctx, vpB, c10); err != nil {
		t.Fatalf("ViewportPressKey(vpB, ProfileSwitch) error = %v", err)
	}
	if sB := eng.ViewportSnapshot(vpB); sB.ActiveProfileID != prof1ID {
		t.Fatalf("expected vpB switched to %s, got %s", prof1ID, sB.ActiveProfileID)
	}
	if eng.Snapshot().ActiveProfileID != prof2.ID {
		t.Fatalf("expected Studio still on %s", prof2.ID)
	}
	if vpBSubCount.Load() == 0 {
		t.Fatal("expected vpB subscriber notified on viewport navigation")
	}

	// FollowHost mirrors Studio and captures current position when turned off.
	if err := eng.SetViewportFollowHost(vpB, true); err != nil {
		t.Fatalf("SetViewportFollowHost(true) error = %v", err)
	}
	if err := eng.NextPage(true); err != nil {
		t.Fatalf("Studio NextPage error = %v", err)
	}
	if sB := eng.ViewportSnapshot(vpB); !sB.FollowHost || sB.ActiveProfileID != prof2.ID || sB.PageIndex != 1 {
		t.Fatalf("expected FollowHost vpB mirroring Studio Watch Page 1, got follow=%v profile=%s page=%d",
			sB.FollowHost, sB.ActiveProfileID, sB.PageIndex)
	}
	if err := eng.SetViewportFollowHost(vpB, false); err != nil {
		t.Fatalf("SetViewportFollowHost(false) error = %v", err)
	}
	if err := eng.PrevPage(true); err != nil {
		t.Fatalf("Studio PrevPage error = %v", err)
	}
	if sB := eng.ViewportSnapshot(vpB); sB.FollowHost || sB.ActiveProfileID != prof2.ID || sB.PageIndex != 1 {
		t.Fatalf("expected vpB frozen at Watch Page 1 after disabling FollowHost, got follow=%v profile=%s page=%d",
			sB.FollowHost, sB.ActiveProfileID, sB.PageIndex)
	}

	// Deleting the profile vpB is viewing clamps vpB cleanly onto Studio's active fallback profile.
	if err := eng.SwitchProfile(prof1ID); err != nil {
		t.Fatalf("SwitchProfile(prof1) error = %v", err)
	}
	if err := eng.DeleteProfile(prof2.ID); err != nil {
		t.Fatalf("DeleteProfile(prof2) error = %v", err)
	}
	if sB := eng.ViewportSnapshot(vpB); sB.ActiveProfileID != prof1ID || sB.PageIndex != 0 {
		t.Fatalf("expected vpB clamped to %s page 0 after profile deletion, got profile=%s page=%d",
			prof1ID, sB.ActiveProfileID, sB.PageIndex)
	}

	// Unregistering vpA (while Studio is on Page 0) leaves sharedSlot appeared; unregistering does not panic.
	unregA()
	unregA()
}
