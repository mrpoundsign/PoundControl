package ui

import (
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"math"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/widget"

	"github.com/mrpoundsign/PoundDeck/internal/app"
	"github.com/mrpoundsign/PoundDeck/pkg/deck"
	"github.com/mrpoundsign/PoundDeck/pkg/tile"
)

var sharedTileCache = tile.NewCache(256)

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

const dragActivationThresholdPx = 10

func widgetAbsolutePos(obj fyne.CanvasObject, local, fallbackAbs fyne.Position) fyne.Position {
	if !fallbackAbs.IsZero() {
		return fallbackAbs
	}
	if curApp := fyne.CurrentApp(); curApp != nil {
		if drv := curApp.Driver(); drv != nil {
			return drv.AbsolutePositionForObject(obj).Add(local)
		}
	}
	return local
}

type deckTileWidget struct {
	widget.BaseWidget
	coords         deck.Coordinates
	title          string
	subtitle       string
	hotkey         string
	accentHex      string
	flash          string
	imageURI       string
	decodedImg     image.Image
	assigned       bool
	autoBack       bool
	selected   bool
	hovered    bool
	dropTarget bool
	moveSource bool

	dragAccumX  float32
	dragAccumY  float32
	dragActive  bool
	dragLastAbs fyne.Position

	canDrag        func() bool
	onTapped       func(deck.Coordinates)
	onSecondaryTap func(deck.Coordinates, fyne.Position)
	onDragMove     func(deck.Coordinates, fyne.Position)
	onDragEnd      func(deck.Coordinates, fyne.Position)
}

