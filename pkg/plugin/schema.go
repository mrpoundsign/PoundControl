package plugin

import "context"

// FieldType identifies the UI control type used to render a declarative setting field in Fyne.
type FieldType string

const (
	FieldText      FieldType = "text"
	FieldMultiline FieldType = "multiline"
	FieldPassword  FieldType = "password"
	FieldNumber    FieldType = "number"
	FieldBool      FieldType = "bool"
	FieldSelect    FieldType = "select"
	FieldColor     FieldType = "color"
	FieldHotkey    FieldType = "hotkey"
)

// SelectOption defines one labeled choice for a FieldSelect or live OptionsKey setting.
type SelectOption struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// SettingField declares one configurable property of an action so PoundDeck can render
// a native Obsidian-Slate inspector form automatically without HTML.
type SettingField struct {
	Key         string         `json:"key"`
	Label       string         `json:"label"`
	Type        FieldType      `json:"type"`
	Default     any            `json:"default,omitempty"`
	Placeholder string         `json:"placeholder,omitempty"`
	Help        string         `json:"help,omitempty"`
	Options     []SelectOption `json:"options,omitempty"`
	OptionsKey  string         `json:"optionsKey,omitempty"`
	Required    bool           `json:"required,omitempty"`
}

// DynamicOptionsProvider can be implemented optionally by a Provider to supply live inspector options
// (such as OBS scenes, audio inputs, or scene sources) for SettingField.OptionsKey.
type DynamicOptionsProvider interface {
	DynamicOptions(ctx context.Context, optionsKey string, currentSettings map[string]any) []SelectOption
}
