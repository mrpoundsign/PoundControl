// Package core provides PoundControl's built-in navigation, folder, multi-action, delay, and counter actions.
package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mrpoundsign/PoundControl/pkg/deck"
	"github.com/mrpoundsign/PoundControl/pkg/plugin"
)

const (
	// PluginUUID identifies the built-in core navigation and macro plugin.
	PluginUUID = "com.github.mrpoundsign.poundcontrol.core"

	ActionPageNext      = PluginUUID + ".page.next"
	ActionPagePrev      = PluginUUID + ".page.prev"
	ActionPageGoTo      = PluginUUID + ".page.goto"
	ActionProfileSwitch = PluginUUID + ".profile.switch"
	ActionFolderOpen    = PluginUUID + ".folder.open"
	ActionFolderBack    = PluginUUID + ".folder.back"
	ActionMultiAction   = PluginUUID + ".multiaction"
	ActionDelay         = PluginUUID + ".delay"
	ActionCounter       = PluginUUID + ".counter"

	maxMultiActionDepth = 8
)

type multiActionDepthKey struct{}

// MultiActionStep defines one sequential step inside an ActionMultiAction sequence.
type MultiActionStep struct {
	ID         string         `json:"id,omitempty"`
	ActionUUID string         `json:"actionUuid"`
	Settings   map[string]any `json:"settings,omitempty"`
	DelayMs    int            `json:"delayMs,omitempty"`
	Disabled   bool           `json:"disabled,omitempty"`
}

type counterState struct {
	value   int
	initial int
}

// Provider implements plugin.Provider for PoundControl's built-in core actions.
type Provider struct {
	mu       sync.RWMutex
	host     plugin.HostAPI
	counters map[string]counterState
	manifest plugin.Manifest
}

// New creates a built-in core plugin provider.
func New() *Provider {
	return &Provider{
		counters: make(map[string]counterState),
		manifest: buildManifest(),
	}
}

