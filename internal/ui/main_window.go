package ui

import (
	"context"
	"fmt"
	"image/color"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/mrpoundsign/PoundDeck/internal/app"
	"github.com/mrpoundsign/PoundDeck/internal/version"
	"github.com/mrpoundsign/PoundDeck/internal/webdeck"
	"github.com/mrpoundsign/PoundDeck/pkg/builtin/obs"
	"github.com/mrpoundsign/PoundDeck/pkg/deck"
	"github.com/mrpoundsign/PoundDeck/pkg/plugin"
)

// dispatchUI marshals UI state updates onto the Fyne main thread.
var dispatchUI = fyne.Do

type gridPreset struct {
	label string
	grid  deck.GridSize
}

const customGridPresetLabel = "Custom"

var studioGridPresets = []gridPreset{
	{label: "3×2 (Mini)", grid: deck.GridSize{Cols: 3, Rows: 2}},
	{label: "4×2 (Compact)", grid: deck.GridSize{Cols: 4, Rows: 2}},
	{label: "5×2 (Phone)", grid: deck.GridSize{Cols: 5, Rows: 2}},
	{label: "5×3 (Standard)", grid: deck.GridSize{Cols: 5, Rows: 3}},
	{label: "6×4 (XL)", grid: deck.GridSize{Cols: 6, Rows: 4}},
	{label: "8×4 (XL+)", grid: deck.GridSize{Cols: 8, Rows: 4}},
}

// MainWindow hosts the PoundDeck desktop studio and interactive virtual macro deck.
type MainWindow struct {
	app          fyne.App
	win          fyne.Window
	engine       *app.Engine
	unsub        func()
	obsCtrl     OBSController
	obsUnsub    func()
	sdCtrl      SDPluginController
	webCtrl     WebDeckController
	webUnsub    func()
	piViewer    PIViewer
	popOut      *PopOutPad
	cfgPath     string
	hotkeyCount func() int

	selectedController deck.ControllerType
	selectedSlot       deck.Coordinates
	selectedDial       int
	runMode            bool
	syncing            bool
	statusNote         string
	lastSnap           app.Snapshot
	profileIDs         []string

	profileSelect  *widget.Select
	pageLabel      *widget.Label
	backBtn        *widget.Button
	undoBtn        *widget.Button
	redoBtn        *widget.Button
	obsBtn         *widget.Button
	webBtn         *widget.Button
	runModeCheck   *widget.Check
	catalogList    *fyne.Container
	catalogButtons map[string]*widget.Button
	surface        *deckSurfaceView
	deckTitle      *canvas.Text
	deckHint       *canvas.Text
	inspector      *ActionInspector
	statusBarLbl   *widget.Label
	confirmDlg     *dialog.ConfirmDialog
	shortcuts      fyne.ShortcutHandler
}

// NewMainWindow constructs and initializes the PoundDeck studio window wired to engine.
func NewMainWindow(a fyne.App, engine *app.Engine) *MainWindow {
	win := a.NewWindow(version.AppName)
	win.Resize(fyne.NewSize(1240, 760))

	mw := &MainWindow{
		app:                a,
		win:                win,
		engine:             engine,
		selectedController: deck.ControllerKeypad,
		selectedSlot:       deck.Coordinates{Row: 0, Col: 0},
		statusNote:         "Ready",
		catalogButtons:     make(map[string]*widget.Button),
	}

	mw.inspector = NewActionInspector(win, engine, mw.setStatus)
	mw.inspector.SetOnOpenPluginsSettings(func() {
		mw.ShowSettingsDialogTab(settingsTabPlugins)
	})
	mw.surface = newDeckSurfaceView(
		mw.handleKeyTap,
		mw.handleDialTap,
		mw.handleDialDelta,
		mw.handleDialPress,
	)

	header := mw.buildHeader()
	catalog := mw.buildActionCatalog()
	center := mw.buildDeckCenter()
	statusBar := mw.buildStatusBar()

	splitRight := container.NewHSplit(center, mw.inspector.Object())
	splitRight.SetOffset(0.67)

	splitMain := container.NewHSplit(catalog, splitRight)
	splitMain.SetOffset(0.23)

	root := container.NewBorder(header, statusBar, nil, nil, splitMain)
	win.SetContent(root)

	mw.addCanvasShortcut(&fyne.ShortcutUndo{}, func(fyne.Shortcut) {
		if len(mw.win.Canvas().Overlays().List()) == 0 {
			mw.handleUndo()
		}
	})
	mw.addCanvasShortcut(&fyne.ShortcutRedo{}, func(fyne.Shortcut) {
		if len(mw.win.Canvas().Overlays().List()) == 0 {
			mw.handleRedo()
		}
	})
	mw.addCanvasShortcut(&desktop.CustomShortcut{
		KeyName:  fyne.KeyZ,
		Modifier: fyne.KeyModifierShortcutDefault | fyne.KeyModifierShift,
	}, func(fyne.Shortcut) {
		if len(mw.win.Canvas().Overlays().List()) == 0 {
			mw.handleRedo()
		}
	})

	mw.applySnapshot(engine.Snapshot())
	mw.unsub = engine.Subscribe(func(snap app.Snapshot) {
		dispatchUI(func() {
			mw.applySnapshot(snap)
		})
	})

	win.SetOnClosed(func() {
		if mw.obsUnsub != nil {
			mw.obsUnsub()
			mw.obsUnsub = nil
		}
		if mw.webUnsub != nil {
			mw.webUnsub()
			mw.webUnsub = nil
		}
		if mw.unsub != nil {
			mw.unsub()
			mw.unsub = nil
		}
		if mw.popOut != nil {
			mw.popOut.Close()
			mw.popOut = nil
		}
		if mw.piViewer != nil {
			mw.piViewer.CloseAll()
		}
	})

	return mw
}

