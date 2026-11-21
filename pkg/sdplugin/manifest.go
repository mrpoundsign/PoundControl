// Package sdplugin implements the Elgato Stream Deck SDK v2/v3 plugin host,
// HTML Property Inspector bridge, static schema extractor, and managed Node.js runtime.
package sdplugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"github.com/mrpoundsign/PoundDeck/pkg/deck"
	"github.com/mrpoundsign/PoundDeck/pkg/plugin"
)

// HostPluginUUID identifies PoundDeck's .sdPlugin host configuration in PluginSettings.
const HostPluginUUID = "com.github.mrpoundsign.pounddeck.sdplugin"

// NodejsConfig declares the Node.js runtime requirements in an Elgato manifest.json.
type NodejsConfig struct {
	Version string `json:"Version"`
	Debug   string `json:"Debug,omitempty"`
}

// OSRequirement declares OS compatibility entries in an Elgato manifest.json.
type OSRequirement struct {
	Platform       string `json:"Platform"`
	MinimumVersion string `json:"MinimumVersion,omitempty"`
}

// SDActionState configures one visual state on a Stream Deck action.
type SDActionState struct {
	Image          string          `json:"Image,omitempty"`
	Title          string          `json:"Title,omitempty"`
	ShowTitle      *bool           `json:"ShowTitle,omitempty"`
	TitleColor     string          `json:"TitleColor,omitempty"`
	TitleAlignment string          `json:"TitleAlignment,omitempty"`
	FontSize       json.RawMessage `json:"FontSize,omitempty"`
}

// SDEncoder configures rotary dial metadata on a Stream Deck+ action.
type SDEncoder struct {
	Layout             string            `json:"layout,omitempty"`
	StackColor         string            `json:"StackColor,omitempty"`
	Icon               string            `json:"Icon,omitempty"`
	TriggerDescription map[string]string `json:"TriggerDescription,omitempty"`
}

// SDAction describes one action declared in an Elgato .sdPlugin manifest.json.
type SDAction struct {
	UUID                  string          `json:"UUID"`
	Name                  string          `json:"Name"`
	Tooltip               string          `json:"Tooltip,omitempty"`
	Icon                  string          `json:"Icon,omitempty"`
	PropertyInspectorPath string          `json:"PropertyInspectorPath,omitempty"`
	Controllers           []string        `json:"Controllers,omitempty"`
	States                []SDActionState `json:"States,omitempty"`
	Encoder               *SDEncoder      `json:"Encoder,omitempty"`
	VisibleInActionsList  *bool           `json:"VisibleInActionsList,omitempty"`
}

// SDManifest models an Elgato Stream Deck SDK v2/v3 plugin manifest.json.
type SDManifest struct {
	SDKVersion            int             `json:"SDKVersion"`
	UUID                  string          `json:"UUID"`
	Name                  string          `json:"Name"`
	Author                string          `json:"Author,omitempty"`
	Version               string          `json:"Version,omitempty"`
	Description           string          `json:"Description,omitempty"`
	Category              string          `json:"Category,omitempty"`
	CategoryIcon          string          `json:"CategoryIcon,omitempty"`
	Icon                  string          `json:"Icon,omitempty"`
	CodePath              string          `json:"CodePath"`
	CodePathWin           string          `json:"CodePathWin,omitempty"`
	CodePathMac           string          `json:"CodePathMac,omitempty"`
	CodePathLinux         string          `json:"CodePathLinux,omitempty"`
	PropertyInspectorPath string          `json:"PropertyInspectorPath,omitempty"`
	Nodejs                *NodejsConfig   `json:"Nodejs,omitempty"`
	OS                    []OSRequirement `json:"OS,omitempty"`
	Actions               []SDAction      `json:"Actions"`
}

