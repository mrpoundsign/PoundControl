package ui

import (
	"fmt"
	"image/color"
	"math"
	"path/filepath"
	"slices"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/mrpoundsign/PoundControl/pkg/deck"
	"github.com/mrpoundsign/PoundControl/pkg/tile"
)

func srgbChannelLuminance(c uint8) float64 {
	v := float64(c) / 255.0
	if v <= 0.04045 {
		return v / 12.92
	}
	return math.Pow((v+0.055)/1.055, 2.4)
}

func relativeLuminance(c color.RGBA) float64 {
	return 0.2126*srgbChannelLuminance(c.R) +
		0.7152*srgbChannelLuminance(c.G) +
		0.0722*srgbChannelLuminance(c.B)
}

func contrastRatio(a, b color.RGBA) float64 {
	la := relativeLuminance(a)
	lb := relativeLuminance(b)
	hi, lo := max(la, lb), min(la, lb)
	return (hi + 0.05) / (lo + 0.05)
}

func contrastTextColor(bg color.RGBA) color.RGBA {
	if contrastRatio(bg, ColorObsidianSlate) > contrastRatio(bg, ColorChromeWhite) {
		return ColorObsidianSlate
	}
	return ColorChromeWhite
}

func secondaryContrastTextColor(bg color.RGBA) color.RGBA {
	if contrastRatio(bg, ColorObsidianSlate) > contrastRatio(bg, ColorChromeWhite) {
		return color.RGBA{R: 51, G: 65, B: 85, A: 255}
	}
	return ColorSecondarySlate
}

type colorSwatchButton struct {
	widget.BaseWidget
	hex      string
	fillCol  color.RGBA
	selected bool
	hovered  bool
	onTap    func()
}

var (
	_ fyne.Tappable     = (*colorSwatchButton)(nil)
	_ desktop.Hoverable = (*colorSwatchButton)(nil)
)

func newColorSwatchButton(hex string, onTap func()) *colorSwatchButton {
	b := &colorSwatchButton{
		hex:     hex,
		fillCol: parseHexColor(hex, ColorDarkCharcoal),
		onTap:   onTap,
	}
	b.ExtendBaseWidget(b)
	return b
}

func (b *colorSwatchButton) SetSelected(sel bool) {
	if b.selected == sel {
		return
	}
	b.selected = sel
	b.Refresh()
}

func (b *colorSwatchButton) Tapped(*fyne.PointEvent) {
	if b.onTap != nil {
		b.onTap()
	}
}

func (b *colorSwatchButton) MouseIn(*desktop.MouseEvent) {
	b.hovered = true
	b.Refresh()
}

func (b *colorSwatchButton) MouseMoved(*desktop.MouseEvent) {}

func (b *colorSwatchButton) MouseOut() {
	b.hovered = false
	b.Refresh()
}

func (b *colorSwatchButton) CreateRenderer() fyne.WidgetRenderer {
	rect := canvas.NewRectangle(b.fillCol)
	rect.CornerRadius = 4
	rect.StrokeColor = ColorSlateBorder
	rect.StrokeWidth = 1.5
	return &colorSwatchRenderer{btn: b, rect: rect}
}

type colorSwatchRenderer struct {
	btn  *colorSwatchButton
	rect *canvas.Rectangle
}

func (r *colorSwatchRenderer) Layout(size fyne.Size) {
	r.rect.Move(fyne.NewPos(1, 1))
	r.rect.Resize(fyne.NewSize(max(float32(8), size.Width-2), max(float32(8), size.Height-2)))
}

func (r *colorSwatchRenderer) MinSize() fyne.Size {
	return fyne.NewSize(22, 22)
}

func (r *colorSwatchRenderer) Refresh() {
	r.rect.FillColor = r.btn.fillCol
	switch {
	case r.btn.selected:
		r.rect.StrokeColor = ColorChromeWhite
		r.rect.StrokeWidth = 2.5
	case r.btn.hovered:
		r.rect.StrokeColor = ColorSkyBlue
		r.rect.StrokeWidth = 2.0
	default:
		r.rect.StrokeColor = ColorSlateBorder
		r.rect.StrokeWidth = 1.0
	}
	r.rect.Refresh()
}

func (r *colorSwatchRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.rect}
}

func (r *colorSwatchRenderer) Destroy() {}

