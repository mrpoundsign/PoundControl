package plugin

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/mrpoundsign/PoundControl/pkg/deck"
)

type registeredAction struct {
	desc     ActionDescriptor
	provider Provider
}

// Registry aggregates all plugin providers and routes action lifecycle and input events.
type Registry struct {
	mu        sync.RWMutex
	host      HostAPI
	providers []Provider
	actions   map[string]registeredAction
}

// NewRegistry creates an empty thread-safe plugin registry.
func NewRegistry() *Registry {
	return &Registry{
		actions: make(map[string]registeredAction),
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
		if cp.Provider == "" {
			cp.Provider = p.Kind()
		}
		r.actions[cp.UUID] = registeredAction{
			desc:     cp,
			provider: p,
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
		if cp.Provider == "" {
			cp.Provider = p.Kind()
		}
		r.actions[cp.UUID] = registeredAction{
			desc:     cp,
			provider: p,
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

// Action looks up an ActionDescriptor by its globally unique action UUID.
func (r *Registry) Action(uuid string) (ActionDescriptor, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, ok := r.actions[uuid]
	if !ok {
		return ActionDescriptor{}, false
	}
	return entry.desc, true
}

// Actions returns all registered action descriptors sorted by Category, Name, then UUID.
func (r *Registry) Actions() []ActionDescriptor {
	r.mu.RLock()
	out := make([]ActionDescriptor, 0, len(r.actions))
	for _, entry := range r.actions {
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

// Dispatch forwards ev to the provider owning ev.ActionUUID.
func (r *Registry) Dispatch(ctx context.Context, ev ActionEvent) error {
	r.mu.RLock()
	entry, ok := r.actions[ev.ActionUUID]
	r.mu.RUnlock()

	if !ok {
		return fmt.Errorf("unknown action %q", ev.ActionUUID)
	}
	return entry.provider.Dispatch(ctx, ev)
}

// DynamicOptions queries registered DynamicOptionsProviders for live inspector choices matching optionsKey.
func (r *Registry) DynamicOptions(ctx context.Context, optionsKey string, currentSettings map[string]any) []SelectOption {
	if strings.TrimSpace(optionsKey) == "" {
		return nil
	}
	providers := r.Providers()
	for _, p := range providers {
		if dop, ok := p.(DynamicOptionsProvider); ok {
			opts := dop.DynamicOptions(ctx, optionsKey, currentSettings)
			if len(opts) > 0 {
				return slices.Clone(opts)
			}
		}
	}
	return nil
}

// PropertyInspectorSession returns an HTML Property Inspector session for actionUUID if its provider supports it.
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
	r.mu.RUnlock()
	if !ok {
		return PropertyInspectorSession{}, false
	}
	pip, hasPI := entry.provider.(PropertyInspectorProvider)
	if !hasPI {
		return PropertyInspectorSession{}, false
	}
	return pip.PropertyInspectorURL(ctxID, actionUUID, controller, coords, dial, settings)
}

// Manifests returns all plugin manifests exposed by registered providers.
func (r *Registry) Manifests() []Manifest {
	providers := r.Providers()
	var out []Manifest
	for _, p := range providers {
		out = append(out, p.Manifests()...)
	}
	return out
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
