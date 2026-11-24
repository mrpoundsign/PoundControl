package ui

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
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
	updateValue      func(any)
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
	onConfigAction   func(actionKey string, settings map[string]any) error
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
		ctrl := newFileFieldControl(s.opts.win, valStr, field.Placeholder, field.FileExtensions)
		s.fields[field.Key] = schemaFieldBinding{
			schema:   field,
			widget:   ctrl.entry,
			control:  ctrl,
			getValue: func() any { return ctrl.Value() },
		}
		out = append(out, widget.NewLabel(field.Label), ctrl.Object())

	case plugin.FieldInfo:
		out = append(out, s.buildInfoField(field, val))

	case plugin.FieldLink:
		out = append(out, s.buildLinkField(field, val))

	case plugin.FieldButton:
		out = append(out, s.buildButtonField(field, val))

	default: // FieldText, FieldNumber
		entry := widget.NewEntry()
		entry.SetPlaceHolder(field.Placeholder)
		if val != nil {
			entry.SetText(fmt.Sprint(val))
		}
		s.fields[field.Key] = schemaFieldBinding{schema: field, widget: entry}
		out = append(out, widget.NewLabel(field.Label), entry)
	}

	if field.Help != "" && !isDisplayOnlyFieldType(field.Type) {
		helpLbl := widget.NewLabel(field.Help)
		helpLbl.Wrapping = fyne.TextWrapWord
		out = append(out, helpLbl)
	}
	return out
}

func (s *schemaFormState) buildInfoField(field plugin.SettingField, val any) fyne.CanvasObject {
	fieldLbl := widget.NewLabel(field.Label)
	valLbl := widget.NewLabel("")
	valLbl.Wrapping = fyne.TextWrapWord

	var curText string
	copyBtn := widget.NewButtonWithIcon("Copy", theme.ContentCopyIcon(), func() {
		if curText == "" {
			return
		}
		if curApp := fyne.CurrentApp(); curApp != nil && curApp.Clipboard() != nil {
			curApp.Clipboard().SetContent(curText)
			if s.opts.onStatus != nil {
				s.opts.onStatus("Copied " + field.Label + " to clipboard")
			}
		}
	})

	rowItems := []fyne.CanvasObject{
		fieldLbl,
		container.NewBorder(nil, nil, nil, copyBtn, valLbl),
	}
	if field.Help != "" {
		helpLbl := widget.NewLabel(field.Help)
		helpLbl.Wrapping = fyne.TextWrapWord
		rowItems = append(rowItems, helpLbl)
	}
	rowBox := container.NewVBox(rowItems...)
	_, defaultCopyable := parseInfoFieldValue(field.Default, false)

	applyInfo := func(v any) {
		text, copyable := parseInfoFieldValue(v, defaultCopyable)
		curText = text
		valLbl.TextStyle = fyne.TextStyle{Bold: copyable, Monospace: copyable}
		valLbl.SetText(text)
		if copyable && text != "" {
			copyBtn.Show()
		} else {
			copyBtn.Hide()
		}
		if text == "" {
			rowBox.Hide()
		} else {
			rowBox.Show()
		}
		rowBox.Refresh()
	}
	applyInfo(val)

	s.fields[field.Key] = schemaFieldBinding{
		schema:      field,
		widget:      valLbl,
		control:     rowBox,
		updateValue: applyInfo,
	}
	return rowBox
}

func (s *schemaFormState) buildLinkField(field plugin.SettingField, val any) fyne.CanvasObject {
	fieldLbl := widget.NewLabel(field.Label)
	hl := widget.NewHyperlink("", nil)

	rowItems := []fyne.CanvasObject{fieldLbl, hl}
	if field.Help != "" {
		helpLbl := widget.NewLabel(field.Help)
		helpLbl.Wrapping = fyne.TextWrapWord
		rowItems = append(rowItems, helpLbl)
	}
	rowBox := container.NewVBox(rowItems...)

	applyLink := func(v any) {
		parsed, label := parseLinkFieldValue(v, field.Placeholder, field.Label)
		if parsed == nil {
			hl.SetText("")
			rowBox.Hide()
		} else {
			hl.SetText(label)
			hl.SetURL(parsed)
			rowBox.Show()
		}
		rowBox.Refresh()
	}
	applyLink(val)

	s.fields[field.Key] = schemaFieldBinding{
		schema:      field,
		widget:      hl,
		control:     rowBox,
		updateValue: applyLink,
	}
	return rowBox
}

