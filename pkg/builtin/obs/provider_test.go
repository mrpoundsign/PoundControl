package obs

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/mrpoundsign/PoundControl/internal/app"
	"github.com/mrpoundsign/PoundControl/pkg/deck"
	"github.com/mrpoundsign/PoundControl/pkg/plugin"
	"github.com/mrpoundsign/PoundControl/pkg/secrets"
)

func setupOBSTestEngine(t *testing.T) (*app.Engine, *Provider, *secrets.MemoryStore) {
	t.Helper()
	sec := secrets.NewMemoryStore()
	prov := New(sec)
	reg := plugin.NewRegistry()
	if err := reg.RegisterProvider(prov); err != nil {
		t.Fatalf("RegisterProvider(obs) error = %v", err)
	}
	eng, err := app.NewEngine(deck.NewMemoryStore(nil), reg)
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	return eng, prov, sec
}

func TestProviderActionsDynamicOptionsAndLiveEvents(t *testing.T) {
	srv := newMockOBSServer("obs-pass")
	defer srv.Close()

	eng, prov, sec := setupOBSTestEngine(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Offline slot shows Offline and returns error when pressed.
	if _, err := eng.AssignKeypadAction(deck.Coordinates{Row: 0, Col: 0}, ActionSwitchScene); err != nil {
		t.Fatalf("AssignKeypadAction error = %v", err)
	}
	if err := eng.UpdateKeypadSlot(deck.Coordinates{Row: 0, Col: 0}, "Main", "", map[string]any{
		"sceneName": "Main",
		"target":    "program",
	}); err != nil {
		t.Fatalf("UpdateKeypadSlot error = %v", err)
	}
	if snap := eng.Snapshot(); snap.Slots["0,0"].Visual.Subtitle != "Offline" || snap.Slots["0,0"].Visual.State != 0 {
		t.Fatalf("expected Offline subtitle and State 0 while disconnected, got (%q, %d)", snap.Slots["0,0"].Visual.Subtitle, snap.Slots["0,0"].Visual.State)
	}
	if err := eng.PressKey(ctx, deck.Coordinates{Row: 0, Col: 0}); err == nil {
		t.Fatal("expected error pressing OBS action while offline")
	}

	// Save connection config + secret and connect.
	if err := prov.SaveConnectionConfig(ConnectionConfig{
		Host:        srv.Host,
		Port:        srv.Port,
		AutoConnect: true,
	}, "obs-pass", true); err != nil {
		t.Fatalf("SaveConnectionConfig error = %v", err)
	}
	if pw, err := sec.Get(SecretKeyPassword); err != nil || pw != "obs-pass" {
		t.Fatalf("expected password stored in secrets store, got (%q, %v)", pw, err)
	}

	if err := prov.Connect(ctx); err != nil {
		t.Fatalf("prov.Connect error = %v", err)
	}

	// Active scene "Main" is highlighted ACTIVE immediately after hydrateState.
	if snap := eng.Snapshot(); snap.Slots["0,0"].Visual.Subtitle != "ACTIVE" || snap.Slots["0,0"].Visual.State != 1 {
		t.Fatalf("expected Main scene ACTIVE with State 1 after connect, got (%q, %d)", snap.Slots["0,0"].Visual.Subtitle, snap.Slots["0,0"].Visual.State)
	}

	// DynamicOptions returns scenes, inputs, and sources.
	scenes := eng.DynamicOptions(ctx, "obs.scenes", nil)
	if len(scenes) != 3 {
		t.Fatalf("expected 3 scenes in DynamicOptions, got %+v", scenes)
	}
	inputs := eng.DynamicOptions(ctx, "obs.inputs", nil)
	if len(inputs) != 2 {
		t.Fatalf("expected 2 inputs in DynamicOptions, got %+v", inputs)
	}
	sources := eng.DynamicOptions(ctx, "obs.sources", map[string]any{"sceneName": "Main"})
	if len(sources) != 2 || sources[0].Value != "Webcam" {
		t.Fatalf("unexpected obs.sources DynamicOptions: %+v", sources)
	}

	// Bind second scene "BRB" at (0,1), source visibility at (0,2), mute at (0,3), stream at (0,4),
	// record at (1,0), replay at (1,1), studio at (1,2), custom at (1,3), volume at Dial 0.
	assignments := []struct {
		coords   deck.Coordinates
		uuid     string
		settings map[string]any
	}{
		{deck.Coordinates{Row: 0, Col: 1}, ActionSwitchScene, map[string]any{"sceneName": "BRB", "target": "program"}},
		{deck.Coordinates{Row: 0, Col: 2}, ActionToggleSource, map[string]any{"sceneName": "Main", "sourceName": "Webcam", "mode": "toggle"}},
		{deck.Coordinates{Row: 0, Col: 3}, ActionToggleMute, map[string]any{"inputName": "Mic/Aux", "mode": "toggle"}},
		{deck.Coordinates{Row: 0, Col: 4}, ActionStream, map[string]any{"mode": "toggle"}},
		{deck.Coordinates{Row: 1, Col: 0}, ActionRecord, map[string]any{"mode": "toggle"}},
		{deck.Coordinates{Row: 1, Col: 1}, ActionReplayBuffer, map[string]any{"mode": "save"}},
		{deck.Coordinates{Row: 1, Col: 2}, ActionStudioMode, map[string]any{"mode": "toggle"}},
		{deck.Coordinates{Row: 1, Col: 3}, ActionCustomRequest, map[string]any{"requestType": "TriggerHotkeyByName", "requestData": `{"hotkeyName":"OBSBasic.Screenshot"}`}},
	}
	for _, a := range assignments {
		if _, err := eng.AssignKeypadAction(a.coords, a.uuid); err != nil {
			t.Fatalf("AssignKeypadAction(%s) error = %v", a.uuid, err)
		}
		if err := eng.UpdateKeypadSlot(a.coords, "", "", a.settings); err != nil {
			t.Fatalf("UpdateKeypadSlot(%s) error = %v", a.uuid, err)
		}
	}
	if _, err := eng.AssignDialAction(0, ActionAudioVolume); err != nil {
		t.Fatalf("AssignDialAction error = %v", err)
	}
	if err := eng.UpdateDialSlot(0, "Mic Gain", "", map[string]any{
		"inputName":  "Mic/Aux",
		"stepDb":     2,
		"pushAction": "toggle_mute",
	}); err != nil {
		t.Fatalf("UpdateDialSlot error = %v", err)
	}

	// Execute all actions.
	for _, a := range assignments {
		if err := eng.PressKey(ctx, a.coords); err != nil {
			t.Fatalf("PressKey(%s) error = %v", a.uuid, err)
		}
	}
	if err := eng.RotateDial(ctx, 0, 2); err != nil {
		t.Fatalf("RotateDial error = %v", err)
	}

	snap := eng.Snapshot()
	if snap.Slots["0,1"].Visual.Subtitle != "ACTIVE" || snap.Slots["0,0"].Visual.Subtitle != "Ready" {
		t.Errorf("unexpected scene switch states: 0,0=%q 0,1=%q",
			snap.Slots["0,0"].Visual.Subtitle, snap.Slots["0,1"].Visual.Subtitle)
	}
	if snap.Slots["0,1"].Visual.State != 1 || snap.Slots["0,0"].Visual.State != 0 {
		t.Errorf("unexpected scene Visual.State: 0,0=%d 0,1=%d, want 0 and 1",
			snap.Slots["0,0"].Visual.State, snap.Slots["0,1"].Visual.State)
	}
	if snap.Slots["0,2"].Visual.Subtitle != "HIDDEN" || snap.Slots["0,2"].Visual.State != 0 {
		t.Errorf("expected Webcam HIDDEN with State 0 after toggle, got (%q, %d)", snap.Slots["0,2"].Visual.Subtitle, snap.Slots["0,2"].Visual.State)
	}
	if snap.Slots["0,3"].Visual.Subtitle != "MUTED" || snap.Slots["0,3"].Visual.State != 1 {
		t.Errorf("expected Mic/Aux MUTED with State 1 after toggle, got (%q, %d)", snap.Slots["0,3"].Visual.Subtitle, snap.Slots["0,3"].Visual.State)
	}
	if snap.Slots["0,4"].Visual.Subtitle != "ON AIR" || snap.Slots["0,4"].Visual.State != 1 {
		t.Errorf("expected Stream ON AIR with State 1, got (%q, %d)", snap.Slots["0,4"].Visual.Subtitle, snap.Slots["0,4"].Visual.State)
	}
	if snap.Slots["1,0"].Visual.Subtitle != "REC" || snap.Slots["1,0"].Visual.State != 1 {
		t.Errorf("expected Record REC with State 1, got (%q, %d)", snap.Slots["1,0"].Visual.Subtitle, snap.Slots["1,0"].Visual.State)
	}
	if snap.Slots["1,2"].Visual.Subtitle != "STUDIO ON" || snap.Slots["1,2"].Visual.State != 1 {
		t.Errorf("expected Studio STUDIO ON with State 1, got (%q, %d)", snap.Slots["1,2"].Visual.Subtitle, snap.Slots["1,2"].Visual.State)
	}

	srv.mu.Lock()
	savedCount := srv.replaySaved
	customCalls := append([]string(nil), srv.customCalls...)
	micDb := srv.inputDb["Mic/Aux"]
	srv.mu.Unlock()
	if savedCount != 1 {
		t.Errorf("replaySaved = %d, want 1", savedCount)
	}
	if len(customCalls) != 1 || customCalls[0] != "OBSBasic.Screenshot" {
		t.Errorf("unexpected customCalls: %v", customCalls)
	}
	if micDb != -2.0 {
		t.Errorf("Mic/Aux dB = %v, want -2.0", micDb)
	}

	// External OBS event updates tile live without local press.
	var mu sync.Mutex
	sceneCh := make(chan string, 4)
	unsub := eng.Subscribe(func(s app.Snapshot) {
		mu.Lock()
		sub := s.Slots["0,0"].Visual.Subtitle
		mu.Unlock()
		if sub == "ACTIVE" {
			select {
			case sceneCh <- sub:
			default:
			}
		}
	})
	defer unsub()

	srv.BroadcastEvent("CurrentProgramSceneChanged", map[string]any{"sceneName": "Main"})
	select {
	case <-sceneCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for external CurrentProgramSceneChanged tile update")
	}
}
