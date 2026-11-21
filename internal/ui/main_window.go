package ui

import (
	"fmt"
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/mrpoundsign/PoundDeck/internal/version"
	"github.com/mrpoundsign/PoundDeck/pkg/deck"
)

// dispatchUI marshals UI state updates onto the Fyne main thread.
// Tests can override this to inspect or queue async UI callbacks.
var dispatchUI = fyne.Do

type deckTileWidget struct {
	widget.BaseWidget
	coords   deck.Coordinates
	title    string
	selected bool
	hovered  bool
	onTapped func(deck.Coordinates)
}

var (
	_ fyne.Tappable     = (*deckTileWidget)(nil)
	_ desktop.Hoverable = (*deckTileWidget)(nil)
)

func newDeckTileWidget(coords deck.Coordinates, title string, onTapped func(deck.Coordinates)) *deckTileWidget {
	t := &deckTileWidget{
		coords:   coords,
		title:    title,
		onTapped: onTapped,
	}
	t.ExtendBaseWidget(t)
	return t
}

func (t *deckTileWidget) SetSelected(selected bool) {
	if t.selected == selected {
		return
	}
	t.selected = selected
	t.Refresh()
}

func (t *deckTileWidget) Tapped(*fyne.PointEvent) {
	if t.onTapped != nil {
		t.onTapped(t.coords)
	}
}

func (t *deckTileWidget) MouseIn(*desktop.MouseEvent) {
	t.hovered = true
	t.Refresh()
}

func (t *deckTileWidget) MouseMoved(*desktop.MouseEvent) {}

func (t *deckTileWidget) MouseOut() {
	t.hovered = false
	t.Refresh()
}

func (t *deckTileWidget) CreateRenderer() fyne.WidgetRenderer {
	bg := canvas.NewRectangle(ColorDarkCharcoal)
	bg.CornerRadius = 10
	bg.StrokeColor = ColorSlateBorder
	bg.StrokeWidth = 1.5

	titleTxt := canvas.NewText(t.title, ColorChromeWhite)
	titleTxt.TextSize = 12
	titleTxt.TextStyle = fyne.TextStyle{Bold: true}
	titleTxt.Alignment = fyne.TextAlignCenter

	coordTxt := canvas.NewText(fmt.Sprintf("(%d, %d)", t.coords.Row, t.coords.Col), ColorMutedText)
	coordTxt.TextSize = 10
	coordTxt.Alignment = fyne.TextAlignCenter

	return &deckTileRenderer{
		tile:     t,
		bg:       bg,
		titleTxt: titleTxt,
		coordTxt: coordTxt,
	}
}

type deckTileRenderer struct {
	tile     *deckTileWidget
	bg       *canvas.Rectangle
	titleTxt *canvas.Text
	coordTxt *canvas.Text
}

func (r *deckTileRenderer) Layout(size fyne.Size) {
	r.bg.Move(fyne.NewPos(0, 0))
	r.bg.Resize(size)

	titleSize := r.titleTxt.MinSize()
	coordSize := r.coordTxt.MinSize()
	totalH := titleSize.Height + 4 + coordSize.Height
	startY := max(float32(0), (size.Height-totalH)/2)

	r.titleTxt.Resize(fyne.NewSize(size.Width-12, titleSize.Height))
	r.titleTxt.Move(fyne.NewPos(6, startY))

	r.coordTxt.Resize(fyne.NewSize(size.Width-12, coordSize.Height))
	r.coordTxt.Move(fyne.NewPos(6, startY+titleSize.Height+4))
}

func (r *deckTileRenderer) MinSize() fyne.Size {
	return fyne.NewSize(92, 92)
}

func (r *deckTileRenderer) Refresh() {
	r.titleTxt.Text = r.tile.title
	r.coordTxt.Text = fmt.Sprintf("(%d, %d)", r.tile.coords.Row, r.tile.coords.Col)

	switch {
	case r.tile.selected:
		r.bg.FillColor = ColorElevatedSlate
		r.bg.StrokeColor = ColorSkyBlue
		r.bg.StrokeWidth = 2.5
		r.titleTxt.Color = ColorChromeWhite
		r.coordTxt.Color = ColorSkyBlue
	case r.tile.hovered:
		r.bg.FillColor = ColorElevatedSlate
		r.bg.StrokeColor = ColorSecondarySlate
		r.bg.StrokeWidth = 1.5
		r.titleTxt.Color = ColorChromeWhite
		r.coordTxt.Color = ColorSecondarySlate
	default:
		r.bg.FillColor = ColorDarkCharcoal
		r.bg.StrokeColor = ColorSlateBorder
		r.bg.StrokeWidth = 1.5
		r.titleTxt.Color = ColorSecondarySlate
		r.coordTxt.Color = ColorMutedText
	}

	r.bg.Refresh()
	r.titleTxt.Refresh()
	r.coordTxt.Refresh()
}

func (r *deckTileRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.bg, r.titleTxt, r.coordTxt}
}