type colorFieldControl struct {
	win           fyne.Window
	title         string
	root          *fyne.Container
	preview       *canvas.Rectangle
	entry         *widget.Entry
	pickBtn       *widget.Button
	clearBtn      *widget.Button
	addPaletteBtn *widget.Button
	swatchRow     *fyne.Container
	swatchBtns    []*colorSwatchButton
	paletteHexes  []string

	syncing     bool
	onChanged   func(string)
	onAddSwatch func(name, hex string) error
	onStatus    func(string)
}

func newColorFieldControl(
	win fyne.Window,
	title string,
	initialHex string,
	swatches []deck.ColorSwatch,
	onAddSwatch func(name, hex string) error,
	onStatus func(string),
) *colorFieldControl {
	c := &colorFieldControl{
		win:         win,
		title:       title,
		onAddSwatch: onAddSwatch,
		onStatus:    onStatus,
	}

	c.preview = canvas.NewRectangle(ColorDarkCharcoal)
	c.preview.CornerRadius = 6
	c.preview.StrokeColor = ColorSlateBorder
	c.preview.StrokeWidth = 1.5
	c.preview.SetMinSize(fyne.NewSize(28, 28))

	c.entry = widget.NewEntry()
	c.entry.SetPlaceHolder("#RRGGBB (default)")
	c.entry.OnChanged = func(_ string) {
		if c.syncing {
			return
		}
		c.syncPreviewAndSwatches()
		if c.onChanged != nil {
			c.onChanged(c.Value())
		}
	}

	c.pickBtn = widget.NewButton("Pick", c.openColorDialog)
	c.clearBtn = widget.NewButton("Clear", func() {
		c.SetValue("")
	})

	btnBox := container.NewHBox(c.pickBtn, c.clearBtn)
	if onAddSwatch != nil {
		c.addPaletteBtn = widget.NewButtonWithIcon("", theme.ContentAddIcon(), c.openSaveToPaletteDialog)
		btnBox.Add(c.addPaletteBtn)
	}

	topRow := container.NewBorder(nil, nil, c.preview, btnBox, c.entry)
	c.swatchRow = container.NewGridWrap(fyne.NewSize(24, 24))
	c.root = container.NewVBox(topRow, c.swatchRow)

	c.RefreshPalette(swatches)
	c.SetValue(initialHex)
	return c
}

func (c *colorFieldControl) Object() fyne.CanvasObject {
	return c.root
}

func (c *colorFieldControl) Value() string {
	raw := strings.TrimSpace(c.entry.Text)
	if raw == "" {
		return ""
	}
	if hex, ok := deck.NormalizeHexColor(raw); ok {
		return hex
	}
	return raw
}

func (c *colorFieldControl) SetValue(raw string) {
	trimmed := strings.TrimSpace(raw)
	if hex, ok := deck.NormalizeHexColor(trimmed); ok {
		trimmed = hex
	}
	c.syncing = true
	c.entry.SetText(trimmed)
	c.syncing = false
	c.syncPreviewAndSwatches()
	if c.onChanged != nil {
		c.onChanged(c.Value())
	}
}

func (c *colorFieldControl) RefreshPalette(swatches []deck.ColorSwatch) {
	c.swatchRow.Objects = nil
	c.swatchBtns = make([]*colorSwatchButton, 0, len(swatches))
	c.paletteHexes = make([]string, 0, len(swatches))
	for _, sw := range swatches {
		hex, ok := deck.NormalizeHexColor(sw.Hex)
		if !ok {
			continue
		}
		targetHex := hex
		btn := newColorSwatchButton(targetHex, func() {
			c.SetValue(targetHex)
		})
		c.swatchBtns = append(c.swatchBtns, btn)
		c.paletteHexes = append(c.paletteHexes, targetHex)
		c.swatchRow.Add(btn)
	}
	if len(c.swatchBtns) == 0 {
		c.swatchRow.Hide()
	} else {
		c.swatchRow.Show()
	}
	c.syncPreviewAndSwatches()
	c.swatchRow.Refresh()
}

func (c *colorFieldControl) syncPreviewAndSwatches() {
	normHex, valid := deck.NormalizeHexColor(c.entry.Text)
	if valid {
		c.preview.FillColor = parseHexColor(normHex, ColorDarkCharcoal)
		c.preview.StrokeColor = ColorChromeWhite
	} else {
		c.preview.FillColor = ColorDarkCharcoal
		c.preview.StrokeColor = ColorSlateBorder
	}
	c.preview.Refresh()

	for i, btn := range c.swatchBtns {
		btn.SetSelected(valid && i < len(c.paletteHexes) && strings.EqualFold(c.paletteHexes[i], normHex))
	}
}

