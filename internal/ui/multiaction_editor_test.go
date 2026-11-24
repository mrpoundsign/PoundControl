package ui

import (
	"slices"
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"github.com/mrpoundsign/PoundControl/pkg/builtin/apps"
	"github.com/mrpoundsign/PoundControl/pkg/builtin/core"
	"github.com/mrpoundsign/PoundControl/pkg/deck"
)

func findButtonWithText(root fyne.CanvasObject, label string) *widget.Button {
	if root == nil {
		return nil
	}
	if btn, ok := root.(*widget.Button); ok {
		if btn.Text == label {
			return btn
		}
		return nil
	}
	if cont, ok := root.(*fyne.Container); ok {
		for _, child := range cont.Objects {
			if found := findButtonWithText(child, label); found != nil {
				return found
			}
		}
	}
	if acc, ok := root.(*widget.Accordion); ok {
		for _, item := range acc.Items {
			if found := findButtonWithText(item.Detail, label); found != nil {
				return found
			}
		}
	}
	return nil
}

func waitForCondition(t *testing.T, timeout time.Duration, cond func() bool) {
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
		t.Fatal("timed out waiting for condition")
	}
}

func TestMultiActionSummaryAndEditorLifecycle(t *testing.T) {
	eng, _, _, mw := newTestStudioWithApps(t)

	// Assign Multi-Action to (0, 0).
	multiBtn, ok := mw.catalogButtons[core.ActionMultiAction]
	if !ok {
		t.Fatal("missing Multi-Action button in catalog")
	}
	test.Tap(multiBtn)
	drainUI()

	editBtn := findButtonWithText(mw.inspector.formBox, "Edit Sequence...")
	if editBtn == nil {
		t.Fatal("expected Edit Sequence... button in Multi-Action inspector")
	}
	if mw.multiEditor.Active() {
		t.Fatal("expected multiEditor inactive initially")
	}

	// Open Sequence Edit Mode.
	test.Tap(editBtn)
	drainUI()

	if !mw.multiEditor.Active() {
		t.Fatal("expected multiEditor active after tapping Edit Sequence...")
	}
	if !mw.multiEditor.Object().Visible() {
		t.Error("expected multiEditor view to be visible")
	}
	if mw.deckCenterObj.Visible() {
		t.Error("expected deckCenterObj to be hidden while editing sequence")
	}
	if !strings.Contains(mw.catalogSub.Text, "Sequence Mode") {
		t.Errorf("expected Sequence Mode catalog subtitle, got %q", mw.catalogSub.Text)
	}

	// Clicking Multi-Action in catalog while in Sequence Mode should not nest or overwrite the slot.
	test.Tap(multiBtn)
	drainUI()
	if len(mw.multiEditor.Steps()) != 0 {
		t.Fatalf("expected nested Multi-Action click to be ignored, got %d steps", len(mw.multiEditor.Steps()))
	}
	if !strings.Contains(mw.statusNote, "individual actions") {
		t.Errorf("expected informative status for nested multi-action, got %q", mw.statusNote)
	}

	// Clicking actions in Action Catalog appends blocks to sequence instead of overwriting slot.
	test.Tap(mw.catalogButtons[apps.ActionHotkey])
	drainUI()
	test.Tap(mw.catalogButtons[core.ActionPageNext])
	drainUI()

	snap := eng.Snapshot()
	slot := snap.Slots[(deck.Coordinates{Row: 0, Col: 0}).SlotKey()]
	if slot.Config == nil || slot.Config.ActionUUID != core.ActionMultiAction {
		t.Fatalf("expected slot to remain ActionMultiAction, got %+v", slot.Config)
	}
	steps := mw.multiEditor.Steps()
	if len(steps) != 2 {
		t.Fatalf("expected 2 appended steps, got %d", len(steps))
	}
	if steps[0].ActionUUID != apps.ActionHotkey || steps[1].ActionUUID != core.ActionPageNext {
		t.Fatalf("unexpected step order: %+v", steps)
	}

	persisted, err := core.ParseMultiActionSteps(slot.Config.Settings)
	if err != nil || len(persisted) != 2 {
		t.Fatalf("expected 2 persisted steps in engine, got %d (err=%v)", len(persisted), err)
	}

	// Back to Deck exits Sequence Edit Mode.
	mw.exitMultiActionEditor()
	drainUI()
	if mw.multiEditor.Active() || mw.multiEditor.Object().Visible() || !mw.deckCenterObj.Visible() {
		t.Fatal("expected clean exit back to Virtual Deck")
	}
	if !strings.Contains(mw.catalogSub.Text, "assign to the selected slot") {
		t.Errorf("expected catalog subtitle restored, got %q", mw.catalogSub.Text)
	}
}

