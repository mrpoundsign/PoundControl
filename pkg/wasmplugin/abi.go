// Package wasmplugin hosts sandboxed, OS-agnostic WebAssembly (.wasm) plugins using pure-Go wazero.
package wasmplugin

import (
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/mrpoundsign/PoundControl/pkg/deck"
	"github.com/mrpoundsign/PoundControl/pkg/plugin"
)

const (
	// HostPluginUUID is the Engine GlobalSettings namespace storing WebAssembly plugin search paths.
	HostPluginUUID = "com.github.mrpoundsign.poundcontrol.wasm"

	// HostModuleName is the Wasm import module name exported to plugins by PoundControl.
	HostModuleName = "poundcontrol"

	maxMemoryPages       = 512 // 32 MiB linear memory cap per plugin module
	defaultCallTimeout   = 5 * time.Second
	maxHTTPResponseBytes = 256 * 1024
	maxHTTPTimeout       = 15 * time.Second
	defaultHTTPTimeout   = 10 * time.Second
	minTimerInterval     = 250 * time.Millisecond
	maxTimersPerPlugin   = 16
	maxGuestPayloadBytes = 4 * 1024 * 1024
)

// GuestManifest is the JSON manifest returned by a Wasm plugin's pc_manifest export.
type GuestManifest struct {
	UUID                 string                `json:"uuid"`
	Name                 string                `json:"name"`
	Version              string                `json:"version"`
	Author               string                `json:"author,omitempty"`
	Description          string                `json:"description,omitempty"`
	Icon                 string                `json:"icon,omitempty"`
	CategoryIcon         string                `json:"categoryIcon,omitempty"`
	Permissions          GuestPermissions      `json:"permissions,omitzero"`
	GlobalSettingsSchema []plugin.SettingField `json:"globalSettingsSchema,omitempty"`
	Actions              []GuestAction         `json:"actions"`
}

// GuestPermissions declares explicit sandbox capabilities requested by a Wasm plugin.
type GuestPermissions struct {
	AllowedHosts []string `json:"allowedHosts,omitempty"`
}

// GuestAction describes an action exposed by a Wasm plugin.
type GuestAction struct {
	UUID           string                `json:"uuid"`
	Name           string                `json:"name"`
	Category       string                `json:"category,omitempty"`
	Tooltip        string                `json:"tooltip,omitempty"`
	Icon           string                `json:"icon,omitempty"`
	DefaultTitle   string                `json:"defaultTitle,omitempty"`
	DefaultColor   string                `json:"defaultColor,omitempty"`
	DefaultImage   string                `json:"defaultImage,omitempty"`
	HideTitle      bool                  `json:"hideTitle,omitempty"`
	Controllers    []deck.ControllerType `json:"controllers,omitempty"`
	SettingsSchema []plugin.SettingField `json:"settingsSchema,omitempty"`
}

// GuestHTTPRequest is passed as JSON from a Wasm plugin to pc_http_request.
type GuestHTTPRequest struct {
	Method    string            `json:"method,omitempty"`
	URL       string            `json:"url"`
	Headers   map[string]string `json:"headers,omitempty"`
	Body      string            `json:"body,omitempty"`
	TimeoutMs int               `json:"timeoutMs,omitempty"`
}

// GuestHTTPResponse is returned as JSON from pc_http_request back into guest memory.
type GuestHTTPResponse struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body,omitempty"`
	Error   string            `json:"error,omitempty"`
}

// GuestTimerEvent is dispatched to a Wasm plugin's pc_on_timer export when a host interval fires.
type GuestTimerEvent struct {
	TimerID string `json:"timerId"`
	Context string `json:"context"`
}

// GuestNavCommand lets a Wasm plugin request page, folder, or profile navigation.
type GuestNavCommand struct {
	Action     string `json:"action"` // "nextPage", "prevPage", "goToPage", "openFolder", "closeFolder", "switchProfile"
	Page       int    `json:"page,omitempty"`
	FolderID   string `json:"folderId,omitempty"`
	FolderName string `json:"folderName,omitempty"`
	ProfileRef string `json:"profileRef,omitempty"`
}

// GuestConfigActionEvent is dispatched to a Wasm plugin's pc_on_config_action export
// when the user clicks a FieldButton in the plugin's Settings tab.
type GuestConfigActionEvent struct {
	Action   string         `json:"action"`
	Settings map[string]any `json:"settings,omitempty"`
}