func (c *colorFieldControl) openColorDialog() {
	if c.win == nil {
		return
	}
	dlg := dialog.NewColorPicker(c.title, "Choose color", func(picked color.Color) {
		if picked == nil {
			return
		}
		nrgba := color.NRGBAModel.Convert(picked).(color.NRGBA)
		hex := fmt.Sprintf("#%02X%02X%02X", nrgba.R, nrgba.G, nrgba.B)
		c.SetValue(hex)
	}, c.win)
	dlg.Advanced = true
	dlg.Show()
	if normHex, ok := deck.NormalizeHexColor(c.entry.Text); ok {
		dlg.SetColor(parseHexColor(normHex, ColorSkyBlue))
	} else {
		dlg.SetColor(ColorSkyBlue)
	}
}

func (c *colorFieldControl) openSaveToPaletteDialog() {
	if c.win == nil || c.onAddSwatch == nil {
		return
	}
	hex, ok := deck.NormalizeHexColor(c.entry.Text)
	if !ok {
		if c.onStatus != nil {
			c.onStatus("Enter or pick a valid #RRGGBB color before saving to palette")
		}
		return
	}
	nameEntry := widget.NewEntry()
	nameEntry.SetPlaceHolder("e.g. Stream Teal")
	nameEntry.SetText(hex)
	items := []*widget.FormItem{
		widget.NewFormItem("Name", nameEntry),
		widget.NewFormItem("Hex", widget.NewLabel(hex)),
	}
	dlg := dialog.NewForm("Save Color to Palette", "Save", "Cancel", items, func(ok bool) {
		if !ok {
			return
		}
		if err := c.onAddSwatch(nameEntry.Text, hex); err != nil {
			if c.onStatus != nil {
				c.onStatus("Save swatch failed: " + err.Error())
			}
			return
		}
		if c.onStatus != nil {
			c.onStatus("Saved " + hex + " to custom palette")
		}
	}, c.win)
	dlg.Resize(fyne.NewSize(340, 180))
	dlg.Show()
}

type imageFieldControl struct {
	win       fyne.Window
	assetsDir func() string
	onStatus  func(string)
	root      *fyne.Container
	thumb     *canvas.Image
	thumbBg   *canvas.Rectangle
	entry     *widget.Entry
	browseBtn *widget.Button
	clearBtn  *widget.Button
}

func newImageFieldControl(
	win fyne.Window,
	initialURI string,
	assetsDir func() string,
	onStatus func(string),
) *imageFieldControl {
	c := &imageFieldControl{
		win:       win,
		assetsDir: assetsDir,
		onStatus:  onStatus,
	}
	c.thumbBg = canvas.NewRectangle(ColorDarkCharcoal)
	c.thumbBg.CornerRadius = 6
	c.thumbBg.StrokeColor = ColorSlateBorder
	c.thumbBg.StrokeWidth = 1
	c.thumbBg.SetMinSize(fyne.NewSize(38, 38))

	c.thumb = canvas.NewImageFromImage(nil)
	c.thumb.FillMode = canvas.ImageFillContain
	c.thumb.ScaleMode = canvas.ImageScaleSmooth
	c.thumb.SetMinSize(fyne.NewSize(34, 34))
	c.thumb.Hide()

	previewBox := container.NewStack(c.thumbBg, container.NewCenter(c.thumb))

	c.entry = widget.NewEntry()
	c.entry.SetPlaceHolder("PNG, JPEG, or SVG path")
	c.entry.OnChanged = func(_ string) {
		c.refreshThumbnail()
	}

	c.browseBtn = widget.NewButton("Browse", c.openFileDialog)
	c.clearBtn = widget.NewButton("Clear", func() {
		c.SetValue("")
	})

	btnBox := container.NewHBox(c.browseBtn, c.clearBtn)
	c.root = container.NewBorder(nil, nil, previewBox, btnBox, c.entry)
	c.SetValue(initialURI)
	return c
}

func (c *imageFieldControl) Object() fyne.CanvasObject {
	return c.root
}