// Window returns the underlying Fyne window.
func (mw *MainWindow) Window() fyne.Window {
	return mw.win
}

// ShowAndRun displays the main window and starts the Fyne event loop.
func (mw *MainWindow) ShowAndRun() {
	mw.win.ShowAndRun()
}

// SelectedSlot returns the currently selected Keypad coordinates.
func (mw *MainWindow) SelectedSlot() deck.Coordinates {
	return mw.selectedSlot
}

// SelectedController returns whether a Keypad slot or Encoder dial is currently selected.
func (mw *MainWindow) SelectedController() deck.ControllerType {
	return mw.selectedController
}

// SelectedDial returns the 0-based index of the currently selected dial.
func (mw *MainWindow) SelectedDial() int {
	return mw.selectedDial
}

// SelectSlot focuses keypad coordinates c and updates the surface, inspector, and status bar.
// Out-of-bounds coordinates are ignored.
func (mw *MainWindow) SelectSlot(c deck.Coordinates) {
	snap := mw.engine.Snapshot()
	if !snap.Grid.Contains(c) {
		return
	}
	mw.selectedController = deck.ControllerKeypad
	mw.selectedSlot = c
	mw.applySnapshot(snap)
}

// SelectDial focuses encoder dial index and updates the surface, inspector, and status bar.
func (mw *MainWindow) SelectDial(dial int) {
	snap := mw.engine.Snapshot()
	if dial < 0 || dial >= snap.Dials {
		return
	}
	mw.selectedController = deck.ControllerEncoder
	mw.selectedDial = dial
	mw.applySnapshot(snap)
}

// SetRunMode toggles between Edit Mode (false) and Run Mode (true).
func (mw *MainWindow) SetRunMode(run bool) {
	mw.runMode = run
	if mw.runModeCheck != nil && mw.runModeCheck.Checked != run {
		mw.syncing = true
		mw.runModeCheck.SetChecked(run)
		mw.syncing = false
	}
	if run {
		mw.deckHint.Text = "Run Mode: clicking tiles triggers actions live"
		mw.setStatus("Run Mode enabled")
	} else {
		mw.deckHint.Text = "Edit Mode: click any slot or dial to inspect & bind actions"
		mw.setStatus("Edit Mode enabled")
	}
	mw.deckHint.Refresh()
}

// OpenPopOutPad opens (or focuses) the floating Run Mode Pop-Out Pad window.
func (mw *MainWindow) OpenPopOutPad() *PopOutPad {
	if mw.popOut != nil {
		mw.popOut.Show()
		return mw.popOut
	}
	mw.popOut = NewPopOutPad(mw.app, mw.engine, func() {
		mw.popOut = nil
	})
	mw.popOut.Show()
	return mw.popOut
}

// SetConfigPath records the config file path displayed in the General settings tab.
func (mw *MainWindow) SetConfigPath(path string) {
	mw.cfgPath = path
}

// SetHotkeyCountProvider sets a callback returning active OS global hotkey registrations.
func (mw *MainWindow) SetHotkeyCountProvider(fn func() int) {
	mw.hotkeyCount = fn
}

// SetHotkeySuspender sets a callback invoked to suspend or resume global OS hotkeys while capturing shortcuts.
func (mw *MainWindow) SetHotkeySuspender(fn func(bool)) {
	if mw.inspector != nil {
		mw.inspector.SetHotkeySuspender(fn)
	}
}

