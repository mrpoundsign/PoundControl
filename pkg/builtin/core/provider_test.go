package core

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mrpoundsign/PoundControl/pkg/deck"
	"github.com/mrpoundsign/PoundControl/pkg/plugin"
)

type mockDeckControl struct {
	calls     []string
	triggerFn func(ctx context.Context, uuid string, settings map[string]any) error
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

func (m *mockDeckControl) TriggerAction(ctx context.Context, uuid string, settings map[string]any) error {
	m.calls = append(m.calls, "trigger:"+uuid)
	if m.triggerFn != nil {
		return m.triggerFn(ctx, uuid, settings)
	}
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

func TestMultiActionNormalizationAndEstimatedDuration(t *testing.T) {
	origSettings := map[string]any{"page": 2}
	raw := []MultiActionStep{
		{ActionUUID: ActionPageGoTo, Settings: origSettings, DelayMs: 150, Disabled: true},
		{ActionUUID: "", DelayMs: 300},
		{ID: "dup", ActionUUID: ActionDelay, Settings: map[string]any{"ms": 200}, DelayMs: 50},
		{ID: "dup", ActionUUID: ActionPageNext},
	}

	norm := NormalizeMultiActionSteps(raw)
	if len(norm) != 5 {
		t.Fatalf("expected 5 normalized steps, got %d: %+v", len(norm), norm)
	}
	if norm[0].ActionUUID != ActionDelay || StepDelayMs(norm[0]) != 150 || !norm[0].Disabled {
		t.Errorf("unexpected norm[0]: %+v", norm[0])
	}
	if norm[1].ActionUUID != ActionPageGoTo || norm[1].DelayMs != 0 || !norm[1].Disabled {
		t.Errorf("unexpected norm[1]: %+v", norm[1])
	}
	origSettings["page"] = 99
	if norm[1].Settings["page"] != 2 {
		t.Errorf("expected deep-cloned settings map, got %v", norm[1].Settings["page"])
	}
	if norm[2].ActionUUID != ActionDelay || StepDelayMs(norm[2]) != 300 || norm[2].Disabled {
		t.Errorf("unexpected norm[2]: %+v", norm[2])
	}
	if norm[3].ActionUUID != ActionDelay || StepDelayMs(norm[3]) != 250 {
		t.Errorf("unexpected norm[3]: %+v", norm[3])
	}
	if norm[4].ActionUUID != ActionPageNext {
		t.Errorf("unexpected norm[4]: %+v", norm[4])
	}

	seen := make(map[string]struct{}, len(norm))
	for i, st := range norm {
		if st.ID == "" {
			t.Fatalf("step %d missing ID", i)
		}
		if _, dup := seen[st.ID]; dup {
			t.Fatalf("duplicate generated ID %q at step %d", st.ID, i)
		}
		seen[st.ID] = struct{}{}
	}

	// Disabled step 0+1 (150ms) should be excluded; enabled steps contribute 300 + 250 = 550ms.
	if got := MultiActionEstimatedDurationMs(norm); got != 550 {
		t.Errorf("MultiActionEstimatedDurationMs() = %d, want 550", got)
	}

	encoded, err := EncodeMultiActionSteps(nil)
	if err != nil || encoded != "[]" {
		t.Fatalf("EncodeMultiActionSteps(nil) = (%q, %v), want (\"[]\", nil)", encoded, err)
	}
	encoded, err = EncodeMultiActionSteps(norm)
	if err != nil {
		t.Fatalf("EncodeMultiActionSteps(norm) error = %v", err)
	}
	parsed, err := ParseMultiActionSteps(map[string]any{"steps": encoded})
	if err != nil {
		t.Fatalf("ParseMultiActionSteps error = %v", err)
	}
	if len(parsed) != len(norm) {
		t.Fatalf("round-trip parsed %d steps, want %d", len(parsed), len(norm))
	}
}

func TestMultiActionDisabledStepsContinueOnErrorAndDepthGuard(t *testing.T) {
	p := New()
	defer func() { _ = p.Close() }()

	deckCtrl := &mockDeckControl{}
	host := &mockHost{deck: deckCtrl}
	p.BindHost(host)
	ctx := context.Background()

	// Disabled step should be skipped; failing step stops sequence when continueOnError=false.
	deckCtrl.triggerFn = func(_ context.Context, uuid string, _ map[string]any) error {
		if uuid == ActionProfileSwitch {
			return errors.New("boom")
		}
		return nil
	}
	steps := []MultiActionStep{
		{ID: "s1", ActionUUID: ActionPageNext, Disabled: true},
		{ID: "s2", ActionUUID: ActionProfileSwitch},
		{ID: "s3", ActionUUID: ActionPagePrev},
	}
	err := p.Dispatch(ctx, plugin.ActionEvent{
		Type:       plugin.EventKeyDown,
		Context:    "ma-stop",
		ActionUUID: ActionMultiAction,
		Settings: map[string]any{
			"steps":           steps,
			"continueOnError": false,
		},
	})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected error containing boom, got %v", err)
	}
	if len(deckCtrl.calls) != 1 || deckCtrl.calls[0] != "trigger:"+ActionProfileSwitch {
		t.Fatalf("expected only failing step to run, got calls=%v", deckCtrl.calls)
	}
	if len(host.patches) == 0 || host.patches[len(host.patches)-1].Flash != "alert" {
		t.Errorf("expected alert flash on error, got %+v", host.patches)
	}

	// With continueOnError=true, remaining enabled steps run and error is still reported.
	deckCtrl.calls = nil
	host.patches = nil
	err = p.Dispatch(ctx, plugin.ActionEvent{
		Type:       plugin.EventKeyDown,
		Context:    "ma-cont",
		ActionUUID: ActionMultiAction,
		Settings: map[string]any{
			"steps":           steps,
			"continueOnError": true,
		},
	})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected error containing boom with continueOnError=true, got %v", err)
	}
	wantCalls := []string{"trigger:" + ActionProfileSwitch, "trigger:" + ActionPagePrev}
	if len(deckCtrl.calls) != len(wantCalls) || deckCtrl.calls[0] != wantCalls[0] || deckCtrl.calls[1] != wantCalls[1] {
		t.Fatalf("calls = %v, want %v", deckCtrl.calls, wantCalls)
	}
	if len(host.patches) == 0 || host.patches[len(host.patches)-1].Flash != "alert" {
		t.Errorf("expected alert flash when any step fails under continueOnError, got %+v", host.patches)
	}

	// Recursion depth guard rejects nested multi-actions beyond maxMultiActionDepth.
	deckCtrl.calls = nil
	host.patches = nil
	nestedSettings := map[string]any{
		"steps": []MultiActionStep{{ID: "loop", ActionUUID: ActionMultiAction}},
	}
	deckCtrl.triggerFn = func(callCtx context.Context, uuid string, _ map[string]any) error {
		if uuid == ActionMultiAction {
			return p.Dispatch(callCtx, plugin.ActionEvent{
				Type:       plugin.EventKeyDown,
				Context:    "ma-loop",
				ActionUUID: ActionMultiAction,
				Settings:   nestedSettings,
			})
		}
		return nil
	}
	err = p.Dispatch(ctx, plugin.ActionEvent{
		Type:       plugin.EventKeyDown,
		Context:    "ma-loop",
		ActionUUID: ActionMultiAction,
		Settings:   nestedSettings,
	})
	if err == nil || !strings.Contains(err.Error(), "depth") {
		t.Fatalf("expected recursion depth error, got %v", err)
	}
}
