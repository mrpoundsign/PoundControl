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
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/mrpoundsign/PoundDeck/internal/app"
	"github.com/mrpoundsign/PoundDeck/internal/version"
	"github.com/mrpoundsign/PoundDeck/pkg/deck"
)

// dispatchUI marshals UI state updates onto the Fyne main thread.
var dispatchUI = fyne.Do

type gridPreset struct {
	label string
	grid  deck.GridSize
}

var studioGridPresets = []gridPreset{
	{label: "3×2 (Mini)", grid: deck.GridSize{Cols: 3, Rows: 2}},
	{label: "4×2 (Compact)", grid: deck.GridSize{Cols: 4, Rows: 2}},
	{label: "5×3 (Standard)", grid: deck.GridSize{Cols: 5, Rows: 3}},
	{label: "6×4 (XL)", grid: deck.GridSize{Cols: 6, Rows: 4}},
	{label: "8×4 (XL+)", grid: deck.GridSize{Cols: 8, Rows: 4}},
}

// MainWindow hosts the PoundDeck desktop studio and interactive virtual macro deck.
type MainWindow struct {
	app    fyne.App
	win    fyne.Window
	engine *app.Engine
	unsub  func()
	popOut *PopOutPad

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
	runModeCheck   *widget.Check
	pluginsBadge   *canvas.Text
	webBadge       *canvas.Text
	catalogButtons map[string]*widget.Button
	surface        *deckSurfaceView
	deckTitle      *canvas.Text
	deckHint       *canvas.Text
	inspector      *ActionInspector
	statusBarLbl   *widget.Label
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

	mw.inspector = NewActionInspector(engine, mw.setStatus)
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

	mw.applySnapshot(engine.Snapshot())
	mw.unsub = engine.Subscribe(func(snap app.Snapshot) {
		dispatchUI(func() {
			mw.applySnapshot(snap)
		})
	})

	win.SetOnClosed(func() {
		if mw.unsub != nil {
			mw.unsub()
			mw.unsub = nil
		}
		if mw.popOut != nil {
			mw.popOut.Close()
			mw.popOut = nil
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

func (mw *MainWindow) setStatus(msg string) {
	mw.statusNote = msg
	mw.refreshStatusBar(mw.lastSnap)
}

func (mw *MainWindow) assignAction(actionUUID string) {
	var err error
	if mw.selectedController == deck.ControllerEncoder {
		_, err = mw.engine.AssignDialAction(mw.selectedDial, actionUUID)
	} else {
		_, err = mw.engine.AssignKeypadAction(mw.selectedSlot, actionUUID)
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
	if mw.pluginsBadge != nil {
		mw.pluginsBadge.Text = fmt.Sprintf("Plugins: %d (%d actions)", snap.PluginCount, snap.ActionCount)
		mw.pluginsBadge.Refresh()
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

	popOutBtn := widget.NewButton("Pop-Out Pad", func() {
		mw.OpenPopOutPad()
	})
	popOutBtn.Importance = widget.HighImportance

	mw.pluginsBadge = canvas.NewText("Plugins: 0", ColorSecondarySlate)
	mw.pluginsBadge.TextSize = 11
	mw.webBadge = canvas.NewText("Web Deck: Off", ColorMutedText)
	mw.webBadge.TextSize = 11

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
		mw.runModeCheck,
		popOutBtn,
		widget.NewSeparator(),
		mw.pluginsBadge,
		widget.NewSeparator(),
		mw.webBadge,
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

	list := container.NewVBox()
	actions := mw.engine.Registry().Actions()

	var currentCategory string
	for _, act := range actions {
		cat := act.Category
		if cat == "" {
			cat = "General"
		}
		if cat != currentCategory {
			if currentCategory != "" {
				list.Add(widget.NewSeparator())
			}
			currentCategory = cat
			groupTitle := canvas.NewText(cat, ColorSkyBlue)
			groupTitle.TextSize = 12
			groupTitle.TextStyle = fyne.TextStyle{Bold: true}
			list.Add(groupTitle)
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
		list.Add(btn)
	}

	// Future milestones roadmap preview.
	list.Add(widget.NewSeparator())
	upcomingTitle := canvas.NewText("Upcoming Providers", ColorMutedText)
	upcomingTitle.TextSize = 11
	upcomingTitle.TextStyle = fyne.TextStyle{Bold: true}
	list.Add(upcomingTitle)
	for _, note := range []string{
		"OBS Studio (WebSocket v5) — M2",
		"App & Macro Control — M3",
		"Elgato .sdPlugin Host — M4",
		"WebAssembly (wazero) — M5",
	} {
		lbl := widget.NewLabel("  • " + note)
		list.Add(lbl)
	}

	panelBg := canvas.NewRectangle(ColorDarkCharcoal)
	return container.NewStack(
		panelBg,
		container.NewBorder(
			container.NewPadded(container.NewVBox(heading, sub, widget.NewSeparator())),
			nil, nil, nil,
			container.NewVScroll(container.NewPadded(list)),
		),
	)
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

func (mw *MainWindow) showCreateProfileDialog() {
	nameEntry := widget.NewEntry()
	nameEntry.SetPlaceHolder("e.g. Streaming / Editing")

	presetLabels := make([]string, len(studioGridPresets))
	for i, p := range studioGridPresets {
		presetLabels[i] = p.label
	}
	gridSelect := widget.NewSelect(presetLabels, nil)
	gridSelect.SetSelected(studioGridPresets[2].label)

	dialsSelect := widget.NewSelect([]string{"0", "2", "4", "6", "8"}, nil)
	dialsSelect.SetSelected("4")

	items := []*widget.FormItem{
		widget.NewFormItem("Name", nameEntry),
		widget.NewFormItem("Grid Size", gridSelect),
		widget.NewFormItem("Encoder Dials", dialsSelect),
	}

	dlg := dialog.NewForm("Create Profile", "Create", "Cancel", items, func(confirmed bool) {
		if !confirmed {
			return
		}
		grid := deck.DefaultGrid()
		if idx := gridSelect.SelectedIndex(); idx >= 0 && idx < len(studioGridPresets) {
			grid = studioGridPresets[idx].grid
		}
		dials, err := strconv.Atoi(dialsSelect.Selected)
		if err != nil {
			dials = 4
		}
		if _, err := mw.engine.CreateProfile(nameEntry.Text, grid, dials); err != nil {
			mw.setStatus("Create profile failed: " + err.Error())
			return
		}
		mw.setStatus("Created profile " + strings.TrimSpace(nameEntry.Text))
	}, mw.win)
	dlg.Resize(fyne.NewSize(380, 220))
	dlg.Show()
}

func (mw *MainWindow) showGridDialog() {
	snap := mw.engine.Snapshot()
	presetLabels := make([]string, len(studioGridPresets))
	currentLabel := studioGridPresets[2].label
	for i, p := range studioGridPresets {
		presetLabels[i] = p.label
		if p.grid == snap.Grid {
			currentLabel = p.label
		}
	}
	gridSelect := widget.NewSelect(presetLabels, nil)
	gridSelect.SetSelected(currentLabel)

	dialsSelect := widget.NewSelect([]string{"0", "2", "4", "6", "8"}, nil)
	dialsSelect.SetSelected(strconv.Itoa(snap.Dials))

	items := []*widget.FormItem{
		widget.NewFormItem("Grid Size", gridSelect),
		widget.NewFormItem("Encoder Dials", dialsSelect),
	}

	dlg := dialog.NewForm("Configure Grid & Dials", "Apply", "Cancel", items, func(confirmed bool) {
		if !confirmed {
			return
		}
		grid := snap.Grid
		if idx := gridSelect.SelectedIndex(); idx >= 0 && idx < len(studioGridPresets) {
			grid = studioGridPresets[idx].grid
		}
		dials, err := strconv.Atoi(dialsSelect.Selected)
		if err != nil {
			dials = snap.Dials
		}
		if err := mw.engine.UpdateProfileGrid(snap.ActiveProfileID, grid, dials); err != nil {
			mw.setStatus("Resize grid failed: " + err.Error())
			return
		}
		mw.setStatus(fmt.Sprintf("Grid updated to %dx%d (%d dials)", grid.Cols, grid.Rows, dials))
	}, mw.win)
	dlg.Resize(fyne.NewSize(360, 190))
	dlg.Show()
}