// ResolveCodePath picks the best executable or script entrypoint for the given GOOS.
func (m SDManifest) ResolveCodePath(goos string) string {
	switch goos {
	case "windows":
		if strings.TrimSpace(m.CodePathWin) != "" {
			return strings.TrimSpace(m.CodePathWin)
		}
	case "darwin":
		if strings.TrimSpace(m.CodePathMac) != "" {
			return strings.TrimSpace(m.CodePathMac)
		}
	case "linux":
		if strings.TrimSpace(m.CodePathLinux) != "" {
			return strings.TrimSpace(m.CodePathLinux)
		}
	}
	return strings.TrimSpace(m.CodePath)
}

// IsNodeJS reports whether entrypoint codePath should be executed via Node.js.
func (m SDManifest) IsNodeJS(codePath string) bool {
	if m.Nodejs != nil {
		return true
	}
	ext := strings.ToLower(filepath.Ext(strings.TrimSpace(codePath)))
	return ext == ".js" || ext == ".mjs" || ext == ".cjs"
}

// MinNodeMajor returns the minimum required Node.js major version (defaulting to 20).
func (m SDManifest) MinNodeMajor() int {
	if m.Nodejs == nil {
		return 20
	}
	raw := strings.TrimPrefix(strings.TrimSpace(m.Nodejs.Version), "v")
	if raw == "" {
		return 20
	}
	if dot := strings.IndexByte(raw, '.'); dot >= 0 {
		raw = raw[:dot]
	}
	major, err := strconv.Atoi(raw)
	if err != nil || major <= 0 {
		return 20
	}
	return major
}

// LoadedBundle represents a parsed and validated .sdPlugin bundle on disk.
type LoadedBundle struct {
	UUID         string
	Dir          string
	CodePath     string
	CodeAbsPath  string
	IsNode       bool
	MinNodeMajor int
	Manifest     SDManifest
	Actions      []plugin.ActionDescriptor
	ActionPI     map[string]string // actionUUID -> clean relative PI path
}

