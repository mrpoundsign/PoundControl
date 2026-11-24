package ui

import (
	"context"
	"fmt"
	"image/color"
	"maps"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/mrpoundsign/PoundControl/internal/app"
	"github.com/mrpoundsign/PoundControl/pkg/builtin/core"
	"github.com/mrpoundsign/PoundControl/pkg/deck"
	"github.com/mrpoundsign/PoundControl/pkg/plugin"
)

type stepExecState uint8

const (
	stepExecIdle stepExecState = iota
	stepExecRunning
	stepExecOK
	stepExecError
)

type multiActionEditorOptions struct {
	engine             *app.Engine
	resolveIcon        func(plugin.ActionDescriptor) fyne.Resource
	onBack             func()
	onStepsChanged     func(steps []core.MultiActionStep, continueOnErr bool) error
	onSelectStep       func(stepIdx int)
	onStepDataSynced   func(stepIdx int, settings map[string]any)
	onBeforeStepChange func() error
	onBeforeHatLeave   func(proceed func())
	onStatus           func(string)
}

// MultiActionEditor renders the center-stage Scratch-lite block stack editor for a Multi-Action slot.
type MultiActionEditor struct {
	opts multiActionEditorOptions

	root          *fyne.Container
	titleLbl      *widget.Label
	metaLbl       *widget.Label
	continueCheck *widget.Check
	addWaitTopBtn *widget.Button
	testRunBtn    *widget.Button
	stackScroll   *container.Scroll
	stackBox      *fyne.Container
	hatWidget     *multiActionHatWidget
	blockWidgets  []*multiActionBlockWidget
	dropSlots     []*multiActionDropSlot

	active        bool
	slotTitle     string
	coords        deck.Coordinates
	steps         []core.MultiActionStep
	continueOnErr bool
	selectedIdx   int // -1 = Hat (Button Properties), >= 0 = step index
	syncing       bool

	dragSrcIdx  int
	dragDropIdx int

	runMu     sync.Mutex
	running   bool
	runGen    uint64
	runCancel context.CancelFunc
	execState map[string]stepExecState
}

