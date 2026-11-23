package ui

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/mrpoundsign/PoundControl/internal/app"
	"github.com/mrpoundsign/PoundControl/internal/hotkey"
	"github.com/mrpoundsign/PoundControl/pkg/deck"
	"github.com/mrpoundsign/PoundControl/pkg/plugin"
)

// ActionInspector renders a schema-driven configuration form for the selected slot or dial.
type ActionInspector struct {
	win                     fyne.Window
	engine                  *app.Engine
	onStatus                func(string)
	onSuspendHotkeys        func(bool)
	piViewer                PIViewer
	sdCtrl                  SDPluginController
	onOpenPluginsSettings   func()
	onOpenPaletteSettings   func()
	onMoveSelected          func()
	onCopySelected          func()
	assetsDir               string
	palette                 []deck.ColorSwatch
	root                    fyne.CanvasObject

	slotLbl    *widget.Label
	actionLbl  *widget.Label
	metaLbl    *widget.Label
	formScroll *container.Scroll
	formBox    *fyne.Container
	accordion  *widget.Accordion
	actionsRow *fyne.Container

	sectionOpen map[string]bool // keyed by strings.ToLower(sectionTitle)

	titleEntry         *widget.Entry
	accentColorCtrl    *colorFieldControl
	bgColorCtrl        *colorFieldControl
	bgImageCtrl        *imageFieldControl
	hideTitleCheck     *widget.Check
	hotkeyEntry        *widget.Entry
	hotkeyCaptureBtn   *widget.Button
	openPIBtn          *widget.Button
	schemaForm         *schemaFormState
	hotkeyFieldButtons map[string]*widget.Button
	fields             map[string]schemaFieldBinding
	saveBtn            *widget.Button
	triggerBtn         *widget.Button
	clearBtn           *widget.Button
	moveBtn            *widget.Button
	copyBtn            *widget.Button
	lastCapture        *hotkeyCaptureSession

	selCtrl         deck.ControllerType
	selCoords       deck.Coordinates
	selDial         int
	boundContext    string
	boundActionID   string
	boundAutoBack   bool
	boundHistoryRev uint64
	curSettings     map[string]any
	syncing         bool
}