func (c *imageFieldControl) Value() string {
	return strings.TrimSpace(c.entry.Text)
}

func (c *imageFieldControl) SetValue(uri string) {
	c.entry.SetText(strings.TrimSpace(uri))
	c.refreshThumbnail()
}

func (c *imageFieldControl) ImportFile(srcPath string) error {
	var dir string
	if c.assetsDir != nil {
		dir = c.assetsDir()
	}
	imported, err := tile.ImportImageAsset(dir, srcPath)
	if err != nil {
		return err
	}
	c.SetValue(imported)
	return nil
}

func (c *imageFieldControl) refreshThumbnail() {
	uri := c.Value()
	if uri == "" {
		c.thumb.Image = nil
		c.thumb.Hide()
		c.thumb.Refresh()
		return
	}
	img, err := sharedTileCache.GetOrDecode(uri, 72)
	if err != nil || img == nil {
		c.thumb.Image = nil
		c.thumb.Hide()
		c.thumb.Refresh()
		return
	}
	c.thumb.Image = tile.CropSquare(img)
	c.thumb.Show()
	c.thumb.Refresh()
}

func (c *imageFieldControl) openFileDialog() {
	if c.win == nil {
		return
	}
	fd := dialog.NewFileOpen(func(r fyne.URIReadCloser, err error) {
		if err != nil {
			if c.onStatus != nil {
				c.onStatus("Select image failed: " + err.Error())
			}
			return
		}
		if r == nil {
			return
		}
		filePath := uriToLocalPath(r.URI())
		_ = r.Close()
		if impErr := c.ImportFile(filePath); impErr != nil {
			if c.onStatus != nil {
				c.onStatus("Import image failed: " + impErr.Error())
			}
			return
		}
		if c.onStatus != nil {
			c.onStatus("Imported button image — click Save to apply")
		}
	}, c.win)
	fd.SetFilter(storage.NewExtensionFileFilter([]string{".png", ".jpg", ".jpeg", ".svg"}))
	fd.Resize(fyne.NewSize(760, 520))
	fd.Show()
}

type fileFieldControl struct {
	win        fyne.Window
	root       *fyne.Container
	entry      *widget.Entry
	browseBtn  *widget.Button
	extensions []string
}

func newFileFieldControl(win fyne.Window, initialPath, placeholder string, extensions []string) *fileFieldControl {
	c := &fileFieldControl{
		win:        win,
		extensions: normalizeFileExtensions(extensions),
	}
	c.entry = widget.NewEntry()
	c.entry.SetPlaceHolder(placeholder)
	c.entry.SetText(initialPath)
	c.browseBtn = widget.NewButton("Browse", c.openFileDialog)
	c.root = container.NewBorder(nil, nil, nil, c.browseBtn, c.entry)
	return c
}

func normalizeFileExtensions(extensions []string) []string {
	if len(extensions) == 0 {
		return nil
	}
	out := make([]string, 0, len(extensions))
	for _, ext := range extensions {
		t := strings.TrimSpace(ext)
		if t == "" {
			continue
		}
		if !strings.HasPrefix(t, ".") {
			t = "." + t
		}
		if !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	return out
}

func (c *fileFieldControl) Object() fyne.CanvasObject {
	return c.root
}

func (c *fileFieldControl) Value() string {
	return strings.TrimSpace(c.entry.Text)
}

func (c *fileFieldControl) SetValue(path string) {
	c.entry.SetText(strings.TrimSpace(path))
}

func (c *fileFieldControl) openFileDialog() {
	if c.win == nil {
		return
	}
	fd := dialog.NewFileOpen(func(r fyne.URIReadCloser, err error) {
		if err != nil || r == nil {
			return
		}
		filePath := uriToLocalPath(r.URI())
		_ = r.Close()
		c.SetValue(filePath)
	}, c.win)
	if len(c.extensions) > 0 {
		fd.SetFilter(storage.NewExtensionFileFilter(c.extensions))
	}
	fd.Resize(fyne.NewSize(760, 520))
	fd.Show()
}

func uriToLocalPath(u fyne.URI) string {
	if u == nil {
		return ""
	}
	p := u.Path()
	if strings.EqualFold(u.Scheme(), "file") {
		if len(p) >= 3 && p[0] == '/' && p[2] == ':' {
			p = p[1:]
		}
		p = filepath.FromSlash(p)
	}
	return filepath.Clean(p)
}
