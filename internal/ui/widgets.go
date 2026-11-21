package ui

import (
	"fmt"
	"image/color"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/widget"

	"github.com/mrpoundsign/PoundDeck/internal/app"
	"github.com/mrpoundsign/PoundDeck/pkg/deck"
)

func parseHexColor(hex string, fallback color.RGBA) color.RGBA {
	s := strings.TrimPrefix(strings.TrimSpace(hex), "#")
	if len(s) != 6 {
		return fallback
	}
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return fallback
	}
	return color.RGBA{
		R: uint8((v >> 16) & 0xff),
		G: uint8((v >> 8) & 0xff),
		B: uint8(v & 0xff),
		A: 255,
	}
}

type deckTileWidget struct {
	widget.BaseWidget
	coords    deck.Coordinates
	title     string
	subtitle  string
	accentHex string
	flash     string
	assigned  bool
	autoBack  bool
	selected  bool
	hovered   bool
	onTapped  func(deck.Coordinates)
}

var (
	_ fyne.Tappable     = (*deckTileWidget)(nil)
	_ desktop.Hoverable = (*deckTileWidget)(nil)
)

func newDeckTileWidget(coords deck.Coordinates, onTapped func(deck.Coordinates)) *deckTileWidget {
	t := &deckTileWidget{
		coords:   coords,
		title:    "Empty",
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

func (t *deckTileWidget) ApplySlot(rendered *app.RenderedSlot, selected bool) {
	if rendered == nil {
		t.title = "Empty"
		t.subtitle = ""
		t.accentHex = ""
		t.flash = ""
		t.assigned = false
		t.autoBack = false
	} else {
		t.title = rendered.Visual.Title
		t.subtitle = rendered.Visual.Subtitle
		t.accentHex = rendered.Visual.Color
		t.flash = rendered.Visual.Flash
		t.assigned = rendered.Config != nil
		t.autoBack = rendered.AutoBack
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

	accentBar := canvas.NewRectangle(ColorSlateBorder)
	accentBar.CornerRadius = 2

	titleTxt := canvas.NewText(t.title, ColorChromeWhite)
	titleTxt.TextSize = 12
	titleTxt.TextStyle = fyne.TextStyle{Bold: true}
	titleTxt.Alignment = fyne.TextAlignCenter

	subTxt := canvas.NewText(t.subtitle, ColorSkyBlue)
	subTxt.TextSize = 12
	subTxt.TextStyle = fyne.TextStyle{Bold: true}
	subTxt.Alignment = fyne.TextAlignCenter

	coordTxt := canvas.NewText(fmt.Sprintf("(%d, %d)", t.coords.Row, t.coords.Col), ColorMutedText)
	coordTxt.TextSize = 10
	coordTxt.Alignment = fyne.TextAlignCenter

	return &deckTileRenderer{
		tile:      t,
		bg:        bg,
		accentBar: accentBar,
		titleTxt:  titleTxt,
		subTxt:    subTxt,
		coordTxt:  coordTxt,
	}
}

type deckTileRenderer struct {
	tile      *deckTileWidget
	bg        *canvas.Rectangle
	accentBar *canvas.Rectangle
	titleTxt  *canvas.Text
	subTxt    *canvas.Text
	coordTxt  *canvas.Text
}

func (r *deckTileRenderer) Layout(size fyne.Size) {
	r.bg.Move(fyne.NewPos(0, 0))
	r.bg.Resize(size)

	barW := max(float32(20), size.Width-24)
	r.accentBar.Move(fyne.NewPos(12, 7))
	r.accentBar.Resize(fyne.NewSize(barW, 3))

	titleSize := r.titleTxt.MinSize()
	coordSize := r.coordTxt.MinSize()

	if r.tile.subtitle != "" {
		subSize := r.subTxt.MinSize()
		totalH := titleSize.Height + 3 + subSize.Height + 4 + coordSize.Height
		startY := max(float32(14), (size.Height-totalH)/2+2)
		r.titleTxt.Resize(fyne.NewSize(size.Width-12, titleSize.Height))
		r.titleTxt.Move(fyne.NewPos(6, startY))
		r.subTxt.Resize(fyne.NewSize(size.Width-12, subSize.Height))
		r.subTxt.Move(fyne.NewPos(6, startY+titleSize.Height+3))
		r.coordTxt.Resize(fyne.NewSize(size.Width-12, coordSize.Height))
		r.coordTxt.Move(fyne.NewPos(6, startY+titleSize.Height+3+subSize.Height+4))
		r.subTxt.Show()
	} else {
		r.subTxt.Hide()
		totalH := titleSize.Height + 4 + coordSize.Height
		startY := max(float32(14), (size.Height-totalH)/2+2)
		r.titleTxt.Resize(fyne.NewSize(size.Width-12, titleSize.Height))
		r.titleTxt.Move(fyne.NewPos(6, startY))
		r.coordTxt.Resize(fyne.NewSize(size.Width-12, coordSize.Height))
		r.coordTxt.Move(fyne.NewPos(6, startY+titleSize.Height+4))
	}
}

func (r *deckTileRenderer) MinSize() fyne.Size {
	return fyne.NewSize(96, 92)
}

func (r *deckTileRenderer) Refresh() {
	r.titleTxt.Text = r.tile.title
	r.subTxt.Text = r.tile.subtitle
	r.coordTxt.Text = fmt.Sprintf("(%d, %d)", r.tile.coords.Row, r.tile.coords.Col)

	accentCol := ColorSlateBorder
	if r.tile.accentHex != "" {
		accentCol = parseHexColor(r.tile.accentHex, ColorSkyBlue)
	} else if r.tile.assigned || r.tile.autoBack {
		accentCol = ColorSkyBlue
	}
	r.accentBar.FillColor = accentCol
	if r.tile.assigned || r.tile.autoBack {
		r.accentBar.Show()
	} else {
		r.accentBar.Hide()
	}

	switch {
	case r.tile.selected:
		r.bg.FillColor = ColorElevatedSlate
		r.bg.StrokeColor = ColorSkyBlue
		r.bg.StrokeWidth = 2.5
		r.titleTxt.Color = ColorChromeWhite
		r.coordTxt.Color = ColorSkyBlue
	case r.tile.flash == "ok":
		r.bg.FillColor = ColorElevatedSlate
		r.bg.StrokeColor = ColorSuccess
		r.bg.StrokeWidth = 2.0
		r.titleTxt.Color = ColorChromeWhite
		r.coordTxt.Color = ColorSecondarySlate
	case r.tile.flash == "alert":
		r.bg.FillColor = ColorElevatedSlate
		r.bg.StrokeColor = ColorError
		r.bg.StrokeWidth = 2.0
		r.titleTxt.Color = ColorChromeWhite
		r.coordTxt.Color = ColorSecondarySlate
	case r.tile.hovered:
		r.bg.FillColor = ColorElevatedSlate
		r.bg.StrokeColor = ColorSecondarySlate
		r.bg.StrokeWidth = 1.5
		r.titleTxt.Color = ColorChromeWhite
		r.coordTxt.Color = ColorSecondarySlate
	case r.tile.assigned || r.tile.autoBack:
		r.bg.FillColor = ColorDarkCharcoal
		r.bg.StrokeColor = accentCol
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

	r.subTxt.Color = accentCol
	r.Layout(r.tile.Size())
	r.bg.Refresh()
	r.accentBar.Refresh()
	r.titleTxt.Refresh()
	r.subTxt.Refresh()
	r.coordTxt.Refresh()
}

func (r *deckTileRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.bg, r.accentBar, r.titleTxt, r.subTxt, r.coordTxt}
}

func (r *deckTileRenderer) Destroy() {}

type dialFaceWidget struct {
	widget.BaseWidget
	index     int
	title     string
	value     string
	accentHex string
	assigned  bool
	selected  bool
	hovered   bool
	onTapped  func(int)
	onScroll  func(int, int)
}

var (
	_ fyne.Tappable     = (*dialFaceWidget)(nil)
	_ fyne.Scrollable   = (*dialFaceWidget)(nil)
	_ desktop.Hoverable = (*dialFaceWidget)(nil)
)

func newDialFaceWidget(index int, onTapped func(int), onScroll func(int, int)) *dialFaceWidget {
	d := &dialFaceWidget{
		index:    index,
		title:    fmt.Sprintf("Dial %d", index+1),
		onTapped: onTapped,
		onScroll: onScroll,
	}
	d.ExtendBaseWidget(d)
	return d
}

func (d *dialFaceWidget) ApplySlot(rendered *app.RenderedSlot, selected bool) {
	if rendered == nil {
		d.title = fmt.Sprintf("Dial %d", d.index+1)
		d.value = "Empty"
		d.accentHex = ""
		d.assigned = false
	} else {
		d.title = rendered.Visual.Title
		d.value = rendered.Visual.Subtitle
		if d.value == "" {
			if raw, ok := rendered.Visual.Feedback["value"]; ok {
				d.value = fmt.Sprint(raw)
			}
		}
		if d.value == "" {
			d.value = "Ready"
		}
		d.accentHex = rendered.Visual.Color
		d.assigned = rendered.Config != nil
	}
	d.selected = selected
	d.Refresh()
}

func (d *dialFaceWidget) Tapped(*fyne.PointEvent) {
	if d.onTapped != nil {
		d.onTapped(d.index)
	}
}

func (d *dialFaceWidget) Scrolled(ev *fyne.ScrollEvent) {
	if d.onScroll == nil || ev.Scrolled.DY == 0 {
		return
	}
	if ev.Scrolled.DY > 0 {
		d.onScroll(d.index, 1)
	} else {
		d.onScroll(d.index, -1)
	}
}

func (d *dialFaceWidget) MouseIn(*desktop.MouseEvent) {
	d.hovered = true
	d.Refresh()
}

func (d *dialFaceWidget) MouseMoved(*desktop.MouseEvent) {}

func (d *dialFaceWidget) MouseOut() {
	d.hovered = false
	d.Refresh()
}

func (d *dialFaceWidget) CreateRenderer() fyne.WidgetRenderer {
	bg := canvas.NewRectangle(ColorDarkCharcoal)
	bg.CornerRadius = 10
	bg.StrokeColor = ColorSlateBorder
	bg.StrokeWidth = 1.5

	ring := canvas.NewCircle(ColorObsidianDeep)
	ring.StrokeColor = ColorSlateBorder
	ring.StrokeWidth = 2.5

	titleTxt := canvas.NewText(d.title, ColorChromeWhite)
	titleTxt.TextSize = 11
	titleTxt.TextStyle = fyne.TextStyle{Bold: true}
	titleTxt.Alignment = fyne.TextAlignCenter

	valTxt := canvas.NewText(d.value, ColorSkyBlue)
	valTxt.TextSize = 11
	valTxt.TextStyle = fyne.TextStyle{Bold: true}
	valTxt.Alignment = fyne.TextAlignCenter

	return &dialFaceRenderer{
		face:     d,
		bg:       bg,
		ring:     ring,
		titleTxt: titleTxt,
		valTxt:   valTxt,
	}
}

type dialFaceRenderer struct {
	face     *dialFaceWidget
	bg       *canvas.Rectangle
	ring     *canvas.Circle
	titleTxt *canvas.Text
	valTxt   *canvas.Text
}

func (r *dialFaceRenderer) Layout(size fyne.Size) {
	r.bg.Move(fyne.NewPos(0, 0))
	r.bg.Resize(size)

	ringSize := float32(30)
	r.ring.Resize(fyne.NewSize(ringSize, ringSize))
	r.ring.Move(fyne.NewPos((size.Width-ringSize)/2, 6))

	titleSize := r.titleTxt.MinSize()
	r.titleTxt.Resize(fyne.NewSize(size.Width-8, titleSize.Height))
	r.titleTxt.Move(fyne.NewPos(4, 40))

	valSize := r.valTxt.MinSize()
	r.valTxt.Resize(fyne.NewSize(size.Width-8, valSize.Height))
	r.valTxt.Move(fyne.NewPos(4, 42+titleSize.Height))
}

func (r *dialFaceRenderer) MinSize() fyne.Size {
	return fyne.NewSize(96, 76)
}

func (r *dialFaceRenderer) Refresh() {
	r.titleTxt.Text = r.face.title
	r.valTxt.Text = r.face.value

	ringColor := ColorSlateBorder
	if r.face.accentHex != "" {
		ringColor = parseHexColor(r.face.accentHex, ColorSkyBlue)
	} else if r.face.assigned {
		ringColor = ColorSkyBlue
	}
	r.ring.StrokeColor = ringColor

	switch {
	case r.face.selected:
		r.bg.FillColor = ColorElevatedSlate
		r.bg.StrokeColor = ColorSkyBlue
		r.bg.StrokeWidth = 2.5
	case r.face.hovered:
		r.bg.FillColor = ColorElevatedSlate
		r.bg.StrokeColor = ColorSecondarySlate
		r.bg.StrokeWidth = 1.5
	default:
		r.bg.FillColor = ColorDarkCharcoal
		r.bg.StrokeColor = ColorSlateBorder
		r.bg.StrokeWidth = 1.5
	}

	if r.face.assigned {
		r.titleTxt.Color = ColorChromeWhite
		r.valTxt.Color = ringColor
	} else {
		r.titleTxt.Color = ColorSecondarySlate
		r.valTxt.Color = ColorMutedText
	}

	r.bg.Refresh()
	r.ring.Refresh()
	r.titleTxt.Refresh()
	r.valTxt.Refresh()
}

func (r *dialFaceRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.bg, r.ring, r.titleTxt, r.valTxt}
}

func (r *dialFaceRenderer) Destroy() {}

type dialControl struct {
	root     fyne.CanvasObject
	face     *dialFaceWidget
	minusBtn *widget.Button
	pressBtn *widget.Button
	plusBtn  *widget.Button
}

type deckSurfaceView struct {
	root       *fyne.Container
	gridHost   *fyne.Container
	dialTitle  *canvas.Text
	dialsHost  *fyne.Container
	tiles      map[string]*deckTileWidget
	dials      []*dialControl
	curGrid    deck.GridSize
	curDialCnt int

	onKeyTap    func(deck.Coordinates)
	onDialTap   func(int)
	onDialDelta func(int, int)
	onDialPress func(int)
}

func newDeckSurfaceView(
	onKeyTap func(deck.Coordinates),
	onDialTap func(int),
	onDialDelta func(int, int),
	onDialPress func(int),
) *deckSurfaceView {
	gridHost := container.NewStack()
	dialTitle := canvas.NewText("Virtual Dials (Encoders)", ColorSecondarySlate)
	dialTitle.TextSize = 12
	dialTitle.TextStyle = fyne.TextStyle{Bold: true}
	dialsHost := container.NewStack()

	root := container.NewVBox(gridHost, dialTitle, dialsHost)
	return &deckSurfaceView{
		root:        root,
		gridHost:    gridHost,
		dialTitle:   dialTitle,
		dialsHost:   dialsHost,
		tiles:       make(map[string]*deckTileWidget),
		onKeyTap:    onKeyTap,
		onDialTap:   onDialTap,
		onDialDelta: onDialDelta,
		onDialPress: onDialPress,
	}
}

func (v *deckSurfaceView) Object() fyne.CanvasObject {
	return v.root
}

func (v *deckSurfaceView) ApplySnapshot(
	snap app.Snapshot,
	hasSelection bool,
	selCtrl deck.ControllerType,
	selKey deck.Coordinates,
	selDial int,
) {
	if snap.Grid != v.curGrid || snap.Dials != v.curDialCnt {
		v.rebuildGeometry(snap.Grid, snap.Dials)
	}

	for row := range snap.Grid.Rows {
		for col := range snap.Grid.Cols {
			c := deck.Coordinates{Row: row, Col: col}
			key := c.SlotKey()
			tile := v.tiles[key]
			if tile == nil {
				continue
			}
			var rPtr *app.RenderedSlot
			if r, ok := snap.Slots[key]; ok {
				rcopy := r
				rPtr = &rcopy
			}
			selected := hasSelection && selCtrl == deck.ControllerKeypad && selKey == c
			tile.ApplySlot(rPtr, selected)
		}
	}

	for i, dc := range v.dials {
		var rPtr *app.RenderedSlot
		if r, ok := snap.DialSlots[i]; ok {
			rcopy := r
			rPtr = &rcopy
		}
		selected := hasSelection && selCtrl == deck.ControllerEncoder && selDial == i
		dc.face.ApplySlot(rPtr, selected)
	}
}

func (v *deckSurfaceView) rebuildGeometry(grid deck.GridSize, dials int) {
	v.curGrid = grid
	v.curDialCnt = dials
	clear(v.tiles)

	gridBox := container.NewGridWithColumns(max(1, grid.Cols))
	for row := range grid.Rows {
		for col := range grid.Cols {
			c := deck.Coordinates{Row: row, Col: col}
			tile := newDeckTileWidget(c, v.onKeyTap)
			v.tiles[c.SlotKey()] = tile
			gridBox.Add(tile)
		}
	}
	v.gridHost.Objects = []fyne.CanvasObject{gridBox}
	v.gridHost.Refresh()

	if dials <= 0 {
		v.dials = nil
		v.dialsHost.Objects = nil
		v.dialTitle.Hide()
		v.dialsHost.Hide()
		v.dialsHost.Refresh()
		return
	}

	v.dialTitle.Show()
	v.dialsHost.Show()
	cols := min(dials, 4)
	dialsGrid := container.NewGridWithColumns(cols)
	v.dials = make([]*dialControl, 0, dials)
	for i := range dials {
		idx := i
		face := newDialFaceWidget(idx, v.onDialTap, v.onDialDelta)
		minusBtn := widget.NewButton("-", func() {
			if v.onDialDelta != nil {
				v.onDialDelta(idx, -1)
			}
		})
		pressBtn := widget.NewButton("●", func() {
			if v.onDialPress != nil {
				v.onDialPress(idx)
			}
		})
		plusBtn := widget.NewButton("+", func() {
			if v.onDialDelta != nil {
				v.onDialDelta(idx, 1)
			}
		})
		btnRow := container.NewGridWithColumns(3, minusBtn, pressBtn, plusBtn)
		card := container.NewVBox(face, btnRow)
		v.dials = append(v.dials, &dialControl{
			root:     card,
			face:     face,
			minusBtn: minusBtn,
			pressBtn: pressBtn,
			plusBtn:  plusBtn,
		})
		dialsGrid.Add(card)
	}
	v.dialsHost.Objects = []fyne.CanvasObject{dialsGrid}
	v.dialsHost.Refresh()
}
