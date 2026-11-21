package plugin

import (
	"context"
	"errors"
	"testing"

	"github.com/mrpoundsign/PoundDeck/pkg/deck"
)

type stubProvider struct {
	name      string
	kind      ProviderKind
	manifests []Manifest
	actions   []ActionDescriptor
	host      HostAPI
	events    []ActionEvent
	closed    bool
	closeErr  error
}

func (s *stubProvider) Kind() ProviderKind     { return s.kind }
func (s *stubProvider) Name() string           { return s.name }
func (s *stubProvider) Manifests() []Manifest  { return s.manifests }
func (s *stubProvider) Actions() []ActionDescriptor { return s.actions }
func (s *stubProvider) BindHost(host HostAPI)  { s.host = host }
func (s *stubProvider) Dispatch(_ context.Context, ev ActionEvent) error {
	s.events = append(s.events, ev)
	return nil
}
func (s *stubProvider) Close() error {
	s.closed = true
	return s.closeErr
}

func (s *stubProvider) DynamicOptions(_ context.Context, key string, _ map[string]any) []SelectOption {
	if key == "stub.items" {
		return []SelectOption{{Label: "Item A", Value: "a"}}
	}
	return nil
}

func (s *stubProvider) PropertyInspectorURL(
	ctxID string,
	actionUUID string,
	_ deck.ControllerType,
	_ deck.Coordinates,
	_ int,
	_ map[string]any,
) (PropertyInspectorSession, bool) {
	return PropertyInspectorSession{
		Title: actionUUID,
		URL:   "http://127.0.0.1:9999/pi/" + ctxID,
	}, true
}

type stubHost struct{}

func (h *stubHost) UpdateVisual(string, VisualPatch)                   {}
func (h *stubHost) SaveSettings(string, map[string]any) error          { return nil }
func (h *stubHost) GetGlobalSettings(string) map[string]any            { return nil }
func (h *stubHost) SaveGlobalSettings(string, map[string]any) error    { return nil }
func (h *stubHost) Log(string, string)                                  {}
func (h *stubHost) Deck() DeckControl                                   { return nil }

func TestRegistryRegistrationSortingDispatchAndClose(t *testing.T) {
	reg := NewRegistry()
	host := &stubHost{}

	p1 := &stubProvider{
		name: "Alpha",
		kind: ProviderBuiltin,
		manifests: []Manifest{
			{
				UUID:     "com.example.alpha",
				Name:     "Alpha",
				Version:  "1.0.0",
				Provider: ProviderBuiltin,
				Actions: []ActionDescriptor{
					{UUID: "com.example.z", Name: "Zebra", Category: "Beta"},
					{UUID: "com.example.a", Name: "Alpha", Category: "Alpha"},
				},
			},
		},
	}
	p1.actions = p1.manifests[0].Actions

	if err := reg.RegisterProvider(p1); err != nil {
		t.Fatalf("RegisterProvider(p1) error = %v", err)
	}

	// SetHost after registration binds existing providers.
	reg.SetHost(host)
	if p1.host != host {
		t.Fatal("expected SetHost to bind previously registered provider")
	}

	// Duplicate action UUID fails.
	dup := &stubProvider{
		name: "Dup",
		kind: ProviderWasm,
		manifests: []Manifest{
			{
				UUID:     "com.example.dup",
				Name:     "Dup",
				Provider: ProviderWasm,
				Actions:  []ActionDescriptor{{UUID: "com.example.a", Name: "Conflict"}},
			},
		},
	}
	dup.actions = dup.manifests[0].Actions
	if err := reg.RegisterProvider(dup); err == nil {
		t.Fatal("expected duplicate action UUID registration to fail")
	}

	// Verify deterministic sorting by Category then Name.
	list := reg.Actions()
	if len(list) != 2 || list[0].UUID != "com.example.a" || list[1].UUID != "com.example.z" {
		t.Fatalf("unexpected sorted Actions(): %+v", list)
	}
	if _, ok := reg.Action("com.example.a"); !ok {
		t.Error("expected Action lookup to find com.example.a")
	}

	// Dispatch routes to owning provider.
	ev := ActionEvent{Type: EventKeyDown, Context: "ctx-1", ActionUUID: "com.example.z"}
	if err := reg.Dispatch(context.Background(), ev); err != nil {
		t.Fatalf("Dispatch() error = %v", err)
	}
	if len(p1.events) != 1 || p1.events[0].Context != "ctx-1" {
		t.Fatalf("unexpected dispatched events: %+v", p1.events)
	}

	if err := reg.Dispatch(context.Background(), ActionEvent{ActionUUID: "missing"}); err == nil {
		t.Error("expected error dispatching unknown action")
	}

	// DynamicOptions delegates to DynamicOptionsProviders.
	opts := reg.DynamicOptions(context.Background(), "stub.items", nil)
	if len(opts) != 1 || opts[0].Value != "a" {
		t.Fatalf("unexpected DynamicOptions(): %+v", opts)
	}
	if empty := reg.DynamicOptions(context.Background(), "other", nil); len(empty) != 0 {
		t.Fatalf("expected empty DynamicOptions for unknown key, got %+v", empty)
	}

	// PropertyInspectorSession and RefreshProvider work cleanly.
	if sess, ok := reg.PropertyInspectorSession("ctx-1", "com.example.z", deck.ControllerKeypad, deck.Coordinates{}, 0, nil); !ok || sess.URL != "http://127.0.0.1:9999/pi/ctx-1" {
		t.Fatalf("unexpected PropertyInspectorSession: %+v, ok=%v", sess, ok)
	}
	p1.manifests[0].Actions = []ActionDescriptor{{UUID: "com.example.b", Name: "Beta", Category: "Beta"}}
	p1.actions = p1.manifests[0].Actions
	if err := reg.RefreshProvider(p1); err != nil {
		t.Fatalf("RefreshProvider(p1) error = %v", err)
	}
	if _, ok := reg.Action("com.example.z"); ok {
		t.Error("expected old action com.example.z removed after RefreshProvider")
	}
	if _, ok := reg.Action("com.example.b"); !ok {
		t.Error("expected new action com.example.b present after RefreshProvider")
	}

	// Verify Close aggregates errors and marks providers closed.
	p1.closeErr = errors.New("boom")
	if err := reg.Close(); err == nil {
		t.Error("expected Close() to surface provider close error")
	}
	if !p1.closed {
		t.Error("expected provider Close() to be called")
	}
	if len(reg.Actions()) != 0 || len(reg.Providers()) != 0 {
		t.Error("expected registry to be empty after Close()")
	}
}