func (gm GuestManifest) toPluginManifest(pluginDir string) plugin.Manifest {
	actions := make([]plugin.ActionDescriptor, 0, len(gm.Actions))
	for _, ga := range gm.Actions {
		ctrls := make([]deck.ControllerType, len(ga.Controllers))
		copy(ctrls, ga.Controllers)
		if len(ctrls) == 0 {
			ctrls = []deck.ControllerType{deck.ControllerKeypad}
		}
		schema := make([]plugin.SettingField, len(ga.SettingsSchema))
		copy(schema, ga.SettingsSchema)

		cat := strings.TrimSpace(ga.Category)
		if cat == "" {
			cat = gm.Name
		}
		actions = append(actions, plugin.ActionDescriptor{
			UUID:           strings.TrimSpace(ga.UUID),
			PluginUUID:     strings.TrimSpace(gm.UUID),
			Name:           strings.TrimSpace(ga.Name),
			Category:       cat,
			Tooltip:        strings.TrimSpace(ga.Tooltip),
			Icon:           sanitizeWasmImageURI(ga.Icon, pluginDir),
			DefaultTitle:   strings.TrimSpace(ga.DefaultTitle),
			DefaultColor:   strings.TrimSpace(ga.DefaultColor),
			DefaultImage:   sanitizeWasmImageURI(ga.DefaultImage, pluginDir),
			HideTitle:      ga.HideTitle,
			Controllers:    ctrls,
			SettingsSchema: schema,
			Provider:       plugin.ProviderWasm,
		})
	}
	var globalSchema []plugin.SettingField
	if len(gm.GlobalSettingsSchema) > 0 {
		globalSchema = make([]plugin.SettingField, len(gm.GlobalSettingsSchema))
		copy(globalSchema, gm.GlobalSettingsSchema)
	}
	return plugin.Manifest{
		UUID:                 strings.TrimSpace(gm.UUID),
		Name:                 strings.TrimSpace(gm.Name),
		Version:              strings.TrimSpace(gm.Version),
		Author:               strings.TrimSpace(gm.Author),
		Description:          strings.TrimSpace(gm.Description),
		Icon:                 sanitizeWasmImageURI(gm.Icon, pluginDir),
		CategoryIcon:         sanitizeWasmImageURI(gm.CategoryIcon, pluginDir),
		Provider:             plugin.ProviderWasm,
		GlobalSettingsSchema: globalSchema,
		Actions:              actions,
	}
}

// sanitizeWasmImageURI restricts Wasm plugin image URIs to inline data:image/* URIs
// or relative paths that stay strictly within pluginDir without escaping via ".." or symlinks.
func sanitizeWasmImageURI(raw, pluginDir string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(trimmed), "data:image/") {
		return trimmed
	}
	// Reject schemes (file:, http:, https:, non-image data:), Windows drive letters, UNC/absolute paths.
	if strings.Contains(trimmed, ":") ||
		strings.HasPrefix(trimmed, "/") ||
		strings.HasPrefix(trimmed, `\`) ||
		filepath.IsAbs(trimmed) {
		return ""
	}
	dirTrimmed := strings.TrimSpace(pluginDir)
	if dirTrimmed == "" {
		return ""
	}
	baseAbs, err := filepath.Abs(dirTrimmed)
	if err != nil {
		return ""
	}
	joined := filepath.Clean(filepath.Join(baseAbs, filepath.FromSlash(trimmed)))
	if !isSubpath(baseAbs, joined, false) {
		return ""
	}
	if evalBase, err := filepath.EvalSymlinks(baseAbs); err == nil {
		if evalTarget, err := filepath.EvalSymlinks(joined); err == nil {
			if !isSubpath(evalBase, evalTarget, false) {
				return ""
			}
		} else if evalParent, err := filepath.EvalSymlinks(filepath.Dir(joined)); err == nil {
			if !isSubpath(evalBase, evalParent, true) {
				return ""
			}
		}
	}
	return joined
}

func isSubpath(baseDir, target string, allowEqual bool) bool {
	rel, err := filepath.Rel(baseDir, target)
	if err != nil {
		return false
	}
	if rel == "." {
		return allowEqual
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return false
	}
	return true
}

// isHostAllowed verifies rawURL uses http/https and matches at least one explicit entry in allowedHosts.
// Patterns accept exact hostnames ("api.example.com"), host:port ("127.0.0.1:8080"), or wildcard subdomains ("*.example.com").
func isHostAllowed(rawURL string, allowedHosts []string) bool {
	if len(allowedHosts) == 0 {
		return false
	}
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return false
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return false
	}
	hostname := strings.ToLower(parsed.Hostname())
	hostPort := strings.ToLower(parsed.Host)
	if hostname == "" || parsed.User != nil {
		return false
	}

	for _, rawPattern := range allowedHosts {
		pat := strings.ToLower(strings.TrimSpace(rawPattern))
		if pat == "" {
			continue
		}
		switch {
		case strings.Contains(pat, ":"):
			if hostPort == pat {
				return true
			}
		case strings.HasPrefix(pat, "*."):
			suffix := pat[1:] // ".example.com"
			if len(hostname) > len(suffix) && strings.HasSuffix(hostname, suffix) {
				return true
			}
		default:
			if hostname == pat {
				return true
			}
		}
	}
	return false
}
