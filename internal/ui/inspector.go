package ui

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/mrpoundsign/PoundDeck/internal/app"
	"github.com/mrpoundsign/PoundDeck/pkg/deck"
	"github.com/mrpoundsign/PoundDeck/pkg/plugin"
)

type colorPreset struct {
	label string
	hex   string
}

var accentColorPresets = []colorPreset{
	{label: "Default", hex: ""},
	{label: "Sky Blue (#38BDF8)", hex: "#38BDF8"},
	{label: "Emerald (#22C55E)", hex: "#22C55E"},
	{label: "Amber (#FBBF24)", hex: "#FBBF24"},
	{label: "Rose (#EF4444)", hex: "#EF4444"},
	{label: "Violet (#A855F7)", hex: "#A855F7"},
	{label: "Slate (#94A3B8)", hex: "#94A3B8"},
}

// ActionInspector renders a schema-driven configuration form for the selected slot or dial.
type ActionInspector struct {
	engine   *app.Engine
	onStatus func(string)
	root     fyne.CanvasObject

	slotLbl    *widget.Label
	actionLbl  *widget.Label
	metaLbl    *widget.Label
	formScroll *container.Scroll
	formBox    *fyne.Container
	actionsRow *fyne.Container

	titleEntry  *widget.Entry
	colorSelect *widget.Select
	fields      map[string]inspectorFieldBinding
	saveBtn     *widget.Button
	triggerBtn  *widget.Button
	clearBtn    *widget.Button

	selCtrl       deck.ControllerType
	selCoords     deck.Coordinates
	selDial       int
	boundContext  string
	boundActionID string
	boundAutoBack bool
	curSettings   map[string]any
	syncing       bool
}

type inspectorFieldBinding struct {
	schema plugin.SettingField
	widget fyne.CanvasObject
	optMap map[string]string // label -> option value for FieldSelect
}

// NewActionInspector constructs an inspector panel bound to engine.
func NewActionInspector(engine *app.Engine, onStatus func(string)) *ActionInspector {
	ins := &ActionInspector{
		engine:   engine,
		onStatus: onStatus,
		fields:   make(map[string]inspectorFieldBinding),
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
	ins.actionsRow = container.NewGridWithColumns(3, ins.saveBtn, ins.triggerBtn, ins.clearBtn)

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

// SetSelection refreshes the inspector to reflect the currently selected slot or dial.
// Form controls are rebuilt only when the bound action context or action UUID changes so active input focus is preserved.
func (ins *ActionInspector) SetSelection(snap app.Snapshot, ctrl deck.ControllerType, coords deck.Coordinates, dial int) {
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
			return
		}
		ins.boundContext = ""
		ins.boundActionID = ""
		ins.boundAutoBack = false
		ins.curSettings = nil
		ins.clearForm("Unassigned", "Select an action in the Action Catalog on the left to bind it to this slot.", false)
		return
	}

	if rendered.AutoBack {
		if ins.boundAutoBack && ins.boundContext == "" {
			return
		}
		ins.boundContext = ""
		ins.boundActionID = ""
		ins.boundAutoBack = true
		ins.curSettings = nil
		ins.clearForm("Auto Folder Back", "Automatic navigation button created inside folders when (0, 0) is empty.", false)
		return
	}

	cfg := rendered.Config
	if cfg == nil {
		return
	}

	sameBinding := ins.boundContext == cfg.Context && ins.boundActionID == cfg.ActionUUID && !ins.boundAutoBack
	ins.boundContext = cfg.Context
	ins.boundActionID = cfg.ActionUUID
	ins.boundAutoBack = false
	ins.curSettings = cfg.Settings

	if sameBinding {
		return
	}
	ins.rebuildForm(cfg, rendered.Action)
}

func (ins *ActionInspector) clearForm(title, message string, canTrigger bool) {
	ins.actionLbl.SetText(title)
	ins.metaLbl.SetText(message)
	clear(ins.fields)
	ins.titleEntry = nil
	ins.colorSelect = nil
	hint := widget.NewLabel("No configurable properties for this slot.")
	hint.Wrapping = fyne.TextWrapWord
	ins.formBox.Objects = []fyne.CanvasObject{hint, layout.NewSpacer()}
	ins.formBox.Refresh()

	ins.saveBtn.Disable()
	ins.clearBtn.Disable()
	if canTrigger {
		ins.triggerBtn.Enable()
	} else {
		ins.triggerBtn.Disable()
	}
}

func (ins *ActionInspector) rebuildForm(cfg *deck.SlotConfig, desc *plugin.ActionDescriptor) {
	ins.syncing = true
	defer func() { ins.syncing = false }()

	clear(ins.fields)
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

	presetLabels := make([]string, len(accentColorPresets))
	selectedPreset := accentColorPresets[0].label
	for i, p := range accentColorPresets {
		presetLabels[i] = p.label
		if strings.EqualFold(p.hex, cfg.Color) {
			selectedPreset = p.label
		}
	}
	ins.colorSelect = widget.NewSelect(presetLabels, nil)
	ins.colorSelect.SetSelected(selectedPreset)

	items := []fyne.CanvasObject{
		widget.NewLabel("Title"),
		ins.titleEntry,
		widget.NewLabel("Accent Color"),
		ins.colorSelect,
	}

	if len(schema) > 0 {
		items = append(items, widget.NewSeparator())
	}

	for _, field := range schema {
		items = append(items, ins.buildSchemaField(field, cfg.Settings)...)
	}

	ins.formBox.Objects = items
	ins.formBox.Refresh()
	ins.formScroll.ScrollToTop()

	ins.saveBtn.Enable()
	ins.triggerBtn.Enable()
	ins.clearBtn.Enable()
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

	default: // FieldText, FieldNumber, FieldColor
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

func (ins *ActionInspector) selectedColorHex() string {
	if ins.colorSelect == nil {
		return ""
	}
	for _, p := range accentColorPresets {
		if p.label == ins.colorSelect.Selected {
			return p.hex
		}
	}
	return ""
}

func (ins *ActionInspector) handleSave() {
	if ins.syncing || ins.boundActionID == "" {
		return
	}
	title := ""
	if ins.titleEntry != nil {
		title = strings.TrimSpace(ins.titleEntry.Text)
	}
	colHex := ins.selectedColorHex()
	settings := ins.collectSettings()

	var err error
	if ins.selCtrl == deck.ControllerEncoder {
		err = ins.engine.UpdateDialSlot(ins.selDial, title, colHex, settings)
	} else {
		err = ins.engine.UpdateKeypadSlot(ins.selCoords, title, colHex, settings)
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
