package ui

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/widget"

	"github.com/mrpoundsign/PoundControl/internal/hotkey"
	"github.com/mrpoundsign/PoundControl/pkg/builtin/apps"
)

type hotkeyCaptureMode int

const (
	captureModeGlobal hotkeyCaptureMode = iota
	captureModeChord
)

type hotkeyCaptureSession struct {
	dialog    *dialog.CustomDialog
	pad       *hotkeyCapturePad
	ctrlChk   *widget.Check
	shiftChk  *widget.Check
	altChk    *widget.Check
	winChk    *widget.Check
	status    *widget.Label
	applyBtn  *widget.Button
	clearBtn  *widget.Button
	cancelBtn *widget.Button
}

type hotkeyCapturePad struct {
	widget.BaseWidget

	focused     bool
	previewText string
	hintText    string
	valid       bool

	heldCtrlL  bool
	heldCtrlR  bool
	heldShiftL bool
	heldShiftR bool
	heldAltL   bool
	heldAltR   bool
	heldWinL   bool
	heldWinR   bool

	suppressTypedKey fyne.KeyName

	onFocusRequest func()
	onKeyCapture   func(ctrl, shift, alt, win, useMods bool, key string)
}

var (
	_ fyne.Focusable    = (*hotkeyCapturePad)(nil)
	_ fyne.Tabbable     = (*hotkeyCapturePad)(nil)
	_ fyne.Shortcutable = (*hotkeyCapturePad)(nil)
	_ fyne.Tappable     = (*hotkeyCapturePad)(nil)
	_ desktop.Keyable   = (*hotkeyCapturePad)(nil)
)

func newHotkeyCapturePad(
	onFocusRequest func(),
	onKeyCapture func(ctrl, shift, alt, win, useMods bool, key string),
) *hotkeyCapturePad {
	p := &hotkeyCapturePad{
		onFocusRequest: onFocusRequest,
		onKeyCapture:   onKeyCapture,
	}
	p.ExtendBaseWidget(p)
	return p
}

func (p *hotkeyCapturePad) SetDisplay(preview, hint string, valid bool) {
	p.previewText = preview
	p.hintText = hint
	p.valid = valid
	p.Refresh()
}

// AcceptsTab keeps Tab inside the capture pad instead of advancing dialog focus.
func (p *hotkeyCapturePad) AcceptsTab() bool {
	return true
}

func (p *hotkeyCapturePad) FocusGained() {
	p.focused = true
	p.Refresh()
}

func (p *hotkeyCapturePad) FocusLost() {
	p.focused = false
	p.heldCtrlL = false
	p.heldCtrlR = false
	p.heldShiftL = false
	p.heldShiftR = false
	p.heldAltL = false
	p.heldAltR = false
	p.heldWinL = false
	p.heldWinR = false
	p.suppressTypedKey = ""
	p.Refresh()
}

func (p *hotkeyCapturePad) TypedRune(rune) {}

func (p *hotkeyCapturePad) Tapped(*fyne.PointEvent) {
	if p.onFocusRequest != nil {
		p.onFocusRequest()
	}
}

func (p *hotkeyCapturePad) KeyDown(ke *fyne.KeyEvent) {
	if ke == nil {
		return
	}
	if p.setModifierState(ke.Name, true) {
		return
	}
	canonical, ok := canonicalKeyFromFyne(ke.Name)
	if !ok {
		return
	}
	// GLFW delivers TypedShortcut or TypedKey immediately after KeyDown on the same press;
	// suppress that duplicate event for the same key name.
	p.suppressTypedKey = ke.Name
	ctrl, shift, alt, win, hasHeld := p.heldModifiers()
	if p.onKeyCapture != nil {
		p.onKeyCapture(ctrl, shift, alt, win, hasHeld, canonical)
	}
}

func (p *hotkeyCapturePad) KeyUp(ke *fyne.KeyEvent) {
	if ke == nil {
		return
	}
	if p.setModifierState(ke.Name, false) {
		return
	}
	if p.suppressTypedKey == ke.Name {
		p.suppressTypedKey = ""
	}
}

func (p *hotkeyCapturePad) TypedKey(ke *fyne.KeyEvent) {
	if ke == nil {
		return
	}
	if p.suppressTypedKey == ke.Name {
		p.suppressTypedKey = ""
		return
	}
	p.suppressTypedKey = ""

	canonical, ok := canonicalKeyFromFyne(ke.Name)
	if !ok {
		return
	}
	ctrl, shift, alt, win, hasHeld := p.heldModifiers()
	if p.onKeyCapture != nil {
		p.onKeyCapture(ctrl, shift, alt, win, hasHeld, canonical)
	}
}