// ShowSettingsDialog opens the tabbed PoundDeck Settings modal dialog on the General tab.
func (mw *MainWindow) ShowSettingsDialog() *dialog.CustomDialog {
	return mw.ShowSettingsDialogTab(settingsTabGeneral)
}

// ShowSettingsDialogTab opens the tabbed PoundDeck Settings modal dialog pre-selected to tab.
func (mw *MainWindow) ShowSettingsDialogTab(tab int) *dialog.CustomDialog {
	hotkeys := 0
	if mw.hotkeyCount != nil {
		hotkeys = mw.hotkeyCount()
	}
	return showSettingsDialog(
		mw.win,
		mw.engine,
		mw.obsCtrl,
		mw.sdCtrl,
		mw.webCtrl,
		mw.cfgPath,
		hotkeys,
		tab,
		func() { mw.OpenPopOutPad() },
		mw.ReloadExternalPlugins,
		mw.setStatus,
	)
}

// SetSDPluginController attaches an external .sdPlugin host controller and refreshes inspector hints.
func (mw *MainWindow) SetSDPluginController(ctrl SDPluginController) {
	mw.sdCtrl = ctrl
	if mw.inspector != nil {
		mw.inspector.SetSDPluginController(ctrl)
		mw.inspector.InvalidateBinding()
	}
	mw.applySnapshot(mw.engine.Snapshot())
}

// SetPIViewer attaches the Property Inspector viewer for interactive HTML plugin views.
func (mw *MainWindow) SetPIViewer(v PIViewer) {
	mw.piViewer = v
	if mw.inspector != nil {
		mw.inspector.SetPIViewer(v)
	}
}

// ReloadExternalPlugins rescans .sdPlugin bundles, refreshes the unified action catalog, and updates visible tiles.
func (mw *MainWindow) ReloadExternalPlugins() error {
	if mw.sdCtrl != nil {
		if err := mw.sdCtrl.Reload(); err != nil {
			return err
		}
		if prov, ok := mw.sdCtrl.(plugin.Provider); ok {
			if err := mw.engine.Registry().RefreshProvider(prov); err != nil {
				return err
			}
		}
	}
	mw.engine.ReloadVisibleActions()
	mw.RefreshActionCatalog()
	if mw.inspector != nil {
		mw.inspector.InvalidateBinding()
	}
	mw.applySnapshot(mw.engine.Snapshot())
	return nil
}

// SetOBSController attaches an OBSController so the Studio header badge displays live OBS status
// and clicking it opens the OBS connection configuration dialog.
func (mw *MainWindow) SetOBSController(ctrl OBSController) {
	if mw.obsUnsub != nil {
		mw.obsUnsub()
		mw.obsUnsub = nil
	}
	mw.obsCtrl = ctrl
	if ctrl == nil {
		mw.obsBtn.Hide()
		return
	}

	mw.obsBtn.Show()
	mw.applyOBSStatus(ctrl.Status())
	mw.obsUnsub = ctrl.SubscribeStatus(func(st obs.ClientStatus) {
		dispatchUI(func() {
			mw.applyOBSStatus(st)
		})
	})
}

func (mw *MainWindow) applyOBSStatus(st obs.ClientStatus) {
	if mw.obsBtn == nil {
		return
	}
	mw.obsBtn.SetText(obsBadgeLabel(st))
	if st.State == obs.StateConnected {
		mw.obsBtn.Importance = widget.HighImportance
		mw.inspector.RefreshDynamicOptions()
	} else {
		mw.obsBtn.Importance = widget.MediumImportance
	}
	mw.obsBtn.Refresh()
}

// SetWebDeckController attaches a WebDeckController so the Studio header badge displays live LAN touch deck
// status and clicking it opens Settings directly on the Web Deck tab.
func (mw *MainWindow) SetWebDeckController(ctrl WebDeckController) {
	if mw.webUnsub != nil {
		mw.webUnsub()
		mw.webUnsub = nil
	}
	mw.webCtrl = ctrl
	if ctrl == nil {
		mw.webBtn.Hide()
		return
	}

	mw.webBtn.Show()
	mw.applyWebDeckStatus(ctrl.Status())
	mw.webUnsub = ctrl.SubscribeStatus(func(st webdeck.Status) {
		dispatchUI(func() {
			mw.applyWebDeckStatus(st)
		})
	})
}

func (mw *MainWindow) applyWebDeckStatus(st webdeck.Status) {
	if mw.webBtn == nil {
		return
	}
	mw.webBtn.SetText(webDeckBadgeLabel(st))
	if st.Running {
		mw.webBtn.Importance = widget.HighImportance
	} else {
		mw.webBtn.Importance = widget.MediumImportance
	}
	mw.webBtn.Refresh()
}