// LoadBundle reads manifest.json (and optional pounddeck.schema.json) from bundleDir.
func LoadBundle(bundleDir string) (*LoadedBundle, error) {
	absDir, err := filepath.Abs(strings.TrimSpace(bundleDir))
	if err != nil {
		return nil, fmt.Errorf("resolve plugin bundle path: %w", err)
	}
	info, err := os.Stat(absDir)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("plugin bundle directory not found: %s", absDir)
	}

	manifestPath := filepath.Join(absDir, "manifest.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("read manifest.json in %s: %w", absDir, err)
	}

	var m SDManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", manifestPath, err)
	}

	m.UUID = strings.TrimSpace(m.UUID)
	if m.UUID == "" {
		base := filepath.Base(absDir)
		m.UUID = strings.TrimSuffix(base, ".sdPlugin")
		m.UUID = strings.TrimSuffix(m.UUID, ".sdplugin")
	}
	if m.UUID == "" {
		return nil, fmt.Errorf("manifest in %s is missing UUID", absDir)
	}
	if strings.TrimSpace(m.Name) == "" {
		return nil, fmt.Errorf("manifest %q is missing Name", m.UUID)
	}
	if len(m.Actions) == 0 {
		return nil, fmt.Errorf("manifest %q declares no Actions", m.UUID)
	}

	codeRel := m.ResolveCodePath(runtime.GOOS)
	if codeRel == "" {
		return nil, fmt.Errorf("manifest %q is missing CodePath", m.UUID)
	}
	codeAbs, ok := ResolveAssetPath(absDir, codeRel)
	if !ok {
		return nil, fmt.Errorf("plugin %q CodePath %q escapes bundle root", m.UUID, codeRel)
	}
	if _, err := os.Stat(codeAbs); err != nil {
		return nil, fmt.Errorf("plugin %q entrypoint %q not found: %w", m.UUID, codeRel, err)
	}

	customSchema, err := loadSidecarSchema(filepath.Join(absDir, "pounddeck.schema.json"))
	if err != nil {
		return nil, fmt.Errorf("plugin %q pounddeck.schema.json: %w", m.UUID, err)
	}

	htmlCache := make(map[string][]byte)
	actionPI := make(map[string]string, len(m.Actions))
	descs := make([]plugin.ActionDescriptor, 0, len(m.Actions))

	cat := strings.TrimSpace(m.Category)
	if cat == "" {
		cat = strings.TrimSpace(m.Name)
	}

	for _, act := range m.Actions {
		actUUID := strings.TrimSpace(act.UUID)
		if actUUID == "" {
			return nil, fmt.Errorf("plugin %q contains an action with empty UUID", m.UUID)
		}
		if act.VisibleInActionsList != nil && !*act.VisibleInActionsList {
			continue
		}

		controllers := parseControllers(act.Controllers)
		piRel := cleanBundleRelPath(act.PropertyInspectorPath)
		if piRel == "" {
			piRel = cleanBundleRelPath(m.PropertyInspectorPath)
		}
		if piRel != "" {
			if _, safe := ResolveAssetPath(absDir, piRel); safe {
				actionPI[actUUID] = piRel
			} else {
				piRel = ""
			}
		}

		schema := customSchema[actUUID]
		if len(schema) == 0 && piRel != "" {
			if piAbs, safe := ResolveAssetPath(absDir, piRel); safe {
				htmlBytes, cached := htmlCache[piAbs]
				if !cached {
					if data, readErr := os.ReadFile(piAbs); readErr == nil {
						htmlBytes = data
						htmlCache[piAbs] = data
					}
				}
				if len(htmlBytes) > 0 {
					schema = ExtractHTMLSchema(htmlBytes, actUUID)
				}
			}
		}

		defImg := ""
		if len(act.States) > 0 && strings.TrimSpace(act.States[0].Image) != "" {
			if imgAbs, safe := ResolveAssetPath(absDir, act.States[0].Image); safe {
				defImg = imgAbs
			}
		}
		if defImg == "" && strings.TrimSpace(act.Icon) != "" {
			if imgAbs, safe := ResolveAssetPath(absDir, act.Icon); safe {
				defImg = imgAbs
			}
		}

		hideTitle := len(act.States) > 0 && act.States[0].ShowTitle != nil && !*act.States[0].ShowTitle
		defTitle := strings.TrimSpace(act.Name)
		if len(act.States) > 0 && strings.TrimSpace(act.States[0].Title) != "" {
			defTitle = strings.TrimSpace(act.States[0].Title)
		}

		descs = append(descs, plugin.ActionDescriptor{
			UUID:           actUUID,
			PluginUUID:     m.UUID,
			Name:           strings.TrimSpace(act.Name),
			Category:       cat,
			Tooltip:        strings.TrimSpace(act.Tooltip),
			DefaultTitle:   defTitle,
			DefaultColor:   "#38BDF8",
			DefaultImage:   defImg,
			HideTitle:      hideTitle,
			Controllers:    controllers,
			SettingsSchema: schema,
			Provider:       plugin.ProviderStreamDeck,
		})
	}

	if len(descs) == 0 {
		return nil, fmt.Errorf("plugin %q has no visible actions", m.UUID)
	}

	return &LoadedBundle{
		UUID:         m.UUID,
		Dir:          absDir,
		CodePath:     codeRel,
		CodeAbsPath:  codeAbs,
		IsNode:       m.IsNodeJS(codeRel),
		MinNodeMajor: m.MinNodeMajor(),
		Manifest:     m,
		Actions:      descs,
		ActionPI:     actionPI,
	}, nil
}

// ToPluginManifest converts this bundle into PoundDeck's unified plugin.Manifest.
func (b *LoadedBundle) ToPluginManifest() plugin.Manifest {
	return plugin.Manifest{
		UUID:        b.UUID,
		Name:        b.Manifest.Name,
		Version:     b.Manifest.Version,
		Author:      b.Manifest.Author,
		Description: b.Manifest.Description,
		Provider:    plugin.ProviderStreamDeck,
		Actions:     slices.Clone(b.Actions),
	}
}