func newMultiActionEditor(opts multiActionEditorOptions) *MultiActionEditor {
	ed := &MultiActionEditor{
		opts:        opts,
		selectedIdx: -1,
		dragSrcIdx:  -1,
		dragDropIdx: -1,
		execState:   make(map[string]stepExecState),
	}

	backBtn := widget.NewButtonWithIcon("Back to Deck", theme.NavigateBackIcon(), func() {
		if ed.opts.onBack != nil {
			ed.opts.onBack()
		}
	})

	ed.titleLbl = widget.NewLabelWithStyle("Sequence", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	ed.metaLbl = widget.NewLabel("")
	ed.metaLbl.TextStyle = fyne.TextStyle{Italic: false}

	ed.continueCheck = widget.NewCheck("Continue on Error", func(checked bool) {
		if ed.syncing {
			return
		}
		revert := func() {
			ed.syncing = true
			ed.continueCheck.SetChecked(!checked)
			ed.syncing = false
		}
		if err := ed.flushActiveStep(); err != nil {
			revert()
			return
		}
		if ed.selectedIdx == -1 && ed.opts.onBeforeHatLeave != nil {
			// Revert visually until the unsaved-changes guard resolves; proceed re-applies checked.
			revert()
			ed.opts.onBeforeHatLeave(func() {
				ed.syncing = true
				ed.continueCheck.SetChecked(checked)
				ed.syncing = false
				ed.continueOnErr = checked
				ed.persistSequence()
			})
			return
		}
		ed.continueOnErr = checked
		ed.persistSequence()
	})

	ed.addWaitTopBtn = widget.NewButtonWithIcon("+ Wait", theme.ContentAddIcon(), func() {
		ed.AppendWait(250)
	})

	ed.testRunBtn = widget.NewButtonWithIcon("Test Run", theme.MediaPlayIcon(), func() {
		if ed.IsRunning() {
			ed.StopTestRun()
			return
		}
		ed.StartTestRun()
	})
	ed.testRunBtn.Importance = widget.HighImportance

	headerLeft := container.NewHBox(
		backBtn,
		container.NewVBox(ed.titleLbl, ed.metaLbl),
	)
	headerRight := container.NewHBox(
		ed.continueCheck,
		ed.addWaitTopBtn,
		ed.testRunBtn,
	)
	headerBar := container.NewStack(
		canvas.NewRectangle(ColorHeaderSurface),
		container.NewPadded(container.NewBorder(nil, nil, headerLeft, headerRight)),
	)
	headerSep := canvas.NewRectangle(ColorElevatedSlate)
	headerSep.SetMinSize(fyne.NewSize(0, 1))
	header := container.NewVBox(headerBar, headerSep)

	ed.hatWidget = newMultiActionHatWidget(func() {
		ed.SelectStep(-1)
	})

	ed.stackBox = container.NewVBox()
	centered := container.New(&maxWidthCenterLayout{maxWidth: 660}, container.NewPadded(ed.stackBox))
	ed.stackScroll = container.NewVScroll(centered)

	canvasBg := canvas.NewRectangle(ColorObsidianSlate)
	body := container.NewStack(canvasBg, ed.stackScroll)

	ed.root = container.NewBorder(header, nil, nil, nil, body)
	ed.root.Hide()
	return ed
}

// Object returns the root Fyne canvas object for center-pane embedding.
func (ed *MultiActionEditor) Object() fyne.CanvasObject {
	return ed.root
}

// Active reports whether the Multi-Action block editor is currently open.
func (ed *MultiActionEditor) Active() bool {
	return ed.active
}

// IsRunning reports whether a live Test Run is currently executing.
func (ed *MultiActionEditor) IsRunning() bool {
	ed.runMu.Lock()
	defer ed.runMu.Unlock()
	return ed.running
}

// Open initializes and displays the editor for the Multi-Action slot at coords.
func (ed *MultiActionEditor) Open(slotTitle string, coords deck.Coordinates, settings map[string]any) {
	ed.StopTestRun()
	ed.active = true
	ed.coords = coords
	ed.selectedIdx = -1
	ed.dragSrcIdx = -1
	ed.dragDropIdx = -1
	clear(ed.execState)

	ed.loadSettings(slotTitle, settings)
	ed.rebuildStack()
	ed.stackScroll.ScrollToTop()
	ed.root.Show()
	ed.root.Refresh()
}

// SyncFromSlot refreshes the editor from external slot changes (e.g., Undo/Redo or Advanced JSON save)
// while preserving step selection by step ID when possible.
func (ed *MultiActionEditor) SyncFromSlot(slotTitle string, settings map[string]any) {
	if !ed.active {
		return
	}
	prevSelectedID := ""
	if ed.selectedIdx >= 0 && ed.selectedIdx < len(ed.steps) {
		prevSelectedID = ed.steps[ed.selectedIdx].ID
	}

	ed.loadSettings(slotTitle, settings)
	if prevSelectedID != "" {
		ed.selectedIdx = -1
		for i, st := range ed.steps {
			if st.ID == prevSelectedID {
				ed.selectedIdx = i
				break
			}
		}
	} else {
		ed.selectedIdx = -1
	}
	ed.rebuildStack()
}

// Close stops any active Test Run and hides the editor.
func (ed *MultiActionEditor) Close() {
	ed.StopTestRun()
	ed.active = false
	ed.dragSrcIdx = -1
	ed.dragDropIdx = -1
	clear(ed.execState)
	ed.root.Hide()
}

// Steps returns a deep-cloned slice of the current normalized steps.
func (ed *MultiActionEditor) Steps() []core.MultiActionStep {
	return cloneMultiActionSteps(ed.steps)
}

// SelectedStepIndex returns -1 when the Hat (Button Properties) is selected, or the 0-based step index.
func (ed *MultiActionEditor) SelectedStepIndex() int {
	return ed.selectedIdx
}

// SelectedStep returns the currently selected step and true, or zero value and false if Hat is selected.
func (ed *MultiActionEditor) SelectedStep() (core.MultiActionStep, bool) {
	if ed.selectedIdx < 0 || ed.selectedIdx >= len(ed.steps) {
		return core.MultiActionStep{}, false
	}
	return cloneMultiActionStep(ed.steps[ed.selectedIdx]), true
}

// SelectStep selects stepIdx (-1 for Hat / Button Properties, >=0 for a specific step) and notifies the inspector.
// Pending edits on an active step are auto-saved first; unsaved edits on the Hat block are guarded.
func (ed *MultiActionEditor) SelectStep(stepIdx int) {
	if stepIdx < -1 || stepIdx >= len(ed.steps) {
		stepIdx = -1
	}
	if stepIdx == ed.selectedIdx {
		return
	}
	if err := ed.flushActiveStep(); err != nil {
		return
	}
	if ed.selectedIdx == -1 && stepIdx >= 0 && ed.opts.onBeforeHatLeave != nil {
		ed.opts.onBeforeHatLeave(func() {
			ed.selectStepDirect(stepIdx)
		})
		return
	}
	ed.selectStepDirect(stepIdx)
}

func (ed *MultiActionEditor) selectStepDirect(stepIdx int) {
	if stepIdx < -1 || stepIdx >= len(ed.steps) {
		stepIdx = -1
	}
	ed.selectedIdx = stepIdx
	ed.syncSelectionVisuals()
	if ed.opts.onSelectStep != nil {
		ed.opts.onSelectStep(stepIdx)
	}
}

// AppendAction appends a new action block with default settings from desc, selects it, and persists.
func (ed *MultiActionEditor) AppendAction(desc plugin.ActionDescriptor) {
	if !ed.active {
		return
	}
	if err := ed.flushActiveStep(); err != nil {
		return
	}
	if ed.selectedIdx == -1 && ed.opts.onBeforeHatLeave != nil {
		ed.opts.onBeforeHatLeave(func() {
			ed.appendActionDirect(desc)
		})
		return
	}
	ed.appendActionDirect(desc)
}

func (ed *MultiActionEditor) appendActionDirect(desc plugin.ActionDescriptor) {
	if !ed.active {
		return
	}
	ed.commitPendingWaitEdits()
	settings := defaultStepSettings(desc)
	step := core.MultiActionStep{
		ID:         deck.NewUUID(),
		ActionUUID: desc.UUID,
		Settings:   settings,
	}
	ed.steps = append(ed.steps, step)
	ed.selectedIdx = len(ed.steps) - 1
	ed.rebuildStack()
	ed.persistSequence()
	if ed.opts.onSelectStep != nil {
		ed.opts.onSelectStep(ed.selectedIdx)
	}
	ed.reportStatus("Added " + desc.Name + " block")
}

// AppendWait appends a Wait block with duration ms, selects it, and persists.
func (ed *MultiActionEditor) AppendWait(ms int) {
	if !ed.active {
		return
	}
	if err := ed.flushActiveStep(); err != nil {
		return
	}
	if ed.selectedIdx == -1 && ed.opts.onBeforeHatLeave != nil {
		ed.opts.onBeforeHatLeave(func() {
			ed.appendWaitDirect(ms)
		})
		return
	}
	ed.appendWaitDirect(ms)
}

func (ed *MultiActionEditor) appendWaitDirect(ms int) {
	if !ed.active {
		return
	}
	ed.commitPendingWaitEdits()
	if ms < 0 {
		ms = 0
	}
	step := core.MultiActionStep{
		ID:         deck.NewUUID(),
		ActionUUID: core.ActionDelay,
		Settings:   map[string]any{"ms": ms},
	}
	ed.steps = append(ed.steps, step)
	ed.selectedIdx = len(ed.steps) - 1
	ed.rebuildStack()
	ed.persistSequence()
	if ed.opts.onSelectStep != nil {
		ed.opts.onSelectStep(ed.selectedIdx)
	}
	ed.reportStatus(fmt.Sprintf("Added %dms Wait block", ms))
}

// UpdateStepSettings replaces settings for stepIdx, updates block display, and persists.
func (ed *MultiActionEditor) UpdateStepSettings(stepIdx int, settings map[string]any) error {
	if stepIdx < 0 || stepIdx >= len(ed.steps) {
		return fmt.Errorf("sequence step %d out of range", stepIdx)
	}
	ed.steps[stepIdx].Settings = cloneStepSettingsMap(settings)
	ed.steps[stepIdx].DelayMs = 0
	if ed.steps[stepIdx].ActionUUID == core.ActionDelay {
		ms := core.StepDelayMs(ed.steps[stepIdx])
		ed.steps[stepIdx].Settings["ms"] = ms
	}
	ed.refreshHeaderAndSummary()
	if stepIdx < len(ed.blockWidgets) {
		ed.blockWidgets[stepIdx].ApplyStep(stepIdx, len(ed.steps), ed.steps[stepIdx], ed.execState[ed.steps[stepIdx].ID], ed.selectedIdx == stepIdx)
	}
	if ed.selectedIdx == stepIdx && ed.opts.onStepDataSynced != nil {
		ed.opts.onStepDataSynced(stepIdx, cloneStepSettingsMap(ed.steps[stepIdx].Settings))
	}
	return ed.saveSequence()
}

// DeleteStep removes stepIdx, adjusts selection, rebuilds the stack, and persists.
func (ed *MultiActionEditor) DeleteStep(stepIdx int) {
	if stepIdx < 0 || stepIdx >= len(ed.steps) {
		return
	}
	if ed.selectedIdx >= 0 && ed.selectedIdx != stepIdx {
		if err := ed.flushActiveStep(); err != nil {
			return
		}
	} else {
		ed.commitPendingWaitEdits()
	}
	if ed.selectedIdx == -1 && ed.opts.onBeforeHatLeave != nil {
		ed.opts.onBeforeHatLeave(func() {
			ed.deleteStepDirect(stepIdx)
		})
		return
	}
	ed.deleteStepDirect(stepIdx)
}

func (ed *MultiActionEditor) deleteStepDirect(stepIdx int) {
	if stepIdx < 0 || stepIdx >= len(ed.steps) {
		return
	}
	removedID := ed.steps[stepIdx].ID
	delete(ed.execState, removedID)

	ed.steps = append(ed.steps[:stepIdx], ed.steps[stepIdx+1:]...)
	switch {
	case len(ed.steps) == 0:
		ed.selectedIdx = -1
	case ed.selectedIdx == stepIdx:
		ed.selectedIdx = min(stepIdx, len(ed.steps)-1)
	case ed.selectedIdx > stepIdx:
		ed.selectedIdx--
	}

	ed.rebuildStack()
	ed.persistSequence()
	if ed.opts.onSelectStep != nil {
		ed.opts.onSelectStep(ed.selectedIdx)
	}
	ed.reportStatus("Removed sequence block")
}

// MoveStep moves the block at fromIdx to toIdx (0-based slice index), keeps selection following the moved step, and persists.
func (ed *MultiActionEditor) MoveStep(fromIdx, toIdx int) {
	if fromIdx < 0 || fromIdx >= len(ed.steps) || toIdx < 0 || toIdx >= len(ed.steps) || fromIdx == toIdx {
		return
	}
	if err := ed.flushActiveStep(); err != nil {
		return
	}
	if ed.selectedIdx == -1 && ed.opts.onBeforeHatLeave != nil {
		ed.opts.onBeforeHatLeave(func() {
			ed.moveStepDirect(fromIdx, toIdx)
		})
		return
	}
	ed.moveStepDirect(fromIdx, toIdx)
}

func (ed *MultiActionEditor) moveStepDirect(fromIdx, toIdx int) {
	if fromIdx < 0 || fromIdx >= len(ed.steps) || toIdx < 0 || toIdx >= len(ed.steps) || fromIdx == toIdx {
		return
	}
	selectedID := ""
	if ed.selectedIdx >= 0 && ed.selectedIdx < len(ed.steps) {
		selectedID = ed.steps[ed.selectedIdx].ID
	}

	moved := ed.steps[fromIdx]
	remaining := make([]core.MultiActionStep, 0, len(ed.steps)-1)
	remaining = append(remaining, ed.steps[:fromIdx]...)
	remaining = append(remaining, ed.steps[fromIdx+1:]...)

	updated := make([]core.MultiActionStep, 0, len(ed.steps))
	updated = append(updated, remaining[:toIdx]...)
	updated = append(updated, moved)
	updated = append(updated, remaining[toIdx:]...)
	ed.steps = updated

	if selectedID != "" {
		for i, st := range ed.steps {
			if st.ID == selectedID {
				ed.selectedIdx = i
				break
			}
		}
	}

	ed.rebuildStack()
	ed.persistSequence()
	if ed.opts.onSelectStep != nil && ed.selectedIdx >= 0 {
		ed.opts.onSelectStep(ed.selectedIdx)
	}
}

// DuplicateStep clones stepIdx directly below itself, selects the copy, and persists.
func (ed *MultiActionEditor) DuplicateStep(stepIdx int) {
	if stepIdx < 0 || stepIdx >= len(ed.steps) {
		return
	}
	if err := ed.flushActiveStep(); err != nil {
		return
	}
	if ed.selectedIdx == -1 && ed.opts.onBeforeHatLeave != nil {
		ed.opts.onBeforeHatLeave(func() {
			ed.duplicateStepDirect(stepIdx)
		})
		return
	}
	ed.duplicateStepDirect(stepIdx)
}

func (ed *MultiActionEditor) duplicateStepDirect(stepIdx int) {
	if stepIdx < 0 || stepIdx >= len(ed.steps) {
		return
	}
	dup := cloneMultiActionStep(ed.steps[stepIdx])
	dup.ID = deck.NewUUID()

	updated := make([]core.MultiActionStep, 0, len(ed.steps)+1)
	updated = append(updated, ed.steps[:stepIdx+1]...)
	updated = append(updated, dup)
	updated = append(updated, ed.steps[stepIdx+1:]...)
	ed.steps = updated
	ed.selectedIdx = stepIdx + 1

	ed.rebuildStack()
	ed.persistSequence()
	if ed.opts.onSelectStep != nil {
		ed.opts.onSelectStep(ed.selectedIdx)
	}
	ed.reportStatus("Duplicated sequence block")
}

// ToggleStepDisabled flips Disabled on stepIdx, updates visuals, and persists.
func (ed *MultiActionEditor) ToggleStepDisabled(stepIdx int) {
	if stepIdx < 0 || stepIdx >= len(ed.steps) {
		return
	}
	if err := ed.flushActiveStep(); err != nil {
		return
	}
	if ed.selectedIdx == -1 && ed.opts.onBeforeHatLeave != nil {
		ed.opts.onBeforeHatLeave(func() {
			ed.toggleStepDisabledDirect(stepIdx)
		})
		return
	}
	ed.toggleStepDisabledDirect(stepIdx)
}

func (ed *MultiActionEditor) toggleStepDisabledDirect(stepIdx int) {
	if stepIdx < 0 || stepIdx >= len(ed.steps) {
		return
	}
	ed.steps[stepIdx].Disabled = !ed.steps[stepIdx].Disabled
	ed.refreshHeaderAndSummary()
	if stepIdx < len(ed.blockWidgets) {
		ed.blockWidgets[stepIdx].ApplyStep(stepIdx, len(ed.steps), ed.steps[stepIdx], ed.execState[ed.steps[stepIdx].ID], ed.selectedIdx == stepIdx)
	}
	ed.persistSequence()
	if ed.opts.onSelectStep != nil && ed.selectedIdx == stepIdx {
		ed.opts.onSelectStep(stepIdx)
	}
}

func (ed *MultiActionEditor) flushActiveStep() error {
	ed.commitPendingWaitEdits()
	if ed.selectedIdx >= 0 && ed.opts.onBeforeStepChange != nil {
		return ed.opts.onBeforeStepChange()
	}
	return nil
}

// StartTestRun executes enabled blocks sequentially with live execution highlighting.
func (ed *MultiActionEditor) StartTestRun() {
	if !ed.active || ed.opts.engine == nil {
		return
	}
	if err := ed.flushActiveStep(); err != nil {
		return
	}
	if len(ed.steps) == 0 {
		ed.reportStatus("Add at least one block before running a test")
		return
	}

	stepsSnapshot := cloneMultiActionSteps(ed.steps)
	continueOnErr := ed.continueOnErr

	ed.runMu.Lock()
	if ed.runCancel != nil {
		ed.runCancel()
	}
	ed.runGen++
	gen := ed.runGen
	ctx, cancel := context.WithCancel(context.Background())
	ed.runCancel = cancel
	ed.running = true
	ed.runMu.Unlock()

	clear(ed.execState)
	ed.syncExecVisuals()
	ed.syncRunButton(true)
	ed.reportStatus("Running sequence...")

	go func() {
		defer cancel()
		var firstErr error
		executed := 0
		for _, step := range stepsSnapshot {
			if step.Disabled {
				continue
			}
			if ctx.Err() != nil {
				break
			}
			executed++
			stepID := step.ID
			dispatchUI(func() {
				if !ed.isCurrentRun(gen) {
					return
				}
				ed.execState[stepID] = stepExecRunning
				ed.syncExecVisuals()
			})

			var stepErr error
			if strings.TrimSpace(step.ActionUUID) == core.ActionDelay {
				ms := core.StepDelayMs(step)
				stepErr = core.SleepContext(ctx, time.Duration(ms)*time.Millisecond)
			} else {
				stepErr = ed.opts.engine.TriggerAction(ctx, step.ActionUUID, step.Settings)
			}

			if ctx.Err() != nil {
				dispatchUI(func() {
					if !ed.isCurrentRun(gen) {
						return
					}
					delete(ed.execState, stepID)
					ed.syncExecVisuals()
				})
				break
			}

			resultState := stepExecOK
			if stepErr != nil {
				resultState = stepExecError
				if firstErr == nil {
					firstErr = stepErr
				}
			}
			dispatchUI(func() {
				if !ed.isCurrentRun(gen) {
					return
				}
				ed.execState[stepID] = resultState
				ed.syncExecVisuals()
			})

			if stepErr != nil && !continueOnErr {
				break
			}
		}

		wasCanceled := ctx.Err() != nil
		dispatchUI(func() {
			ed.runMu.Lock()
			if ed.runGen != gen {
				ed.runMu.Unlock()
				return
			}
			ed.running = false
			ed.runCancel = nil
			ed.runMu.Unlock()

			ed.syncRunButton(false)
			switch {
			case wasCanceled:
				clear(ed.execState)
				ed.syncExecVisuals()
				ed.reportStatus("Sequence test stopped")
			case executed == 0:
				ed.reportStatus("All blocks in sequence are muted")
			case firstErr != nil:
				ed.reportStatus("Sequence test failed: " + firstErr.Error())
			default:
				ed.reportStatus("Sequence test completed")
			}

			if !wasCanceled {
				time.AfterFunc(1200*time.Millisecond, func() {
					dispatchUI(func() {
						ed.runMu.Lock()
						stillValid := ed.active && !ed.running && ed.runGen == gen
						ed.runMu.Unlock()
						if !stillValid {
							return
						}
						clear(ed.execState)
						ed.syncExecVisuals()
					})
				})
			}
		})
	}()
}

// StopTestRun cancels any running sequence test immediately.
func (ed *MultiActionEditor) StopTestRun() {
	ed.runMu.Lock()
	cancel := ed.runCancel
	wasRunning := ed.running
	ed.runGen++
	ed.running = false
	ed.runCancel = nil
	ed.runMu.Unlock()

	if cancel != nil {
		cancel()
	}
	if wasRunning {
		clear(ed.execState)
		ed.syncExecVisuals()
		ed.syncRunButton(false)
	}
}

func (ed *MultiActionEditor) isCurrentRun(gen uint64) bool {
	ed.runMu.Lock()
	defer ed.runMu.Unlock()
	return ed.active && ed.running && ed.runGen == gen
}

func (ed *MultiActionEditor) syncRunButton(running bool) {
	if ed.testRunBtn == nil {
		return
	}
	if running {
		ed.testRunBtn.SetText("Stop")
		ed.testRunBtn.SetIcon(theme.MediaStopIcon())
		ed.testRunBtn.Importance = widget.DangerImportance
	} else {
		ed.testRunBtn.SetText("Test Run")
		ed.testRunBtn.SetIcon(theme.MediaPlayIcon())
		ed.testRunBtn.Importance = widget.HighImportance
	}
	ed.testRunBtn.Refresh()
}

func (ed *MultiActionEditor) loadSettings(slotTitle string, settings map[string]any) {
	trimmedTitle := strings.TrimSpace(slotTitle)
	if trimmedTitle == "" {
		trimmedTitle = "Multi-Action"
	}
	ed.slotTitle = trimmedTitle

	rawSteps, err := core.ParseMultiActionSteps(settings)
	if err != nil {
		ed.steps = nil
		ed.reportStatus("Invalid Multi-Action JSON: " + err.Error())
	} else {
		ed.steps = core.NormalizeMultiActionSteps(rawSteps)
	}
	ed.continueOnErr = core.MultiActionContinueOnError(settings)

	ed.syncing = true
	if ed.continueCheck != nil {
		ed.continueCheck.SetChecked(ed.continueOnErr)
	}
	ed.syncing = false
	ed.refreshHeaderAndSummary()
}

func (ed *MultiActionEditor) refreshHeaderAndSummary() {
	activeCount := 0
	for _, st := range ed.steps {
		if !st.Disabled {
			activeCount++
		}
	}
	estMs := core.MultiActionEstimatedDurationMs(ed.steps)
	durText := formatDurationCompact(estMs)
	summary := fmt.Sprintf("%d blocks", len(ed.steps))
	if activeCount != len(ed.steps) {
		summary = fmt.Sprintf("%d active / %d blocks", activeCount, len(ed.steps))
	}
	if estMs > 0 {
		summary += " · " + durText
	}

	if ed.titleLbl != nil {
		ed.titleLbl.SetText("Sequence: " + ed.slotTitle)
	}
	if ed.metaLbl != nil {
		ed.metaLbl.SetText(fmt.Sprintf("Slot (%d, %d) · %s", ed.coords.Row, ed.coords.Col, summary))
	}
	if ed.hatWidget != nil {
		ed.hatWidget.SetSummary(summary, ed.selectedIdx == -1)
	}
}

func (ed *MultiActionEditor) rebuildStack() {
	ed.refreshHeaderAndSummary()

	n := len(ed.steps)
	ed.blockWidgets = make([]*multiActionBlockWidget, n)
	ed.dropSlots = make([]*multiActionDropSlot, n+1)

	objs := make([]fyne.CanvasObject, 0, n*2+4)
	objs = append(objs, ed.hatWidget)

	firstDrop := newMultiActionDropSlot(n > 0)
	ed.dropSlots[0] = firstDrop
	objs = append(objs, firstDrop)

	if n == 0 {
		emptyCard := ed.buildEmptyStateCard()
		objs = append(objs, emptyCard)
	} else {
		for i, st := range ed.steps {
			idx := i
			var desc *plugin.ActionDescriptor
			if ed.opts.engine != nil && ed.opts.engine.Registry() != nil {
				if d, ok := ed.opts.engine.Registry().Action(st.ActionUUID); ok {
					dcopy := d
					desc = &dcopy
				}
			}
			var actIcon fyne.Resource
			if desc != nil && ed.opts.resolveIcon != nil {
				actIcon = ed.opts.resolveIcon(*desc)
			}

			bw := newMultiActionBlockWidget(multiActionBlockHandlers{
				step:  st,
				desc:  desc,
				icon:  actIcon,
				onTap: func() { ed.SelectStep(idx) },
				onFocusWait: func() {
					if ed.selectedIdx != idx {
						ed.SelectStep(idx)
					}
				},
				onMoveUp: func() {
					if idx > 0 {
						ed.MoveStep(idx, idx-1)
					}
				},
				onMoveDown: func() {
					if idx+1 < len(ed.steps) {
						ed.MoveStep(idx, idx+1)
					}
				},
				onToggleMute: func() { ed.ToggleStepDisabled(idx) },
				onDuplicate:  func() { ed.DuplicateStep(idx) },
				onDelete:     func() { ed.DeleteStep(idx) },
				onCommitWait: func(ms int) {
					_ = ed.UpdateStepSettings(idx, map[string]any{"ms": max(0, ms)})
				},
				onDragMove: func(absPos fyne.Position) { ed.handleBlockDragMove(idx, absPos) },
				onDragEnd:  func() { ed.handleBlockDragEnd(idx) },
			})
			bw.ApplyStep(idx, n, st, ed.execState[st.ID], ed.selectedIdx == idx)
			ed.blockWidgets[idx] = bw
			objs = append(objs, bw)

			ds := newMultiActionDropSlot(idx+1 < n)
			ed.dropSlots[idx+1] = ds
			objs = append(objs, ds)
		}
	}

	waitQuickBtn := widget.NewButtonWithIcon("+ Wait (250ms)", theme.ContentAddIcon(), func() {
		ed.AppendWait(250)
	})
	tipLbl := widget.NewLabel("Click any action in the Action Catalog on the left to append a block.")
	tipLbl.Wrapping = fyne.TextWrapWord
	footer := container.NewBorder(nil, nil, waitQuickBtn, nil, tipLbl)
	objs = append(objs, widget.NewSeparator(), footer)

	ed.stackBox.Objects = objs
	ed.stackBox.Refresh()
}

func (ed *MultiActionEditor) buildEmptyStateCard() fyne.CanvasObject {
	bg := canvas.NewRectangle(ColorDarkCharcoal)
	bg.CornerRadius = 10
	bg.StrokeColor = ColorSlateBorder
	bg.StrokeWidth = 1

	msg := widget.NewLabel("No blocks yet. Click any action in the Action Catalog on the left to append a step, or click + Wait to insert a delay.")
	msg.Wrapping = fyne.TextWrapWord
	msg.Alignment = fyne.TextAlignCenter

	return container.NewStack(bg, container.NewPadded(msg))
}

func (ed *MultiActionEditor) syncSelectionVisuals() {
	if ed.hatWidget != nil {
		ed.hatWidget.SetSelected(ed.selectedIdx == -1)
	}
	for i, bw := range ed.blockWidgets {
		if bw != nil {
			bw.SetSelected(ed.selectedIdx == i)
		}
	}
}

func (ed *MultiActionEditor) syncExecVisuals() {
	for i, bw := range ed.blockWidgets {
		if bw != nil && i < len(ed.steps) {
			bw.SetExecState(ed.execState[ed.steps[i].ID])
		}
	}
}

func (ed *MultiActionEditor) commitPendingWaitEdits() {
	for _, bw := range ed.blockWidgets {
		if bw != nil {
			bw.commitWaitIfPending()
		}
	}
}

func (ed *MultiActionEditor) persistSequence() {
	_ = ed.saveSequence()
}

func (ed *MultiActionEditor) saveSequence() error {
	if ed.opts.onStepsChanged == nil {
		return nil
	}
	err := ed.opts.onStepsChanged(cloneMultiActionSteps(ed.steps), ed.continueOnErr)
	if err != nil {
		ed.reportStatus("Save sequence failed: " + err.Error())
	}
	return err
}

func (ed *MultiActionEditor) handleBlockDragMove(srcIdx int, absPos fyne.Position) {
	if len(ed.blockWidgets) <= 1 {
		return
	}
	ed.dragSrcIdx = srcIdx
	insertIdx := 0
	if curApp := fyne.CurrentApp(); curApp != nil {
		if drv := curApp.Driver(); drv != nil {
			for i, bw := range ed.blockWidgets {
				if bw == nil {
					continue
				}
				pos := drv.AbsolutePositionForObject(bw)
				centerY := pos.Y + bw.Size().Height/2
				if absPos.Y >= centerY {
					insertIdx = i + 1
				}
			}
		}
	}
	if insertIdx == srcIdx || insertIdx == srcIdx+1 {
		ed.dragDropIdx = -1
	} else {
		ed.dragDropIdx = insertIdx
	}
	for i, ds := range ed.dropSlots {
		if ds != nil {
			ds.SetHighlighted(i == ed.dragDropIdx)
		}
	}
}

func (ed *MultiActionEditor) handleBlockDragEnd(srcIdx int) {
	dropIdx := ed.dragDropIdx
	ed.dragSrcIdx = -1
	ed.dragDropIdx = -1
	for _, ds := range ed.dropSlots {
		if ds != nil {
			ds.SetHighlighted(false)
		}
	}
	if dropIdx < 0 || dropIdx == srcIdx || dropIdx == srcIdx+1 {
		return
	}
	targetIdx := dropIdx
	if dropIdx > srcIdx {
		targetIdx = dropIdx - 1
	}
	ed.MoveStep(srcIdx, targetIdx)
}

func (ed *MultiActionEditor) reportStatus(msg string) {
	if ed.opts.onStatus != nil {
		ed.opts.onStatus(msg)
	}
}

type maxWidthCenterLayout struct {
	maxWidth float32
}

func (l *maxWidthCenterLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	var minSz fyne.Size
	for _, obj := range objects {
		if !obj.Visible() {
			continue
		}
		cs := obj.MinSize()
		minSz.Width = max(minSz.Width, min(cs.Width, l.maxWidth))
		minSz.Height = max(minSz.Height, cs.Height)
	}
	return minSz
}

func (l *maxWidthCenterLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	for _, obj := range objects {
		if !obj.Visible() {
			continue
		}
		w := min(size.Width, l.maxWidth)
		if w <= 0 {
			w = size.Width
		}
		x := max(float32(0), (size.Width-w)/2)
		h := max(size.Height, obj.MinSize().Height)
		obj.Move(fyne.NewPos(x, 0))
		obj.Resize(fyne.NewSize(w, h))
	}
}

type multiActionHatWidget struct {
	widget.BaseWidget
	summary  string
	selected bool
	hovered  bool
	onTap    func()
}

var (
	_ fyne.Tappable     = (*multiActionHatWidget)(nil)
	_ desktop.Hoverable = (*multiActionHatWidget)(nil)
)

func newMultiActionHatWidget(onTap func()) *multiActionHatWidget {
	h := &multiActionHatWidget{
		summary: "0 blocks",
		onTap:   onTap,
	}
	h.ExtendBaseWidget(h)
	return h
}

func (h *multiActionHatWidget) SetSummary(summary string, selected bool) {
	h.summary = summary
	h.selected = selected
	h.Refresh()
}

func (h *multiActionHatWidget) SetSelected(selected bool) {
	if h.selected == selected {
		return
	}
	h.selected = selected
	h.Refresh()
}

func (h *multiActionHatWidget) Tapped(*fyne.PointEvent) {
	if h.onTap != nil {
		h.onTap()
	}
}

func (h *multiActionHatWidget) MouseIn(*desktop.MouseEvent) {
	h.hovered = true
	h.Refresh()
}

func (h *multiActionHatWidget) MouseMoved(*desktop.MouseEvent) {}

func (h *multiActionHatWidget) MouseOut() {
	h.hovered = false
	h.Refresh()
}

func (h *multiActionHatWidget) CreateRenderer() fyne.WidgetRenderer {
	bg := canvas.NewRectangle(color.RGBA{R: 18, G: 38, B: 27, A: 255})
	bg.CornerRadius = 14
	bg.StrokeColor = ColorSuccess
	bg.StrokeWidth = 1.5

	accent := canvas.NewRectangle(ColorSuccess)
	accent.CornerRadius = 2

	notch := canvas.NewRectangle(ColorSuccess)
	notch.CornerRadius = 2

	title := canvas.NewText("▶  WHEN BUTTON PRESSED", ColorChromeWhite)
	title.TextSize = 12
	title.TextStyle = fyne.TextStyle{Bold: true}

	badgeBg := canvas.NewRectangle(ColorObsidianSlate)
	badgeBg.CornerRadius = 8
	badgeBg.StrokeColor = ColorSlateBorder
	badgeBg.StrokeWidth = 1

	badgeTxt := canvas.NewText(h.summary, ColorSecondarySlate)
	badgeTxt.TextSize = 11
	badgeTxt.TextStyle = fyne.TextStyle{Bold: true}
	badgeTxt.Alignment = fyne.TextAlignCenter

	return &multiActionHatRenderer{
		hat:      h,
		bg:       bg,
		accent:   accent,
		notch:    notch,
		title:    title,
		badgeBg:  badgeBg,
		badgeTxt: badgeTxt,
	}
}

type multiActionHatRenderer struct {
	hat      *multiActionHatWidget
	bg       *canvas.Rectangle
	accent   *canvas.Rectangle
	notch    *canvas.Rectangle
	title    *canvas.Text
	badgeBg  *canvas.Rectangle
	badgeTxt *canvas.Text
}

func (r *multiActionHatRenderer) MinSize() fyne.Size {
	return fyne.NewSize(300, 52)
}

func (r *multiActionHatRenderer) Layout(size fyne.Size) {
	bodyH := max(float32(36), size.Height-5)
	r.bg.Move(fyne.NewPos(0, 0))
	r.bg.Resize(fyne.NewSize(size.Width, bodyH))

	r.accent.Move(fyne.NewPos(6, 8))
	r.accent.Resize(fyne.NewSize(5, max(float32(12), bodyH-16)))

	r.notch.Move(fyne.NewPos(34, bodyH))
	r.notch.Resize(fyne.NewSize(26, 5))

	badgeMin := r.badgeTxt.MinSize()
	badgeW := badgeMin.Width + 18
	badgeH := badgeMin.Height + 8
	badgeX := max(float32(180), size.Width-badgeW-12)
	badgeY := (bodyH - badgeH) / 2
	r.badgeBg.Move(fyne.NewPos(badgeX, badgeY))
	r.badgeBg.Resize(fyne.NewSize(badgeW, badgeH))
	r.badgeTxt.Move(fyne.NewPos(badgeX+9, badgeY+4))
	r.badgeTxt.Resize(fyne.NewSize(badgeMin.Width, badgeMin.Height))

	titleMin := r.title.MinSize()
	titleW := max(float32(40), badgeX-26)
	r.title.Text = truncateCanvasText("▶  WHEN BUTTON PRESSED", titleW, 12, fyne.TextStyle{Bold: true})
	r.title.Move(fyne.NewPos(18, (bodyH-titleMin.Height)/2))
	r.title.Resize(fyne.NewSize(titleW, titleMin.Height))
}

func (r *multiActionHatRenderer) Refresh() {
	r.badgeTxt.Text = r.hat.summary
	switch {
	case r.hat.selected:
		r.bg.FillColor = color.RGBA{R: 22, G: 48, B: 35, A: 255}
		r.bg.StrokeColor = ColorSkyBlue
		r.bg.StrokeWidth = 2.0
		r.notch.FillColor = ColorSkyBlue
	case r.hat.hovered:
		r.bg.FillColor = color.RGBA{R: 20, G: 43, B: 31, A: 255}
		r.bg.StrokeColor = ColorSecondarySlate
		r.bg.StrokeWidth = 1.5
		r.notch.FillColor = ColorSuccess
	default:
		r.bg.FillColor = color.RGBA{R: 18, G: 38, B: 27, A: 255}
		r.bg.StrokeColor = ColorSuccess
		r.bg.StrokeWidth = 1.5
		r.notch.FillColor = ColorSuccess
	}
	r.bg.Refresh()
	r.notch.Refresh()
	r.badgeTxt.Refresh()
	r.Layout(r.hat.Size())
}

func (r *multiActionHatRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.bg, r.accent, r.notch, r.title, r.badgeBg, r.badgeTxt}
}