func buildManifest() plugin.Manifest {
	actions := []plugin.ActionDescriptor{
		{
			UUID:         ActionPageNext,
			PluginUUID:   PluginUUID,
			Name:         "Next Page",
			Category:     "Core Navigation",
			Tooltip:      "Advance to the next page in the active profile",
			DefaultTitle: "Next Page",
			DefaultColor: "#38BDF8",
			Controllers:  []deck.ControllerType{deck.ControllerKeypad},
			Provider:     plugin.ProviderBuiltin,
			SettingsSchema: []plugin.SettingField{
				{
					Key:     "wrap",
					Label:   "Wrap Around",
					Type:    plugin.FieldBool,
					Default: true,
					Help:    "Loop back to Page 1 after the final page.",
				},
			},
		},
		{
			UUID:         ActionPagePrev,
			PluginUUID:   PluginUUID,
			Name:         "Previous Page",
			Category:     "Core Navigation",
			Tooltip:      "Return to the previous page in the active profile",
			DefaultTitle: "Prev Page",
			DefaultColor: "#38BDF8",
			Controllers:  []deck.ControllerType{deck.ControllerKeypad},
			Provider:     plugin.ProviderBuiltin,
			SettingsSchema: []plugin.SettingField{
				{
					Key:     "wrap",
					Label:   "Wrap Around",
					Type:    plugin.FieldBool,
					Default: true,
					Help:    "Loop to the last page when on Page 1.",
				},
			},
		},
		{
			UUID:         ActionPageGoTo,
			PluginUUID:   PluginUUID,
			Name:         "Go To Page",
			Category:     "Core Navigation",
			Tooltip:      "Jump directly to a 1-based page number",
			DefaultTitle: "Page 1",
			DefaultColor: "#38BDF8",
			Controllers:  []deck.ControllerType{deck.ControllerKeypad},
			Provider:     plugin.ProviderBuiltin,
			SettingsSchema: []plugin.SettingField{
				{
					Key:         "page",
					Label:       "Page Number",
					Type:        plugin.FieldNumber,
					Default:     1,
					Placeholder: "1",
					Required:    true,
				},
			},
		},
		{
			UUID:         ActionProfileSwitch,
			PluginUUID:   PluginUUID,
			Name:         "Switch Profile",
			Category:     "Core Navigation",
			Tooltip:      "Switch the active deck profile by name or ID",
			DefaultTitle: "Profile",
			DefaultColor: "#A855F7",
			Controllers:  []deck.ControllerType{deck.ControllerKeypad},
			Provider:     plugin.ProviderBuiltin,
			SettingsSchema: []plugin.SettingField{
				{
					Key:         "profile",
					Label:       "Profile Name or ID",
					Type:        plugin.FieldText,
					Placeholder: "Default",
					Required:    true,
				},
			},
		},
		{
			UUID:         ActionFolderOpen,
			PluginUUID:   PluginUUID,
			Name:         "Open Folder",
			Category:     "Core Navigation",
			Tooltip:      "Open a nested sub-deck folder",
			DefaultTitle: "Folder",
			DefaultColor: "#FBBF24",
			Controllers:  []deck.ControllerType{deck.ControllerKeypad},
			Provider:     plugin.ProviderBuiltin,
			SettingsSchema: []plugin.SettingField{
				{
					Key:         "folderName",
					Label:       "Folder Name",
					Type:        plugin.FieldText,
					Default:     "Folder",
					Placeholder: "Sub-Deck",
				},
				{
					Key:         "folderId",
					Label:       "Folder ID (optional)",
					Type:        plugin.FieldText,
					Section:     plugin.SectionAdvanced,
					Placeholder: "Leave empty to auto-link by slot",
				},
			},
		},
		{
			UUID:         ActionFolderBack,
			PluginUUID:   PluginUUID,
			Name:         "Folder Back",
			Category:     "Core Navigation",
			Tooltip:      "Exit the current folder back to its parent page or folder",
			DefaultTitle: "Back",
			DefaultColor: "#94A3B8",
			Controllers:  []deck.ControllerType{deck.ControllerKeypad},
			Provider:     plugin.ProviderBuiltin,
		},
		{
			UUID:         ActionMultiAction,
			PluginUUID:   PluginUUID,
			Name:         "Multi-Action",
			Category:     "Core Actions",
			Tooltip:      "Run a sequential list of actions and delays",
			DefaultTitle: "Multi",
			DefaultColor: "#22C55E",
			Controllers:  []deck.ControllerType{deck.ControllerKeypad},
			Provider:     plugin.ProviderBuiltin,
			SettingsSchema: []plugin.SettingField{
				{
					Key:     "continueOnError",
					Label:   "Continue on Error",
					Type:    plugin.FieldBool,
					Section: plugin.SectionAdvanced,
					Default: false,
					Help:    "Keep running remaining steps if one step fails.",
				},
				{
					Key:         "steps",
					Label:       "Steps JSON",
					Type:        plugin.FieldMultiline,
					Section:     plugin.SectionAdvanced,
					Default:     "[]",
					Placeholder: `[{"actionUuid":"com.github.mrpoundsign.poundcontrol.core.delay","settings":{"ms":150}}]`,
					Help:        "JSON array of {\"actionUuid\", \"settings\", \"delayMs\", \"disabled\"} objects executed in order.",
				},
			},
		},
		{
			UUID:         ActionDelay,
			PluginUUID:   PluginUUID,
			Name:         "Delay",
			Category:     "Core Actions",
			Tooltip:      "Pause execution for a configurable duration in milliseconds",
			DefaultTitle: "Delay",
			DefaultColor: "#64748B",
			Controllers:  []deck.ControllerType{deck.ControllerKeypad},
			Provider:     plugin.ProviderBuiltin,
			SettingsSchema: []plugin.SettingField{
				{
					Key:         "ms",
					Label:       "Duration (ms)",
					Type:        plugin.FieldNumber,
					Default:     250,
					Placeholder: "250",
				},
			},
		},
		{
			UUID:         ActionCounter,
			PluginUUID:   PluginUUID,
			Name:         "Counter",
			Category:     "Core Actions",
			Tooltip:      "Increment or dial-adjust an on-tile counter",
			DefaultTitle: "Count",
			DefaultColor: "#38BDF8",
			Controllers:  []deck.ControllerType{deck.ControllerKeypad, deck.ControllerEncoder},
			Provider:     plugin.ProviderBuiltin,
			SettingsSchema: []plugin.SettingField{
				{
					Key:         "initial",
					Label:       "Initial Value",
					Type:        plugin.FieldNumber,
					Default:     0,
					Placeholder: "0",
				},
				{
					Key:         "step",
					Label:       "Step Delta",
					Type:        plugin.FieldNumber,
					Default:     1,
					Placeholder: "1",
				},
			},
		},
	}

	return plugin.Manifest{
		UUID:        PluginUUID,
		Name:        "Core Navigation & Actions",
		Version:     "0.1.0",
		Author:      "PoundControl",
		Description: "Built-in page, profile, folder, multi-action, delay, and counter actions.",
		Provider:    plugin.ProviderBuiltin,
		Required:    true,
		Actions:     actions,
	}
}