func TestMultiActionWaitReorderMuteDuplicateDeleteAndUndoRedo(t *testing.T) {
	eng, _, _, mw := newTestStudioWithApps(t)

	test.Tap(mw.catalogButtons[core.ActionMultiAction])
	drainUI()
	mw.openSelectedMultiActionEditor()
	drainUI()

	test.Tap(mw.catalogButtons[apps.ActionHotkey])
	drainUI()
	test.Tap(mw.multiEditor.addWaitTopBtn)
	drainUI()
	test.Tap(mw.catalogButtons[core.ActionPageNext])
	drainUI()

	steps := mw.multiEditor.Steps()
	if len(steps) != 3 {
		t.Fatalf("expected 3 steps, got %d", len(steps))
	}
	if steps[1].ActionUUID != core.ActionDelay || core.StepDelayMs(steps[1]) != 250 {
		t.Fatalf("expected 250ms wait block at index 1, got %+v", steps[1])
	}

	// Click 500ms preset on Wait block.
	waitBlock := mw.multiEditor.blockWidgets[1]
	preset500 := findButtonWithText(waitBlock.contentBox, "500ms")
	if preset500 == nil {
		t.Fatal("missing 500ms preset button on Wait block")
	}
	test.Tap(preset500)
	drainUI()
	if got := core.StepDelayMs(mw.multiEditor.Steps()[1]); got != 500 {
		t.Fatalf("after 500ms preset, StepDelayMs = %d, want 500", got)
	}

	// Edit Wait entry inline and trigger FocusLost.
	waitBlock = mw.multiEditor.blockWidgets[1]
	waitBlock.waitEntry.SetText("750")
	waitBlock.waitEntry.FocusLost()
	drainUI()
	if got := core.StepDelayMs(mw.multiEditor.Steps()[1]); got != 750 {
		t.Fatalf("after inline FocusLost edit, StepDelayMs = %d, want 750", got)
	}

	// Toggle Continue on Error.
	mw.multiEditor.continueCheck.SetChecked(true)
	drainUI()
	snap := eng.Snapshot()
	slotCfg := snap.Slots["0,0"].Config
	if !core.MultiActionContinueOnError(slotCfg.Settings) {
		t.Error("expected continueOnError=true persisted in engine")
	}

	// Reorder Wait block up from index 1 -> 0 using upBtn.
	test.Tap(mw.multiEditor.blockWidgets[1].upBtn)
	drainUI()
	steps = mw.multiEditor.Steps()
	if steps[0].ActionUUID != core.ActionDelay || steps[1].ActionUUID != apps.ActionHotkey {
		t.Fatalf("expected Wait moved to index 0, got %+v", steps)
	}

	// Duplicate block 0 -> inserts clone at index 1.
	test.Tap(mw.multiEditor.blockWidgets[0].dupBtn)
	drainUI()
	steps = mw.multiEditor.Steps()
	if len(steps) != 4 || steps[1].ActionUUID != core.ActionDelay || steps[1].ID == steps[0].ID {
		t.Fatalf("unexpected duplicate result: %+v", steps)
	}

	// Mute block 2 (ActionHotkey).
	test.Tap(mw.multiEditor.blockWidgets[2].muteBtn)
	drainUI()
	if !mw.multiEditor.Steps()[2].Disabled {
		t.Fatal("expected block 2 to be marked Disabled")
	}

	// Delete block 1 (duplicate Wait) and verify Undo/Redo round-trip.
	test.Tap(mw.multiEditor.blockWidgets[1].delBtn)
	drainUI()
	if len(mw.multiEditor.Steps()) != 3 {
		t.Fatalf("expected 3 steps after delete, got %d", len(mw.multiEditor.Steps()))
	}

	mw.handleUndo()
	drainUI()
	if len(mw.multiEditor.Steps()) != 4 {
		t.Fatalf("expected Undo to restore deleted block (4 steps), got %d", len(mw.multiEditor.Steps()))
	}

	mw.handleRedo()
	drainUI()
	if len(mw.multiEditor.Steps()) != 3 {
		t.Fatalf("expected Redo to re-delete block (3 steps), got %d", len(mw.multiEditor.Steps()))
	}
}