func (s *schemaFormState) buildButtonField(field plugin.SettingField, val any) fyne.CanvasObject {
	var (
		btn            *widget.Button
		rowBox         *fyne.Container
		inFlight       bool
		pluginDisabled bool
	)

	applyButton := func(v any) {
		label, disabled := parseButtonFieldValue(v, field.Label)
		pluginDisabled = disabled
		btn.SetText(label)
		if inFlight || pluginDisabled {
			btn.Disable()
		} else {
			btn.Enable()
		}
		rowBox.Refresh()
	}

	btn = widget.NewButton(field.Label, func() {
		if inFlight || pluginDisabled {
			return
		}
		if s.opts.onConfigAction == nil {
			if s.opts.onStatus != nil {
				s.opts.onStatus("Config actions are not available")
			}
			return
		}
		cur := s.collectSettings()
		inFlight = true
		btnLabel := btn.Text
		btn.Disable()
		if s.opts.onStatus != nil {
			s.opts.onStatus("Running " + btnLabel + "...")
		}
		go func() {
			err := s.opts.onConfigAction(field.Key, cur)
			dispatchUI(func() {
				inFlight = false
				if s.opts.engine != nil && s.opts.secretPluginUUID != "" {
					latest := s.opts.engine.GlobalSettings(s.opts.secretPluginUUID)
					s.refreshDynamicFields(latest)
					s.refreshSecretStatesPreserveInput()
				}
				if pluginDisabled {
					btn.Disable()
				} else {
					btn.Enable()
				}
				if s.opts.onStatus != nil {
					if err != nil {
						s.opts.onStatus(fmt.Sprintf("%s failed: %v", btnLabel, err))
					} else {
						s.opts.onStatus(btnLabel + " completed")
					}
				}
			})
		}()
	})

	rowItems := []fyne.CanvasObject{btn}
	if field.Help != "" {
		helpLbl := widget.NewLabel(field.Help)
		helpLbl.Wrapping = fyne.TextWrapWord
		rowItems = append(rowItems, helpLbl)
	}
	rowBox = container.NewVBox(rowItems...)
	applyButton(val)

	s.fields[field.Key] = schemaFieldBinding{
		schema:      field,
		widget:      btn,
		control:     rowBox,
		updateValue: applyButton,
	}
	return rowBox
}