func loadSidecarSchema(path string) (map[string][]plugin.SettingField, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var parsed map[string][]plugin.SettingField
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, err
	}
	return parsed, nil
}

func parseControllers(raw []string) []deck.ControllerType {
	if len(raw) == 0 {
		return []deck.ControllerType{deck.ControllerKeypad}
	}
	out := make([]deck.ControllerType, 0, len(raw))
	for _, c := range raw {
		switch strings.ToLower(strings.TrimSpace(c)) {
		case "encoder":
			if !slices.Contains(out, deck.ControllerEncoder) {
				out = append(out, deck.ControllerEncoder)
			}
		case "keypad":
			if !slices.Contains(out, deck.ControllerKeypad) {
				out = append(out, deck.ControllerKeypad)
			}
		}
	}
	if len(out) == 0 {
		return []deck.ControllerType{deck.ControllerKeypad}
	}
	return out
}

func cleanBundleRelPath(rel string) string {
	trimmed := strings.TrimSpace(strings.ReplaceAll(rel, "\\", "/"))
	trimmed = strings.TrimLeft(trimmed, "/")
	if trimmed == "" {
		return ""
	}
	return trimmed
}

// ResolveAssetPath resolves relPath safely inside bundleDir, rejecting path traversal outside bundleDir.
func ResolveAssetPath(bundleDir, relPath string) (string, bool) {
	cleaned := cleanBundleRelPath(relPath)
	if cleaned == "" {
		return "", false
	}
	absRoot, err := filepath.Abs(bundleDir)
	if err != nil {
		return "", false
	}
	candidate := filepath.Clean(filepath.Join(absRoot, filepath.FromSlash(cleaned)))
	rel, err := filepath.Rel(absRoot, candidate)
	if err != nil {
		return "", false
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", false
	}
	return candidate, true
}

// DiscoverBundles finds all valid *.sdPlugin bundle directories referenced by searchPaths.
// Each element of searchPaths may either be a direct *.sdPlugin folder or a parent directory
// containing *.sdPlugin subdirectories.
func DiscoverBundles(searchPaths []string) []string {
	seen := make(map[string]struct{})
	var out []string

	addBundle := func(dir string) {
		abs, err := filepath.Abs(strings.TrimSpace(dir))
		if err != nil {
			return
		}
		if _, err := os.Stat(filepath.Join(abs, "manifest.json")); err != nil {
			return
		}
		key := abs
		if runtime.GOOS == "windows" {
			key = strings.ToLower(abs)
		}
		if _, dup := seen[key]; dup {
			return
		}
		seen[key] = struct{}{}
		out = append(out, abs)
	}

	for _, raw := range searchPaths {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		if strings.HasSuffix(strings.ToLower(trimmed), ".sdplugin") {
			addBundle(trimmed)
			continue
		}
		entries, err := os.ReadDir(trimmed)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			name := entry.Name()
			if !strings.HasSuffix(strings.ToLower(name), ".sdplugin") {
				continue
			}
			full := filepath.Join(trimmed, name)
			if st, statErr := os.Stat(full); statErr == nil && st.IsDir() {
				addBundle(full)
			}
		}
	}
	slices.Sort(out)
	return out
}

// DefaultElgatoPluginsDir returns the OS-standard local Elgato Stream Deck Plugins directory if known.
func DefaultElgatoPluginsDir() string {
	switch runtime.GOOS {
	case "windows":
		if appData := strings.TrimSpace(os.Getenv("APPDATA")); appData != "" {
			return filepath.Join(appData, "Elgato", "StreamDeck", "Plugins")
		}
	case "darwin":
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			return filepath.Join(home, "Library", "Application Support", "com.elgato.StreamDeck", "Plugins")
		}
	}
	return ""
}