var (
	_ fyne.Tappable          = (*deckTileWidget)(nil)
	_ fyne.SecondaryTappable = (*deckTileWidget)(nil)
	_ fyne.Draggable         = (*deckTileWidget)(nil)
	_ desktop.Hoverable      = (*deckTileWidget)(nil)
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
		t.hotkey = ""
		t.accentHex = ""
		t.flash = ""
		t.imageURI = ""
		t.decodedImg = nil
		t.assigned = false
		t.autoBack = false
	} else {
		t.title = rendered.Visual.Title
		t.subtitle = rendered.Visual.Subtitle
		if rendered.Config != nil {
			t.hotkey = rendered.Config.Hotkey
		} else {
			t.hotkey = ""
		}
		t.accentHex = rendered.Visual.Color
		t.flash = rendered.Visual.Flash
		uri := strings.TrimSpace(rendered.Visual.ImageURI)
		if uri != t.imageURI {
			t.imageURI = uri
			if uri == "" {
				t.decodedImg = nil
			} else if decoded, err := sharedTileCache.GetOrDecode(uri, 144); err == nil {
				t.decodedImg = decoded
			} else {
				t.decodedImg = nil
			}
		}
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

func (t *deckTileWidget) TappedSecondary(ev *fyne.PointEvent) {
	if t.onSecondaryTap != nil {
		t.onSecondaryTap(t.coords, widgetAbsolutePos(t, ev.Position, ev.AbsolutePosition))
	}
}

func (t *deckTileWidget) Dragged(ev *fyne.DragEvent) {
	if t.onDragMove == nil {
		return
	}
	abs := widgetAbsolutePos(t, ev.Position, ev.AbsolutePosition)
	t.dragLastAbs = abs
	if !t.dragActive {
		if !t.assigned || t.autoBack || (t.canDrag != nil && !t.canDrag()) {
			return
		}
		t.dragAccumX += ev.Dragged.DX
		t.dragAccumY += ev.Dragged.DY
		if math.Hypot(float64(t.dragAccumX), float64(t.dragAccumY)) < dragActivationThresholdPx {
			return
		}
		t.dragActive = true
	}
	t.onDragMove(t.coords, abs)
}

func (t *deckTileWidget) DragEnd() {
	wasActive := t.dragActive
	abs := t.dragLastAbs
	t.dragAccumX = 0
	t.dragAccumY = 0
	t.dragActive = false
	if wasActive && t.onDragEnd != nil {
		t.onDragEnd(t.coords, abs)
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

	artImg := canvas.NewImageFromImage(t.decodedImg)
	artImg.FillMode = canvas.ImageFillContain
	artImg.ScaleMode = canvas.ImageScaleSmooth
	artImg.SetMinSize(fyne.NewSize(0, 0))
	if t.decodedImg == nil {
		artImg.Hide()
	}

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
		tile:         t,
		bg:           bg,
		accentBar:    accentBar,
		artImg:       artImg,
		titleTxt:     titleTxt,
		subTxt:       subTxt,
		coordTxt:     coordTxt,
		lastImageURI: t.imageURI,
		hasArt:       t.decodedImg != nil,
	}
}

type deckTileRenderer struct {
	tile         *deckTileWidget
	bg           *canvas.Rectangle
	accentBar    *canvas.Rectangle
	artImg       *canvas.Image
	titleTxt     *canvas.Text
	subTxt       *canvas.Text
	coordTxt     *canvas.Text
	lastImageURI string
	hasArt       bool
}

func (r *deckTileRenderer) Layout(size fyne.Size) {
	r.bg.Move(fyne.NewPos(0, 0))
	r.bg.Resize(size)

	barW := max(float32(20), size.Width-24)
	r.accentBar.Move(fyne.NewPos(12, 7))
	r.accentBar.Resize(fyne.NewSize(barW, 3))

	hasArt := r.tile.decodedImg != nil
	fullArt := hasArt && strings.TrimSpace(r.tile.title) == "" && strings.TrimSpace(r.tile.subtitle) == ""
	if fullArt {
		r.accentBar.Hide()
		r.titleTxt.Hide()
		r.subTxt.Hide()
		r.coordTxt.Hide()
		pad := float32(6)
		r.artImg.Move(fyne.NewPos(pad, pad))
		r.artImg.Resize(fyne.NewSize(max(float32(16), size.Width-pad*2), max(float32(16), size.Height-pad*2)))
		r.artImg.Show()
		return
	}

	r.titleTxt.Show()
	r.coordTxt.Show()
	titleSize := r.titleTxt.MinSize()
	coordSize := r.coordTxt.MinSize()

	if hasArt {
		iconRatio := float32(0.36)
		if r.tile.subtitle != "" {
			iconRatio = 0.28
		}
		iconSize := min(float32(34), max(float32(20), size.Height*iconRatio))
		r.artImg.Move(fyne.NewPos((size.Width-iconSize)/2, 13))
		r.artImg.Resize(fyne.NewSize(iconSize, iconSize))
		r.artImg.Show()

		startY := 13 + iconSize + 3
		if r.tile.subtitle != "" {
			subSize := r.subTxt.MinSize()
			r.titleTxt.Resize(fyne.NewSize(size.Width-12, titleSize.Height))
			r.titleTxt.Move(fyne.NewPos(6, startY))
			r.subTxt.Resize(fyne.NewSize(size.Width-12, subSize.Height))
			r.subTxt.Move(fyne.NewPos(6, startY+titleSize.Height+2))
			r.coordTxt.Resize(fyne.NewSize(size.Width-12, coordSize.Height))
			r.coordTxt.Move(fyne.NewPos(6, startY+titleSize.Height+2+subSize.Height+2))
			r.subTxt.Show()
		} else {
			r.subTxt.Hide()
			r.titleTxt.Resize(fyne.NewSize(size.Width-12, titleSize.Height))
			r.titleTxt.Move(fyne.NewPos(6, startY))
			r.coordTxt.Resize(fyne.NewSize(size.Width-12, coordSize.Height))
			r.coordTxt.Move(fyne.NewPos(6, startY+titleSize.Height+2))
		}
		return
	}

	r.artImg.Hide()
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
	coordText := fmt.Sprintf("(%d, %d)", r.tile.coords.Row, r.tile.coords.Col)
	if r.tile.hotkey != "" {
		coordText += " • " + r.tile.hotkey
	}
	r.coordTxt.Text = coordText

	hasDecoded := r.tile.decodedImg != nil
	if r.lastImageURI != r.tile.imageURI || r.hasArt != hasDecoded {
		r.artImg.Image = r.tile.decodedImg
		r.lastImageURI = r.tile.imageURI
		r.hasArt = hasDecoded
	}

	fullArt := hasDecoded && strings.TrimSpace(r.tile.title) == "" && strings.TrimSpace(r.tile.subtitle) == ""

	accentCol := ColorSlateBorder
	if r.tile.accentHex != "" {
		accentCol = parseHexColor(r.tile.accentHex, ColorSkyBlue)
	} else if r.tile.assigned || r.tile.autoBack {
		accentCol = ColorSkyBlue
	}
	r.accentBar.FillColor = accentCol
	if !fullArt && (r.tile.assigned || r.tile.autoBack) {
		r.accentBar.Show()
	} else {
		r.accentBar.Hide()
	}

	switch {
	case r.tile.dropTarget:
		r.bg.FillColor = ColorElevatedSlate
		r.bg.StrokeColor = ColorSkyBlue
		r.bg.StrokeWidth = 3.0
		r.titleTxt.Color = ColorChromeWhite
		r.coordTxt.Color = ColorSkyBlue
	case r.tile.moveSource:
		r.bg.FillColor = ColorElevatedSlate
		r.bg.StrokeColor = ColorWarning
		r.bg.StrokeWidth = 2.5
		r.titleTxt.Color = ColorChromeWhite
		r.coordTxt.Color = ColorWarning
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
	r.artImg.Refresh()
	r.titleTxt.Refresh()
	r.subTxt.Refresh()
	r.coordTxt.Refresh()
}

func (r *deckTileRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.bg, r.accentBar, r.artImg, r.titleTxt, r.subTxt, r.coordTxt}
}

func (r *deckTileRenderer) Destroy() {}

type dialFaceWidget struct {
	widget.BaseWidget
	index          int
	title          string
	value          string
	accentHex      string
	hasIndicator   bool
	indicatorPct   float64
	assigned     bool
	selected     bool
	hovered      bool
	dropTarget   bool
	moveSource   bool

	dragAccumX  float32
	dragAccumY  float32
	dragActive  bool
	dragLastAbs fyne.Position

	canDrag        func() bool
	onTapped       func(int)
	onScroll       func(int, int)
	onSecondaryTap func(int, fyne.Position)
	onDragMove     func(int, fyne.Position)
	onDragEnd      func(int, fyne.Position)
}

var (
	_ fyne.Tappable          = (*dialFaceWidget)(nil)
	_ fyne.SecondaryTappable = (*dialFaceWidget)(nil)
	_ fyne.Draggable         = (*dialFaceWidget)(nil)
	_ fyne.Scrollable        = (*dialFaceWidget)(nil)
	_ desktop.Hoverable      = (*dialFaceWidget)(nil)
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
		d.hasIndicator = false
		d.indicatorPct = 0
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
		d.hasIndicator = false
		d.indicatorPct = 0
		if rawInd, ok := rendered.Visual.Feedback["indicator"]; ok {
			if pct, valid := parseDialIndicator(rawInd); valid {
				d.hasIndicator = true
				d.indicatorPct = pct
			}
		}
		d.accentHex = rendered.Visual.Color
		d.assigned = rendered.Config != nil
	}
	d.selected = selected
	d.Refresh()
}