// NewActionInspector constructs an inspector panel bound to engine.
func NewActionInspector(win fyne.Window, engine *app.Engine, onStatus func(string)) *ActionInspector {
	ins := &ActionInspector{
		win:         win,
		engine:      engine,
		onStatus:    onStatus,
		sectionOpen: make(map[string]bool),
	}
	ins.schemaForm = newSchemaFormState(schemaFormOptions{
		win:       win,
		engine:    engine,
		assetsDir: func() string { return ins.assetsDir },
		palette:   func() []deck.ColorSwatch { return ins.palette },
		onStatus:  ins.reportStatus,
		onCaptureHotkey: func(title string, target *widget.Entry) {
			ins.openHotkeyCapture(title, target, captureModeChord)
		},
	})
	ins.fields = ins.schemaForm.fields
	ins.hotkeyFieldButtons = ins.schemaForm.hotkeyFieldButtons

	heading := widget.NewLabelWithStyle("Action Inspector", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	ins.slotLbl = widget.NewLabel("Slot: Row 0, Col 0")
	ins.actionLbl = widget.NewLabel("Unassigned")
	ins.actionLbl.TextStyle = fyne.TextStyle{Bold: true}
	ins.metaLbl = widget.NewLabel("")
	ins.metaLbl.Wrapping = fyne.TextWrapWord

	ins.formBox = container.NewVBox()
	ins.formScroll = container.NewVScroll(ins.formBox)

	ins.saveBtn = widget.NewButtonWithIcon("Save", theme.DocumentSaveIcon(), ins.handleSave)
	ins.saveBtn.Importance = widget.HighImportance
	ins.triggerBtn = widget.NewButtonWithIcon("Trigger", theme.MediaPlayIcon(), ins.handleTrigger)
	ins.clearBtn = widget.NewButtonWithIcon("Clear", theme.DeleteIcon(), ins.handleClear)
	ins.moveBtn = widget.NewButton("Move...", func() {
		if ins.onMoveSelected != nil {
			ins.onMoveSelected()
		}
	})
	ins.moveBtn.Disable()
	ins.copyBtn = widget.NewButtonWithIcon("Copy", theme.ContentCopyIcon(), func() {
		if ins.onCopySelected != nil {
			ins.onCopySelected()
		}
	})
	ins.copyBtn.Disable()
	ins.actionsRow = container.NewVBox(
		container.NewGridWithColumns(2, ins.moveBtn, ins.copyBtn),
		container.NewGridWithColumns(3, ins.saveBtn, ins.triggerBtn, ins.clearBtn),
	)

	top := container.NewVBox(
		heading,
		widget.NewSeparator(),
		ins.slotLbl,
		ins.actionLbl,
		ins.metaLbl,
		widget.NewSeparator(),
	)

	panelBg := canvas.NewRectangle(ColorDarkCharcoal)
	ins.root = container.NewStack(
		panelBg,
		container.NewPadded(container.NewBorder(top, ins.actionsRow, nil, nil, ins.formScroll)),
	)

	return ins
}

// Object returns the root Fyne canvas object for layout embedding.
func (ins *ActionInspector) Object() fyne.CanvasObject {
	return ins.root
}

// SetHotkeySuspender sets a callback used to pause OS global hotkeys while capturing a new shortcut.
func (ins *ActionInspector) SetHotkeySuspender(fn func(bool)) {
	ins.onSuspendHotkeys = fn
}

// SetPIViewer attaches a Property Inspector viewer used for interactive plugin HTML UIs.
func (ins *ActionInspector) SetPIViewer(v PIViewer) {
	ins.piViewer = v
}

// SetSDPluginController attaches an external .sdPlugin host controller for runtime status prompts.
func (ins *ActionInspector) SetSDPluginController(ctrl SDPluginController) {
	ins.sdCtrl = ctrl
}

// SetOnOpenPluginsSettings registers a callback invoked when the user clicks "Install Node.js in Settings".
func (ins *ActionInspector) SetOnOpenPluginsSettings(fn func()) {
	ins.onOpenPluginsSettings = fn
}

// SetOnOpenPaletteSettings registers a callback invoked when the user opens the palette editor.
func (ins *ActionInspector) SetOnOpenPaletteSettings(fn func()) {
	ins.onOpenPaletteSettings = fn
}

// SetAssetsDir configures the directory where imported custom button images are stored.
func (ins *ActionInspector) SetAssetsDir(dir string) {
	ins.assetsDir = strings.TrimSpace(dir)
}

// SetOnMoveSelected registers a callback invoked when the user clicks Move in the inspector.
func (ins *ActionInspector) SetOnMoveSelected(fn func()) {
	ins.onMoveSelected = fn
}

// SetOnCopySelected registers a callback invoked when the user clicks Copy in the inspector.
func (ins *ActionInspector) SetOnCopySelected(fn func()) {
	ins.onCopySelected = fn
}

// InvalidateBinding clears the cached inspector binding so the next SetSelection rebuilds controls cleanly.
func (ins *ActionInspector) InvalidateBinding() {
	ins.boundContext = ""
	ins.boundActionID = ""
	ins.boundAutoBack = false
}

func (ins *ActionInspector) refreshPaletteSwatches(swatches []deck.ColorSwatch) {
	if slices.Equal(ins.palette, swatches) {
		return
	}
	ins.palette = slices.Clone(swatches)
	if ins.accentColorCtrl != nil {
		ins.accentColorCtrl.RefreshPalette(ins.palette)
	}
	if ins.bgColorCtrl != nil {
		ins.bgColorCtrl.RefreshPalette(ins.palette)
	}
	ins.schemaForm.refreshPalette(ins.palette)
}

// SetSelection refreshes the inspector to reflect the currently selected slot or dial.
// Form controls are rebuilt only when the bound action context or action UUID changes so active input focus is preserved.
func (ins *ActionInspector) SetSelection(snap app.Snapshot, ctrl deck.ControllerType, coords deck.Coordinates, dial int) {
	ins.refreshPaletteSwatches(snap.ColorPalette)
	ins.selCtrl = ctrl
	ins.selCoords = coords
	ins.selDial = dial

	var rendered *app.RenderedSlot
	if ctrl == deck.ControllerEncoder {
		ins.slotLbl.SetText(fmt.Sprintf("Controller: Encoder Dial %d", dial+1))
		if r, ok := snap.DialSlots[dial]; ok {
			rcopy := r
			rendered = &rcopy
		}
	} else {
		ins.slotLbl.SetText(fmt.Sprintf("Slot: Row %d, Col %d (Keypad)", coords.Row, coords.Col))
		if r, ok := snap.Slots[coords.SlotKey()]; ok {
			rcopy := r
			rendered = &rcopy
		}
	}

	if rendered == nil {
		if ins.boundContext == "" && ins.boundActionID == "" && !ins.boundAutoBack && len(ins.formBox.Objects) > 0 {
			ins.boundHistoryRev = snap.HistoryRevision
			return
		}
		ins.boundContext = ""
		ins.boundActionID = ""
		ins.boundAutoBack = false
		ins.boundHistoryRev = snap.HistoryRevision
		ins.curSettings = nil
		ins.clearForm("Unassigned", "Select an action in the Action Catalog on the left to bind it to this slot.", false)
		return
	}

	if rendered.AutoBack {
		if ins.boundAutoBack && ins.boundContext == "" {
			ins.boundHistoryRev = snap.HistoryRevision
			return
		}
		ins.boundContext = ""
		ins.boundActionID = ""
		ins.boundAutoBack = true
		ins.boundHistoryRev = snap.HistoryRevision
		ins.curSettings = nil
		ins.clearForm("Auto Folder Back", "Automatic navigation button created inside folders when (0, 0) is empty.", false)
		return
	}

	cfg := rendered.Config
	if cfg == nil {
		return
	}

	sameBinding := ins.boundContext == cfg.Context &&
		ins.boundActionID == cfg.ActionUUID &&
		!ins.boundAutoBack &&
		ins.boundHistoryRev == snap.HistoryRevision
	ins.boundContext = cfg.Context
	ins.boundActionID = cfg.ActionUUID
	ins.boundAutoBack = false
	ins.boundHistoryRev = snap.HistoryRevision
	ins.curSettings = cfg.Settings

	if sameBinding {
		return
	}
	ins.rebuildForm(cfg, rendered.Action)
}

func (ins *ActionInspector) clearForm(title, message string, canTrigger bool) {
	ins.saveSectionOpenState()
	ins.accordion = nil
	ins.actionLbl.SetText(title)
	ins.metaLbl.SetText(message)
	ins.schemaForm.reset()
	ins.titleEntry = nil
	ins.accentColorCtrl = nil
	ins.bgColorCtrl = nil
	ins.bgImageCtrl = nil
	ins.hideTitleCheck = nil
	ins.hotkeyEntry = nil
	ins.hotkeyCaptureBtn = nil
	ins.openPIBtn = nil
	hint := widget.NewLabel("No configurable properties for this slot.")
	hint.Wrapping = fyne.TextWrapWord
	ins.formBox.Objects = []fyne.CanvasObject{hint, layout.NewSpacer()}
	ins.formBox.Refresh()

	ins.saveBtn.Disable()
	ins.clearBtn.Disable()
	if ins.moveBtn != nil {
		ins.moveBtn.Disable()
	}
	if ins.copyBtn != nil {
		ins.copyBtn.Disable()
	}
	if canTrigger {
		ins.triggerBtn.Enable()
	} else {
		ins.triggerBtn.Disable()
	}
}

func (ins *ActionInspector) rebuildForm(cfg *deck.SlotConfig, desc *plugin.ActionDescriptor) {
	ins.syncing = true
	defer func() { ins.syncing = false }()

	ins.saveSectionOpenState()
	ins.accordion = nil
	ins.schemaForm.reset()
	ins.hotkeyCaptureBtn = nil
	ins.openPIBtn = nil
	name := cfg.ActionUUID
	meta := string(plugin.ProviderBuiltin)
	var schema []plugin.SettingField
	if desc != nil {
		name = desc.Name
		meta = fmt.Sprintf("%s • %s", desc.Category, desc.Provider)
		if desc.Tooltip != "" {
			meta += "\n" + desc.Tooltip
		}
		schema = desc.SettingsSchema
	}

	ins.actionLbl.SetText(name)
	ins.metaLbl.SetText(meta)

	ins.titleEntry = widget.NewEntry()
	ins.titleEntry.SetPlaceHolder("Tile label")
	ins.titleEntry.SetText(cfg.Title)

	paletteBtn := widget.NewButton("Palette...", func() {
		if ins.onOpenPaletteSettings != nil {
			ins.onOpenPaletteSettings()
		}
	})
	accentHeader := container.NewBorder(nil, nil, nil, paletteBtn, widget.NewLabel("Accent Color"))
	ins.accentColorCtrl = newColorFieldControl(
		ins.win,
		"Accent Color",
		cfg.Color,
		ins.palette,
		ins.engine.AddColorSwatch,
		ins.reportStatus,
	)

	ins.bgColorCtrl = newColorFieldControl(
		ins.win,
		"Background Color",
		cfg.BackgroundColor,
		ins.palette,
		ins.engine.AddColorSwatch,
		ins.reportStatus,
	)

	var hotkeyRow fyne.CanvasObject
	if ins.selCtrl == deck.ControllerKeypad {
		ins.bgImageCtrl = newImageFieldControl(
			ins.win,
			cfg.BackgroundImage,
			func() string { return ins.assetsDir },
			ins.reportStatus,
		)
		ins.hideTitleCheck = widget.NewCheck("Hide Title on Button", nil)
		ins.hideTitleCheck.SetChecked(cfg.HideTitle)

		ins.hotkeyEntry = widget.NewEntry()
		ins.hotkeyEntry.SetPlaceHolder("e.g. Ctrl+Shift+F1 or F13 (global)")
		ins.hotkeyEntry.SetText(cfg.Hotkey)
		ins.hotkeyCaptureBtn = widget.NewButton("Capture", func() {
			ins.openHotkeyCapture("Capture Global Hotkey", ins.hotkeyEntry, captureModeGlobal)
		})
		hotkeyRow = container.NewBorder(nil, nil, nil, ins.hotkeyCaptureBtn, ins.hotkeyEntry)
	} else {
		ins.bgImageCtrl = nil
		ins.hideTitleCheck = nil
		ins.hotkeyEntry = nil
		ins.hotkeyCaptureBtn = nil
	}

	var items []fyne.CanvasObject
	if ins.sdCtrl != nil {
		if reqMajor, needsNode := ins.sdCtrl.NeedsNodeForAction(cfg.ActionUUID); needsNode {
			warnLbl := widget.NewLabel(fmt.Sprintf(
				"Node.js %d+ runtime is required for this external plugin to run. You can install or configure Node.js in Settings -> Plugins.",
				reqMajor,
			))
			warnLbl.Wrapping = fyne.TextWrapWord
			warnLbl.Importance = widget.WarningImportance
			installBtn := widget.NewButtonWithIcon("Install Node.js in Settings", theme.DownloadIcon(), func() {
				if ins.onOpenPluginsSettings != nil {
					ins.onOpenPluginsSettings()
				}
			})
			installBtn.Importance = widget.HighImportance
			items = append(items, warnLbl, installBtn, widget.NewSeparator())
		}
	}

	grouped := groupSchemaFields(schema)
	var accItems []*widget.AccordionItem

	genObjs := make([]fyne.CanvasObject, 0, 2+len(grouped.general)*2)
	genObjs = append(genObjs, widget.NewLabel("Title"), ins.titleEntry)
	genObjs = append(genObjs, ins.buildSchemaObjects(grouped.general, cfg.Settings)...)
	accItems = append(accItems, ins.makeAccordionSection(plugin.SectionGeneral, genObjs))

	for _, cg := range grouped.custom {
		accItems = append(accItems, ins.makeAccordionSection(cg.title, ins.buildSchemaObjects(cg.fields, cfg.Settings)))
	}

	appObjs := []fyne.CanvasObject{
		accentHeader,
		ins.accentColorCtrl.Object(),
		widget.NewLabel("Background Color"),
		ins.bgColorCtrl.Object(),
	}
	if ins.selCtrl == deck.ControllerKeypad {
		appObjs = append(appObjs,
			widget.NewLabel("Background Image"),
			ins.bgImageCtrl.Object(),
			ins.hideTitleCheck,
		)
	}
	appObjs = append(appObjs, ins.buildSchemaObjects(grouped.appearance, cfg.Settings)...)
	accItems = append(accItems, ins.makeAccordionSection(plugin.SectionAppearance, appObjs))

	if ins.selCtrl == deck.ControllerKeypad || len(grouped.trigger) > 0 {
		var trigObjs []fyne.CanvasObject
		if ins.selCtrl == deck.ControllerKeypad {
			trigObjs = append(trigObjs, widget.NewLabel("Global Hotkey"), hotkeyRow)
		}
		trigObjs = append(trigObjs, ins.buildSchemaObjects(grouped.trigger, cfg.Settings)...)
		accItems = append(accItems, ins.makeAccordionSection(plugin.SectionTrigger, trigObjs))
	}

	if len(grouped.advanced) > 0 {
		advObjs := ins.buildSchemaObjects(grouped.advanced, cfg.Settings)
		accItems = append(accItems, ins.makeAccordionSection(plugin.SectionAdvanced, advObjs))
	}

	ins.accordion = widget.NewAccordion(accItems...)
	ins.accordion.MultiOpen = true
	items = append(items, ins.accordion)

	if _, ok := ins.engine.PropertyInspectorSession(ins.selCtrl, ins.selCoords, ins.selDial); ok {
		ins.openPIBtn = widget.NewButtonWithIcon("Open Full Property Inspector", theme.DocumentCreateIcon(), func() {
			if ins.piViewer == nil {
				if ins.onStatus != nil {
					ins.onStatus("Property Inspector viewer is unavailable")
				}
				return
			}
			sess, ok := ins.engine.PropertyInspectorSession(ins.selCtrl, ins.selCoords, ins.selDial)
			if !ok {
				if ins.onStatus != nil {
					ins.onStatus("Property Inspector session is no longer available")
				}
				return
			}
			if err := ins.piViewer.Open(sess.Title, sess.URL); err != nil {
				if ins.onStatus != nil {
					ins.onStatus(fmt.Sprintf("Open Property Inspector failed: %v", err))
				}
				return
			}
			if ins.onStatus != nil {
				ins.onStatus("Opened Property Inspector: " + sess.Title)
			}
		})
		items = append(items, widget.NewSeparator(), ins.openPIBtn)
	}

	ins.formBox.Objects = items
	ins.formBox.Refresh()
	ins.formScroll.ScrollToTop()

	ins.saveBtn.Enable()
	ins.triggerBtn.Enable()
	ins.clearBtn.Enable()
	if ins.moveBtn != nil {
		ins.moveBtn.Enable()
	}
	if ins.copyBtn != nil {
		ins.copyBtn.Enable()
	}
}

func (ins *ActionInspector) saveSectionOpenState() {
	if ins.accordion == nil {
		return
	}
	if ins.sectionOpen == nil {
		ins.sectionOpen = make(map[string]bool)
	}
	for _, item := range ins.accordion.Items {
		if item != nil {
			ins.sectionOpen[strings.ToLower(item.Title)] = item.Open
		}
	}
}

func (ins *ActionInspector) isSectionOpen(title string) bool {
	if open, ok := ins.sectionOpen[strings.ToLower(title)]; ok {
		return open
	}
	return !strings.EqualFold(title, plugin.SectionAdvanced)
}

func (ins *ActionInspector) makeAccordionSection(title string, objs []fyne.CanvasObject) *widget.AccordionItem {
	item := widget.NewAccordionItem(title, container.NewPadded(container.NewVBox(objs...)))
	item.Open = ins.isSectionOpen(title)
	return item
}

func (ins *ActionInspector) buildSchemaObjects(fields []plugin.SettingField, settings map[string]any) []fyne.CanvasObject {
	return ins.schemaForm.buildFields(fields, settings)
}

// RefreshDynamicOptions repopulates any live SelectEntry dropdowns from registered dynamic providers.
func (ins *ActionInspector) RefreshDynamicOptions() {
	ins.schemaForm.refreshDynamicOptions()
}

func (ins *ActionInspector) handleSave() {
	if ins.syncing || ins.boundActionID == "" {
		return
	}
	title := ""
	if ins.titleEntry != nil {
		title = strings.TrimSpace(ins.titleEntry.Text)
	}
	var colHex string
	if ins.accentColorCtrl != nil {
		colHex = ins.accentColorCtrl.Value()
	}
	var bgColHex string
	if ins.bgColorCtrl != nil {
		bgColHex = ins.bgColorCtrl.Value()
	}
	var bgImage string
	if ins.bgImageCtrl != nil {
		bgImage = ins.bgImageCtrl.Value()
	}
	hideTitle := ins.hideTitleCheck != nil && ins.hideTitleCheck.Checked

	settings, err := ins.schemaForm.collectAndValidate()
	if err != nil {
		ins.reportStatus(err.Error())
		return
	}

	canonicalHotkey := ""
	if ins.selCtrl == deck.ControllerKeypad && ins.hotkeyEntry != nil {
		rawHK := strings.TrimSpace(ins.hotkeyEntry.Text)
		if rawHK != "" {
			spec, hkErr := hotkey.Parse(rawHK)
			if hkErr != nil {
				ins.reportStatus("Invalid hotkey: " + hkErr.Error())
				return
			}
			canonicalHotkey = spec.String()
			ins.hotkeyEntry.SetText(canonicalHotkey)
		}
	}

	appearance := app.SlotAppearance{
		Title:           title,
		Color:           colHex,
		BackgroundColor: bgColHex,
		BackgroundImage: bgImage,
		HideTitle:       hideTitle,
		SetHotkey:       true,
		Hotkey:          canonicalHotkey,
	}

	var saveErr error
	if ins.selCtrl == deck.ControllerEncoder {
		saveErr = ins.engine.UpdateDialSlotAppearance(ins.selDial, appearance, settings)
	} else {
		saveErr = ins.engine.UpdateKeypadSlotAppearance(ins.selCoords, appearance, settings)
	}
	if saveErr != nil {
		ins.reportStatus("Save failed: " + saveErr.Error())
		return
	}
	ins.reportStatus("Saved " + ins.actionLbl.Text)
}

func (ins *ActionInspector) handleTrigger() {
	if ins.boundActionID == "" {
		return
	}
	var err error
	if ins.selCtrl == deck.ControllerEncoder {
		err = ins.engine.PressDial(context.Background(), ins.selDial)
	} else {
		err = ins.engine.PressKey(context.Background(), ins.selCoords)
	}
	if err != nil {
		ins.reportStatus("Trigger failed: " + err.Error())
		return
	}
	ins.reportStatus("Triggered " + ins.actionLbl.Text)
}

func (ins *ActionInspector) handleClear() {
	var err error
	if ins.selCtrl == deck.ControllerEncoder {
		err = ins.engine.ClearDialSlot(ins.selDial)
	} else {
		err = ins.engine.ClearKeypadSlot(ins.selCoords)
	}
	if err != nil {
		ins.reportStatus("Clear failed: " + err.Error())
		return
	}
	ins.reportStatus("Cleared slot")
}

func (ins *ActionInspector) reportStatus(msg string) {
	if ins.onStatus != nil {
		ins.onStatus(msg)
	}
}

func (ins *ActionInspector) openHotkeyCapture(title string, target *widget.Entry, mode hotkeyCaptureMode) {
	if ins.win == nil || target == nil {
		return
	}
	ins.lastCapture = openHotkeyCaptureDialog(
		ins.win,
		title,
		target.Text,
		mode,
		ins.onSuspendHotkeys,
		func(captured string) {
			target.SetText(captured)
			if captured == "" {
				ins.reportStatus("Cleared shortcut — click Save to apply")
				return
			}
			ins.reportStatus("Captured " + captured + " — click Save to apply")
		},
	)
}