func TestMultiActionStepInspectorAndParentAppearancePreservation(t *testing.T) {
	eng, _, appsDrv, mw := newTestStudioWithApps(t)

	test.Tap(mw.catalogButtons[core.ActionMultiAction])
	drainUI()

	// Configure parent Multi-Action title, accent color, and hotkey.
	mw.inspector.titleEntry.SetText("Go Live")
	mw.inspector.accentColorCtrl.SetValue("#22C55E")
	mw.inspector.hotkeyEntry.SetText("Ctrl+Shift+L")
	test.Tap(mw.inspector.saveBtn)
	drainUI()

	mw.openSelectedMultiActionEditor()
	drainUI()
	test.Tap(mw.catalogButtons[apps.ActionHotkey])
	drainUI()

	if !mw.inspector.stepMode {
		t.Fatal("expected inspector in stepMode after appending action")
	}
	if !strings.Contains(mw.inspector.slotLbl.Text, "Sequence Step 1 of 1") {
		t.Errorf("unexpected step slot label: %q", mw.inspector.slotLbl.Text)
	}
	if mw.inspector.clearBtn.Text != "Remove" {
		t.Errorf("expected clearBtn text 'Remove' in stepMode, got %q", mw.inspector.clearBtn.Text)
	}
	if mw.inspector.moveCopyRow.Visible() {
		t.Error("expected moveCopyRow hidden in stepMode")
	}

	// Edit step setting in Step Inspector and save.
	chordEntry := mw.inspector.fields["chord"].widget.(*widget.Entry)
	chordEntry.SetText("Ctrl+Shift+M")
	test.Tap(mw.inspector.saveBtn)
	drainUI()

	steps := mw.multiEditor.Steps()
	if steps[0].Settings["chord"] != "Ctrl+Shift+M" {
		t.Fatalf("expected step chord saved as Ctrl+Shift+M, got %v", steps[0].Settings["chord"])
	}
	if !strings.Contains(mw.multiEditor.blockWidgets[0].summaryTxt.Text, "Ctrl+Shift+M") {
		t.Errorf("expected block summary to show Ctrl+Shift+M, got %q", mw.multiEditor.blockWidgets[0].summaryTxt.Text)
	}

	// Parent button appearance & hotkey must remain intact.
	parentSlot := eng.Snapshot().Slots["0,0"].Config
	if parentSlot.Title != "Go Live" || parentSlot.Color != "#22C55E" || parentSlot.Hotkey != "Ctrl+Shift+L" {
		t.Fatalf("parent slot modified unexpectedly: %+v", parentSlot)
	}

	// Trigger tests just this step.
	test.Tap(mw.inspector.triggerBtn)
	drainUI()
	appsDrv.mu.Lock()
	chordsLen := len(appsDrv.chords)
	appsDrv.mu.Unlock()
	if chordsLen != 1 {
		t.Fatalf("expected 1 triggered chord from step test, got %d", chordsLen)
	}

	// Tapping Hat returns inspector to Button Properties.
	test.Tap(mw.multiEditor.hatWidget)
	drainUI()
	if mw.inspector.stepMode {
		t.Fatal("expected Hat tap to exit stepMode")
	}
	if mw.inspector.titleEntry == nil || mw.inspector.titleEntry.Text != "Go Live" {
		t.Fatalf("expected parent titleEntry 'Go Live', got %+v", mw.inspector.titleEntry)
	}
	if mw.inspector.clearBtn.Text != "Clear" || !mw.inspector.moveCopyRow.Visible() {
		t.Error("expected normal inspector action row restored")
	}

	// Re-select step, then use Button Properties back button and Remove button.
	test.Tap(mw.multiEditor.blockWidgets[0])
	drainUI()
	backPropsBtn := findButtonWithText(mw.inspector.formBox, "Button Properties")
	if backPropsBtn == nil {
		t.Fatal("expected Button Properties button in stepMode")
	}
	test.Tap(backPropsBtn)
	drainUI()
	if mw.inspector.stepMode {
		t.Fatal("expected Button Properties button to exit stepMode")
	}

	test.Tap(mw.multiEditor.blockWidgets[0])
	drainUI()
	test.Tap(mw.inspector.clearBtn) // "Remove"
	drainUI()
	if len(mw.multiEditor.Steps()) != 0 {
		t.Fatalf("expected step removed, got %d steps", len(mw.multiEditor.Steps()))
	}
	if mw.inspector.stepMode {
		t.Fatal("expected inspector to return to button properties when last step removed")
	}
}