func (r *multiActionHatRenderer) Destroy() {}

type multiActionDropSlot struct {
	widget.BaseWidget
	showSpine   bool
	highlighted bool
}

func newMultiActionDropSlot(showSpine bool) *multiActionDropSlot {
	d := &multiActionDropSlot{showSpine: showSpine}
	d.ExtendBaseWidget(d)
	return d
}

func (d *multiActionDropSlot) SetHighlighted(hl bool) {
	if d.highlighted == hl {
		return
	}
	d.highlighted = hl
	d.Refresh()
}

func (d *multiActionDropSlot) CreateRenderer() fyne.WidgetRenderer {
	spine := canvas.NewRectangle(ColorSlateBorder)
	bar := canvas.NewRectangle(color.Transparent)
	bar.CornerRadius = 2
	return &multiActionDropSlotRenderer{slot: d, spine: spine, bar: bar}
}

type multiActionDropSlotRenderer struct {
	slot  *multiActionDropSlot
	spine *canvas.Rectangle
	bar   *canvas.Rectangle
}

func (r *multiActionDropSlotRenderer) MinSize() fyne.Size {
	return fyne.NewSize(100, 10)
}

func (r *multiActionDropSlotRenderer) Layout(size fyne.Size) {
	r.spine.Move(fyne.NewPos(46, 0))
	r.spine.Resize(fyne.NewSize(2, size.Height))
	r.bar.Move(fyne.NewPos(4, (size.Height-4)/2))
	r.bar.Resize(fyne.NewSize(max(float32(16), size.Width-8), 4))
}

