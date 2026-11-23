package ui

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"github.com/mrpoundsign/PoundControl/internal/app"
	"github.com/mrpoundsign/PoundControl/pkg/deck"
	"github.com/mrpoundsign/PoundControl/pkg/plugin"
)

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

type schemaFieldBinding struct {
	schema           plugin.SettingField
	widget           fyne.CanvasObject
	control          any
	getValue         func() any
	optMap           map[string]string // label -> option value for FieldSelect
	clearSecretCheck *widget.Check     // non-nil for keyring-backed FieldPassword fields
	secretEntry      *widget.Entry     // non-nil for keyring-backed FieldPassword fields
}

type schemaFormOptions struct {
	win              fyne.Window
	engine           *app.Engine
	assetsDir        func() string
	palette          func() []deck.ColorSwatch
	onStatus         func(string)
	onCaptureHotkey  func(title string, target *widget.Entry)
	secretPluginUUID string // when non-empty, FieldPassword fields use engine's keyring secret store
}

type schemaFormState struct {
	opts               schemaFormOptions
	fields             map[string]schemaFieldBinding
	hotkeyFieldButtons map[string]*widget.Button
}

func newSchemaFormState(opts schemaFormOptions) *schemaFormState {
	return &schemaFormState{
		opts:               opts,
		fields:             make(map[string]schemaFieldBinding),
		hotkeyFieldButtons: make(map[string]*widget.Button),
	}
}

func (s *schemaFormState) reset() {
	clear(s.fields)
	clear(s.hotkeyFieldButtons)
}

func (s *schemaFormState) currentPalette() []deck.ColorSwatch {
	if s.opts.palette != nil {
		return s.opts.palette()
	}
	if s.opts.engine != nil {
		return s.opts.engine.ColorPalette()
	}
	return nil
}

func (s *schemaFormState) currentAssetsDir() string {
	if s.opts.assetsDir != nil {
		return s.opts.assetsDir()
	}
	return ""
}

func (s *schemaFormState) buildFields(fields []plugin.SettingField, settings map[string]any) []fyne.CanvasObject {
	out := make([]fyne.CanvasObject, 0, len(fields)*2)
	for _, field := range fields {
		out = append(out, s.buildField(field, settings)...)
	}
	return out
}