func (r *deckTileRenderer) Destroy() {}

// MainWindow hosts the PoundDeck desktop studio and virtual macro deck grid.
type MainWindow struct {
	app          fyne.App
	win          fyne.Window
	grid         deck.GridSize
	selectedSlot deck.Coordinates
	tiles        map[string]*deckTileWidget

	profileSelect    *widget.Select
	pageLabel        *widget.Label
	inspectorSlotLbl *widget.Label
	inspectorTypeLbl *widget.Label
	inspectorHintLbl *widget.Label
	statusBarLbl     *widget.Label
}

// NewMainWindow constructs and initializes the PoundDeck studio window.
func NewMainWindow(a fyne.App) *MainWindow {
	win := a.NewWindow(version.AppName)
	win.Resize(fyne.NewSize(1180, 740))

	grid := deck.DefaultGrid()
	mw := &MainWindow{
		app:          a,
		win:          win,
		grid:         grid,
		selectedSlot: deck.Coordinates{Row: 0, Col: 0},
		tiles:        make(map[string]*deckTileWidget, grid.TotalSlots()),
	}

	header := mw.buildHeader()
	catalog := mw.buildActionCatalog()
	center := mw.buildDeckCenter()
	inspector := mw.buildInspector()
	statusBar := mw.buildStatusBar()

	splitRight := container.NewHSplit(center, inspector)
	splitRight.SetOffset(0.68)

	splitMain := container.NewHSplit(catalog, splitRight)
	splitMain.SetOffset(0.22)

	root := container.NewBorder(header, statusBar, nil, nil, splitMain)
	win.SetContent(root)
	mw.SelectSlot(deck.Coordinates{Row: 0, Col: 0})

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

// SelectedSlot returns the coordinates of the currently focused keypad slot.
func (mw *MainWindow) SelectedSlot() deck.Coordinates {
	return mw.selectedSlot
}

// SelectSlot highlights the specified keypad coordinates and updates the inspector and status bar.
// Out-of-bounds coordinates are ignored.
func (mw *MainWindow) SelectSlot(c deck.Coordinates) {
	if !mw.grid.Contains(c) {
		return
	}
	mw.selectedSlot = c
	for key, tile := range mw.tiles {
		tile.SetSelected(key == c.SlotKey())
	}
	mw.syncSelectionLabels()
}

func (mw *MainWindow) syncSelectionLabels() {
	if mw.inspectorSlotLbl != nil {
		mw.inspectorSlotLbl.SetText(fmt.Sprintf("Slot: Row %d, Col %d", mw.selectedSlot.Row, mw.selectedSlot.Col))
	}
	if mw.statusBarLbl != nil {
		mw.statusBarLbl.SetText(fmt.Sprintf(
			"Ready | Selected: (%d, %d) | Grid: %dx%d | Plugins: 0 | Web Deck: Stopped",
			mw.selectedSlot.Row,
			mw.selectedSlot.Col,
			mw.grid.Cols,
			mw.grid.Rows,
		))
	}
}

func (mw *MainWindow) buildHeader() fyne.CanvasObject {
	title := canvas.NewText(version.AppName, ColorChromeWhite)
	title.TextSize = 18
	title.TextStyle = fyne.TextStyle{Bold: true}

	ver := canvas.NewText("v"+version.Version, ColorSkyBlue)
	ver.TextSize = 12
	ver.TextStyle = fyne.TextStyle{Bold: true}

	mw.profileSelect = widget.NewSelect([]string{"Default Profile"}, nil)
	mw.profileSelect.SetSelected("Default Profile")

	mw.pageLabel = widget.NewLabel("Page 1 / 1")

	pluginsBadge := canvas.NewText("Plugins: 0", ColorSecondarySlate)
	pluginsBadge.TextSize = 11

	webBadge := canvas.NewText("Web Deck: Off", ColorMutedText)
	webBadge.TextSize = 11

	row := container.NewHBox(
		title,
		ver,
		widget.NewSeparator(),
		widget.NewLabel("Profile:"),
		mw.profileSelect,
		mw.pageLabel,
		layout.NewSpacer(),
		pluginsBadge,
		widget.NewSeparator(),
		webBadge,
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
	sub := widget.NewLabel("Built-in & Dynamic Providers")

	type catalogGroup struct {
		title string
		items []string
	}
	groups := []catalogGroup{
		{title: "Core Navigation", items: []string{"Folder / Subpage", "Next / Previous Page", "Switch Profile", "Multi-Action"}},
		{title: "OBS Studio (WS v5)", items: []string{"Switch Scene", "Toggle Source", "Audio Mute / Volume", "Stream / Record / Replay"}},
		{title: "App & System Control", items: []string{"Launch App / Script", "Open URL", "HTTP / WS Webhook", "Hotkey / Media Control"}},
		{title: "Stream Deck (.sdPlugin)", items: []string{"SDK v2/v3 Host (Milestone 4)"}},
		{title: "Wasm Plugins (wazero)", items: []string{"Go / Rust / Zig / C++ (Milestone 5)"}},
	}

	list := container.NewVBox()
	for _, g := range groups {
		groupTitle := canvas.NewText(g.title, ColorSkyBlue)
		groupTitle.TextSize = 12
		groupTitle.TextStyle = fyne.TextStyle{Bold: true}
		list.Add(groupTitle)

		for _, item := range g.items {
			rowLbl := widget.NewLabel("  • " + item)
			list.Add(rowLbl)
		}
		list.Add(widget.NewSeparator())
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
	deckTitle := canvas.NewText("Virtual Deck (5×3)", ColorChromeWhite)
	deckTitle.TextSize = 14
	deckTitle.TextStyle = fyne.TextStyle{Bold: true}

	deckHint := canvas.NewText("Click any slot to inspect or bind actions", ColorSecondarySlate)
	deckHint.TextSize = 11

	gridObj := container.NewGridWithColumns(mw.grid.Cols)
	for row := range mw.grid.Rows {
		for col := range mw.grid.Cols {
			c := deck.Coordinates{Row: row, Col: col}
			tile := newDeckTileWidget(c, "Empty", func(coords deck.Coordinates) {
				mw.SelectSlot(coords)
			})
			mw.tiles[c.SlotKey()] = tile
			gridObj.Add(tile)
		}
	}

	// Virtual Encoder Dial placeholders foreshadowing Stream Deck+ / Encoder action support.
	dialTitle := canvas.NewText("Virtual Dials (Encoders)", ColorSecondarySlate)
	dialTitle.TextSize = 12
	dialTitle.TextStyle = fyne.TextStyle{Bold: true}

	dialsRow := container.NewGridWithColumns(4)
	for i := range 4 {
		dialsRow.Add(newDialPlaceholderCard(fmt.Sprintf("Dial %d", i+1)))
	}

	padFrameBg := canvas.NewRectangle(color.RGBA{R: 15, G: 17, B: 21, A: 255})
	padFrameBg.CornerRadius = 14
	padFrameBg.StrokeColor = ColorElevatedSlate
	padFrameBg.StrokeWidth = 2

	padBody := container.NewVBox(
		container.NewHBox(deckTitle, layout.NewSpacer(), deckHint),
		widget.NewSeparator(),
		gridObj,
		widget.NewSeparator(),
		dialTitle,
		dialsRow,
	)

	centered := container.NewCenter(
		container.NewStack(padFrameBg, container.NewPadded(container.NewPadded(padBody))),
	)

	canvasBg := canvas.NewRectangle(ColorObsidianSlate)
	return container.NewStack(canvasBg, container.NewScroll(centered))
}

func newDialPlaceholderCard(label string) fyne.CanvasObject {
	cardBg := canvas.NewRectangle(ColorDarkCharcoal)
	cardBg.CornerRadius = 10
	cardBg.StrokeColor = ColorSlateBorder
	cardBg.StrokeWidth = 1

	ring := canvas.NewCircle(ColorObsidianDeep)
	ring.StrokeColor = ColorSlateBorder
	ring.StrokeWidth = 2
	ringBox := container.NewGridWrap(fyne.NewSize(34, 34), ring)

	txt := canvas.NewText(label, ColorMutedText)
	txt.TextSize = 11
	txt.Alignment = fyne.TextAlignCenter

	return container.NewStack(
		cardBg,
		container.NewPadded(container.NewVBox(
			container.NewCenter(ringBox),
			txt,
		)),
	)
}

func (mw *MainWindow) buildInspector() fyne.CanvasObject {
	heading := widget.NewLabelWithStyle("Action Inspector", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})

	mw.inspectorSlotLbl = widget.NewLabel("Slot: Row 0, Col 0")
	mw.inspectorTypeLbl = widget.NewLabel("Controller: " + string(deck.ControllerKeypad))
	mw.inspectorHintLbl = widget.NewLabel(
		"No action assigned to this slot yet.\n\n" +
			"Action binding, custom titles/icons, profile persistence, and native/HTML Property Inspectors " +
			"wire up starting in Milestone 1.",
	)
	mw.inspectorHintLbl.Wrapping = fyne.TextWrapWord

	panelBg := canvas.NewRectangle(ColorDarkCharcoal)
	content := container.NewVBox(
		heading,
		widget.NewSeparator(),
		mw.inspectorSlotLbl,
		mw.inspectorTypeLbl,
		widget.NewSeparator(),
		mw.inspectorHintLbl,
		layout.NewSpacer(),
		widget.NewButtonWithIcon("Assign Action (Coming in M1)", theme.SettingsIcon(), nil),
	)
	// Disable placeholder CTA until Milestone 1 action assignment is implemented.
	if btn, ok := content.Objects[len(content.Objects)-1].(*widget.Button); ok {
		btn.Disable()
	}

	return container.NewStack(panelBg, container.NewPadded(content))
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

// RunOnUI invokes fn on the main Fyne thread via dispatchUI.
func RunOnUI(fn func()) {
	dispatchUI(fn)
}