// Kind returns plugin.ProviderBuiltin.
func (p *Provider) Kind() plugin.ProviderKind {
	return plugin.ProviderBuiltin
}

// Name returns the built-in plugin display name.
func (p *Provider) Name() string {
	return p.manifest.Name
}

// Manifests returns the core plugin manifest.
func (p *Provider) Manifests() []plugin.Manifest {
	return []plugin.Manifest{p.manifest}
}

// Actions returns all built-in core action descriptors.
func (p *Provider) Actions() []plugin.ActionDescriptor {
	out := make([]plugin.ActionDescriptor, len(p.manifest.Actions))
	copy(out, p.manifest.Actions)
	return out
}

// BindHost sets the HostAPI used by core actions to update tiles and navigate the deck.
func (p *Provider) BindHost(host plugin.HostAPI) {
	p.mu.Lock()
	p.host = host
	p.mu.Unlock()
}

// Dispatch processes lifecycle and user input events for core actions.
func (p *Provider) Dispatch(ctx context.Context, ev plugin.ActionEvent) error {
	p.mu.RLock()
	host := p.host
	p.mu.RUnlock()

	switch ev.ActionUUID {
	case ActionCounter:
		return p.handleCounter(host, ev)
	case ActionDelay:
		if ev.Type == plugin.EventKeyDown || ev.Type == plugin.EventDialPress {
			return SleepContext(ctx, time.Duration(intSetting(ev.Settings, "ms", 250))*time.Millisecond)
		}
		return nil
	case ActionMultiAction:
		if ev.Type == plugin.EventKeyDown || ev.Type == plugin.EventDialPress {
			return p.handleMultiAction(ctx, host, ev)
		}
		return nil
	case ActionPageNext, ActionPagePrev, ActionPageGoTo, ActionProfileSwitch, ActionFolderOpen, ActionFolderBack:
		if ev.Type != plugin.EventKeyDown && ev.Type != plugin.EventDialPress {
			return nil
		}
		return p.handleNavigation(host, ev)
	default:
		return fmt.Errorf("core provider: unsupported action %q", ev.ActionUUID)
	}
}

// Close releases internal runtime state.
func (p *Provider) Close() error {
	p.mu.Lock()
	clear(p.counters)
	p.host = nil
	p.mu.Unlock()
	return nil
}

func (p *Provider) handleNavigation(host plugin.HostAPI, ev plugin.ActionEvent) error {
	if host == nil || host.Deck() == nil {
		return errors.New("core provider: deck controller unavailable")
	}
	deckCtrl := host.Deck()

	switch ev.ActionUUID {
	case ActionPageNext:
		return deckCtrl.NextPage(boolSetting(ev.Settings, "wrap", true))
	case ActionPagePrev:
		return deckCtrl.PrevPage(boolSetting(ev.Settings, "wrap", true))
	case ActionPageGoTo:
		return deckCtrl.GoToPage(intSetting(ev.Settings, "page", 1))
	case ActionProfileSwitch:
		ref := stringSetting(ev.Settings, "profile", "")
		if ref == "" {
			return errors.New("switch profile requires a profile name or ID")
		}
		return deckCtrl.SwitchProfile(ref)
	case ActionFolderOpen:
		fid := stringSetting(ev.Settings, "folderId", "")
		if fid == "" {
			fid = ev.Context
		}
		name := stringSetting(ev.Settings, "folderName", "Folder")
		if name == "" {
			name = "Folder"
		}
		return deckCtrl.OpenFolder(fid, name)
	case ActionFolderBack:
		return deckCtrl.CloseFolder()
	default:
		return fmt.Errorf("core provider: unhandled navigation action %q", ev.ActionUUID)
	}
}

func (p *Provider) handleCounter(host plugin.HostAPI, ev plugin.ActionEvent) error {
	initial := intSetting(ev.Settings, "initial", 0)
	step := intSetting(ev.Settings, "step", 1)

	p.mu.Lock()
	st, exists := p.counters[ev.Context]
	if !exists {
		st = counterState{value: initial, initial: initial}
	}

	flash := ""
	switch ev.Type {
	case plugin.EventWillAppear, plugin.EventSettingsChanged:
		if st.initial != initial {
			st.value = initial
			st.initial = initial
		}
	case plugin.EventKeyDown, plugin.EventDialPress:
		st.value += step
		flash = "ok"
	case plugin.EventDialRotate:
		st.value += ev.Ticks * step
		flash = "ok"
	case plugin.EventWillDisappear, plugin.EventKeyUp:
		p.mu.Unlock()
		return nil
	}
	p.counters[ev.Context] = st
	valStr := strconv.Itoa(st.value)
	p.mu.Unlock()

	if host != nil {
		host.UpdateVisual(ev.Context, plugin.VisualPatch{
			Subtitle: new(valStr),
			Feedback: map[string]any{"value": valStr},
			Flash:    flash,
		})
	}
	return nil
}

