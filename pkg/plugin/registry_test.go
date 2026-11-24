package plugin

import (
	"context"
	"errors"
	"testing"

	"github.com/mrpoundsign/PoundControl/pkg/deck"
)

type stubProvider struct {
	name             string
	kind             ProviderKind
	manifests        []Manifest
	actions          []ActionDescriptor
	host             HostAPI
	events           []ActionEvent
	enabled          map[string]bool
	optKey           string
	closed           bool
	closeErr         error
	configActionFn   func(ctx context.Context, pluginUUID, actionKey string, settings map[string]any) error
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
func (s *stubProvider) SetPluginEnabled(_ context.Context, pluginUUID string, enabled bool) error {
	if s.enabled == nil {
		s.enabled = make(map[string]bool)
	}
	s.enabled[pluginUUID] = enabled
	return nil
}
func (s *stubProvider) OnConfigAction(ctx context.Context, pluginUUID, actionKey string, settings map[string]any) error {
	if s.configActionFn != nil {
		return s.configActionFn(ctx, pluginUUID, actionKey, settings)
	}
	return errors.New("config actions unsupported")
}
func (s *stubProvider) Close() error {
	s.closed = true
	return s.closeErr
}

func (s *stubProvider) DynamicOptions(_ context.Context, key string, _ map[string]any) []SelectOption {
	wantKey := s.optKey
	if wantKey == "" {
		wantKey = "stub.items"
	}
	if key == wantKey {
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

func TestRegistryEnableDisableAndRequiredPlugins(t *testing.T) {
	reg := NewRegistry()
	reqProv := &stubProvider{
		name:   "Required",
		kind:   ProviderBuiltin,
		optKey: "req.items",
		manifests: []Manifest{{
			UUID:     "com.example.req",
			Name:     "Required Core",
			Provider: ProviderBuiltin,
			Required: true,
			Actions:  []ActionDescriptor{{UUID: "com.example.req.act", Name: "Core Act"}},
		}},
	}
	reqProv.actions = reqProv.manifests[0].Actions

	optProv := &stubProvider{
		name: "Optional",
		kind: ProviderBuiltin,
		manifests: []Manifest{{
			UUID:     "com.example.opt",
			Name:     "Optional Feature",
			Provider: ProviderBuiltin,
			Actions:  []ActionDescriptor{{UUID: "com.example.opt.act", Name: "Opt Act"}},
		}},
	}
	optProv.actions = optProv.manifests[0].Actions

	if err := reg.RegisterProvider(reqProv); err != nil {
		t.Fatalf("RegisterProvider(reqProv) error = %v", err)
	}
	if err := reg.RegisterProvider(optProv); err != nil {
		t.Fatalf("RegisterProvider(optProv) error = %v", err)
	}

	// Required plugins ignore SetDisabledPlugins and fail SetPluginEnabled(false).
	reg.SetDisabledPlugins([]string{"com.example.req", "com.example.opt"})
	if !reg.IsPluginEnabled("com.example.req") {
		t.Fatal("expected required plugin to remain enabled")
	}
	if reg.IsPluginEnabled("com.example.opt") {
		t.Fatal("expected optional plugin to be disabled")
	}
	if err := reg.SetPluginEnabled("com.example.req", false); err == nil {
		t.Fatal("expected error disabling required plugin")
	}

	// Filtering hides disabled plugin from Manifests, Actions, Action, Dispatch, DynamicOptions, PI; keeps AllManifests & PluginForAction.
	if len(reg.Manifests()) != 1 || len(reg.AllManifests()) != 2 {
		t.Fatalf("unexpected Manifests/AllManifests counts: %d / %d", len(reg.Manifests()), len(reg.AllManifests()))
	}
	if _, ok := reg.Action("com.example.opt.act"); ok {
		t.Fatal("expected disabled action hidden from Action()")
	}
	if len(reg.Actions()) != 1 {
		t.Fatalf("expected 1 enabled action, got %d", len(reg.Actions()))
	}
	if m, ok := reg.PluginForAction("com.example.opt.act"); !ok || m.UUID != "com.example.opt" {
		t.Fatalf("expected PluginForAction to resolve disabled plugin, got (%+v, %v)", m, ok)
	}
	if err := reg.Dispatch(context.Background(), ActionEvent{ActionUUID: "com.example.opt.act"}); err == nil {
		t.Fatal("expected error dispatching disabled action")
	}
	if opts := reg.DynamicOptions(context.Background(), "stub.items", nil); len(opts) != 0 {
		t.Fatalf("expected disabled provider skipped for DynamicOptions, got %+v", opts)
	}
	if _, ok := reg.PropertyInspectorSession("c", "com.example.opt.act", deck.ControllerKeypad, deck.Coordinates{}, 0, nil); ok {
		t.Fatal("expected disabled action PI session false")
	}

	// Re-enabling restores lookups & notifies listener.
	if err := reg.SetPluginEnabled("com.example.opt", true); err != nil {
		t.Fatalf("SetPluginEnabled(true) error = %v", err)
	}
	if err := reg.NotifyPluginEnabled(context.Background(), "com.example.opt", true); err != nil {
		t.Fatalf("NotifyPluginEnabled() error = %v", err)
	}
	if !optProv.enabled["com.example.opt"] {
		t.Fatal("expected PluginLifecycleListener invoked on re-enable")
	}
	if opts := reg.DynamicOptions(context.Background(), "stub.items", nil); len(opts) != 1 {
		t.Fatalf("expected DynamicOptions restored after re-enable, got %+v", opts)
	}
	if _, ok := reg.Action("com.example.opt.act"); !ok {
		t.Fatal("expected action restored after SetPluginEnabled(true)")
	}
}

type noConfigActionProvider struct {
	manifests []Manifest
	actions   []ActionDescriptor
}

func (n *noConfigActionProvider) Kind() ProviderKind          { return ProviderBuiltin }
func (n *noConfigActionProvider) Name() string                { return "NoConfigAction" }
func (n *noConfigActionProvider) Manifests() []Manifest       { return n.manifests }
func (n *noConfigActionProvider) Actions() []ActionDescriptor { return n.actions }
func (n *noConfigActionProvider) BindHost(HostAPI)            {}
func (n *noConfigActionProvider) Dispatch(context.Context, ActionEvent) error {
	return nil
}
func (n *noConfigActionProvider) Close() error { return nil }

func TestRegistryInvokeConfigAction(t *testing.T) {
	reg := NewRegistry()

	var (
		gotPlugin   string
		gotAction   string
		gotSettings map[string]any
	)
	p := &stubProvider{
		name: "Interactive",
		kind: ProviderWasm,
		manifests: []Manifest{{
			UUID:     "com.example.interactive",
			Name:     "Interactive",
			Provider: ProviderWasm,
			Actions:  []ActionDescriptor{{UUID: "com.example.interactive.act", Name: "Act"}},
		}},
		configActionFn: func(_ context.Context, pluginUUID, actionKey string, settings map[string]any) error {
			gotPlugin = pluginUUID
			gotAction = actionKey
			gotSettings = settings
			return nil
		},
	}
	p.actions = p.manifests[0].Actions
	if err := reg.RegisterProvider(p); err != nil {
		t.Fatalf("RegisterProvider(p) error = %v", err)
	}

	noAct := &noConfigActionProvider{
		manifests: []Manifest{{
			UUID:     "com.example.static",
			Name:     "Static",
			Provider: ProviderBuiltin,
			Actions:  []ActionDescriptor{{UUID: "com.example.static.act", Name: "Static Act"}},
		}},
	}
	noAct.actions = noAct.manifests[0].Actions
	if err := reg.RegisterProvider(noAct); err != nil {
		t.Fatalf("RegisterProvider(noAct) error = %v", err)
	}

	if err := reg.InvokeConfigAction(context.Background(), "com.example.interactive", "authConnect", map[string]any{"clientId": "abc"}); err != nil {
		t.Fatalf("InvokeConfigAction() error = %v", err)
	}
	if gotPlugin != "com.example.interactive" || gotAction != "authConnect" || gotSettings["clientId"] != "abc" {
		t.Fatalf("unexpected InvokeConfigAction delivery: plugin=%q action=%q settings=%+v", gotPlugin, gotAction, gotSettings)
	}

	if err := reg.InvokeConfigAction(context.Background(), "com.example.unknown", "authConnect", nil); err == nil {
		t.Fatal("expected error for unknown plugin")
	}
	if err := reg.InvokeConfigAction(context.Background(), "com.example.static", "btn", nil); err == nil {
		t.Fatal("expected error when provider does not implement ConfigActionHandler")
	}
	if err := reg.InvokeConfigAction(context.Background(), "com.example.interactive", "", nil); err == nil {
		t.Fatal("expected error for empty actionKey")
	}

	if err := reg.SetPluginEnabled("com.example.interactive", false); err != nil {
		t.Fatalf("SetPluginEnabled(false) error = %v", err)
	}
	if err := reg.InvokeConfigAction(context.Background(), "com.example.interactive", "authConnect", nil); err == nil {
		t.Fatal("expected error invoking config action on disabled plugin")
	}
}
