package plugin

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
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