func (p *Provider) handleMultiAction(ctx context.Context, host plugin.HostAPI, ev plugin.ActionEvent) error {
	if host == nil || host.Deck() == nil {
		return errors.New("core provider: deck controller unavailable")
	}
	depth, _ := ctx.Value(multiActionDepthKey{}).(int)
	if depth >= maxMultiActionDepth {
		host.UpdateVisual(ev.Context, plugin.VisualPatch{Flash: "alert"})
		return fmt.Errorf("multi-action nesting depth exceeded maximum (%d)", maxMultiActionDepth)
	}
	childCtx := context.WithValue(ctx, multiActionDepthKey{}, depth+1)

	steps, err := ParseMultiActionSteps(ev.Settings)
	if err != nil {
		host.UpdateVisual(ev.Context, plugin.VisualPatch{Flash: "alert"})
		return err
	}

	continueOnErr := MultiActionContinueOnError(ev.Settings)
	var firstErr error
	for i, step := range steps {
		if step.Disabled {
			continue
		}
		if err := childCtx.Err(); err != nil {
			return err
		}
		if step.DelayMs > 0 {
			if err := SleepContext(childCtx, time.Duration(step.DelayMs)*time.Millisecond); err != nil {
				return err
			}
		}
		if strings.TrimSpace(step.ActionUUID) != "" {
			if err := host.Deck().TriggerAction(childCtx, step.ActionUUID, step.Settings); err != nil {
				stepErr := fmt.Errorf("multi-action step %d (%s): %w", i, step.ActionUUID, err)
				if !continueOnErr {
					host.UpdateVisual(ev.Context, plugin.VisualPatch{Flash: "alert"})
					return stepErr
				}
				if firstErr == nil {
					firstErr = stepErr
				}
			}
		}
	}
	if firstErr != nil {
		host.UpdateVisual(ev.Context, plugin.VisualPatch{Flash: "alert"})
		return firstErr
	}
	host.UpdateVisual(ev.Context, plugin.VisualPatch{Flash: "ok"})
	return nil
}

// ParseMultiActionSteps decodes the "steps" entry from Multi-Action slot settings.
func ParseMultiActionSteps(settings map[string]any) ([]MultiActionStep, error) {
	if settings == nil {
		return nil, nil
	}
	raw, ok := settings["steps"]
	if !ok || raw == nil {
		return nil, nil
	}
	switch v := raw.(type) {
	case string:
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return nil, nil
		}
		var steps []MultiActionStep
		if err := json.Unmarshal([]byte(trimmed), &steps); err != nil {
			return nil, fmt.Errorf("invalid multi-action steps JSON: %w", err)
		}
		return steps, nil
	case []MultiActionStep:
		return v, nil
	default:
		encoded, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("marshal multi-action steps: %w", err)
		}
		var steps []MultiActionStep
		if err := json.Unmarshal(encoded, &steps); err != nil {
			return nil, fmt.Errorf("decode multi-action steps: %w", err)
		}
		return steps, nil
	}
}

