// Package sdplugin implements the Elgato Stream Deck SDK v2/v3 plugin host,
// HTML Property Inspector bridge, static schema extractor, and managed Node.js runtime.
package sdplugin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"github.com/mrpoundsign/PoundControl/pkg/deck"
	"github.com/mrpoundsign/PoundControl/pkg/plugin"
)

// HostPluginUUID identifies PoundControl's .sdPlugin host configuration in PluginSettings.
const HostPluginUUID = "com.github.mrpoundsign.poundcontrol.sdplugin"

// ErrEncryptedManifest indicates that a .sdPlugin bundle's manifest.json uses Elgato Marketplace
// DRM binary encryption ("ELGATO" header) and cannot be loaded by third-party hosts.
var ErrEncryptedManifest = errors.New("encrypted Elgato Marketplace manifest (DRM-protected); cannot be loaded by third-party hosts")

var elgatoEncryptedMagic = []byte("ELGATO")

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

// SDManifest models an Elgato Stream Deck SDK v2/v3 or OpenAction plugin manifest.json.
type SDManifest struct {
	SDKVersion            int               `json:"SDKVersion"`
	UUID                  string            `json:"UUID"`
	Name                  string            `json:"Name"`
	Author                string            `json:"Author,omitempty"`
	Version               string            `json:"Version,omitempty"`
	Description           string            `json:"Description,omitempty"`
	Category              string            `json:"Category,omitempty"`
	CategoryIcon          string            `json:"CategoryIcon,omitempty"`
	Icon                  string            `json:"Icon,omitempty"`
	CodePath              string            `json:"CodePath"`
	CodePathWin           string            `json:"CodePathWin,omitempty"`
	CodePathMac           string            `json:"CodePathMac,omitempty"`
	CodePathLin           string            `json:"CodePathLin,omitempty"`
	CodePathLinux         string            `json:"CodePathLinux,omitempty"`
	CodePaths             map[string]string `json:"CodePaths,omitempty"`
	PropertyInspectorPath string            `json:"PropertyInspectorPath,omitempty"`
	Nodejs                *NodejsConfig     `json:"Nodejs,omitempty"`
	OS                    []OSRequirement   `json:"OS,omitempty"`
	Actions               []SDAction        `json:"Actions"`
}

// ResolveCodePath picks the best executable or script entrypoint for the given GOOS and current GOARCH.
func (m SDManifest) ResolveCodePath(goos string) string {
	return m.ResolveCodePathFor(goos, runtime.GOARCH)
}

// ResolveCodePathFor picks the best executable or script entrypoint for the given GOOS and GOARCH.
// Precedence:
//  1. Exact OpenAction / Rust target triple in CodePaths
//  2. OS-specific CodePathWin / CodePathMac / CodePathLin / CodePathLinux
//  3. Universal CodePath fallback
func (m SDManifest) ResolveCodePathFor(goos, goarch string) string {
	if triple := openActionTargetTriple(goos, goarch); triple != "" && len(m.CodePaths) > 0 {
		if p := strings.TrimSpace(m.CodePaths[triple]); p != "" {
			return p
		}
		for k, v := range m.CodePaths {
			if strings.EqualFold(strings.TrimSpace(k), triple) {
				if p := strings.TrimSpace(v); p != "" {
					return p
				}
			}
		}
	}
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
		if strings.TrimSpace(m.CodePathLin) != "" {
			return strings.TrimSpace(m.CodePathLin)
		}
		if strings.TrimSpace(m.CodePathLinux) != "" {
			return strings.TrimSpace(m.CodePathLinux)
		}
	}
	return strings.TrimSpace(m.CodePath)
}