func (mw *MainWindow) setStatus(msg string) {
	mw.statusNote = msg
	mw.refreshStatusBar(mw.lastSnap)
}

func (mw *MainWindow) addCanvasShortcut(s fyne.Shortcut, fn func(fyne.Shortcut)) {
	mw.win.Canvas().AddShortcut(s, fn)
	mw.shortcuts.AddShortcut(s, fn)
}

func (mw *MainWindow) handleUndo() {
	label, err := mw.engine.Undo()
	if err != nil {
		mw.setStatus("Undo failed: " + err.Error())
		return
	}
	if label == "" {
		mw.setStatus("Nothing to undo")
		return
	}
	mw.setStatus("Undid: " + label)
}

func (mw *MainWindow) handleRedo() {
	label, err := mw.engine.Redo()
	if err != nil {
		mw.setStatus("Redo failed: " + err.Error())
		return
	}
	if label == "" {
		mw.setStatus("Nothing to redo")
		return
	}
	mw.setStatus("Redid: " + label)
}

func (mw *MainWindow) assignAction(actionUUID string) {
	newDesc, hasDesc := mw.engine.Registry().Action(actionUUID)
	if hasDesc && !newDesc.SupportsController(mw.selectedController) {
		mw.performAssignAction(mw.selectedController, mw.selectedSlot, mw.selectedDial, actionUUID)
		return
	}

	snap := mw.engine.Snapshot()
	var (
		occupied  bool
		existing  app.RenderedSlot
		targetLbl string
	)
	targetCtrl := mw.selectedController
	targetCoords := mw.selectedSlot
	targetDial := mw.selectedDial

	if targetCtrl == deck.ControllerEncoder {
		existing, occupied = snap.DialSlots[targetDial]
		occupied = occupied && existing.Config != nil
		targetLbl = fmt.Sprintf("Encoder Dial %d", targetDial+1)
	} else {
		existing, occupied = snap.Slots[targetCoords.SlotKey()]
		occupied = occupied && existing.Config != nil && !existing.AutoBack
		targetLbl = fmt.Sprintf("Slot (%d, %d)", targetCoords.Row, targetCoords.Col)
	}

	if !occupied {
		mw.performAssignAction(targetCtrl, targetCoords, targetDial, actionUUID)
		return
	}

	curActionName := existing.Config.ActionUUID
	if existing.Action != nil && existing.Action.Name != "" {
		curActionName = existing.Action.Name
	}
	curDesc := curActionName
	if curTitle := strings.TrimSpace(existing.Config.Title); curTitle != "" && !strings.EqualFold(curTitle, curActionName) {
		curDesc = fmt.Sprintf("%q (%s)", curTitle, curActionName)
	}

	newActionName := actionUUID
	if hasDesc && newDesc.Name != "" {
		newActionName = newDesc.Name
	}

	msg := fmt.Sprintf(
		"%s currently has %s configured.\n\nReplace it with %s?\n\nYou can still undo with Ctrl+Z or Undo.",
		targetLbl, curDesc, newActionName,
	)

	dlg := dialog.NewConfirm("Replace Existing Action?", msg, func(confirmed bool) {
		mw.confirmDlg = nil
		if !confirmed {
			mw.setStatus("Kept existing action")
			return
		}
		mw.performAssignAction(targetCtrl, targetCoords, targetDial, actionUUID)
	}, mw.win)
	dlg.SetConfirmText("Replace")
	dlg.SetDismissText("Cancel")
	dlg.SetConfirmImportance(widget.DangerImportance)
	mw.confirmDlg = dlg
	dlg.Show()
}

func (mw *MainWindow) performAssignAction(ctrl deck.ControllerType, coords deck.Coordinates, dial int, actionUUID string) {
	var err error
	if ctrl == deck.ControllerEncoder {
		_, err = mw.engine.AssignDialAction(dial, actionUUID)
	} else {
		_, err = mw.engine.AssignKeypadAction(coords, actionUUID)
	}
	if err != nil {
		mw.setStatus(err.Error())
		return
	}
	if desc, ok := mw.engine.Registry().Action(actionUUID); ok {
		mw.setStatus("Assigned " + desc.Name)
	} else {
		mw.setStatus("Assigned action")
	}
}

func (mw *MainWindow) handleKeyTap(c deck.Coordinates) {
	if mw.runMode {
		if err := mw.engine.PressKey(context.Background(), c); err != nil {
			mw.setStatus("Action error: " + err.Error())
		}
		return
	}
	mw.SelectSlot(c)
}