func (r *multiActionDropSlotRenderer) Refresh() {
	if r.slot.showSpine {
		r.spine.Show()
	} else {
		r.spine.Hide()
	}
	if r.slot.highlighted {
		r.bar.FillColor = ColorSkyBlue
	} else {
		r.bar.FillColor = color.Transparent
	}
	r.spine.Refresh()
	r.bar.Refresh()
}

func (r *multiActionDropSlotRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.spine, r.bar}
}

func (r *multiActionDropSlotRenderer) Destroy() {}

type focusLossEntry struct {
	widget.Entry
	onCommit func(string)
	onFocus  func()
}

func newFocusLossEntry(initial string, onFocus func(), onCommit func(string)) *focusLossEntry {
	e := &focusLossEntry{onFocus: onFocus, onCommit: onCommit}
	e.ExtendBaseWidget(e)
	e.SetText(initial)
	e.OnSubmitted = func(text string) {
		if e.onCommit != nil {
			e.onCommit(text)
		}
	}
	return e
}

func (e *focusLossEntry) FocusGained() {
	e.Entry.FocusGained()
	if e.onFocus != nil {
		e.onFocus()
	}
}

func (e *focusLossEntry) FocusLost() {
	e.Entry.FocusLost()
	if e.onCommit != nil {
		e.onCommit(e.Text)
	}
}