func TestMultiActionTestRunSkipsMutedAndStopsCleanly(t *testing.T) {
	_, _, appsDrv, mw := newTestStudioWithApps(t)

	test.Tap(mw.catalogButtons[core.ActionMultiAction])
	drainUI()
	mw.openSelectedMultiActionEditor()
	drainUI()

	// Step 0: Hotkey Ctrl+Alt+1 (muted)
	test.Tap(mw.catalogButtons[apps.ActionHotkey])
	drainUI()
	mw.inspector.fields["chord"].widget.(*widget.Entry).SetText("Ctrl+Alt+1")
	test.Tap(mw.inspector.saveBtn)
	drainUI()
	mw.multiEditor.ToggleStepDisabled(0)
	drainUI()

	// Step 1: Wait 20ms
	mw.multiEditor.AppendWait(20)
	drainUI()

	// Step 2: Hotkey Ctrl+Alt+2
	test.Tap(mw.catalogButtons[apps.ActionHotkey])
	drainUI()
	mw.inspector.fields["chord"].widget.(*widget.Entry).SetText("Ctrl+Alt+2")
	test.Tap(mw.inspector.saveBtn)
	drainUI()

	test.Tap(mw.multiEditor.testRunBtn)
	waitForCondition(t, 3*time.Second, func() bool {
		return !mw.multiEditor.IsRunning()
	})

	appsDrv.mu.Lock()
	gotChords := slices.Clone(appsDrv.chords)
	appsDrv.chords = nil
	appsDrv.mu.Unlock()
	if len(gotChords) != 1 || gotChords[0].Key != "2" {
		t.Fatalf("expected only unmuted Ctrl+Alt+2 to run, got %+v", gotChords)
	}

	// Verify StopTestRun cancels a long Wait before reaching subsequent steps.
	if err := mw.multiEditor.UpdateStepSettings(1, map[string]any{"ms": 5000}); err != nil {
		t.Fatalf("UpdateStepSettings error = %v", err)
	}
	drainUI()
	mw.multiEditor.StartTestRun()
	time.Sleep(25 * time.Millisecond)
	mw.multiEditor.StopTestRun()
	drainUI()

	if mw.multiEditor.IsRunning() {
		t.Fatal("expected Test Run to stop immediately on StopTestRun")
	}
	time.Sleep(30 * time.Millisecond)
	drainUI()
	appsDrv.mu.Lock()
	afterStopLen := len(appsDrv.chords)
	appsDrv.mu.Unlock()
	if afterStopLen != 0 {
		t.Fatalf("expected subsequent hotkey not to fire after StopTestRun, got %d", afterStopLen)
	}
}

func TestMultiActionExitTransitionsRunModeEscapeAndClearSlot(t *testing.T) {
	_, _, _, mw := newTestStudioWithApps(t)

	test.Tap(mw.catalogButtons[core.ActionMultiAction])
	drainUI()

	// 1. Exiting via Run Mode.
	mw.openSelectedMultiActionEditor()
	drainUI()
	mw.SetRunMode(true)
	drainUI()
	if mw.isMultiEditing() || !mw.deckCenterObj.Visible() {
		t.Fatal("expected SetRunMode(true) to exit multi-action editor")
	}
	mw.SetRunMode(false)
	drainUI()

	// 2. Exiting via Escape key.
	mw.openSelectedMultiActionEditor()
	drainUI()
	mw.win.Canvas().OnTypedKey()(&fyne.KeyEvent{Name: fyne.KeyEscape})
	drainUI()
	if mw.isMultiEditing() || !mw.deckCenterObj.Visible() {
		t.Fatal("expected Escape key to exit multi-action editor")
	}

	// 3. Exiting via Clear slot from Hat inspector.
	mw.openSelectedMultiActionEditor()
	drainUI()
	test.Tap(mw.inspector.clearBtn)
	drainUI()
	if mw.isMultiEditing() || !mw.deckCenterObj.Visible() {
		t.Fatal("expected clearing slot to exit multi-action editor")
	}
}

