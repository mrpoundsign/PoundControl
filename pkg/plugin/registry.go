package plugin

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/mrpoundsign/PoundControl/pkg/deck"
)

type registeredAction struct {
	desc       ActionDescriptor
	pluginUUID string
	pluginName string
	provider   Provider
}

// Registry aggregates all plugin providers and routes action lifecycle and input events.
type Registry struct {
	mu        sync.RWMutex
	host      HostAPI
	providers []Provider
	actions   map[string]registeredAction
	disabled  map[string]bool
}

// NewRegistry creates an empty thread-safe plugin registry.
func NewRegistry() *Registry {
	return &Registry{
		actions:  make(map[string]registeredAction),
		disabled: make(map[string]bool),
	}
}

// SetHost binds host to all currently registered providers and any providers registered later.
func (r *Registry) SetHost(host HostAPI) {
	r.mu.Lock()
	r.host = host
	providers := slices.Clone(r.providers)
	r.mu.Unlock()

	for _, p := range providers {
		p.BindHost(host)
	}
}

// RegisterProvider validates a provider's manifests and registers its actions for dispatch.
func (r *Registry) RegisterProvider(p Provider) error {
	if p == nil {
		return errors.New("cannot register nil plugin provider")
	}
	manifests := p.Manifests()
	for _, m := range manifests {
		if err := m.Validate(); err != nil {
			return err
		}
	}

	actions := p.Actions()

	r.mu.Lock()
	for _, act := range actions {
		uuid := strings.TrimSpace(act.UUID)
		if uuid == "" {
			r.mu.Unlock()
			return fmt.Errorf("provider %q returned action with empty UUID", p.Name())
		}
		if _, exists := r.actions[uuid]; exists {
			r.mu.Unlock()
			return fmt.Errorf("duplicate action UUID %q", uuid)
		}
	}
	for _, act := range actions {
		cp := act
		pid, pname := resolveActionPluginMeta(cp, manifests)
		if cp.PluginUUID == "" && pid != "" {
			cp.PluginUUID = pid
		}
		if cp.Provider == "" {
			cp.Provider = p.Kind()
		}
		r.actions[cp.UUID] = registeredAction{
			desc:       cp,
			pluginUUID: pid,
			pluginName: pname,
			provider:   p,
		}
	}
	for _, m := range manifests {
		if m.Required {
			delete(r.disabled, m.UUID)
		}
	}
	r.providers = append(r.providers, p)
	host := r.host
	r.mu.Unlock()

	if host != nil {
		p.BindHost(host)
	}
	return nil
}

// RefreshProvider atomically replaces all action descriptors registered by p with its current manifests and actions.
// If p is not yet registered, RefreshProvider registers it.
func (r *Registry) RefreshProvider(p Provider) error {
	if p == nil {
		return errors.New("cannot refresh nil plugin provider")
	}
	manifests := p.Manifests()
	for _, m := range manifests {
		if err := m.Validate(); err != nil {
			return err
		}
	}

	actions := p.Actions()
	seenNew := make(map[string]struct{}, len(actions))
	for _, act := range actions {
		uuid := strings.TrimSpace(act.UUID)
		if uuid == "" {
			return fmt.Errorf("provider %q returned action with empty UUID", p.Name())
		}
		if _, dup := seenNew[uuid]; dup {
			return fmt.Errorf("provider %q returned duplicate action UUID %q", p.Name(), uuid)
		}
		seenNew[uuid] = struct{}{}
	}

	r.mu.Lock()
	for _, act := range actions {
		uuid := strings.TrimSpace(act.UUID)
		if existing, exists := r.actions[uuid]; exists && existing.provider != p {
			r.mu.Unlock()
			return fmt.Errorf("duplicate action UUID %q", uuid)
		}
	}
	for uuid, entry := range r.actions {
		if entry.provider == p {
			delete(r.actions, uuid)
		}
	}
	for _, act := range actions {
		cp := act
		pid, pname := resolveActionPluginMeta(cp, manifests)
		if cp.PluginUUID == "" && pid != "" {
			cp.PluginUUID = pid
		}
		if cp.Provider == "" {
			cp.Provider = p.Kind()
		}
		r.actions[cp.UUID] = registeredAction{
			desc:       cp,
			pluginUUID: pid,
			pluginName: pname,
			provider:   p,
		}
	}
	for _, m := range manifests {
		if m.Required {
			delete(r.disabled, m.UUID)
		}
	}
	if !slices.Contains(r.providers, p) {
		r.providers = append(r.providers, p)
	}
	host := r.host
	r.mu.Unlock()

	if host != nil {
		p.BindHost(host)
	}
	return nil
}