func (e *focusLossEntry) MinSize() fyne.Size {
	ms := e.Entry.MinSize()
	return fyne.NewSize(max(float32(72), ms.Width), ms.Height)
}

type multiActionBlockHandlers struct {
	step         core.MultiActionStep
	desc         *plugin.ActionDescriptor
	icon         fyne.Resource
	onTap        func()
	onFocusWait  func()
	onMoveUp     func()
	onMoveDown   func()
	onToggleMute func()
	onDuplicate  func()
	onDelete     func()
	onCommitWait func(int)
	onDragMove   func(fyne.Position)
	onDragEnd    func()
}

type multiActionBlockWidget struct {
	widget.BaseWidget
	handlers  multiActionBlockHandlers
	index     int
	total     int
	step      core.MultiActionStep
	execState stepExecState
	selected  bool
	hovered   bool

	dragAccumX  float32
	dragAccumY  float32
	dragActive  bool
	dragLastAbs fyne.Position

	gripTxt     *canvas.Text
	iconImg     *widget.Icon
	titleTxt    *canvas.Text
	summaryTxt  *canvas.Text
	waitEntry   *focusLossEntry
	waitUnitLbl *widget.Label
	presetBtns  []*widget.Button
	upBtn       *widget.Button
	downBtn     *widget.Button
	muteBtn     *widget.Button
	dupBtn      *widget.Button
	delBtn      *widget.Button
	controlsRow *fyne.Container
	contentBox  *fyne.Container
	lastWaitMs  int
}