func TestMultiActionLegacyDelayNormalizationAndMissingActionStep(t *testing.T) {
	eng, _, _, mw := newTestStudioWithApps(t)

	coords := deck.Coordinates{Row: 0, Col: 0}
	if _, err := eng.AssignKeypadAction(coords, core.ActionMultiAction); err != nil {
		t.Fatalf("AssignKeypadAction error = %v", err)
	}
	legacyJSON := `[{"actionUuid":"com.example.missing.action","settings":{"foo":"bar"},"delayMs":400}]`
	if err := eng.UpdateKeypadSlot(coords, "Legacy", "", map[string]any{"steps": legacyJSON}); err != nil {
		t.Fatalf("UpdateKeypadSlot error = %v", err)
	}
	drainUI()

	mw.openSelectedMultiActionEditor()
	drainUI()

	steps := mw.multiEditor.Steps()
	if len(steps) != 2 {
		t.Fatalf("expected legacy delayMs split into 2 blocks, got %d: %+v", len(steps), steps)
	}
	if steps[0].ActionUUID != core.ActionDelay || core.StepDelayMs(steps[0]) != 400 {
		t.Fatalf("expected 400ms Wait block first, got %+v", steps[0])
	}
	if steps[1].ActionUUID != "com.example.missing.action" || steps[1].DelayMs != 0 {
		t.Fatalf("expected missing action second with cleared delayMs, got %+v", steps[1])
	}

	// Select missing action block -> Step Inspector exposes Step Settings JSON fallback.
	mw.multiEditor.SelectStep(1)
	drainUI()
	if !mw.inspector.stepMode || mw.inspector.stepRawJSONEntry == nil {
		t.Fatal("expected Step Inspector raw JSON fallback for missing action")
	}
	mw.inspector.stepRawJSONEntry.SetText(`{"foo":"baz"}`)
	test.Tap(mw.inspector.saveBtn)
	drainUI()

	if got := mw.multiEditor.Steps()[1].Settings["foo"]; got != "baz" {
		t.Fatalf("expected saved fallback setting foo=baz, got %v", got)
	}
}