func resolveActionPluginMeta(act ActionDescriptor, manifests []Manifest) (uuid, name string) {
	reqUUID := strings.TrimSpace(act.PluginUUID)
	if reqUUID != "" {
		for _, m := range manifests {
			if m.UUID == reqUUID {
				return m.UUID, m.Name
			}
		}
		return reqUUID, reqUUID
	}
	for _, m := range manifests {
		for _, ma := range m.Actions {
			if ma.UUID == act.UUID {
				return m.UUID, m.Name
			}
		}
	}
	if len(manifests) == 1 {
		return manifests[0].UUID, manifests[0].Name
	}
	return "", ""
}

// Action looks up an enabled ActionDescriptor by its globally unique action UUID.
func (r *Registry) Action(uuid string) (ActionDescriptor, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, ok := r.actions[uuid]
	if !ok {
		return ActionDescriptor{}, false
	}
	if entry.pluginUUID != "" && r.disabled[entry.pluginUUID] {
		return ActionDescriptor{}, false
	}
	return entry.desc, true
}

// Actions returns all registered enabled action descriptors sorted by Category, Name, then UUID.
func (r *Registry) Actions() []ActionDescriptor {
	r.mu.RLock()
	out := make([]ActionDescriptor, 0, len(r.actions))
	for _, entry := range r.actions {
		if entry.pluginUUID != "" && r.disabled[entry.pluginUUID] {
			continue
		}
		out = append(out, entry.desc)
	}
	r.mu.RUnlock()

	slices.SortFunc(out, func(a, b ActionDescriptor) int {
		if c := cmp.Compare(a.Category, b.Category); c != 0 {
			return c
		}
		if c := cmp.Compare(a.Name, b.Name); c != 0 {
			return c
		}
		return cmp.Compare(a.UUID, b.UUID)
	})
	return out
}

// Providers returns a snapshot of all registered plugin providers.
func (r *Registry) Providers() []Provider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return slices.Clone(r.providers)
}

// Dispatch forwards ev to the provider owning ev.ActionUUID unless its plugin is disabled.
func (r *Registry) Dispatch(ctx context.Context, ev ActionEvent) error {
	r.mu.RLock()
	entry, ok := r.actions[ev.ActionUUID]
	isDisabled := ok && entry.pluginUUID != "" && r.disabled[entry.pluginUUID]
	pluginName := entry.pluginName
	if pluginName == "" {
		pluginName = entry.pluginUUID
	}
	r.mu.RUnlock()

	if !ok {
		return fmt.Errorf("unknown action %q", ev.ActionUUID)
	}
	if isDisabled {
		return fmt.Errorf("plugin %q is disabled — enable it in the Plugins tab", pluginName)
	}
	return entry.provider.Dispatch(ctx, ev)
}

// DynamicOptions queries enabled registered DynamicOptionsProviders for live inspector choices matching optionsKey.
func (r *Registry) DynamicOptions(ctx context.Context, optionsKey string, currentSettings map[string]any) []SelectOption {
	if strings.TrimSpace(optionsKey) == "" {
		return nil
	}
	providers := r.Providers()
	for _, p := range providers {
		if r.providerAllDisabled(p) {
			continue
		}
		if dop, ok := p.(DynamicOptionsProvider); ok {
			opts := dop.DynamicOptions(ctx, optionsKey, currentSettings)
			if len(opts) > 0 {
				return slices.Clone(opts)
			}
		}
	}
	return nil
}