func (mw *MainWindow) handleDialTap(dial int) {
	if mw.runMode {
		if err := mw.engine.PressDial(context.Background(), dial); err != nil {
			mw.setStatus("Dial error: " + err.Error())
		}
		return
	}
	mw.SelectDial(dial)
}

func (mw *MainWindow) handleDialDelta(dial, delta int) {
	if err := mw.engine.RotateDial(context.Background(), dial, delta); err != nil {
		mw.setStatus("Dial error: " + err.Error())
	}
}

func (mw *MainWindow) handleDialPress(dial int) {
	if err := mw.engine.PressDial(context.Background(), dial); err != nil {
		mw.setStatus("Dial error: " + err.Error())
	}
}

func (mw *MainWindow) applySnapshot(snap app.Snapshot) {
	mw.lastSnap = snap
	mw.syncing = true

	// Clamp selection when switching into a smaller grid or dial count.
	if snap.Grid.Rows > 0 && snap.Grid.Cols > 0 && !snap.Grid.Contains(mw.selectedSlot) {
		mw.selectedSlot = deck.Coordinates{
			Row: min(mw.selectedSlot.Row, snap.Grid.Rows-1),
			Col: min(mw.selectedSlot.Col, snap.Grid.Cols-1),
		}
	}
	if mw.selectedController == deck.ControllerEncoder && (snap.Dials <= 0 || mw.selectedDial >= snap.Dials) {
		mw.selectedController = deck.ControllerKeypad
		mw.selectedDial = 0
	}

	// Sync Profile selector.
	if mw.profileSelect != nil {
		names := make([]string, len(snap.Profiles))
		ids := make([]string, len(snap.Profiles))
		activeName := ""
		for i, p := range snap.Profiles {
			names[i] = p.Name
			ids[i] = p.ID
			if p.ID == snap.ActiveProfileID {
				activeName = p.Name
			}
		}
		mw.profileIDs = ids
		mw.profileSelect.SetOptions(names)
		if activeName != "" {
			mw.profileSelect.SetSelected(activeName)
		}
	}

	// Sync Page / Folder breadcrumb controls.
	if mw.pageLabel != nil {
		if snap.InFolder {
			mw.pageLabel.SetText("Folder: " + strings.Join(snap.FolderPath, " / "))
			mw.backBtn.Enable()
		} else {
			mw.pageLabel.SetText(fmt.Sprintf("Page %d / %d", snap.PageIndex+1, max(1, snap.PageCount)))
			mw.backBtn.Disable()
		}
	}

	if mw.deckTitle != nil {
		mw.deckTitle.Text = fmt.Sprintf("Virtual Deck (%dx%d)", snap.Grid.Cols, snap.Grid.Rows)
		mw.deckTitle.Refresh()
	}

	if mw.undoBtn != nil {
		if snap.CanUndo {
			mw.undoBtn.Enable()
		} else {
			mw.undoBtn.Disable()
		}
	}
	if mw.redoBtn != nil {
		if snap.CanRedo {
			mw.redoBtn.Enable()
		} else {
			mw.redoBtn.Disable()
		}
	}

	mw.syncing = false

	mw.surface.ApplySnapshot(snap, true, mw.selectedController, mw.selectedSlot, mw.selectedDial)
	mw.inspector.SetSelection(snap, mw.selectedController, mw.selectedSlot, mw.selectedDial)
	mw.refreshStatusBar(snap)
}

func (mw *MainWindow) refreshStatusBar(snap app.Snapshot) {
	if mw.statusBarLbl == nil {
		return
	}
	selDesc := fmt.Sprintf("(%d, %d)", mw.selectedSlot.Row, mw.selectedSlot.Col)
	if mw.selectedController == deck.ControllerEncoder {
		selDesc = fmt.Sprintf("Dial %d", mw.selectedDial+1)
	}
	note := mw.statusNote
	if note == "" {
		note = "Ready"
	}
	mw.statusBarLbl.SetText(fmt.Sprintf(
		"%s | Selected: %s | Grid: %dx%d | Plugins: %d | Web Deck: Off",
		note,
		selDesc,
		snap.Grid.Cols,
		snap.Grid.Rows,
		snap.PluginCount,
	))
}