func parseDialIndicator(raw any) (float64, bool) {
	if m, ok := raw.(map[string]any); ok {
		if enabled, ok := m["enabled"].(bool); ok && !enabled {
			return 0, false
		}
		raw = m["value"]
	}
	var val float64
	switch v := raw.(type) {
	case float64:
		val = v
	case float32:
		val = float64(v)
	case int:
		val = float64(v)
	case int64:
		val = float64(v)
	case json.Number:
		parsed, err := v.Float64()
		if err != nil {
			return 0, false
		}
		val = parsed
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(v, "%")), 64)
		if err != nil {
			return 0, false
		}
		val = parsed
	default:
		return 0, false
	}
	if val < 0 {
		val = 0
	}
	if val > 100 {
		val = 100
	}
	return val, true
}

func (d *dialFaceWidget) Tapped(*fyne.PointEvent) {
	if d.onTapped != nil {
		d.onTapped(d.index)
	}
}

func (d *dialFaceWidget) TappedSecondary(ev *fyne.PointEvent) {
	if d.onSecondaryTap != nil {
		d.onSecondaryTap(d.index, widgetAbsolutePos(d, ev.Position, ev.AbsolutePosition))
	}
}

func (d *dialFaceWidget) Dragged(ev *fyne.DragEvent) {
	if d.onDragMove == nil {
		return
	}
	abs := widgetAbsolutePos(d, ev.Position, ev.AbsolutePosition)
	d.dragLastAbs = abs
	if !d.dragActive {
		if !d.assigned || (d.canDrag != nil && !d.canDrag()) {
			return
		}
		d.dragAccumX += ev.Dragged.DX
		d.dragAccumY += ev.Dragged.DY
		if math.Hypot(float64(d.dragAccumX), float64(d.dragAccumY)) < dragActivationThresholdPx {
			return
		}
		d.dragActive = true
	}
	d.onDragMove(d.index, abs)
}