func (s *schemaFormState) buildField(field plugin.SettingField, settings map[string]any) []fyne.CanvasObject {
	val, exists := settings[field.Key]
	if !exists {
		val = field.Default
	}

	var out []fyne.CanvasObject
	switch field.Type {
	case plugin.FieldBool:
		chk := widget.NewCheck(field.Label, nil)
		chk.SetChecked(toBoolValue(val))
		s.fields[field.Key] = schemaFieldBinding{schema: field, widget: chk}
		out = append(out, chk)

	case plugin.FieldSelect:
		if field.OptionsKey != "" {
			var opts []plugin.SelectOption
			if s.opts.engine != nil {
				opts = s.opts.engine.DynamicOptions(context.Background(), field.OptionsKey, settings)
			}
			choices := make([]string, len(opts))
			for i, opt := range opts {
				choices[i] = opt.Value
			}
			se := widget.NewSelectEntry(choices)
			se.SetPlaceHolder(field.Placeholder)
			if val != nil {
				se.SetText(fmt.Sprint(val))
			}
			s.fields[field.Key] = schemaFieldBinding{schema: field, widget: se}
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
			s.fields[field.Key] = schemaFieldBinding{schema: field, widget: sel, optMap: optMap}
			out = append(out, widget.NewLabel(field.Label), sel)
		}

	case plugin.FieldMultiline:
		entry := widget.NewMultiLineEntry()
		entry.SetMinRowsVisible(4)
		entry.SetPlaceHolder(field.Placeholder)
		if val != nil {
			entry.SetText(fmt.Sprint(val))
		}
		s.fields[field.Key] = schemaFieldBinding{schema: field, widget: entry}
		out = append(out, widget.NewLabel(field.Label), entry)

	case plugin.FieldPassword:
		entry := widget.NewPasswordEntry()
		if s.opts.secretPluginUUID != "" && s.opts.engine != nil {
			hasSaved := s.opts.engine.HasGlobalSecret(s.opts.secretPluginUUID, field.Key)
			clearChk := widget.NewCheck("Clear saved "+field.Label, nil)
			if hasSaved {
				entry.SetPlaceHolder("•••••••• (saved in OS keyring — enter to replace)")
			} else {
				placeholder := field.Placeholder
				if placeholder == "" {
					placeholder = "Stored securely in OS keyring"
				}
				entry.SetPlaceHolder(placeholder)
				clearChk.Disable()
			}
			s.fields[field.Key] = schemaFieldBinding{
				schema:           field,
				widget:           entry,
				secretEntry:      entry,
				clearSecretCheck: clearChk,
			}
			out = append(out, widget.NewLabel(field.Label), entry, clearChk)
		} else {
			entry.SetPlaceHolder(field.Placeholder)
			if val != nil {
				entry.SetText(fmt.Sprint(val))
			}
			s.fields[field.Key] = schemaFieldBinding{schema: field, widget: entry}
			out = append(out, widget.NewLabel(field.Label), entry)
		}

	case plugin.FieldHotkey:
		entry := widget.NewEntry()
		entry.SetPlaceHolder(field.Placeholder)
		if val != nil {
			entry.SetText(fmt.Sprint(val))
		}
		capBtn := widget.NewButton("Capture", func() {
			if s.opts.onCaptureHotkey != nil {
				s.opts.onCaptureHotkey("Capture "+field.Label, entry)
			}
		})
		s.hotkeyFieldButtons[field.Key] = capBtn
		s.fields[field.Key] = schemaFieldBinding{schema: field, widget: entry}
		out = append(out, widget.NewLabel(field.Label), container.NewBorder(nil, nil, nil, capBtn, entry))

	case plugin.FieldColor:
		valStr := ""
		if val != nil {
			valStr = fmt.Sprint(val)
		}
		var addSwatch func(name, hex string) error
		if s.opts.engine != nil {
			addSwatch = s.opts.engine.AddColorSwatch
		}
		ctrl := newColorFieldControl(s.opts.win, field.Label, valStr, s.currentPalette(), addSwatch, s.opts.onStatus)
		s.fields[field.Key] = schemaFieldBinding{
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
		ctrl := newImageFieldControl(s.opts.win, valStr, s.currentAssetsDir, s.opts.onStatus)
		s.fields[field.Key] = schemaFieldBinding{
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
		ctrl := newFileFieldControl(s.opts.win, valStr, field.Placeholder)
		s.fields[field.Key] = schemaFieldBinding{
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
		s.fields[field.Key] = schemaFieldBinding{schema: field, widget: entry}
		out = append(out, widget.NewLabel(field.Label), entry)
	}

	if field.Help != "" {
		helpLbl := widget.NewLabel(field.Help)
		helpLbl.Wrapping = fyne.TextWrapWord
		out = append(out, helpLbl)
	}
	return out
}

func (s *schemaFormState) collectSettings() map[string]any {
	out := make(map[string]any, len(s.fields))
	for key, binding := range s.fields {
		if binding.secretEntry != nil {
			if binding.clearSecretCheck != nil && binding.clearSecretCheck.Checked {
				out[key] = ""
			} else if binding.secretEntry.Text != "" {
				out[key] = binding.secretEntry.Text
			}
			// Omitting untouched secret key preserves existing OS keyring entry.
			continue
		}
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

func (s *schemaFormState) collectAndValidate() (map[string]any, error) {
	settings := s.collectSettings()
	for key, binding := range s.fields {
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
			return nil, fmt.Errorf("invalid color %q for %s (expected #RRGGBB)", raw, binding.schema.Label)
		}
		settings[key] = norm
	}
	return settings, nil
}

func (s *schemaFormState) refreshSecretStates() {
	if s.opts.secretPluginUUID == "" || s.opts.engine == nil {
		return
	}
	for key, binding := range s.fields {
		if binding.secretEntry == nil {
			continue
		}
		binding.secretEntry.SetText("")
		hasSaved := s.opts.engine.HasGlobalSecret(s.opts.secretPluginUUID, key)
		if binding.clearSecretCheck != nil {
			binding.clearSecretCheck.SetChecked(false)
			if hasSaved {
				binding.clearSecretCheck.Enable()
			} else {
				binding.clearSecretCheck.Disable()
			}
		}
		if hasSaved {
			binding.secretEntry.SetPlaceHolder("•••••••• (saved in OS keyring — enter to replace)")
		} else {
			placeholder := binding.schema.Placeholder
			if placeholder == "" {
				placeholder = "Stored securely in OS keyring"
			}
			binding.secretEntry.SetPlaceHolder(placeholder)
		}
	}
}

func (s *schemaFormState) refreshPalette(swatches []deck.ColorSwatch) {
	for _, b := range s.fields {
		if ctrl, ok := b.control.(*colorFieldControl); ok && ctrl != nil {
			ctrl.RefreshPalette(swatches)
		}
	}
}

func (s *schemaFormState) refreshDynamicOptions() {
	if len(s.fields) == 0 || s.opts.engine == nil {
		return
	}
	cur := s.collectSettings()
	for _, binding := range s.fields {
		se, ok := binding.widget.(*widget.SelectEntry)
		if !ok || binding.schema.OptionsKey == "" {
			continue
		}
		opts := s.opts.engine.DynamicOptions(context.Background(), binding.schema.OptionsKey, cur)
		choices := make([]string, len(opts))
		for i, opt := range opts {
			choices[i] = opt.Value
		}
		se.SetOptions(choices)
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