func TestMultiActionStepAutoSaveAcrossSwitchesTestRunAndClose(t *testing.T) {
	eng, _, appsDrv, mw := newTestStudioWithApps(t)

	test.Tap(mw.catalogButtons[core.ActionMultiAction])
	drainUI()
	mw.openSelectedMultiActionEditor()
	drainUI()

	// Append Hotkey (step 0) and Go To Page (step 1).
	test.Tap(mw.catalogButtons[apps.ActionHotkey])
	drainUI()
	test.Tap(mw.catalogButtons[core.ActionPageGoTo])
	drainUI()

	// Step 1 is active now; switch back to step 0 without editing -> zero extra HistoryRevision changes.
	revBefore := eng.Snapshot().HistoryRevision
	test.Tap(mw.multiEditor.blockWidgets[0])
	drainUI()
	if got := eng.Snapshot().HistoryRevision; got != revBefore {
		t.Fatalf("selecting without editing mutated history rev %d -> %d", revBefore, got)
	}

	// Edit step 0 chord WITHOUT clicking Save, then tap step 1 -> step 0 auto-saves!
	mw.inspector.fields["chord"].widget.(*widget.Entry).SetText("Ctrl+Shift+1")
	test.Tap(mw.multiEditor.blockWidgets[1])
	drainUI()

	if mw.unsavedDlg != nil {
		t.Fatal("expected step switching to auto-save without showing unsaved dialog")
	}
	steps := mw.multiEditor.Steps()
	if steps[0].Settings["chord"] != "Ctrl+Shift+1" {
		t.Fatalf("expected step 0 chord auto-saved to Ctrl+Shift+1, got %v", steps[0].Settings["chord"])
	}
	if !strings.Contains(mw.multiEditor.blockWidgets[0].summaryTxt.Text, "Ctrl+Shift+1") {
		t.Errorf("expected block 0 summary updated to Ctrl+Shift+1, got %q", mw.multiEditor.blockWidgets[0].summaryTxt.Text)
	}

	// Edit step 1 page WITHOUT clicking Save, then tap Hat -> step 1 auto-saves!
	mw.inspector.fields["page"].widget.(*widget.Entry).SetText("3")
	test.Tap(mw.multiEditor.hatWidget)
	drainUI()

	steps = mw.multiEditor.Steps()
	if got := steps[1].Settings["page"]; got != float64(3) && got != 3 {
		t.Fatalf("expected step 1 page auto-saved as 3, got %v (%T)", got, got)
	}

	// Edit step 0 chord WITHOUT clicking Save, then click Test Run -> auto-saves and executes new chord!
	test.Tap(mw.multiEditor.blockWidgets[0])
	drainUI()
	mw.inspector.fields["chord"].widget.(*widget.Entry).SetText("Ctrl+Shift+2")
	test.Tap(mw.multiEditor.testRunBtn)
	waitForCondition(t, 3*time.Second, func() bool {
		return !mw.multiEditor.IsRunning()
	})

	if got := mw.multiEditor.Steps()[0].Settings["chord"]; got != "Ctrl+Shift+2" {
		t.Fatalf("expected Test Run to auto-save chord Ctrl+Shift+2, got %v", got)
	}
	appsDrv.mu.Lock()
	chords := slices.Clone(appsDrv.chords)
	appsDrv.chords = nil
	appsDrv.mu.Unlock()
	if len(chords) != 1 || chords[0].Key != "2" {
		t.Fatalf("expected Test Run to fire auto-saved Ctrl+Shift+2, got %+v", chords)
	}

	// Edit step 0 chord WITHOUT clicking Save, then press Escape -> auto-saves before exiting editor!
	mw.inspector.fields["chord"].widget.(*widget.Entry).SetText("Ctrl+Shift+3")
	mw.win.Canvas().OnTypedKey()(&fyne.KeyEvent{Name: fyne.KeyEscape})
	drainUI()

	if mw.isMultiEditing() {
		t.Fatal("expected Escape to auto-save step and exit sequence editor")
	}
	persisted, err := core.ParseMultiActionSteps(eng.Snapshot().Slots["0,0"].Config.Settings)
	if err != nil || len(persisted) != 2 || persisted[0].Settings["chord"] != "Ctrl+Shift+3" {
		t.Fatalf("expected Escape exit to preserve auto-saved Ctrl+Shift+3, got %+v (err=%v)", persisted, err)
	}

	// Verify invalid JSON in a missing-action step blocks switching so work isn't lost,
	// and valid edit in Step Mode auto-saves on window close request.
	if err := eng.UpdateKeypadSlot(deck.Coordinates{Row: 0, Col: 0}, "Multi", "", map[string]any{
		"steps": `[{"id":"m1","actionUuid":"com.example.missing.action","settings":{"foo":"bar"}},{"id":"s2","actionUuid":"` + apps.ActionHotkey + `","settings":{"chord":"Ctrl+Alt+9"}}]`,
	}); err != nil {
		t.Fatalf("UpdateKeypadSlot error = %v", err)
	}
	drainUI()
	mw.openSelectedMultiActionEditor()
	drainUI()
	mw.multiEditor.SelectStep(0)
	drainUI()
	mw.inspector.stepRawJSONEntry.SetText("{broken")
	test.Tap(mw.multiEditor.blockWidgets[1])
	drainUI()
	if mw.multiEditor.SelectedStepIndex() != 0 {
		t.Fatalf("expected invalid JSON auto-save failure to keep selection on step 0, got %d", mw.multiEditor.SelectedStepIndex())
	}

	// Fix JSON and test handleCloseRequest auto-saves without unsaved dialog.
	mw.inspector.stepRawJSONEntry.SetText(`{"foo":"updated"}`)
	mw.handleCloseRequest()
	drainUI()
	if mw.unsavedDlg != nil {
		t.Fatal("expected step auto-save on close not to show unsaved changes dialog")
	}
	finalSteps, err := core.ParseMultiActionSteps(eng.Snapshot().Slots["0,0"].Config.Settings)
	if err != nil || finalSteps[0].Settings["foo"] != "updated" {
		t.Fatalf("expected handleCloseRequest to auto-save step JSON, got %+v (err=%v)", finalSteps, err)
	}
}