func openActionTargetTriple(goos, goarch string) string {
	switch goos {
	case "windows":
		switch goarch {
		case "amd64":
			return "x86_64-pc-windows-msvc"
		case "arm64":
			return "aarch64-pc-windows-msvc"
		}
	case "darwin":
		switch goarch {
		case "amd64":
			return "x86_64-apple-darwin"
		case "arm64":
			return "aarch64-apple-darwin"
		}
	case "linux":
		switch goarch {
		case "amd64":
			return "x86_64-unknown-linux-gnu"
		case "arm64":
			return "aarch64-unknown-linux-gnu"
		}
	}
	return ""
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

// LoadBundle reads manifest.json (and optional poundcontrol.schema.json) from bundleDir.
func LoadBundle(bundleDir string) (*LoadedBundle, error) {
	absDir, err := filepath.Abs(strings.TrimSpace(bundleDir))
	if err != nil {
		return nil, fmt.Errorf("resolve plugin bundle path: %w", err)
	}
	info, err := os.Stat(absDir)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("plugin bundle directory not found: %s", absDir)
	}

	m, err := loadManifestWithOverlay(absDir, runtime.GOOS)
	if err != nil {
		return nil, err
	}
	applyManifestLocalization(absDir, &m)

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

	codeRel := m.ResolveCodePathFor(runtime.GOOS, runtime.GOARCH)
	if codeRel == "" {
		return nil, fmt.Errorf("manifest %q is missing CodePath", m.UUID)
	}
	codeAbs, ok := ResolveAssetPath(absDir, codeRel)
	if !ok {
		return nil, fmt.Errorf("plugin %q CodePath %q escapes bundle root", m.UUID, codeRel)
	}
	if _, err := os.Stat(codeAbs); err != nil {
		resolved := false
		if runtime.GOOS == "windows" {
			lower := strings.ToLower(codeAbs)
			for _, ext := range []string{".exe", ".cmd", ".bat"} {
				if strings.HasSuffix(lower, ext) {
					continue
				}
				if st, statErr := os.Stat(codeAbs + ext); statErr == nil && !st.IsDir() {
					codeAbs += ext
					codeRel += ext
					resolved = true
					break
				}
			}
		}
		if !resolved {
			return nil, fmt.Errorf("plugin %q entrypoint %q not found: %w", m.UUID, codeRel, err)
		}
	}

	customSchema, err := loadSidecarSchema(filepath.Join(absDir, "poundcontrol.schema.json"))
	if err != nil {
		return nil, fmt.Errorf("plugin %q poundcontrol.schema.json: %w", m.UUID, err)
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
		actIcon := ""
		if strings.TrimSpace(act.Icon) != "" {
			if imgAbs, safe := ResolveAssetPath(absDir, act.Icon); safe {
				actIcon = imgAbs
			}
		}
		if defImg == "" {
			defImg = actIcon
		}
		if actIcon == "" {
			actIcon = defImg
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
			Icon:           actIcon,
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

// ToPluginManifest converts this bundle into PoundControl's unified plugin.Manifest.
func (b *LoadedBundle) ToPluginManifest() plugin.Manifest {
	icon := ""
	if strings.TrimSpace(b.Manifest.Icon) != "" {
		if abs, safe := ResolveAssetPath(b.Dir, b.Manifest.Icon); safe {
			icon = abs
		}
	}
	catIcon := ""
	if strings.TrimSpace(b.Manifest.CategoryIcon) != "" {
		if abs, safe := ResolveAssetPath(b.Dir, b.Manifest.CategoryIcon); safe {
			catIcon = abs
		}
	}
	return plugin.Manifest{
		UUID:         b.UUID,
		Name:         b.Manifest.Name,
		Version:      b.Manifest.Version,
		Author:       b.Manifest.Author,
		Description:  b.Manifest.Description,
		Icon:         icon,
		CategoryIcon: catIcon,
		Provider:     plugin.ProviderStreamDeck,
		Actions:      slices.Clone(b.Actions),
	}
}

func manifestOverlayFilename(goos string) string {
	switch goos {
	case "windows":
		return "manifest.windows.json"
	case "darwin":
		return "manifest.macos.json"
	case "linux":
		return "manifest.linux.json"
	default:
		return ""
	}
}

func loadManifestWithOverlay(absDir, goos string) (SDManifest, error) {
	manifestPath := filepath.Join(absDir, "manifest.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return SDManifest{}, fmt.Errorf("read manifest.json in %s: %w", absDir, err)
	}
	if bytes.HasPrefix(raw, elgatoEncryptedMagic) {
		return SDManifest{}, fmt.Errorf("%s: %w", manifestPath, ErrEncryptedManifest)
	}

	var baseObj map[string]any
	if err := json.Unmarshal(raw, &baseObj); err != nil {
		return SDManifest{}, fmt.Errorf("parse %s: %w", manifestPath, err)
	}
	if baseObj == nil {
		baseObj = make(map[string]any)
	}

	if overlayName := manifestOverlayFilename(goos); overlayName != "" {
		overlayPath := filepath.Join(absDir, overlayName)
		if overlayRaw, readErr := os.ReadFile(overlayPath); readErr == nil {
			if bytes.HasPrefix(overlayRaw, elgatoEncryptedMagic) {
				return SDManifest{}, fmt.Errorf("%s: %w", overlayPath, ErrEncryptedManifest)
			}
			var overlayObj map[string]any
			if err := json.Unmarshal(overlayRaw, &overlayObj); err != nil {
				return SDManifest{}, fmt.Errorf("parse %s: %w", overlayPath, err)
			}
			mergeJSONObjects(baseObj, overlayObj)
		} else if !errors.Is(readErr, os.ErrNotExist) {
			return SDManifest{}, fmt.Errorf("read %s: %w", overlayPath, readErr)
		}
	}

	merged, err := json.Marshal(baseObj)
	if err != nil {
		return SDManifest{}, fmt.Errorf("marshal merged manifest in %s: %w", absDir, err)
	}
	var m SDManifest
	if err := json.Unmarshal(merged, &m); err != nil {
		return SDManifest{}, fmt.Errorf("parse %s: %w", manifestPath, err)
	}
	return m, nil
}

func mergeJSONObjects(dst, src map[string]any) {
	for k, v := range src {
		srcMap, srcIsMap := v.(map[string]any)
		dstMap, dstIsMap := dst[k].(map[string]any)
		if srcIsMap && dstIsMap {
			cp := maps.Clone(dstMap)
			mergeJSONObjects(cp, srcMap)
			dst[k] = cp
			continue
		}
		dst[k] = v
	}
}

func applyManifestLocalization(absDir string, m *SDManifest) {
	raw, err := os.ReadFile(filepath.Join(absDir, "en.json"))
	if err != nil {
		return
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		return
	}
	readStr := func(key string) string {
		val, ok := root[key]
		if !ok {
			return ""
		}
		var s string
		if err := json.Unmarshal(val, &s); err != nil {
			return ""
		}
		return strings.TrimSpace(s)
	}
	if s := readStr("Name"); s != "" {
		m.Name = s
	}
	if s := readStr("Description"); s != "" {
		m.Description = s
	}
	if s := readStr("Category"); s != "" {
		m.Category = s
	}

	type locAction struct {
		Name    string `json:"Name"`
		Tooltip string `json:"Tooltip"`
		States  []struct {
			Title string `json:"Title"`
		} `json:"States"`
	}
	for i := range m.Actions {
		actUUID := strings.TrimSpace(m.Actions[i].UUID)
		if actUUID == "" {
			continue
		}
		rawAct, ok := root[actUUID]
		if !ok {
			continue
		}
		var la locAction
		if err := json.Unmarshal(rawAct, &la); err != nil {
			continue
		}
		if s := strings.TrimSpace(la.Name); s != "" {
			m.Actions[i].Name = s
		}
		if s := strings.TrimSpace(la.Tooltip); s != "" {
			m.Actions[i].Tooltip = s
		}
		for si := 0; si < len(la.States) && si < len(m.Actions[i].States); si++ {
			if s := strings.TrimSpace(la.States[si].Title); s != "" {
				m.Actions[i].States[si].Title = s
			}
		}
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

// DefaultOpenDeckPluginsDir returns the OS-standard local OpenDeck plugins directory if known.
func DefaultOpenDeckPluginsDir() string {
	switch runtime.GOOS {
	case "windows":
		if appData := strings.TrimSpace(os.Getenv("APPDATA")); appData != "" {
			return filepath.Join(appData, "opendeck", "plugins")
		}
	case "darwin":
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			return filepath.Join(home, "Library", "Application Support", "opendeck", "plugins")
		}
	case "linux":
		if xdg := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); xdg != "" {
			return filepath.Join(xdg, "opendeck", "plugins")
		}
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			return filepath.Join(home, ".config", "opendeck", "plugins")
		}
	}
	return ""
}