func (p *hotkeyCapturePad) TypedShortcut(s fyne.Shortcut) {
	ks, ok := s.(fyne.KeyboardShortcut)
	if !ok {
		return
	}
	if p.suppressTypedKey == ks.Key() {
		p.suppressTypedKey = ""
		return
	}
	p.suppressTypedKey = ""

	canonical, ok := canonicalKeyFromFyne(ks.Key())
	if !ok {
		return
	}
	mod := ks.Mod()
	ctrl := mod&fyne.KeyModifierControl != 0
	shift := mod&fyne.KeyModifierShift != 0
	alt := mod&fyne.KeyModifierAlt != 0
	win := mod&fyne.KeyModifierSuper != 0
	useMods := ctrl || shift || alt || win
	if p.onKeyCapture != nil {
		p.onKeyCapture(ctrl, shift, alt, win, useMods, canonical)
	}
}

func (p *hotkeyCapturePad) setModifierState(name fyne.KeyName, down bool) bool {
	switch name {
	case desktop.KeyControlLeft:
		p.heldCtrlL = down
		return true
	case desktop.KeyControlRight:
		p.heldCtrlR = down
		return true
	case desktop.KeyShiftLeft:
		p.heldShiftL = down
		return true
	case desktop.KeyShiftRight:
		p.heldShiftR = down
		return true
	case desktop.KeyAltLeft:
		p.heldAltL = down
		return true
	case desktop.KeyAltRight:
		p.heldAltR = down
		return true
	case desktop.KeySuperLeft:
		p.heldWinL = down
		return true
	case desktop.KeySuperRight:
		p.heldWinR = down
		return true
	default:
		return false
	}
}

func (p *hotkeyCapturePad) heldModifiers() (ctrl, shift, alt, win, any bool) {
	ctrl = p.heldCtrlL || p.heldCtrlR
	shift = p.heldShiftL || p.heldShiftR
	alt = p.heldAltL || p.heldAltR
	win = p.heldWinL || p.heldWinR
	any = ctrl || shift || alt || win
	return ctrl, shift, alt, win, any
}

func (p *hotkeyCapturePad) MinSize() fyne.Size {
	return fyne.NewSize(340, 96)
}

func (p *hotkeyCapturePad) CreateRenderer() fyne.WidgetRenderer {
	bg := canvas.NewRectangle(ColorObsidianSlate)
	bg.CornerRadius = 10
	bg.StrokeWidth = 2

	preview := canvas.NewText("", ColorChromeWhite)
	preview.TextSize = 22
	preview.TextStyle = fyne.TextStyle{Bold: true}
	preview.Alignment = fyne.TextAlignCenter

	hint := canvas.NewText("", ColorSecondarySlate)
	hint.TextSize = 11
	hint.Alignment = fyne.TextAlignCenter

	content := container.NewCenter(container.NewVBox(preview, hint))
	r := &hotkeyCapturePadRenderer{
		pad:     p,
		bg:      bg,
		preview: preview,
		hint:    hint,
		objects: []fyne.CanvasObject{bg, content},
		content: content,
	}
	r.Refresh()
	return r
}

type hotkeyCapturePadRenderer struct {
	pad     *hotkeyCapturePad
	bg      *canvas.Rectangle
	preview *canvas.Text
	hint    *canvas.Text
	content fyne.CanvasObject
	objects []fyne.CanvasObject
}

func (r *hotkeyCapturePadRenderer) Layout(size fyne.Size) {
	r.bg.Resize(size)
	r.content.Move(fyne.NewPos(8, 8))
	r.content.Resize(fyne.NewSize(max(0, size.Width-16), max(0, size.Height-16)))
}

func (r *hotkeyCapturePadRenderer) MinSize() fyne.Size {
	return r.pad.MinSize()
}

func (r *hotkeyCapturePadRenderer) Refresh() {
	if r.pad.focused {
		r.bg.StrokeColor = ColorSkyBlue
	} else {
		r.bg.StrokeColor = ColorElevatedSlate
	}
	r.bg.Refresh()

	if r.pad.previewText == "" {
		r.preview.Text = "Press any key combination…"
		r.preview.Color = ColorMutedText
	} else {
		r.preview.Text = r.pad.previewText
		if r.pad.valid {
			r.preview.Color = ColorChromeWhite
		} else {
			r.preview.Color = ColorWarning
		}
	}
	r.preview.Refresh()

	r.hint.Text = r.pad.hintText
	r.hint.Refresh()
}