func TestUnsavedSlotChangesGuardAcrossNavigationAndClose(t *testing.T) {
	eng, _, _, mw := newTestStudioWithApps(t)
	if _, err := eng.AddPage("Page 2"); err != nil {
		t.Fatalf("AddPage error = %v", err)
	}
	if _, err := eng.CreateProfile("Second", deck.DefaultGrid(), 0); err != nil {
		t.Fatalf("CreateProfile error = %v", err)
	}
	if err := eng.SwitchProfile(eng.Snapshot().Profiles[0].ID); err != nil {
		t.Fatalf("SwitchProfile error = %v", err)
	}
	drainUI()

	c00 := deck.Coordinates{Row: 0, Col: 0}
	c01 := deck.Coordinates{Row: 0, Col: 1}
	mw.SelectSlot(c00)
	drainUI()
	test.Tap(mw.catalogButtons[core.ActionCounter])
	drainUI()

	// 1. Edit Title without saving -> tapping (0,1) triggers Unsaved Changes dialog; Cancel preserves edits on (0,0).
	mw.inspector.titleEntry.SetText("Draft Title")
	test.Tap(mw.surface.tiles[c01.SlotKey()])
	drainUI()

	if mw.unsavedDlg == nil {
		t.Fatal("expected Unsaved Changes dialog when tapping another tile with unsaved edits")
	}
	if mw.SelectedSlot() != c00 {
		t.Fatalf("expected selection to remain at (0,0) while dialog open, got %+v", mw.SelectedSlot())
	}

	test.Tap(mw.unsavedCancelBtn)
	drainUI()
	if mw.unsavedDlg != nil || mw.SelectedSlot() != c00 || mw.inspector.titleEntry.Text != "Draft Title" {
		t.Fatalf("expected Cancel to stay on (0,0) with Draft Title, slot=%+v title=%q", mw.SelectedSlot(), mw.inspector.titleEntry.Text)
	}

	// 2. Tap (0,1) again -> Save persists "Draft Title" and selects (0,1).
	test.Tap(mw.surface.tiles[c01.SlotKey()])
	drainUI()
	if mw.unsavedSaveBtn == nil {
		t.Fatal("expected unsavedSaveBtn on dialog")
	}
	test.Tap(mw.unsavedSaveBtn)
	drainUI()

	if mw.SelectedSlot() != c01 {
		t.Fatalf("expected Save in dialog to move selection to (0,1), got %+v", mw.SelectedSlot())
	}
	if got := eng.Snapshot().Slots[c00.SlotKey()].Config.Title; got != "Draft Title" {
		t.Fatalf("expected (0,0) title saved as Draft Title, got %q", got)
	}

	// 3. Return to (0,0), make unsaved edit -> Discard drops edit and selects (0,1).
	test.Tap(mw.surface.tiles[c00.SlotKey()])
	drainUI()
	mw.inspector.titleEntry.SetText("Should Discard")
	test.Tap(mw.surface.tiles[c01.SlotKey()])
	drainUI()
	if mw.unsavedDiscardBtn == nil {
		t.Fatal("expected unsavedDiscardBtn on dialog")
	}
	test.Tap(mw.unsavedDiscardBtn)
	drainUI()

	if mw.SelectedSlot() != c01 {
		t.Fatalf("expected Discard to move selection to (0,1), got %+v", mw.SelectedSlot())
	}
	if got := eng.Snapshot().Slots[c00.SlotKey()].Config.Title; got != "Draft Title" {
		t.Fatalf("expected (0,0) title to remain Draft Title after Discard, got %q", got)
	}

	// 4. Guard page switch, profile switch, and Run Mode checkbox.
	test.Tap(mw.surface.tiles[c00.SlotKey()])
	drainUI()
	mw.inspector.titleEntry.SetText("Unsaved Nav")

	// Page switch blocked on Cancel, proceeds on Discard.
	test.Tap(mw.pageButtons[1])
	drainUI()
	if mw.unsavedDlg == nil || eng.Snapshot().PageIndex != 0 {
		t.Fatalf("expected page switch guarded on unsaved edits, dlg=%v page=%d", mw.unsavedDlg != nil, eng.Snapshot().PageIndex)
	}
	test.Tap(mw.unsavedCancelBtn)
	drainUI()

	// Profile switch blocked on Cancel (and dropdown reverts to current profile).
	mw.profileSelect.SetSelected("Second")
	drainUI()
	if mw.unsavedDlg == nil || eng.Snapshot().ActiveProfileName == "Second" {
		t.Fatalf("expected profile switch guarded, active=%q", eng.Snapshot().ActiveProfileName)
	}
	if mw.profileSelect.Selected == "Second" {
		t.Errorf("expected profile dropdown reverted while dialog open, got %q", mw.profileSelect.Selected)
	}
	test.Tap(mw.unsavedCancelBtn)
	drainUI()

	// Run Mode checkbox blocked on Cancel, proceeds on Save.
	mw.runModeCheck.SetChecked(true)
	drainUI()
	if mw.unsavedDlg == nil || mw.runMode || mw.runModeCheck.Checked {
		t.Fatalf("expected Run Mode checkbox guarded, runMode=%v checked=%v", mw.runMode, mw.runModeCheck.Checked)
	}
	test.Tap(mw.unsavedSaveBtn)
	drainUI()
	if !mw.runMode || eng.Snapshot().Slots[c00.SlotKey()].Config.Title != "Unsaved Nav" {
		t.Fatalf("expected Save to persist Unsaved Nav and enter Run Mode, runMode=%v", mw.runMode)
	}
	mw.SetRunMode(false)
	drainUI()

	// 5. Multi-Action Hat unsaved guard when selecting a step block.
	test.Tap(mw.surface.tiles[c01.SlotKey()])
	drainUI()
	test.Tap(mw.catalogButtons[core.ActionMultiAction])
	drainUI()
	mw.openSelectedMultiActionEditor()
	drainUI()
	mw.multiEditor.AppendWait(100)
	drainUI()
	test.Tap(mw.multiEditor.hatWidget)
	drainUI()
	mw.inspector.titleEntry.SetText("Hat Unsaved")
	test.Tap(mw.multiEditor.blockWidgets[0])
	drainUI()
	if mw.unsavedDlg == nil || mw.multiEditor.SelectedStepIndex() != -1 {
		t.Fatalf("expected Hat unsaved changes to guard clicking step block, dlg=%v step=%d", mw.unsavedDlg != nil, mw.multiEditor.SelectedStepIndex())
	}
	test.Tap(mw.unsavedDiscardBtn)
	drainUI()
	if mw.multiEditor.SelectedStepIndex() != 0 {
		t.Fatalf("expected Discard to proceed to step 0, got %d", mw.multiEditor.SelectedStepIndex())
	}

	// 6. Window close intercept blocks closing on unsaved slot edits; Cancel keeps open, Save persists & closes.
	test.Tap(mw.multiEditor.hatWidget)
	drainUI()
	mw.inspector.titleEntry.SetText("Save On Close")
	mw.handleCloseRequest()
	drainUI()
	if mw.unsavedDlg == nil {
		t.Fatal("expected handleCloseRequest to show Unsaved Changes dialog")
	}
	test.Tap(mw.unsavedCancelBtn)
	drainUI()
	if mw.unsavedDlg != nil {
		t.Fatal("expected Cancel to dismiss unsaved dialog")
	}
	mw.handleCloseRequest()
	drainUI()
	if mw.unsavedSaveBtn == nil {
		t.Fatal("expected unsavedSaveBtn on close dialog")
	}
	test.Tap(mw.unsavedSaveBtn)
	drainUI()
	if got := eng.Snapshot().Slots[c01.SlotKey()].Config.Title; got != "Save On Close" {
		t.Fatalf("expected Save on close to persist title Save On Close, got %q", got)
	}
}
