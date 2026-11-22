package plugin

import (
	"context"

	"github.com/mrpoundsign/PoundDeck/pkg/deck"
)

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
	FieldImage     FieldType = "image"
	FieldFile      FieldType = "file"
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

// PropertyInspectorSession describes an HTML/WebView Property Inspector endpoint for a slot or dial.
type PropertyInspectorSession struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

// PropertyInspectorProvider can be implemented optionally by a Provider (such as .sdPlugin hosts)
// to provide an HTML Property Inspector URL for a bound action instance.
type PropertyInspectorProvider interface {
	PropertyInspectorURL(
		ctxID string,
		actionUUID string,
		controller deck.ControllerType,
		coords deck.Coordinates,
		dial int,
		settings map[string]any,
	) (PropertyInspectorSession, bool)
}