func (r *Registry) providerAllDisabled(p Provider) bool {
	manifests := p.Manifests()
	if len(manifests) == 0 {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, m := range manifests {
		if !r.disabled[m.UUID] {
			return false
		}
	}
	return true
}

// PropertyInspectorSession returns an HTML Property Inspector session for actionUUID if its plugin is enabled and supports it.
func (r *Registry) PropertyInspectorSession(
	ctxID string,
	actionUUID string,
	controller deck.ControllerType,
	coords deck.Coordinates,
	dial int,
	settings map[string]any,
) (PropertyInspectorSession, bool) {
	r.mu.RLock()
	entry, ok := r.actions[actionUUID]
	isDisabled := ok && entry.pluginUUID != "" && r.disabled[entry.pluginUUID]
	r.mu.RUnlock()
	if !ok || isDisabled {
		return PropertyInspectorSession{}, false
	}
	pip, hasPI := entry.provider.(PropertyInspectorProvider)
	if !hasPI {
		return PropertyInspectorSession{}, false
	}
	return pip.PropertyInspectorURL(ctxID, actionUUID, controller, coords, dial, settings)
}

// Manifests returns all enabled plugin manifests exposed by registered providers.
func (r *Registry) Manifests() []Manifest {
	providers := r.Providers()
	var out []Manifest
	for _, p := range providers {
		for _, m := range p.Manifests() {
			if r.IsPluginEnabled(m.UUID) {
				out = append(out, m)
			}
		}
	}
	return out
}

// AllManifests returns every registered plugin manifest (both enabled and disabled).
func (r *Registry) AllManifests() []Manifest {
	providers := r.Providers()
	var out []Manifest
	for _, p := range providers {
		out = append(out, p.Manifests()...)
	}
	return out
}

// PluginForAction resolves the owning plugin manifest for actionUUID regardless of enabled state.
func (r *Registry) PluginForAction(actionUUID string) (Manifest, bool) {
	r.mu.RLock()
	entry, ok := r.actions[actionUUID]
	r.mu.RUnlock()
	if !ok || entry.pluginUUID == "" {
		return Manifest{}, false
	}
	for _, m := range entry.provider.Manifests() {
		if m.UUID == entry.pluginUUID {
			return m, true
		}
	}
	return Manifest{
		UUID:     entry.pluginUUID,
		Name:     entry.pluginName,
		Provider: entry.provider.Kind(),
	}, true
}

// SetDisabledPlugins replaces the active disabled-plugin set, ignoring any manifest marked Required.
func (r *Registry) SetDisabledPlugins(uuids []string) {
	next := make(map[string]bool, len(uuids))
	for _, raw := range uuids {
		if id := strings.TrimSpace(raw); id != "" {
			next[id] = true
		}
	}
	providers := r.Providers()
	for _, p := range providers {
		for _, m := range p.Manifests() {
			if m.Required {
				delete(next, m.UUID)
			}
		}
	}

	r.mu.Lock()
	r.disabled = next
	r.mu.Unlock()
}

// SetPluginEnabled enables or disables pluginUUID unless the plugin manifest is marked Required.
func (r *Registry) SetPluginEnabled(pluginUUID string, enabled bool) error {
	pid := strings.TrimSpace(pluginUUID)
	if pid == "" {
		return errors.New("plugin UUID cannot be empty")
	}
	if !enabled {
		if m, ok := r.Manifest(pid); ok && m.Required {
			return fmt.Errorf("plugin %q is required and cannot be disabled", m.Name)
		}
	}

	r.mu.Lock()
	if enabled {
		delete(r.disabled, pid)
	} else {
		r.disabled[pid] = true
	}
	r.mu.Unlock()
	return nil
}

// IsPluginEnabled reports whether pluginUUID is currently enabled.
func (r *Registry) IsPluginEnabled(pluginUUID string) bool {
	pid := strings.TrimSpace(pluginUUID)
	if pid == "" {
		return true
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return !r.disabled[pid]
}

// DisabledPlugins returns a sorted snapshot of disabled plugin UUIDs.
func (r *Registry) DisabledPlugins() []string {
	r.mu.RLock()
	out := make([]string, 0, len(r.disabled))
	for pid := range r.disabled {
		out = append(out, pid)
	}
	r.mu.RUnlock()
	slices.Sort(out)
	return out
}

// NotifyPluginEnabled informs the provider owning pluginUUID (if it implements
// PluginLifecycleListener) that its enabled state changed.
func (r *Registry) NotifyPluginEnabled(ctx context.Context, pluginUUID string, enabled bool) error {
	pid := strings.TrimSpace(pluginUUID)
	if pid == "" {
		return nil
	}
	for _, p := range r.Providers() {
		for _, m := range p.Manifests() {
			if m.UUID != pid {
				continue
			}
			listener, ok := p.(PluginLifecycleListener)
			if !ok {
				return nil
			}
			return listener.SetPluginEnabled(ctx, pid, enabled)
		}
	}
	return nil
}

// Manifest looks up a registered plugin manifest by its plugin UUID.
func (r *Registry) Manifest(pluginUUID string) (Manifest, bool) {
	pid := strings.TrimSpace(pluginUUID)
	if pid == "" {
		return Manifest{}, false
	}
	for _, p := range r.Providers() {
		for _, m := range p.Manifests() {
			if m.UUID == pid {
				return m, true
			}
		}
	}
	return Manifest{}, false
}

// NotifyGlobalSettingsChanged informs the provider owning pluginUUID (if it implements
// GlobalSettingsListener) that its global settings were updated.
func (r *Registry) NotifyGlobalSettingsChanged(ctx context.Context, pluginUUID string, settings map[string]any) error {
	pid := strings.TrimSpace(pluginUUID)
	if pid == "" {
		return nil
	}
	for _, p := range r.Providers() {
		for _, m := range p.Manifests() {
			if m.UUID != pid {
				continue
			}
			listener, ok := p.(GlobalSettingsListener)
			if !ok {
				return nil
			}
			var cloned map[string]any
			if len(settings) > 0 {
				cloned = maps.Clone(settings)
			} else {
				cloned = make(map[string]any)
			}
			return listener.OnGlobalSettingsChanged(ctx, pid, cloned)
		}
	}
	return nil
}

// InvokeConfigAction forwards an interactive FieldButton action to the provider owning pluginUUID
// (when enabled and implementing ConfigActionHandler).
func (r *Registry) InvokeConfigAction(ctx context.Context, pluginUUID, actionKey string, settings map[string]any) error {
	pid := strings.TrimSpace(pluginUUID)
	if pid == "" {
		return errors.New("plugin UUID cannot be empty")
	}
	actKey := strings.TrimSpace(actionKey)
	if actKey == "" {
		return errors.New("config action key cannot be empty")
	}
	manifest, ok := r.Manifest(pid)
	if !ok {
		return fmt.Errorf("unknown plugin %q", pid)
	}
	if !r.IsPluginEnabled(pid) {
		name := manifest.Name
		if name == "" {
			name = pid
		}
		return fmt.Errorf("plugin %q is disabled — enable it in the Plugins tab", name)
	}
	for _, p := range r.Providers() {
		for _, m := range p.Manifests() {
			if m.UUID != pid {
				continue
			}
			handler, ok := p.(ConfigActionHandler)
			if !ok {
				name := m.Name
				if name == "" {
					name = pid
				}
				return fmt.Errorf("plugin %q does not handle config actions", name)
			}
			var cloned map[string]any
			if len(settings) > 0 {
				cloned = maps.Clone(settings)
			} else {
				cloned = make(map[string]any)
			}
			return handler.OnConfigAction(ctx, pid, actKey, cloned)
		}
	}
	return fmt.Errorf("unknown plugin %q", pid)
}

// Close closes every registered provider and returns any joined close errors.
func (r *Registry) Close() error {
	r.mu.Lock()
	providers := slices.Clone(r.providers)
	r.providers = nil
	clear(r.actions)
	r.mu.Unlock()

	var errs []error
	for _, p := range providers {
		if err := p.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