func (mw *MainWindow) buildHeader() fyne.CanvasObject {
	title := canvas.NewText(version.AppName, ColorChromeWhite)
	title.TextSize = 18
	title.TextStyle = fyne.TextStyle{Bold: true}

	ver := canvas.NewText("v"+version.Version, ColorSkyBlue)
	ver.TextSize = 12
	ver.TextStyle = fyne.TextStyle{Bold: true}

	mw.profileSelect = widget.NewSelect([]string{"Default"}, func(_ string) {
		if mw.syncing {
			return
		}
		idx := mw.profileSelect.SelectedIndex()
		if idx >= 0 && idx < len(mw.profileIDs) {
			if err := mw.engine.SwitchProfile(mw.profileIDs[idx]); err != nil {
				mw.setStatus("Switch profile failed: " + err.Error())
			}
		}
	})

	newProfileBtn := widget.NewButtonWithIcon("", theme.ContentAddIcon(), mw.showCreateProfileDialog)
	gridBtn := widget.NewButtonWithIcon("Grid", theme.GridIcon(), mw.showGridDialog)

	prevPageBtn := widget.NewButtonWithIcon("", theme.NavigateBackIcon(), func() {
		if err := mw.engine.PrevPage(true); err != nil {
			mw.setStatus("Page switch failed: " + err.Error())
		}
	})
	mw.pageLabel = widget.NewLabel("Page 1 / 1")
	nextPageBtn := widget.NewButtonWithIcon("", theme.NavigateNextIcon(), func() {
		if err := mw.engine.NextPage(true); err != nil {
			mw.setStatus("Page switch failed: " + err.Error())
		}
	})
	addPageBtn := widget.NewButton("+ Page", func() {
		if _, err := mw.engine.AddPage(""); err != nil {
			mw.setStatus("Add page failed: " + err.Error())
		}
	})
	mw.backBtn = widget.NewButton("← Back", func() {
		if err := mw.engine.CloseFolder(); err != nil {
			mw.setStatus("Close folder failed: " + err.Error())
		}
	})
	mw.backBtn.Disable()

	mw.runModeCheck = widget.NewCheck("Run Mode", func(checked bool) {
		if mw.syncing {
			return
		}
		mw.SetRunMode(checked)
	})

	mw.undoBtn = widget.NewButtonWithIcon("", theme.ContentUndoIcon(), mw.handleUndo)
	mw.undoBtn.Disable()
	mw.redoBtn = widget.NewButtonWithIcon("", theme.ContentRedoIcon(), mw.handleRedo)
	mw.redoBtn.Disable()

	mw.obsBtn = widget.NewButton("OBS: Off", func() {
		showOBSConnectionDialog(mw.win, mw.obsCtrl, mw.setStatus)
	})
	mw.obsBtn.Hide()

	mw.webBtn = widget.NewButton("Web Deck: Off", func() {
		mw.ShowSettingsDialogTab(settingsTabWeb)
	})
	mw.webBtn.Hide()

	popOutBtn := widget.NewButton("Pop-Out Pad", func() {
		mw.OpenPopOutPad()
	})
	popOutBtn.Importance = widget.HighImportance

	settingsBtn := widget.NewButtonWithIcon("Settings", theme.SettingsIcon(), func() {
		mw.ShowSettingsDialog()
	})

	row := container.NewHBox(
		title,
		ver,
		widget.NewSeparator(),
		widget.NewLabel("Profile:"),
		mw.profileSelect,
		newProfileBtn,
		gridBtn,
		widget.NewSeparator(),
		prevPageBtn,
		mw.pageLabel,
		nextPageBtn,
		addPageBtn,
		mw.backBtn,
		layout.NewSpacer(),
		mw.undoBtn,
		mw.redoBtn,
		mw.obsBtn,
		mw.webBtn,
		mw.runModeCheck,
		popOutBtn,
		settingsBtn,
	)

	bg := canvas.NewRectangle(ColorHeaderSurface)
	sep := canvas.NewRectangle(ColorElevatedSlate)
	sep.SetMinSize(fyne.NewSize(0, 1))

	return container.NewBorder(nil, sep, nil, nil,
		container.NewStack(bg, container.NewPadded(row)),
	)
}