func (d *dialFaceWidget) DragEnd() {
	wasActive := d.dragActive
	abs := d.dragLastAbs
	d.dragAccumX = 0
	d.dragAccumY = 0
	d.dragActive = false
	if wasActive && d.onDragEnd != nil {
		d.onDragEnd(d.index, abs)
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

	indBg := canvas.NewRectangle(ColorObsidianDeep)
	indBg.CornerRadius = 2
	indBg.Hide()

	indFill := canvas.NewRectangle(ColorSkyBlue)
	indFill.CornerRadius = 2
	indFill.Hide()

	return &dialFaceRenderer{
		face:     d,
		bg:       bg,
		ring:     ring,
		titleTxt: titleTxt,
		valTxt:   valTxt,
		indBg:    indBg,
		indFill:  indFill,
	}
}

type dialFaceRenderer struct {
	face     *dialFaceWidget
	bg       *canvas.Rectangle
	ring     *canvas.Circle
	titleTxt *canvas.Text
	valTxt   *canvas.Text
	indBg    *canvas.Rectangle
	indFill  *canvas.Rectangle
}

func (r *dialFaceRenderer) Layout(size fyne.Size) {
	r.bg.Move(fyne.NewPos(0, 0))
	r.bg.Resize(size)

	ringSize := float32(30)
	ringY := float32(6)
	titleY := float32(40)
	if r.face.hasIndicator {
		ringSize = 24
		ringY = 5
		titleY = 32
	}
	r.ring.Resize(fyne.NewSize(ringSize, ringSize))
	r.ring.Move(fyne.NewPos((size.Width-ringSize)/2, ringY))

	titleSize := r.titleTxt.MinSize()
	r.titleTxt.Resize(fyne.NewSize(size.Width-8, titleSize.Height))
	r.titleTxt.Move(fyne.NewPos(4, titleY))

	valSize := r.valTxt.MinSize()
	r.valTxt.Resize(fyne.NewSize(size.Width-8, valSize.Height))
	r.valTxt.Move(fyne.NewPos(4, titleY+2+titleSize.Height))

	if r.face.hasIndicator {
		barW := max(float32(16), size.Width-20)
		barY := size.Height - 9
		r.indBg.Move(fyne.NewPos(10, barY))
		r.indBg.Resize(fyne.NewSize(barW, 4))
		r.indBg.Show()

		fillW := float32(r.face.indicatorPct/100.0) * barW
		if fillW < 0 {
			fillW = 0
		}
		if fillW > barW {
			fillW = barW
		}
		r.indFill.Move(fyne.NewPos(10, barY))
		r.indFill.Resize(fyne.NewSize(fillW, 4))
		if fillW > 0 {
			r.indFill.Show()
		} else {
			r.indFill.Hide()
		}
	} else {
		r.indBg.Hide()
		r.indFill.Hide()
	}
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
	r.indFill.FillColor = ringColor

	switch {
	case r.face.dropTarget:
		r.bg.FillColor = ColorElevatedSlate
		r.bg.StrokeColor = ColorSkyBlue
		r.bg.StrokeWidth = 3.0
	case r.face.moveSource:
		r.bg.FillColor = ColorElevatedSlate
		r.bg.StrokeColor = ColorWarning
		r.bg.StrokeWidth = 2.5
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

	r.Layout(r.face.Size())
	r.bg.Refresh()
	r.ring.Refresh()
	r.titleTxt.Refresh()
	r.valTxt.Refresh()
	r.indBg.Refresh()
	r.indFill.Refresh()
}

func (r *dialFaceRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.bg, r.ring, r.titleTxt, r.valTxt, r.indBg, r.indFill}
}

func (r *dialFaceRenderer) Destroy() {}

type dialControl struct {
	root     fyne.CanvasObject
	face     *dialFaceWidget
	minusBtn *widget.Button
	pressBtn *widget.Button
	plusBtn  *widget.Button
}

type surfaceEditHandlers struct {
	canEdit         func() bool
	onKeySecondary  func(deck.Coordinates, fyne.Position)
	onKeyDragMove   func(deck.Coordinates, fyne.Position)
	onKeyDragEnd    func(deck.Coordinates, fyne.Position)
	onDialSecondary func(int, fyne.Position)
	onDialDragMove  func(int, fyne.Position)
	onDialDragEnd   func(int, fyne.Position)
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
	edit        surfaceEditHandlers
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

// SetEditHandlers attaches optional Studio editing gestures (right-click & drag-and-drop) onto all tiles and dials.
func (v *deckSurfaceView) SetEditHandlers(h surfaceEditHandlers) {
	v.edit = h
	for _, tile := range v.tiles {
		v.bindTileEdit(tile)
	}
	for _, dc := range v.dials {
		v.bindDialEdit(dc.face)
	}
}

func (v *deckSurfaceView) editingActive() bool {
	return v.edit.canEdit == nil || v.edit.canEdit()
}

func (v *deckSurfaceView) bindTileEdit(tile *deckTileWidget) {
	if tile == nil {
		return
	}
	tile.canDrag = v.editingActive
	tile.onSecondaryTap = func(c deck.Coordinates, abs fyne.Position) {
		if v.editingActive() && v.edit.onKeySecondary != nil {
			v.edit.onKeySecondary(c, abs)
		}
	}
	tile.onDragMove = func(c deck.Coordinates, abs fyne.Position) {
		if v.editingActive() && v.edit.onKeyDragMove != nil {
			v.edit.onKeyDragMove(c, abs)
		}
	}
	tile.onDragEnd = func(c deck.Coordinates, abs fyne.Position) {
		if v.editingActive() && v.edit.onKeyDragEnd != nil {
			v.edit.onKeyDragEnd(c, abs)
		}
	}
}

func (v *deckSurfaceView) bindDialEdit(face *dialFaceWidget) {
	if face == nil {
		return
	}
	face.canDrag = v.editingActive
	face.onSecondaryTap = func(idx int, abs fyne.Position) {
		if v.editingActive() && v.edit.onDialSecondary != nil {
			v.edit.onDialSecondary(idx, abs)
		}
	}
	face.onDragMove = func(idx int, abs fyne.Position) {
		if v.editingActive() && v.edit.onDialDragMove != nil {
			v.edit.onDialDragMove(idx, abs)
		}
	}
	face.onDragEnd = func(idx int, abs fyne.Position) {
		if v.editingActive() && v.edit.onDialDragEnd != nil {
			v.edit.onDialDragEnd(idx, abs)
		}
	}
}

// TileAtPosition returns the keypad tile overlapping canvas position absPos, if any.
func (v *deckSurfaceView) TileAtPosition(absPos fyne.Position) (deck.Coordinates, bool) {
	curApp := fyne.CurrentApp()
	if curApp == nil || curApp.Driver() == nil {
		return deck.Coordinates{}, false
	}
	drv := curApp.Driver()
	for _, tile := range v.tiles {
		sz := tile.Size()
		if sz.Width <= 0 || sz.Height <= 0 {
			continue
		}
		pos := drv.AbsolutePositionForObject(tile)
		if absPos.X >= pos.X && absPos.X <= pos.X+sz.Width &&
			absPos.Y >= pos.Y && absPos.Y <= pos.Y+sz.Height {
			return tile.coords, true
		}
	}
	return deck.Coordinates{}, false
}

// DialAtPosition returns the encoder dial overlapping canvas position absPos, if any.
func (v *deckSurfaceView) DialAtPosition(absPos fyne.Position) (int, bool) {
	curApp := fyne.CurrentApp()
	if curApp == nil || curApp.Driver() == nil {
		return 0, false
	}
	drv := curApp.Driver()
	for _, dc := range v.dials {
		sz := dc.face.Size()
		if sz.Width <= 0 || sz.Height <= 0 {
			continue
		}
		pos := drv.AbsolutePositionForObject(dc.face)
		if absPos.X >= pos.X && absPos.X <= pos.X+sz.Width &&
			absPos.Y >= pos.Y && absPos.Y <= pos.Y+sz.Height {
			return dc.face.index, true
		}
	}
	return 0, false
}

// SetMoveHighlights updates move-source and drag-drop highlight rings across keypad tiles and dials.
func (v *deckSurfaceView) SetMoveHighlights(
	hasMoveKey bool,
	moveKey deck.Coordinates,
	hasMoveDial bool,
	moveDial int,
	hasDropKey bool,
	dropKey deck.Coordinates,
	hasDropDial bool,
	dropDial int,
) {
	for _, tile := range v.tiles {
		wantMove := hasMoveKey && tile.coords == moveKey
		wantDrop := hasDropKey && tile.coords == dropKey
		if tile.moveSource != wantMove || tile.dropTarget != wantDrop {
			tile.moveSource = wantMove
			tile.dropTarget = wantDrop
			tile.Refresh()
		}
	}
	for _, dc := range v.dials {
		wantMove := hasMoveDial && dc.face.index == moveDial
		wantDrop := hasDropDial && dc.face.index == dropDial
		if dc.face.moveSource != wantMove || dc.face.dropTarget != wantDrop {
			dc.face.moveSource = wantMove
			dc.face.dropTarget = wantDrop
			dc.face.Refresh()
		}
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
			v.bindTileEdit(tile)
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
		v.bindDialEdit(face)
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