var (
	_ fyne.Tappable     = (*multiActionBlockWidget)(nil)
	_ fyne.Draggable    = (*multiActionBlockWidget)(nil)
	_ desktop.Hoverable = (*multiActionBlockWidget)(nil)
)

func newMultiActionBlockWidget(h multiActionBlockHandlers) *multiActionBlockWidget {
	b := &multiActionBlockWidget{
		handlers: h,
		step:     h.step,
	}

	b.gripTxt = canvas.NewText("⋮⋮", ColorMutedText)
	b.gripTxt.TextSize = 14
	b.gripTxt.TextStyle = fyne.TextStyle{Bold: true}

	b.titleTxt = canvas.NewText("", ColorChromeWhite)
	b.titleTxt.TextSize = 12
	b.titleTxt.TextStyle = fyne.TextStyle{Bold: true}

	b.summaryTxt = canvas.NewText("", ColorSecondarySlate)
	b.summaryTxt.TextSize = 11

	b.upBtn = widget.NewButtonWithIcon("", theme.MoveUpIcon(), func() {
		if h.onMoveUp != nil {
			h.onMoveUp()
		}
	})
	b.upBtn.Importance = widget.LowImportance

	b.downBtn = widget.NewButtonWithIcon("", theme.MoveDownIcon(), func() {
		if h.onMoveDown != nil {
			h.onMoveDown()
		}
	})
	b.downBtn.Importance = widget.LowImportance

	b.muteBtn = widget.NewButtonWithIcon("", theme.VisibilityIcon(), func() {
		if h.onToggleMute != nil {
			h.onToggleMute()
		}
	})
	b.muteBtn.Importance = widget.LowImportance

	b.dupBtn = widget.NewButtonWithIcon("", theme.ContentCopyIcon(), func() {
		if h.onDuplicate != nil {
			h.onDuplicate()
		}
	})
	b.dupBtn.Importance = widget.LowImportance

	b.delBtn = widget.NewButtonWithIcon("", theme.DeleteIcon(), func() {
		if h.onDelete != nil {
			h.onDelete()
		}
	})
	b.delBtn.Importance = widget.LowImportance

	b.controlsRow = container.NewHBox(b.upBtn, b.downBtn, b.muteBtn, b.dupBtn, b.delBtn)

	if h.icon != nil {
		b.iconImg = widget.NewIcon(h.icon)
	}
	if b.isWaitBlock() {
		b.ensureWaitControls(core.StepDelayMs(h.step))
	}

	b.ExtendBaseWidget(b)
	return b
}

func (b *multiActionBlockWidget) isWaitBlock() bool {
	return strings.TrimSpace(b.step.ActionUUID) == core.ActionDelay
}

func (b *multiActionBlockWidget) ensureWaitControls(ms int) {
	if b.waitEntry != nil {
		return
	}
	b.lastWaitMs = ms
	b.waitEntry = newFocusLossEntry(strconv.Itoa(ms), b.handlers.onFocusWait, func(raw string) {
		b.commitWaitValue(raw)
	})
	b.waitEntry.SetPlaceHolder("250")
	b.waitUnitLbl = widget.NewLabel("ms")

	presets := []struct {
		label string
		ms    int
	}{
		{label: "100ms", ms: 100},
		{label: "250ms", ms: 250},
		{label: "500ms", ms: 500},
		{label: "1s", ms: 1000},
	}
	b.presetBtns = make([]*widget.Button, 0, len(presets))
	presetObjs := make([]fyne.CanvasObject, 0, len(presets)+1)
	presetLbl := widget.NewLabel("Presets:")
	presetObjs = append(presetObjs, presetLbl)
	for _, p := range presets {
		targetMs := p.ms
		btn := widget.NewButton(p.label, func() {
			b.lastWaitMs = targetMs
			b.waitEntry.SetText(strconv.Itoa(targetMs))
			if b.handlers.onCommitWait != nil {
				b.handlers.onCommitWait(targetMs)
			}
		})
		btn.Importance = widget.LowImportance
		b.presetBtns = append(b.presetBtns, btn)
		presetObjs = append(presetObjs, btn)
	}

	waitRow := container.NewHBox(
		widget.NewLabel("Wait"),
		b.waitEntry,
		b.waitUnitLbl,
	)
	presetRow := container.NewHBox(presetObjs...)
	b.contentBox = container.NewVBox(waitRow, presetRow)
}

func (b *multiActionBlockWidget) commitWaitValue(raw string) {
	if b.waitEntry == nil {
		return
	}
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		b.waitEntry.SetText(strconv.Itoa(b.lastWaitMs))
		return
	}
	parsed, err := strconv.Atoi(trimmed)
	if err != nil || parsed < 0 {
		b.waitEntry.SetText(strconv.Itoa(b.lastWaitMs))
		return
	}
	if parsed == b.lastWaitMs {
		return
	}
	b.lastWaitMs = parsed
	if b.handlers.onCommitWait != nil {
		b.handlers.onCommitWait(parsed)
	}
}

func (b *multiActionBlockWidget) commitWaitIfPending() {
	if !b.isWaitBlock() || b.waitEntry == nil {
		return
	}
	b.commitWaitValue(b.waitEntry.Text)
}

