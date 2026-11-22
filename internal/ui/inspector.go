package ui

import (
	"context"
	"fmt"
	"slices"
	"strconv"
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
	hotkeyFieldButtons map[string]*widget.Button
	fields             map[string]inspectorFieldBinding
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

type inspectorFieldBinding struct {
	schema   plugin.SettingField
	widget   fyne.CanvasObject
	control  any
	getValue func() any
	optMap   map[string]string // label -> option value for FieldSelect
}

// NewActionInspector constructs an inspector panel bound to engine.
func NewActionInspector(win fyne.Window, engine *app.Engine, onStatus func(string)) *ActionInspector {
	ins := &ActionInspector{
		win:                win,
		engine:             engine,
		onStatus:           onStatus,
		sectionOpen:        make(map[string]bool),
		hotkeyFieldButtons: make(map[string]*widget.Button),
		fields:             make(map[string]inspectorFieldBinding),
	}

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
	for _, b := range ins.fields {
		if ctrl, ok := b.control.(*colorFieldControl); ok && ctrl != nil {
			ctrl.RefreshPalette(ins.palette)
		}
	}
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
	clear(ins.fields)
	clear(ins.hotkeyFieldButtons)
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
	clear(ins.fields)
	clear(ins.hotkeyFieldButtons)
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

type customFieldGroup struct {
	title  string
	fields []plugin.SettingField
}

type groupedSchemaFields struct {
	general    []plugin.SettingField
	custom     []customFieldGroup
	appearance []plugin.SettingField
	trigger    []plugin.SettingField
	advanced   []plugin.SettingField
}

func groupSchemaFields(schema []plugin.SettingField) groupedSchemaFields {
	var out groupedSchemaFields
	customIdx := make(map[string]int)
	for _, field := range schema {
		canon, isCanon := plugin.NormalizeSection(field.Section)
		if !isCanon {
			key := strings.ToLower(canon)
			if idx, ok := customIdx[key]; ok {
				out.custom[idx].fields = append(out.custom[idx].fields, field)
			} else {
				customIdx[key] = len(out.custom)
				out.custom = append(out.custom, customFieldGroup{
					title:  canon,
					fields: []plugin.SettingField{field},
				})
			}
			continue
		}
		switch canon {
		case plugin.SectionGeneral:
			out.general = append(out.general, field)
		case plugin.SectionAppearance:
			out.appearance = append(out.appearance, field)
		case plugin.SectionTrigger:
			out.trigger = append(out.trigger, field)
		case plugin.SectionAdvanced:
			out.advanced = append(out.advanced, field)
		}
	}
	return out
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
	out := make([]fyne.CanvasObject, 0, len(fields)*2)
	for _, field := range fields {
		out = append(out, ins.buildSchemaField(field, settings)...)
	}
	return out
}

func (ins *ActionInspector) buildSchemaField(field plugin.SettingField, settings map[string]any) []fyne.CanvasObject {
	val, exists := settings[field.Key]
	if !exists {
		val = field.Default
	}

	var out []fyne.CanvasObject
	switch field.Type {
	case plugin.FieldBool:
		chk := widget.NewCheck(field.Label, nil)
		chk.SetChecked(toBoolValue(val))
		ins.fields[field.Key] = inspectorFieldBinding{schema: field, widget: chk}
		out = append(out, chk)

	case plugin.FieldSelect:
		if field.OptionsKey != "" {
			opts := ins.engine.DynamicOptions(context.Background(), field.OptionsKey, settings)
			choices := make([]string, len(opts))
			for i, opt := range opts {
				choices[i] = opt.Value
			}
			se := widget.NewSelectEntry(choices)
			se.SetPlaceHolder(field.Placeholder)
			if val != nil {
				se.SetText(fmt.Sprint(val))
			}
			ins.fields[field.Key] = inspectorFieldBinding{schema: field, widget: se}
			out = append(out, widget.NewLabel(field.Label), se)
		} else {
			labels := make([]string, len(field.Options))
			optMap := make(map[string]string, len(field.Options))
			selLabel := ""
			curVal := fmt.Sprint(val)
			for i, opt := range field.Options {
				labels[i] = opt.Label
				optMap[opt.Label] = opt.Value
				if opt.Value == curVal || opt.Label == curVal {
					selLabel = opt.Label
				}
			}
			if selLabel == "" && len(labels) > 0 {
				selLabel = labels[0]
			}
			sel := widget.NewSelect(labels, nil)
			if selLabel != "" {
				sel.SetSelected(selLabel)
			}
			ins.fields[field.Key] = inspectorFieldBinding{schema: field, widget: sel, optMap: optMap}
			out = append(out, widget.NewLabel(field.Label), sel)
		}

	case plugin.FieldMultiline:
		entry := widget.NewMultiLineEntry()
		entry.SetMinRowsVisible(4)
		entry.SetPlaceHolder(field.Placeholder)
		if val != nil {
			entry.SetText(fmt.Sprint(val))
		}
		ins.fields[field.Key] = inspectorFieldBinding{schema: field, widget: entry}
		out = append(out, widget.NewLabel(field.Label), entry)

	case plugin.FieldPassword:
		entry := widget.NewPasswordEntry()
		entry.SetPlaceHolder(field.Placeholder)
		if val != nil {
			entry.SetText(fmt.Sprint(val))
		}
		ins.fields[field.Key] = inspectorFieldBinding{schema: field, widget: entry}
		out = append(out, widget.NewLabel(field.Label), entry)

	case plugin.FieldHotkey:
		entry := widget.NewEntry()
		entry.SetPlaceHolder(field.Placeholder)
		if val != nil {
			entry.SetText(fmt.Sprint(val))
		}
		capBtn := widget.NewButton("Capture", func() {
			ins.openHotkeyCapture("Capture "+field.Label, entry, captureModeChord)
		})
		ins.hotkeyFieldButtons[field.Key] = capBtn
		ins.fields[field.Key] = inspectorFieldBinding{schema: field, widget: entry}
		out = append(out, widget.NewLabel(field.Label), container.NewBorder(nil, nil, nil, capBtn, entry))

	case plugin.FieldColor:
		valStr := ""
		if val != nil {
			valStr = fmt.Sprint(val)
		}
		ctrl := newColorFieldControl(ins.win, field.Label, valStr, ins.palette, ins.engine.AddColorSwatch, ins.reportStatus)
		ins.fields[field.Key] = inspectorFieldBinding{
			schema:   field,
			widget:   ctrl.entry,
			control:  ctrl,
			getValue: func() any { return ctrl.Value() },
		}
		out = append(out, widget.NewLabel(field.Label), ctrl.Object())

	case plugin.FieldImage:
		valStr := ""
		if val != nil {
			valStr = fmt.Sprint(val)
		}
		ctrl := newImageFieldControl(ins.win, valStr, func() string { return ins.assetsDir }, ins.reportStatus)
		ins.fields[field.Key] = inspectorFieldBinding{
			schema:   field,
			widget:   ctrl.entry,
			control:  ctrl,
			getValue: func() any { return ctrl.Value() },
		}
		out = append(out, widget.NewLabel(field.Label), ctrl.Object())

	case plugin.FieldFile:
		valStr := ""
		if val != nil {
			valStr = fmt.Sprint(val)
		}
		ctrl := newFileFieldControl(ins.win, valStr, field.Placeholder)
		ins.fields[field.Key] = inspectorFieldBinding{
			schema:   field,
			widget:   ctrl.entry,
			control:  ctrl,
			getValue: func() any { return ctrl.Value() },
		}
		out = append(out, widget.NewLabel(field.Label), ctrl.Object())

	default: // FieldText, FieldNumber
		entry := widget.NewEntry()
		entry.SetPlaceHolder(field.Placeholder)
		if val != nil {
			entry.SetText(fmt.Sprint(val))
		}
		ins.fields[field.Key] = inspectorFieldBinding{schema: field, widget: entry}
		out = append(out, widget.NewLabel(field.Label), entry)
	}

	if field.Help != "" {
		helpLbl := widget.NewLabel(field.Help)
		helpLbl.Wrapping = fyne.TextWrapWord
		out = append(out, helpLbl)
	}
	return out
}

func (ins *ActionInspector) collectSettings() map[string]any {
	out := make(map[string]any, len(ins.fields))
	for key, binding := range ins.fields {
		if binding.getValue != nil {
			out[key] = binding.getValue()
			continue
		}
		switch w := binding.widget.(type) {
		case *widget.Check:
			out[key] = w.Checked
		case *widget.Select:
			if v, ok := binding.optMap[w.Selected]; ok {
				out[key] = v
			} else {
				out[key] = w.Selected
			}
		case *widget.SelectEntry:
			out[key] = strings.TrimSpace(w.Text)
		case *widget.Entry:
			if binding.schema.Type == plugin.FieldNumber {
				out[key] = parseNumberValue(w.Text, binding.schema.Default)
			} else {
				out[key] = w.Text
			}
		}
	}
	return out
}

// RefreshDynamicOptions repopulates any live SelectEntry dropdowns from registered dynamic providers.
func (ins *ActionInspector) RefreshDynamicOptions() {
	if len(ins.fields) == 0 {
		return
	}
	cur := ins.collectSettings()
	for _, binding := range ins.fields {
		se, ok := binding.widget.(*widget.SelectEntry)
		if !ok || binding.schema.OptionsKey == "" {
			continue
		}
		opts := ins.engine.DynamicOptions(context.Background(), binding.schema.OptionsKey, cur)
		choices := make([]string, len(opts))
		for i, opt := range opts {
			choices[i] = opt.Value
		}
		se.SetOptions(choices)
	}
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

	settings := ins.collectSettings()
	for key, binding := range ins.fields {
		if binding.schema.Type != plugin.FieldColor {
			continue
		}
		raw, _ := settings[key].(string)
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			settings[key] = ""
			continue
		}
		norm, ok := deck.NormalizeHexColor(trimmed)
		if !ok {
			ins.reportStatus(fmt.Sprintf("Invalid color %q for %s (expected #RRGGBB)", raw, binding.schema.Label))
			return
		}
		settings[key] = norm
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

	var err error
	if ins.selCtrl == deck.ControllerEncoder {
		err = ins.engine.UpdateDialSlotAppearance(ins.selDial, appearance, settings)
	} else {
		err = ins.engine.UpdateKeypadSlotAppearance(ins.selCoords, appearance, settings)
	}
	if err != nil {
		ins.reportStatus("Save failed: " + err.Error())
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

func toBoolValue(v any) bool {
	switch b := v.(type) {
	case bool:
		return b
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(b))
		return err == nil && parsed
	default:
		return false
	}
}

func parseNumberValue(raw string, fallback any) any {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return fallback
	}
	if iv, err := strconv.Atoi(trimmed); err == nil {
		return iv
	}
	if fv, err := strconv.ParseFloat(trimmed, 64); err == nil {
		return fv
	}
	return fallback
}
