package core

import (
	"context"
	"testing"
	"time"

	"github.com/mrpoundsign/PoundDeck/pkg/deck"
	"github.com/mrpoundsign/PoundDeck/pkg/plugin"
)

type mockDeckControl struct {
	calls []string
}

func (m *mockDeckControl) SwitchProfile(ref string) error {
	m.calls = append(m.calls, "profile:"+ref)
	return nil
}

func (m *mockDeckControl) NextPage(wrap bool) error {
	if wrap {
		m.calls = append(m.calls, "next:wrap")
	} else {
		m.calls = append(m.calls, "next:nowrap")
	}
	return nil
}

func (m *mockDeckControl) PrevPage(wrap bool) error {
	if wrap {
		m.calls = append(m.calls, "prev:wrap")
	} else {
		m.calls = append(m.calls, "prev:nowrap")
	}
	return nil
}

func (m *mockDeckControl) GoToPage(p int) error {
	m.calls = append(m.calls, "goto:"+string(rune('0'+p)))
	return nil
}

func (m *mockDeckControl) OpenFolder(id, name string) error {
	m.calls = append(m.calls, "folder:"+id+":"+name)
	return nil
}

func (m *mockDeckControl) CloseFolder() error {
	m.calls = append(m.calls, "folder:back")
	return nil
}

func (m *mockDeckControl) TriggerAction(_ context.Context, uuid string, _ map[string]any) error {
	m.calls = append(m.calls, "trigger:"+uuid)
	return nil
}

type mockHost struct {
	deck    *mockDeckControl
	patches []plugin.VisualPatch
}

func (h *mockHost) UpdateVisual(_ string, patch plugin.VisualPatch) {
	h.patches = append(h.patches, patch)
}

func (h *mockHost) SaveSettings(string, map[string]any) error       { return nil }
func (h *mockHost) GetGlobalSettings(string) map[string]any         { return nil }
func (h *mockHost) SaveGlobalSettings(string, map[string]any) error { return nil }
func (h *mockHost) Log(string, string)                               {}
func (h *mockHost) Deck() plugin.DeckControl                        { return h.deck }

func TestCoreProviderNavigationAndManifest(t *testing.T) {
	p := New()
	defer func() { _ = p.Close() }()

	if p.Kind() != plugin.ProviderBuiltin {
		t.Fatalf("Kind() = %q, want builtin", p.Kind())
	}
	for _, m := range p.Manifests() {
		if err := m.Validate(); err != nil {
			t.Fatalf("Manifest.Validate() error = %v", err)
		}
	}

	host := &mockHost{deck: &mockDeckControl{}}
	p.BindHost(host)

	ctx := context.Background()
	events := []plugin.ActionEvent{
		{Type: plugin.EventKeyDown, Context: "c1", ActionUUID: ActionPageNext},
		{Type: plugin.EventKeyDown, Context: "c2", ActionUUID: ActionPagePrev, Settings: map[string]any{"wrap": false}},
		{Type: plugin.EventKeyDown, Context: "c3", ActionUUID: ActionPageGoTo, Settings: map[string]any{"page": 2}},
		{Type: plugin.EventKeyDown, Context: "c4", ActionUUID: ActionProfileSwitch, Settings: map[string]any{"profile": "OBS"}},
		{Type: plugin.EventKeyDown, Context: "c5", ActionUUID: ActionFolderOpen, Settings: map[string]any{"folderName": "Scenes"}},
		{Type: plugin.EventKeyDown, Context: "c6", ActionUUID: ActionFolderBack},
	}

	for _, ev := range events {
		if err := p.Dispatch(ctx, ev); err != nil {
			t.Fatalf("Dispatch(%s) error = %v", ev.ActionUUID, err)
		}
	}

	want := []string{
		"next:wrap",
		"prev:nowrap",
		"goto:2",
		"profile:OBS",
		"folder:c5:Scenes",
		"folder:back",
	}
	if len(host.deck.calls) != len(want) {
		t.Fatalf("calls = %v, want %v", host.deck.calls, want)
	}
	for i := range want {
		if host.deck.calls[i] != want[i] {
			t.Errorf("call[%d] = %q, want %q", i, host.deck.calls[i], want[i])
		}
	}
}

func TestCoreProviderCounterAndMultiActionAndDelayCancel(t *testing.T) {
	p := New()
	defer func() { _ = p.Close() }()

	host := &mockHost{deck: &mockDeckControl{}}
	p.BindHost(host)
	ctx := context.Background()

	// Counter appears at initial 10, presses +2, then encoder rotates -3 ticks (-6).
	settings := map[string]any{"initial": 10, "step": 2}
	if err := p.Dispatch(ctx, plugin.ActionEvent{
		Type: plugin.EventWillAppear, Context: "cnt", ActionUUID: ActionCounter, Controller: deck.ControllerEncoder, Settings: settings,
	}); err != nil {
		t.Fatalf("WillAppear error = %v", err)
	}
	if err := p.Dispatch(ctx, plugin.ActionEvent{
		Type: plugin.EventKeyDown, Context: "cnt", ActionUUID: ActionCounter, Settings: settings,
	}); err != nil {
		t.Fatalf("KeyDown error = %v", err)
	}
	if err := p.Dispatch(ctx, plugin.ActionEvent{
		Type: plugin.EventDialRotate, Context: "cnt", ActionUUID: ActionCounter, Controller: deck.ControllerEncoder, Settings: settings, Ticks: -3,
	}); err != nil {
		t.Fatalf("DialRotate error = %v", err)
	}

	if len(host.patches) != 3 {
		t.Fatalf("expected 3 visual patches, got %d", len(host.patches))
	}
	if *host.patches[0].Subtitle != "10" || *host.patches[1].Subtitle != "12" || *host.patches[2].Subtitle != "6" {
		t.Fatalf("unexpected counter sequence: %q -> %q -> %q",
			*host.patches[0].Subtitle, *host.patches[1].Subtitle, *host.patches[2].Subtitle)
	}
	if host.patches[2].Feedback["value"] != "6" || host.patches[2].Flash != "ok" {
		t.Errorf("unexpected encoder feedback/flash: %+v", host.patches[2])
	}

	// Multi-Action executes ordered steps.
	if err := p.Dispatch(ctx, plugin.ActionEvent{
		Type:       plugin.EventKeyDown,
		Context:    "ma",
		ActionUUID: ActionMultiAction,
		Settings: map[string]any{
			"steps": `[{"actionUuid":"` + ActionPageNext + `"},{"actionUuid":"` + ActionDelay + `","settings":{"ms":1},"delayMs":1}]`,
		},
	}); err != nil {
		t.Fatalf("MultiAction error = %v", err)
	}
	if len(host.deck.calls) != 2 || host.deck.calls[0] != "trigger:"+ActionPageNext {
		t.Fatalf("unexpected multiaction calls: %v", host.deck.calls)
	}

	// Delay cancels cleanly when context is canceled.
	cctx, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
	defer cancel()
	if err := p.Dispatch(cctx, plugin.ActionEvent{
		Type: plugin.EventKeyDown, Context: "d1", ActionUUID: ActionDelay, Settings: map[string]any{"ms": 5000},
	}); err == nil {
		t.Error("expected Delay to fail on canceled context")
	}
}