func (b *multiActionBlockWidget) ApplyStep(index, total int, step core.MultiActionStep, execState stepExecState, selected bool) {
	b.index = index
	b.total = total
	b.step = step
	b.execState = execState
	b.selected = selected

	if b.index <= 0 {
		b.upBtn.Disable()
	} else {
		b.upBtn.Enable()
	}
	if b.index >= b.total-1 {
		b.downBtn.Disable()
	} else {
		b.downBtn.Enable()
	}
	if step.Disabled {
		b.muteBtn.SetIcon(theme.VisibilityOffIcon())
	} else {
		b.muteBtn.SetIcon(theme.VisibilityIcon())
	}

	if b.isWaitBlock() {
		ms := core.StepDelayMs(step)
		b.ensureWaitControls(ms)
		if b.lastWaitMs != ms || b.waitEntry.Text == "" {
			b.lastWaitMs = ms
			b.waitEntry.SetText(strconv.Itoa(ms))
		}
	}

	b.Refresh()
}

func (b *multiActionBlockWidget) SetSelected(selected bool) {
	if b.selected == selected {
		return
	}
	b.selected = selected
	b.Refresh()
}

func (b *multiActionBlockWidget) SetExecState(st stepExecState) {
	if b.execState == st {
		return
	}
	b.execState = st
	b.Refresh()
}

func (b *multiActionBlockWidget) Tapped(*fyne.PointEvent) {
	if b.handlers.onTap != nil {
		b.handlers.onTap()
	}
}

func (b *multiActionBlockWidget) Dragged(ev *fyne.DragEvent) {
	if b.handlers.onDragMove == nil || b.total <= 1 {
		return
	}
	abs := widgetAbsolutePos(b, ev.Position, ev.AbsolutePosition)
	b.dragLastAbs = abs
	if !b.dragActive {
		b.dragAccumX += ev.Dragged.DX
		b.dragAccumY += ev.Dragged.DY
		if b.dragAccumX*b.dragAccumX+b.dragAccumY*b.dragAccumY < dragActivationThresholdPx*dragActivationThresholdPx {
			return
		}
		b.dragActive = true
	}
	b.handlers.onDragMove(abs)
}

func (b *multiActionBlockWidget) DragEnd() {
	wasActive := b.dragActive
	b.dragAccumX = 0
	b.dragAccumY = 0
	b.dragActive = false
	if wasActive && b.handlers.onDragEnd != nil {
		b.handlers.onDragEnd()
	}
}

func (b *multiActionBlockWidget) MouseIn(*desktop.MouseEvent) {
	b.hovered = true
	b.Refresh()
}

func (b *multiActionBlockWidget) MouseMoved(*desktop.MouseEvent) {}

func (b *multiActionBlockWidget) MouseOut() {
	b.hovered = false
	b.Refresh()
}

func (b *multiActionBlockWidget) CreateRenderer() fyne.WidgetRenderer {
	bg := canvas.NewRectangle(ColorDarkCharcoal)
	bg.CornerRadius = 10
	bg.StrokeColor = ColorSlateBorder
	bg.StrokeWidth = 1

	topNotch := canvas.NewRectangle(ColorObsidianSlate)
	topNotch.CornerRadius = 2

	bottomNotch := canvas.NewRectangle(ColorSlateBorder)
	bottomNotch.CornerRadius = 2

	stripe := canvas.NewRectangle(ColorSkyBlue)
	stripe.CornerRadius = 2

	objs := []fyne.CanvasObject{
		bg,
		topNotch,
		bottomNotch,
		stripe,
		b.gripTxt,
		b.controlsRow,
	}
	if b.contentBox != nil {
		objs = append(objs, b.contentBox)
	} else {
		if b.iconImg != nil {
			objs = append(objs, b.iconImg)
		}
		objs = append(objs, b.titleTxt, b.summaryTxt)
	}

	return &multiActionBlockRenderer{
		block:       b,
		bg:          bg,
		topNotch:    topNotch,
		bottomNotch: bottomNotch,
		stripe:      stripe,
		objs:        objs,
	}
}

type multiActionBlockRenderer struct {
	block       *multiActionBlockWidget
	bg          *canvas.Rectangle
	topNotch    *canvas.Rectangle
	bottomNotch *canvas.Rectangle
	stripe      *canvas.Rectangle
	objs        []fyne.CanvasObject
}

func (r *multiActionBlockRenderer) MinSize() fyne.Size {
	ctrlMin := r.block.controlsRow.MinSize()
	if r.block.contentBox != nil {
		cMin := r.block.contentBox.MinSize()
		return fyne.NewSize(max(float32(320), cMin.Width+ctrlMin.Width+52), max(float32(64), cMin.Height+18)+5)
	}
	return fyne.NewSize(max(float32(320), ctrlMin.Width+190), 62)
}

func (r *multiActionBlockRenderer) Layout(size fyne.Size) {
	hasBottomNotch := r.block.index+1 < r.block.total
	notchH := float32(0)
	if hasBottomNotch {
		notchH = 5
	}
	bodyH := max(float32(44), size.Height-notchH)

	r.bg.Move(fyne.NewPos(0, 0))
	r.bg.Resize(fyne.NewSize(size.Width, bodyH))

	r.topNotch.Move(fyne.NewPos(34, 0))
	r.topNotch.Resize(fyne.NewSize(26, 3))

	if hasBottomNotch {
		r.bottomNotch.Show()
		r.bottomNotch.Move(fyne.NewPos(34, bodyH))
		r.bottomNotch.Resize(fyne.NewSize(26, 5))
	} else {
		r.bottomNotch.Hide()
	}

	r.stripe.Move(fyne.NewPos(6, 7))
	r.stripe.Resize(fyne.NewSize(5, max(float32(14), bodyH-14)))

	gripSz := r.block.gripTxt.MinSize()
	r.block.gripTxt.Move(fyne.NewPos(17, (bodyH-gripSz.Height)/2))
	r.block.gripTxt.Resize(gripSz)

	ctrlSz := r.block.controlsRow.MinSize()
	ctrlX := max(float32(120), size.Width-ctrlSz.Width-8)
	r.block.controlsRow.Move(fyne.NewPos(ctrlX, (bodyH-ctrlSz.Height)/2))
	r.block.controlsRow.Resize(ctrlSz)

	contentX := float32(34)
	if r.block.contentBox != nil {
		cMin := r.block.contentBox.MinSize()
		cW := max(float32(80), ctrlX-contentX-8)
		r.block.contentBox.Move(fyne.NewPos(contentX, max(float32(6), (bodyH-cMin.Height)/2)))
		r.block.contentBox.Resize(fyne.NewSize(cW, cMin.Height))
		return
	}

	textX := contentX
	if r.block.iconImg != nil {
		iconSz := float32(20)
		r.block.iconImg.Move(fyne.NewPos(contentX, (bodyH-iconSz)/2))
		r.block.iconImg.Resize(fyne.NewSize(iconSz, iconSz))
		textX += iconSz + 8
	}

	textW := max(float32(40), ctrlX-textX-8)
	titleMin := r.block.titleTxt.MinSize()
	sumMin := r.block.summaryTxt.MinSize()
	totalTextH := titleMin.Height + sumMin.Height + 3
	startY := max(float32(6), (bodyH-totalTextH)/2)

	r.block.titleTxt.Text = truncateCanvasText(r.block.titleString(), textW, 12, fyne.TextStyle{Bold: true})
	r.block.titleTxt.Move(fyne.NewPos(textX, startY))
	r.block.titleTxt.Resize(fyne.NewSize(textW, titleMin.Height))

	r.block.summaryTxt.Text = truncateCanvasText(r.block.summaryString(), textW, 11, fyne.TextStyle{})
	r.block.summaryTxt.Move(fyne.NewPos(textX, startY+titleMin.Height+3))
	r.block.summaryTxt.Resize(fyne.NewSize(textW, sumMin.Height))
}