// NormalizeMultiActionSteps converts legacy per-step delayMs offsets into explicit ActionDelay blocks,
// assigns unique IDs to every step, and deep-clones settings maps.
func NormalizeMultiActionSteps(steps []MultiActionStep) []MultiActionStep {
	if len(steps) == 0 {
		return nil
	}
	out := make([]MultiActionStep, 0, len(steps))
	seenIDs := make(map[string]struct{}, len(steps))
	uniqueID := func(raw string) string {
		id := strings.TrimSpace(raw)
		if id != "" {
			if _, exists := seenIDs[id]; !exists {
				seenIDs[id] = struct{}{}
				return id
			}
		}
		for {
			id = deck.NewUUID()
			if _, exists := seenIDs[id]; !exists {
				seenIDs[id] = struct{}{}
				return id
			}
		}
	}

	for _, step := range steps {
		uuid := strings.TrimSpace(step.ActionUUID)
		cloned := cloneStepMap(step.Settings)
		if uuid == "" {
			if step.DelayMs > 0 {
				out = append(out, MultiActionStep{
					ID:         uniqueID(step.ID),
					ActionUUID: ActionDelay,
					Settings:   map[string]any{"ms": step.DelayMs},
					Disabled:   step.Disabled,
				})
			}
			continue
		}
		if uuid == ActionDelay {
			ms := intSetting(cloned, "ms", 250)
			if step.DelayMs > 0 {
				if _, hasMs := cloned["ms"]; hasMs {
					ms += step.DelayMs
				} else {
					ms = step.DelayMs
				}
			}
			ms = max(0, ms)
			cloned["ms"] = ms
			out = append(out, MultiActionStep{
				ID:         uniqueID(step.ID),
				ActionUUID: ActionDelay,
				Settings:   cloned,
				Disabled:   step.Disabled,
			})
			continue
		}
		if step.DelayMs > 0 {
			out = append(out, MultiActionStep{
				ID:         uniqueID(""),
				ActionUUID: ActionDelay,
				Settings:   map[string]any{"ms": step.DelayMs},
				Disabled:   step.Disabled,
			})
		}
		out = append(out, MultiActionStep{
			ID:         uniqueID(step.ID),
			ActionUUID: uuid,
			Settings:   cloned,
			Disabled:   step.Disabled,
		})
	}
	return out
}

// EncodeMultiActionSteps serializes steps into the JSON string persisted under settings["steps"].
func EncodeMultiActionSteps(steps []MultiActionStep) (string, error) {
	if len(steps) == 0 {
		return "[]", nil
	}
	encoded, err := json.Marshal(steps)
	if err != nil {
		return "", fmt.Errorf("encode multi-action steps: %w", err)
	}
	return string(encoded), nil
}

// MultiActionContinueOnError reports whether a Multi-Action sequence keeps executing remaining steps after an error.
func MultiActionContinueOnError(settings map[string]any) bool {
	return boolSetting(settings, "continueOnError", false)
}

// StepDelayMs returns the delay duration in milliseconds represented by step.
func StepDelayMs(step MultiActionStep) int {
	if strings.TrimSpace(step.ActionUUID) == ActionDelay {
		return max(0, intSetting(step.Settings, "ms", 250))
	}
	return max(0, step.DelayMs)
}

// MultiActionEstimatedDurationMs returns the total delay duration in milliseconds across all enabled steps.
func MultiActionEstimatedDurationMs(steps []MultiActionStep) int {
	total := 0
	for _, step := range steps {
		if step.Disabled {
			continue
		}
		if strings.TrimSpace(step.ActionUUID) == ActionDelay {
			total += max(0, intSetting(step.Settings, "ms", 250))
		}
		if step.DelayMs > 0 {
			total += step.DelayMs
		}
	}
	return total
}

// SleepContext pauses for d or returns early if ctx is canceled.
func SleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func cloneStepMap(src map[string]any) map[string]any {
	if len(src) == 0 {
		return make(map[string]any)
	}
	out := make(map[string]any, len(src))
	for k, v := range src {
		out[k] = cloneStepValue(v)
	}
	return out
}

func cloneStepValue(v any) any {
	switch tv := v.(type) {
	case map[string]any:
		return cloneStepMap(tv)
	case []any:
		cp := make([]any, len(tv))
		for i, elem := range tv {
			cp[i] = cloneStepValue(elem)
		}
		return cp
	case map[string]string:
		return maps.Clone(tv)
	case []string:
		return slices.Clone(tv)
	default:
		return v
	}
}

func stringSetting(settings map[string]any, key, def string) string {
	if settings == nil {
		return def
	}
	v, ok := settings[key]
	if !ok || v == nil {
		return def
	}
	s, ok := v.(string)
	if !ok {
		return fmt.Sprint(v)
	}
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return def
	}
	return trimmed
}

func boolSetting(settings map[string]any, key string, def bool) bool {
	if settings == nil {
		return def
	}
	v, ok := settings[key]
	if !ok || v == nil {
		return def
	}
	switch b := v.(type) {
	case bool:
		return b
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(b))
		if err == nil {
			return parsed
		}
		return def
	default:
		return def
	}
}

func intSetting(settings map[string]any, key string, def int) int {
	if settings == nil {
		return def
	}
	v, ok := settings[key]
	if !ok || v == nil {
		return def
	}
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(n))
		if err == nil {
			return parsed
		}
		return def
	default:
		return def
	}
}
