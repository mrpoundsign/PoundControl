package plugin

import (
	"context"
	"strings"

	"github.com/mrpoundsign/PoundControl/pkg/deck"
)

// Canonical inspector section names recognized by PoundControl's Action Inspector.
// Plugins may use these constants (matched case-insensitively) to merge settings
// alongside built-in controls, or provide any custom section title.
const (
	SectionGeneral    = "General"
	SectionAppearance = "Appearance"
	SectionTrigger    = "Trigger"
	SectionAdvanced   = "Advanced"
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

// SettingField declares one configurable property of an action so PoundControl can render
// a native Obsidian-Slate inspector form automatically without HTML.
type SettingField struct {
	Key         string         `json:"key"`
	Label       string         `json:"label"`
	Type        FieldType      `json:"type"`
	Section     string         `json:"section,omitempty"`
	Default     any            `json:"default,omitempty"`
	Placeholder string         `json:"placeholder,omitempty"`
	Help        string         `json:"help,omitempty"`
	Options     []SelectOption `json:"options,omitempty"`
	OptionsKey  string         `json:"optionsKey,omitempty"`
	Required    bool           `json:"required,omitempty"`
}

// NormalizeSection maps raw (case-insensitively) to a canonical inspector section when it matches
// General (or empty), Appearance, Trigger, or Advanced; otherwise it returns the trimmed custom title and false.
func NormalizeSection(raw string) (canonical string, isCanonical bool) {
	trimmed := strings.TrimSpace(raw)
	switch strings.ToLower(trimmed) {
	case "", "general":
		return SectionGeneral, true
	case "appearance":
		return SectionAppearance, true
	case "trigger":
		return SectionTrigger, true
	case "advanced":
		return SectionAdvanced, true
	default:
		return trimmed, false
	}
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