func (r *multiActionBlockRenderer) Refresh() {
	b := r.block

	stripeCol := ColorSkyBlue
	switch {
	case b.step.Disabled:
		stripeCol = ColorMutedText
	case b.isWaitBlock():
		stripeCol = ColorWarning
	case b.handlers.desc != nil && strings.TrimSpace(b.handlers.desc.DefaultColor) != "":
		stripeCol = parseHexColor(b.handlers.desc.DefaultColor, ColorSkyBlue)
	}
	r.stripe.FillColor = stripeCol

	fillCol := ColorDarkCharcoal
	if b.isWaitBlock() {
		fillCol = color.RGBA{R: 26, G: 24, B: 18, A: 255}
	}
	switch {
	case b.step.Disabled:
		fillCol = ColorObsidianSlate
	case b.execState == stepExecRunning:
		fillCol = blendRGBA(ColorElevatedSlate, ColorSkyBlue, 0.20)
	case b.selected || b.hovered:
		fillCol = ColorElevatedSlate
	}
	r.bg.FillColor = fillCol

	switch {
	case b.execState == stepExecRunning:
		r.bg.StrokeColor = ColorSkyBlue
		r.bg.StrokeWidth = 2.5
	case b.execState == stepExecOK:
		r.bg.StrokeColor = ColorSuccess
		r.bg.StrokeWidth = 2.0
	case b.execState == stepExecError:
		r.bg.StrokeColor = ColorError
		r.bg.StrokeWidth = 2.0
	case b.selected:
		r.bg.StrokeColor = ColorSkyBlue
		r.bg.StrokeWidth = 2.0
	case b.hovered:
		r.bg.StrokeColor = ColorSecondarySlate
		r.bg.StrokeWidth = 1.5
	default:
		r.bg.StrokeColor = ColorSlateBorder
		r.bg.StrokeWidth = 1.0
	}
	r.bottomNotch.FillColor = r.bg.StrokeColor

	if b.step.Disabled {
		b.titleTxt.Color = ColorSecondarySlate
		b.summaryTxt.Color = ColorMutedText
	} else {
		b.titleTxt.Color = ColorChromeWhite
		b.summaryTxt.Color = ColorSecondarySlate
	}

	r.bg.Refresh()
	r.stripe.Refresh()
	r.bottomNotch.Refresh()
	b.titleTxt.Refresh()
	b.summaryTxt.Refresh()
	r.Layout(b.Size())
}

func (r *multiActionBlockRenderer) Objects() []fyne.CanvasObject {
	return r.objs
}

func (r *multiActionBlockRenderer) Destroy() {}

func (b *multiActionBlockWidget) titleString() string {
	var name string
	switch {
	case b.isWaitBlock():
		name = "Wait"
	case b.handlers.desc != nil && strings.TrimSpace(b.handlers.desc.Name) != "":
		name = b.handlers.desc.Name
		if cat := strings.TrimSpace(b.handlers.desc.Category); cat != "" {
			name += " · " + cat
		}
	default:
		name = "Missing Action (" + b.step.ActionUUID + ")"
	}
	return fmt.Sprintf("%d. %s", b.index+1, name)
}

func (b *multiActionBlockWidget) summaryString() string {
	return summarizeMultiActionStep(b.handlers.desc, b.step)
}

func summarizeMultiActionStep(desc *plugin.ActionDescriptor, step core.MultiActionStep) string {
	var text string
	uuid := strings.TrimSpace(step.ActionUUID)
	switch uuid {
	case core.ActionDelay:
		text = "Wait " + formatDurationCompact(core.StepDelayMs(step))
	case core.ActionPageGoTo:
		text = fmt.Sprintf("Go to Page %v", step.Settings["page"])
	case core.ActionPageNext, core.ActionPagePrev:
		if toBoolValue(step.Settings["wrap"]) {
			text = "Wrap around pages"
		} else {
			text = "Stop at edge"
		}
	case core.ActionProfileSwitch:
		if p := settingStr(step.Settings, "profile"); p != "" {
			text = "Profile: " + p
		}
	case core.ActionFolderOpen:
		if f := settingStr(step.Settings, "folderName"); f != "" {
			text = "Folder: " + f
		}
	default:
		text = summarizeCommonKeys(desc, step.Settings)
	}

	if text == "" {
		if desc != nil && len(desc.SettingsSchema) == 0 && desc.Provider != plugin.ProviderStreamDeck {
			text = "Ready (no configuration needed)"
		} else {
			text = "Click to configure..."
		}
	}
	if step.Disabled {
		return "[Muted] " + text
	}
	return text
}

func summarizeCommonKeys(desc *plugin.ActionDescriptor, settings map[string]any) string {
	if len(settings) == 0 {
		return ""
	}
	if scene := settingStr(settings, "sceneName"); scene != "" {
		if src := settingStr(settings, "sourceName"); src != "" {
			if mode := settingStr(settings, "mode"); mode != "" {
				return fmt.Sprintf("%s / %s (%s)", scene, src, mode)
			}
			return fmt.Sprintf("%s / %s", scene, src)
		}
		return "Scene: " + scene
	}
	if input := settingStr(settings, "inputName"); input != "" {
		if mode := settingStr(settings, "mode"); mode != "" {
			return fmt.Sprintf("%s (%s)", input, mode)
		}
		return "Input: " + input
	}
	if req := settingStr(settings, "requestType"); req != "" {
		return "Request: " + req
	}
	if chord := settingStr(settings, "chord"); chord != "" {
		return "Hotkey: " + chord
	}
	if cmd := settingStr(settings, "command"); cmd != "" {
		if args := settingStr(settings, "args"); args != "" {
			return cmd + " " + args
		}
		return cmd
	}
	if u := settingStr(settings, "url"); u != "" {
		if m := settingStr(settings, "method"); m != "" {
			return m + " " + u
		}
		return u
	}
	if txt := settingStr(settings, "text"); txt != "" {
		return fmt.Sprintf("%q", txt)
	}
	if fp := settingStr(settings, "filePath"); fp != "" {
		return "Audio: " + filepath.Base(fp)
	}
	if mode := settingStr(settings, "mode"); mode != "" {
		return "Mode: " + mode
	}

	if desc != nil {
		for _, f := range desc.SettingsSchema {
			if f.Type == plugin.FieldPassword || f.Type == plugin.FieldInfo || f.Type == plugin.FieldLink || f.Type == plugin.FieldButton {
				continue
			}
			if v, ok := settings[f.Key]; ok && v != nil {
				s := strings.TrimSpace(fmt.Sprint(v))
				if s != "" && s != "[]" && s != "{}" {
					return f.Label + ": " + s
				}
			}
		}
	}

	return fmt.Sprintf("%d setting(s) configured", len(settings))
}

func settingStr(settings map[string]any, key string) string {
	if settings == nil {
		return ""
	}
	v, ok := settings[key]
	if !ok || v == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(v))
}

func formatDurationCompact(ms int) string {
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	if ms%1000 == 0 {
		return fmt.Sprintf("%ds", ms/1000)
	}
	return fmt.Sprintf("%.2gs", float64(ms)/1000.0)
}

func defaultStepSettings(desc plugin.ActionDescriptor) map[string]any {
	out := make(map[string]any, len(desc.SettingsSchema))
	for _, field := range desc.SettingsSchema {
		if field.Default != nil {
			out[field.Key] = field.Default
		}
	}
	return out
}

func cloneMultiActionStep(st core.MultiActionStep) core.MultiActionStep {
	cp := st
	cp.Settings = cloneStepSettingsMap(st.Settings)
	return cp
}

func cloneMultiActionSteps(steps []core.MultiActionStep) []core.MultiActionStep {
	if len(steps) == 0 {
		return nil
	}
	out := make([]core.MultiActionStep, len(steps))
	for i, st := range steps {
		out[i] = cloneMultiActionStep(st)
	}
	return out
}

func cloneStepSettingsMap(src map[string]any) map[string]any {
	if len(src) == 0 {
		return make(map[string]any)
	}
	out := make(map[string]any, len(src))
	for k, v := range src {
		out[k] = cloneStepSettingVal(v)
	}
	return out
}

func cloneStepSettingVal(v any) any {
	switch tv := v.(type) {
	case map[string]any:
		return cloneStepSettingsMap(tv)
	case []any:
		cp := make([]any, len(tv))
		for i, elem := range tv {
			cp[i] = cloneStepSettingVal(elem)
		}
		return cp
	case map[string]string:
		return maps.Clone(tv)
	case []string:
		return slices.Clone(tv)
	default:
		return v
	}
}