func (mw *MainWindow) buildActionCatalog() fyne.CanvasObject {
	heading := widget.NewLabelWithStyle("Action Catalog", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	sub := widget.NewLabel("Click an action to assign to the selected slot")
	sub.Wrapping = fyne.TextWrapWord

	mw.catalogList = container.NewVBox()
	mw.RefreshActionCatalog()

	panelBg := canvas.NewRectangle(ColorDarkCharcoal)
	return container.NewStack(
		panelBg,
		container.NewBorder(
			container.NewPadded(container.NewVBox(heading, sub, widget.NewSeparator())),
			nil, nil, nil,
			container.NewVScroll(container.NewPadded(mw.catalogList)),
		),
	)
}

// RefreshActionCatalog rebuilds the left-hand Action Catalog from currently registered plugin providers.
func (mw *MainWindow) RefreshActionCatalog() {
	if mw.catalogList == nil {
		return
	}
	clear(mw.catalogButtons)

	var items []fyne.CanvasObject
	actions := mw.engine.Registry().Actions()

	var currentCategory string
	for _, act := range actions {
		cat := act.Category
		if cat == "" {
			cat = "General"
		}
		if cat != currentCategory {
			if currentCategory != "" {
				items = append(items, widget.NewSeparator())
			}
			currentCategory = cat
			groupTitle := canvas.NewText(cat, ColorSkyBlue)
			groupTitle.TextSize = 12
			groupTitle.TextStyle = fyne.TextStyle{Bold: true}
			items = append(items, groupTitle)
		}

		uuid := act.UUID
		label := act.Name
		if act.SupportsController(deck.ControllerEncoder) {
			label += " (Key / Dial)"
		}
		btn := widget.NewButton(label, func() {
			mw.assignAction(uuid)
		})
		btn.Alignment = widget.ButtonAlignLeading
		mw.catalogButtons[uuid] = btn
		items = append(items, btn)
	}

	// Future milestones roadmap preview.
	items = append(items, widget.NewSeparator())
	upcomingTitle := canvas.NewText("Upcoming Providers", ColorMutedText)
	upcomingTitle.TextSize = 11
	upcomingTitle.TextStyle = fyne.TextStyle{Bold: true}
	items = append(items, upcomingTitle)
	for _, note := range []string{
		"WebAssembly (wazero) — M5",
	} {
		lbl := widget.NewLabel("  • " + note)
		items = append(items, lbl)
	}

	mw.catalogList.Objects = items
	mw.catalogList.Refresh()
}

func (mw *MainWindow) buildDeckCenter() fyne.CanvasObject {
	mw.deckTitle = canvas.NewText("Virtual Deck (5×3)", ColorChromeWhite)
	mw.deckTitle.TextSize = 14
	mw.deckTitle.TextStyle = fyne.TextStyle{Bold: true}

	mw.deckHint = canvas.NewText("Edit Mode: click any slot or dial to inspect & bind actions", ColorSecondarySlate)
	mw.deckHint.TextSize = 11

	padFrameBg := canvas.NewRectangle(color.RGBA{R: 15, G: 17, B: 21, A: 255})
	padFrameBg.CornerRadius = 14
	padFrameBg.StrokeColor = ColorElevatedSlate
	padFrameBg.StrokeWidth = 2

	padBody := container.NewVBox(
		container.NewHBox(mw.deckTitle, layout.NewSpacer(), mw.deckHint),
		widget.NewSeparator(),
		mw.surface.Object(),
	)

	centered := container.NewCenter(
		container.NewStack(padFrameBg, container.NewPadded(container.NewPadded(padBody))),
	)

	canvasBg := canvas.NewRectangle(ColorObsidianSlate)
	return container.NewStack(canvasBg, container.NewScroll(centered))
}

func (mw *MainWindow) buildStatusBar() fyne.CanvasObject {
	mw.statusBarLbl = widget.NewLabel("Ready")
	bg := canvas.NewRectangle(ColorHeaderSurface)
	sep := canvas.NewRectangle(ColorElevatedSlate)
	sep.SetMinSize(fyne.NewSize(0, 1))

	return container.NewBorder(sep, nil, nil, nil,
		container.NewStack(bg, container.NewPadded(mw.statusBarLbl)),
	)
}

func buildGridDimensionControls(initial deck.GridSize, initialDials int) (
	presetSelect *widget.Select,
	colsEntry *widget.Entry,
	rowsEntry *widget.Entry,
	dialsSelect *widget.Select,
	resolve func() (deck.GridSize, int, error),
) {
	if initial.Validate() != nil {
		initial = deck.DefaultGrid()
	}

	presetLabels := make([]string, 0, len(studioGridPresets)+1)
	matchedPreset := customGridPresetLabel
	for _, p := range studioGridPresets {
		presetLabels = append(presetLabels, p.label)
		if p.grid == initial {
			matchedPreset = p.label
		}
	}
	presetLabels = append(presetLabels, customGridPresetLabel)

	colsEntry = widget.NewEntry()
	colsEntry.SetPlaceHolder("1–16")
	colsEntry.SetText(strconv.Itoa(initial.Cols))

	rowsEntry = widget.NewEntry()
	rowsEntry.SetPlaceHolder("1–16")
	rowsEntry.SetText(strconv.Itoa(initial.Rows))

	syncing := false
	presetSelect = widget.NewSelect(presetLabels, func(choice string) {
		if syncing {
			return
		}
		for _, p := range studioGridPresets {
			if p.label == choice {
				syncing = true
				colsEntry.SetText(strconv.Itoa(p.grid.Cols))
				rowsEntry.SetText(strconv.Itoa(p.grid.Rows))
				syncing = false
				return
			}
		}
	})
	presetSelect.SetSelected(matchedPreset)

	syncPresetFromEntries := func(_ string) {
		if syncing {
			return
		}
		cols, cErr := strconv.Atoi(strings.TrimSpace(colsEntry.Text))
		rows, rErr := strconv.Atoi(strings.TrimSpace(rowsEntry.Text))
		target := customGridPresetLabel
		if cErr == nil && rErr == nil {
			for _, p := range studioGridPresets {
				if p.grid.Cols == cols && p.grid.Rows == rows {
					target = p.label
					break
				}
			}
		}
		if presetSelect.Selected != target {
			syncing = true
			presetSelect.SetSelected(target)
			syncing = false
		}
	}
	colsEntry.OnChanged = syncPresetFromEntries
	rowsEntry.OnChanged = syncPresetFromEntries

	dialOptions := []string{"0", "1", "2", "3", "4", "5", "6", "7", "8"}
	dialsSelect = widget.NewSelect(dialOptions, nil)
	dialStr := strconv.Itoa(max(0, min(8, initialDials)))
	dialsSelect.SetSelected(dialStr)

	resolve = func() (deck.GridSize, int, error) {
		cols, err := strconv.Atoi(strings.TrimSpace(colsEntry.Text))
		if err != nil {
			return deck.GridSize{}, 0, fmt.Errorf("invalid columns value %q", colsEntry.Text)
		}
		rows, err := strconv.Atoi(strings.TrimSpace(rowsEntry.Text))
		if err != nil {
			return deck.GridSize{}, 0, fmt.Errorf("invalid rows value %q", rowsEntry.Text)
		}
		grid := deck.GridSize{Cols: cols, Rows: rows}
		if err := grid.Validate(); err != nil {
			return deck.GridSize{}, 0, err
		}
		dials, err := strconv.Atoi(dialsSelect.Selected)
		if err != nil {
			dials = initialDials
		}
		return grid, dials, nil
	}

	return presetSelect, colsEntry, rowsEntry, dialsSelect, resolve
}

func (mw *MainWindow) showCreateProfileDialog() {
	nameEntry := widget.NewEntry()
	nameEntry.SetPlaceHolder("e.g. Phone 5×2 / Streaming")

	presetSelect, colsEntry, rowsEntry, dialsSelect, resolveGrid := buildGridDimensionControls(deck.DefaultGrid(), 4)

	items := []*widget.FormItem{
		widget.NewFormItem("Name", nameEntry),
		widget.NewFormItem("Preset", presetSelect),
		widget.NewFormItem("Columns", colsEntry),
		widget.NewFormItem("Rows", rowsEntry),
		widget.NewFormItem("Encoder Dials", dialsSelect),
	}

	dlg := dialog.NewForm("Create Profile", "Create", "Cancel", items, func(confirmed bool) {
		if !confirmed {
			return
		}
		grid, dials, err := resolveGrid()
		if err != nil {
			mw.setStatus("Invalid grid configuration: " + err.Error())
			return
		}
		if _, err := mw.engine.CreateProfile(nameEntry.Text, grid, dials); err != nil {
			mw.setStatus("Create profile failed: " + err.Error())
			return
		}
		mw.setStatus("Created profile " + strings.TrimSpace(nameEntry.Text))
	}, mw.win)
	dlg.Resize(fyne.NewSize(400, 290))
	dlg.Show()
}

func (mw *MainWindow) showGridDialog() {
	snap := mw.engine.Snapshot()
	presetSelect, colsEntry, rowsEntry, dialsSelect, resolveGrid := buildGridDimensionControls(snap.Grid, snap.Dials)

	items := []*widget.FormItem{
		widget.NewFormItem("Preset", presetSelect),
		widget.NewFormItem("Columns", colsEntry),
		widget.NewFormItem("Rows", rowsEntry),
		widget.NewFormItem("Encoder Dials", dialsSelect),
	}

	dlg := dialog.NewForm("Configure Grid & Dials", "Apply", "Cancel", items, func(confirmed bool) {
		if !confirmed {
			return
		}
		grid, dials, err := resolveGrid()
		if err != nil {
			mw.setStatus("Invalid grid configuration: " + err.Error())
			return
		}
		if err := mw.engine.UpdateProfileGrid(snap.ActiveProfileID, grid, dials); err != nil {
			mw.setStatus("Resize grid failed: " + err.Error())
			return
		}
		mw.setStatus(fmt.Sprintf("Grid updated to %dx%d (%d dials)", grid.Cols, grid.Rows, dials))
	}, mw.win)
	dlg.Resize(fyne.NewSize(380, 260))
	dlg.Show()
}