func (s *schemaFormState) collectSettings() map[string]any {
	out := make(map[string]any, len(s.fields))
	for key, binding := range s.fields {
		if isDisplayOnlyFieldType(binding.schema.Type) {
			continue
		}
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
	s.syncSecretStates(true)
}

func (s *schemaFormState) refreshSecretStatesPreserveInput() {
	s.syncSecretStates(false)
}

func (s *schemaFormState) syncSecretStates(clearInput bool) {
	if s.opts.secretPluginUUID == "" || s.opts.engine == nil {
		return
	}
	for key, binding := range s.fields {
		if binding.secretEntry == nil {
			continue
		}
		hasSaved := s.opts.engine.HasGlobalSecret(s.opts.secretPluginUUID, key)
		if clearInput {
			binding.secretEntry.SetText("")
			if binding.clearSecretCheck != nil {
				binding.clearSecretCheck.SetChecked(false)
			}
		}
		if binding.clearSecretCheck != nil {
			if hasSaved {
				binding.clearSecretCheck.Enable()
			} else {
				binding.clearSecretCheck.SetChecked(false)
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

func (s *schemaFormState) refreshDynamicFields(settings map[string]any) {
	for key, binding := range s.fields {
		if binding.updateValue == nil {
			continue
		}
		val, exists := settings[key]
		if !exists {
			val = binding.schema.Default
		}
		binding.updateValue(val)
	}
}

func (s *schemaFormState) setFieldValues(settings map[string]any) {
	for key, binding := range s.fields {
		val, exists := settings[key]
		if !exists {
			val = binding.schema.Default
		}
		if binding.updateValue != nil {
			binding.updateValue(val)
			continue
		}
		if binding.secretEntry != nil {
			continue
		}
		switch ctrl := binding.control.(type) {
		case *colorFieldControl:
			valStr := ""
			if val != nil {
				valStr = fmt.Sprint(val)
			}
			if ctrl.Value() != valStr {
				ctrl.SetValue(valStr)
			}
			continue
		case *imageFieldControl:
			valStr := ""
			if val != nil {
				valStr = fmt.Sprint(val)
			}
			if ctrl.Value() != valStr {
				ctrl.SetValue(valStr)
			}
			continue
		case *fileFieldControl:
			valStr := ""
			if val != nil {
				valStr = fmt.Sprint(val)
			}
			if ctrl.Value() != valStr {
				ctrl.SetValue(valStr)
			}
			continue
		}
		switch w := binding.widget.(type) {
		case *widget.Check:
			b := toBoolValue(val)
			if w.Checked != b {
				w.SetChecked(b)
			}
		case *widget.Select:
			curVal := ""
			if val != nil {
				curVal = fmt.Sprint(val)
			}
			for lbl, optVal := range binding.optMap {
				if optVal == curVal || lbl == curVal {
					if w.Selected != lbl {
						w.SetSelected(lbl)
					}
					break
				}
			}
		case *widget.SelectEntry:
			valStr := ""
			if val != nil {
				valStr = fmt.Sprint(val)
			}
			if w.Text != valStr {
				w.SetText(valStr)
			}
		case *widget.Entry:
			valStr := ""
			if val != nil {
				valStr = fmt.Sprint(val)
			}
			if w.Text != valStr {
				w.SetText(valStr)
			}
		}
	}
}

func isDisplayOnlyFieldType(t plugin.FieldType) bool {
	switch t {
	case plugin.FieldInfo, plugin.FieldLink, plugin.FieldButton:
		return true
	default:
		return false
	}
}

func parseInfoFieldValue(val any, defaultCopyable bool) (text string, copyable bool) {
	switch v := val.(type) {
	case nil:
		return "", defaultCopyable
	case string:
		return strings.TrimSpace(v), defaultCopyable
	case map[string]string:
		text = strings.TrimSpace(v["text"])
		if rawCopy, has := v["copyable"]; has {
			copyable = toBoolValue(rawCopy)
		} else {
			copyable = defaultCopyable
		}
		return text, copyable
	case map[string]any:
		if rawText, hasText := v["text"]; hasText && rawText != nil {
			text = strings.TrimSpace(fmt.Sprint(rawText))
		}
		if rawCopy, has := v["copyable"]; has {
			copyable = toBoolValue(rawCopy)
		} else {
			copyable = defaultCopyable
		}
		return text, copyable
	default:
		return strings.TrimSpace(fmt.Sprint(val)), defaultCopyable
	}
}

func parseLinkFieldValue(val any, placeholder, fallbackLabel string) (*url.URL, string) {
	var rawURL, label string
	switch v := val.(type) {
	case nil:
		return nil, ""
	case string:
		rawURL = strings.TrimSpace(v)
	case map[string]string:
		rawURL = strings.TrimSpace(v["url"])
		label = strings.TrimSpace(v["label"])
	case map[string]any:
		if u, ok := v["url"]; ok && u != nil {
			rawURL = strings.TrimSpace(fmt.Sprint(u))
		}
		if l, ok := v["label"]; ok && l != nil {
			label = strings.TrimSpace(fmt.Sprint(l))
		}
	default:
		rawURL = strings.TrimSpace(fmt.Sprint(val))
	}
	if rawURL == "" {
		return nil, ""
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, ""
	}
	scheme := strings.ToLower(parsed.Scheme)
	if (scheme != "http" && scheme != "https") || parsed.Host == "" {
		return nil, ""
	}
	if label == "" {
		label = strings.TrimSpace(placeholder)
	}
	if label == "" {
		label = strings.TrimSpace(fallbackLabel)
	}
	if label == "" {
		label = rawURL
	}
	return parsed, label
}

func parseButtonFieldValue(val any, fallbackLabel string) (label string, disabled bool) {
	label = strings.TrimSpace(fallbackLabel)
	switch v := val.(type) {
	case nil:
	case string:
		if t := strings.TrimSpace(v); t != "" {
			label = t
		}
	case map[string]string:
		if t := strings.TrimSpace(v["label"]); t != "" {
			label = t
		}
		disabled = toBoolValue(v["disabled"])
	case map[string]any:
		if l, ok := v["label"]; ok && l != nil {
			if t := strings.TrimSpace(fmt.Sprint(l)); t != "" {
				label = t
			}
		}
		disabled = toBoolValue(v["disabled"])
	default:
		if t := strings.TrimSpace(fmt.Sprint(val)); t != "" {
			label = t
		}
	}
	if label == "" {
		label = "Run"
	}
	return label, disabled
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