func (r *hotkeyCapturePadRenderer) Objects() []fyne.CanvasObject {
	return r.objects
}

func (r *hotkeyCapturePadRenderer) Destroy() {}

func canonicalKeyFromFyne(name fyne.KeyName) (string, bool) {
	switch name {
	case "", desktop.KeyControlLeft, desktop.KeyControlRight,
		desktop.KeyShiftLeft, desktop.KeyShiftRight,
		desktop.KeyAltLeft, desktop.KeyAltRight,
		desktop.KeySuperLeft, desktop.KeySuperRight,
		desktop.KeyMenu, desktop.KeyCapsLock:
		return "", false
	case fyne.KeyReturn, fyne.KeyEnter:
		return "Enter", true
	case fyne.KeyEscape:
		return "Escape", true
	case fyne.KeyTab:
		return "Tab", true
	case fyne.KeySpace:
		return "Space", true
	case fyne.KeyBackspace:
		return "Backspace", true
	case fyne.KeyInsert:
		return "Insert", true
	case fyne.KeyDelete:
		return "Delete", true
	case fyne.KeyHome:
		return "Home", true
	case fyne.KeyEnd:
		return "End", true
	case fyne.KeyPageUp:
		return "PageUp", true
	case fyne.KeyPageDown:
		return "PageDown", true
	case fyne.KeyUp:
		return "Up", true
	case fyne.KeyDown:
		return "Down", true
	case fyne.KeyLeft:
		return "Left", true
	case fyne.KeyRight:
		return "Right", true
	}

	upper := strings.ToUpper(strings.TrimSpace(string(name)))
	if len(upper) == 1 && ((upper[0] >= 'A' && upper[0] <= 'Z') || (upper[0] >= '0' && upper[0] <= '9')) {
		return upper, true
	}
	for i := 1; i <= 24; i++ {
		fName := fmt.Sprintf("F%d", i)
		if upper == fName {
			return fName, true
		}
	}
	if upper == "PAUSE" {
		return "Pause", true
	}
	return "", false
}

func buildHotkeyString(ctrl, shift, alt, win bool, key string) string {
	parts := make([]string, 0, 5)
	if ctrl {
		parts = append(parts, "Ctrl")
	}
	if shift {
		parts = append(parts, "Shift")
	}
	if alt {
		parts = append(parts, "Alt")
	}
	if win {
		parts = append(parts, "Win")
	}
	if key != "" {
		parts = append(parts, key)
	}
	return strings.Join(parts, "+")
}

func parseInitialHotkey(raw string, mode hotkeyCaptureMode) (ctrl, shift, alt, win bool, key string) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return false, false, false, false, ""
	}
	if mode == captureModeGlobal {
		if spec, err := hotkey.Parse(trimmed); err == nil {
			return spec.Ctrl, spec.Shift, spec.Alt, spec.Win, spec.Key
		}
	}
	if ch, err := apps.ParseChord(trimmed); err == nil {
		return ch.Ctrl, ch.Shift, ch.Alt, ch.Win, ch.Key
	}

	for part := range strings.SplitSeq(trimmed, "+") {
		tok := strings.TrimSpace(part)
		if tok == "" {
			continue
		}
		switch strings.ToLower(tok) {
		case "ctrl", "control":
			ctrl = true
		case "shift":
			shift = true
		case "alt", "option":
			alt = true
		case "win", "super", "meta", "cmd", "command":
			win = true
		default:
			if key == "" {
				if ch, err := apps.ParseChord(tok); err == nil {
					key = ch.Key
				}
			}
		}
	}
	return ctrl, shift, alt, win, key
}

func validateCapturedHotkey(raw string, mode hotkeyCaptureMode) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", errors.New("press any key combination to capture")
	}
	if mode == captureModeGlobal {
		spec, err := hotkey.Parse(trimmed)
		if err != nil {
			return "", err
		}
		return spec.String(), nil
	}
	ch, err := apps.ParseChord(trimmed)
	if err != nil {
		return "", err
	}
	return ch.String(), nil
}

