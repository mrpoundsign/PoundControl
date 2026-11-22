// Package plugin defines the unified plugin manifest, schema, runtime, and registry contracts
// shared across built-in Go plugins, Elgato .sdPlugin hosts, and wazero WebAssembly plugins.
package plugin

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/mrpoundsign/PoundControl/pkg/deck"
)

// ProviderKind identifies how a plugin is hosted and executed by PoundControl.
type ProviderKind string

const (
	// ProviderBuiltin denotes an in-process native Go plugin compiled with PoundControl.
	ProviderBuiltin ProviderKind = "builtin"
	// ProviderStreamDeck denotes an external Elgato SDK v2/v3 .sdPlugin process connected via WebSocket.
	ProviderStreamDeck ProviderKind = "sdplugin"
	// ProviderWasm denotes a sandboxed WebAssembly plugin executed by wazero.
	ProviderWasm ProviderKind = "wasm"
)

// ActionDescriptor describes a single assignable action exposed by a plugin.
type ActionDescriptor struct {
	UUID           string                `json:"uuid"`
	PluginUUID     string                `json:"pluginUuid"`
	Name           string                `json:"name"`
	Category       string                `json:"category,omitempty"`
	Tooltip        string                `json:"tooltip,omitempty"`
	Icon           string                `json:"icon,omitempty"`
	DefaultTitle   string                `json:"defaultTitle,omitempty"`
	DefaultColor   string                `json:"defaultColor,omitempty"`
	DefaultImage   string                `json:"defaultImage,omitempty"`
	HideTitle      bool                  `json:"hideTitle,omitempty"`
	Controllers    []deck.ControllerType `json:"controllers,omitempty"`
	SettingsSchema []SettingField        `json:"settingsSchema,omitempty"`
	Provider       ProviderKind          `json:"provider"`
}

// SupportsController reports whether the action can bind to the given controller type.
// When Controllers is empty, Keypad is assumed by convention.
func (a ActionDescriptor) SupportsController(c deck.ControllerType) bool {
	if len(a.Controllers) == 0 {
		return c == deck.ControllerKeypad
	}
	return slices.Contains(a.Controllers, c)
}

// Manifest describes a plugin and its available actions independently of its runtime backend.
type Manifest struct {
	UUID         string             `json:"uuid"`
	Name         string             `json:"name"`
	Version      string             `json:"version"`
	Author       string             `json:"author,omitempty"`
	Description  string             `json:"description,omitempty"`
	Icon         string             `json:"icon,omitempty"`
	CategoryIcon string             `json:"categoryIcon,omitempty"`
	Provider     ProviderKind       `json:"provider"`
	Actions      []ActionDescriptor `json:"actions"`
}

// Validate verifies required metadata and action uniqueness in a plugin manifest.
func (m Manifest) Validate() error {
	if strings.TrimSpace(m.UUID) == "" {
		return errors.New("plugin manifest UUID is required")
	}
	if strings.TrimSpace(m.Name) == "" {
		return fmt.Errorf("plugin %q manifest Name is required", m.UUID)
	}
	if m.Provider == "" {
		return fmt.Errorf("plugin %q manifest Provider is required", m.UUID)
	}
	seen := make(map[string]struct{}, len(m.Actions))
	for i, act := range m.Actions {
		uuid := strings.TrimSpace(act.UUID)
		if uuid == "" {
			return fmt.Errorf("plugin %q action[%d] UUID is required", m.UUID, i)
		}
		if strings.TrimSpace(act.Name) == "" {
			return fmt.Errorf("plugin %q action %q Name is required", m.UUID, uuid)
		}
		if _, dup := seen[uuid]; dup {
			return fmt.Errorf("plugin %q declares duplicate action UUID %q", m.UUID, uuid)
		}
		seen[uuid] = struct{}{}
	}
	return nil
}

// Provider supplies and executes actions from one plugin backend in the PoundControl registry.
type Provider interface {
	Kind() ProviderKind
	Name() string
	Manifests() []Manifest
	Actions() []ActionDescriptor
	BindHost(host HostAPI)
	Dispatch(ctx context.Context, ev ActionEvent) error
	Close() error
}