func openHotkeyCaptureDialog(
	win fyne.Window,
	title string,
	initialRaw string,
	mode hotkeyCaptureMode,
	onSuspend func(bool),
	onCaptured func(string),
) *hotkeyCaptureSession {
	if win == nil {
		return nil
	}

	initCtrl, initShift, initAlt, initWin, capturedKey := parseInitialHotkey(initialRaw, mode)
	syncingMods := true
	canonicalValid := ""

	var pad *hotkeyCapturePad
	var ctrlChk, shiftChk, altChk, winChk *widget.Check
	statusLbl := widget.NewLabel("")
	statusLbl.Wrapping = fyne.TextWrapWord

	var applyBtn *widget.Button

	updateState := func() {
		raw := buildHotkeyString(ctrlChk.Checked, shiftChk.Checked, altChk.Checked, winChk.Checked, capturedKey)
		if raw == "" {
			canonicalValid = ""
			pad.SetDisplay("", "Click here and press any key combo (A-Z, 0-9, F1-F24, Tab, Esc…)", false)
			statusLbl.SetText("Tip: check modifiers manually for OS-reserved combos like Win+Shift+S or Alt+F4.")
			applyBtn.Disable()
			return
		}
		canon, err := validateCapturedHotkey(raw, mode)
		if err != nil {
			canonicalValid = ""
			pad.SetDisplay(raw, "Press a supported non-modifier key or adjust modifiers above", false)
			statusLbl.SetText(err.Error())
			applyBtn.Disable()
			return
		}
		canonicalValid = canon
		pad.SetDisplay(canon, "Press another key to replace, or click Apply", true)
		statusLbl.SetText("Ready: " + canon)
		applyBtn.Enable()
	}

	refocusPad := func() {
		if win.Canvas() != nil {
			win.Canvas().Focus(pad)
		}
	}

	onModChanged := func(bool) {
		if syncingMods {
			return
		}
		updateState()
		refocusPad()
	}

	ctrlChk = widget.NewCheck("Ctrl", onModChanged)
	shiftChk = widget.NewCheck("Shift", onModChanged)
	altChk = widget.NewCheck("Alt", onModChanged)
	winChk = widget.NewCheck("Win", onModChanged)
	ctrlChk.SetChecked(initCtrl)
	shiftChk.SetChecked(initShift)
	altChk.SetChecked(initAlt)
	winChk.SetChecked(initWin)
	syncingMods = false

	pad = newHotkeyCapturePad(
		refocusPad,
		func(ctrl, shift, alt, win, useMods bool, key string) {
			capturedKey = key
			if useMods {
				syncingMods = true
				ctrlChk.SetChecked(ctrl)
				shiftChk.SetChecked(shift)
				altChk.SetChecked(alt)
				winChk.SetChecked(win)
				syncingMods = false
			}
			updateState()
		},
	)

	var dlg *dialog.CustomDialog
	applyBtn = widget.NewButton("Apply", func() {
		if canonicalValid == "" {
			return
		}
		res := canonicalValid
		dlg.Hide()
		if onCaptured != nil {
			onCaptured(res)
		}
	})
	applyBtn.Importance = widget.HighImportance

	clearBtn := widget.NewButton("Clear", func() {
		dlg.Hide()
		if onCaptured != nil {
			onCaptured("")
		}
	})

	cancelBtn := widget.NewButton("Cancel", func() {
		dlg.Hide()
	})

	updateState()

	modsRow := container.NewHBox(widget.NewLabel("Modifiers:"), ctrlChk, shiftChk, altChk, winChk)
	btnsRow := container.NewHBox(clearBtn, layout.NewSpacer(), cancelBtn, applyBtn)
	content := container.NewVBox(
		modsRow,
		pad,
		statusLbl,
		widget.NewSeparator(),
		btnsRow,
	)

	dlg = dialog.NewCustomWithoutButtons(title, content, win)
	dlg.Resize(fyne.NewSize(420, 300))

	var resumeOnce sync.Once
	dlg.SetOnClosed(func() {
		resumeOnce.Do(func() {
			if onSuspend != nil {
				onSuspend(false)
			}
		})
	})

	if onSuspend != nil {
		onSuspend(true)
	}
	dlg.Show()
	refocusPad()

	return &hotkeyCaptureSession{
		dialog:    dlg,
		pad:       pad,
		ctrlChk:   ctrlChk,
		shiftChk:  shiftChk,
		altChk:    altChk,
		winChk:    winChk,
		status:    statusLbl,
		applyBtn:  applyBtn,
		clearBtn:  clearBtn,
		cancelBtn: cancelBtn,
	}
}
