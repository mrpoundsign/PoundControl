package obs

import (
	"context"
	"math"
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
		"stepPct":    2.5,
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

	// Mic/Aux started muted (via 0,3 toggle) so dial shows MUTED and indicator 0,
	// while underlying OBS volume advanced from 79.5% (+2 * 2.5% = 84.5%).
	dial0 := snap.DialSlots[0].Visual
	if dial0.Subtitle != "MUTED" || dial0.Feedback["indicator"] != 0 {
		t.Errorf("expected muted dial visual with indicator 0, got subtitle=%q feedback=%+v", dial0.Subtitle, dial0.Feedback)
	}

	srv.mu.Lock()
	savedCount := srv.replaySaved
	customCalls := append([]string(nil), srv.customCalls...)
	micMul := srv.inputMul["Mic/Aux"]
	srv.mu.Unlock()
	if savedCount != 1 {
		t.Errorf("replaySaved = %d, want 1", savedCount)
	}
	if len(customCalls) != 1 || customCalls[0] != "OBSBasic.Screenshot" {
		t.Errorf("unexpected customCalls: %v", customCalls)
	}
	wantMul := 0.845 * 0.845 * 0.845
	if math.Abs(micMul-wantMul) > 0.001 {
		t.Errorf("Mic/Aux mul = %v, want %v (84.5%% cubic)", micMul, wantMul)
	}

	// Unmute Mic/Aux via dial press; visual should show 84.5% and indicator 84.5.
	if err := eng.PressDial(ctx, 0); err != nil {
		t.Fatalf("PressDial error = %v", err)
	}
	dial0 = eng.Snapshot().DialSlots[0].Visual
	if dial0.Subtitle != "84.5%" || dial0.Feedback["value"] != "84.5%" || dial0.Feedback["indicator"] != 84.5 {
		t.Errorf("expected unmuted dial visual 84.5%% with indicator 84.5, got subtitle=%q feedback=%+v", dial0.Subtitle, dial0.Feedback)
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

func TestAudioVolumeCubicFaderHalfPctAndNullDbHandling(t *testing.T) {
	if got := mulToFaderPct(faderPctToMul(0)); got != 0 {
		t.Fatalf("roundtrip 0%% = %v", got)
	}
	if got := mulToFaderPct(faderPctToMul(50)); got != 50 {
		t.Fatalf("roundtrip 50%% = %v", got)
	}
	if got := mulToFaderPct(faderPctToMul(72.5)); got != 72.5 {
		t.Fatalf("roundtrip 72.5%% = %v", got)
	}
	if got := mulToFaderPct(faderPctToMul(100)); got != 100 {
		t.Fatalf("roundtrip 100%% = %v", got)
	}
	if got := formatVolumePct(72.0); got != "72%" {
		t.Fatalf("formatVolumePct(72.0) = %q, want 72%%", got)
	}
	if got := formatVolumePct(72.5); got != "72.5%" {
		t.Fatalf("formatVolumePct(72.5) = %q, want 72.5%%", got)
	}

	// OBS serializes -inf dB as nil inputVolumeDb alongside inputVolumeMul: 0.0.
	if pct, ok := extractInputVolumePct(map[string]any{"inputVolumeDb": nil, "inputVolumeMul": 0.0}); !ok || pct != 0 {
		t.Fatalf("extractInputVolumePct(nil dB, 0 mul) = (%v, %v), want (0, true)", pct, ok)
	}
	if pct, ok := extractInputVolumePct(map[string]any{"inputVolumeDb": nil}); !ok || pct != 0 {
		t.Fatalf("extractInputVolumePct(nil dB only) = (%v, %v), want (0, true)", pct, ok)
	}

	srv := newMockOBSServer("")
	defer srv.Close()

	eng, prov, _ := setupOBSTestEngine(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := prov.SaveConnectionConfig(ConnectionConfig{Host: srv.Host, Port: srv.Port}, "", false); err != nil {
		t.Fatalf("SaveConnectionConfig error = %v", err)
	}
	if err := prov.Connect(ctx); err != nil {
		t.Fatalf("Connect error = %v", err)
	}

	if _, err := eng.AssignDialAction(0, ActionAudioVolume); err != nil {
		t.Fatalf("AssignDialAction error = %v", err)
	}
	if err := eng.UpdateDialSlot(0, "Music", "", map[string]any{
		"inputName":  "Desktop Audio",
		"stepPct":    0.5,
		"pushAction": "reset_100",
	}); err != nil {
		t.Fatalf("UpdateDialSlot error = %v", err)
	}

	// Keypad set exact 12.5% and legacy fallback stepDb/targetDb/reset_0db.
	c00 := deck.Coordinates{Row: 0, Col: 0}
	if _, err := eng.AssignKeypadAction(c00, ActionAudioVolume); err != nil {
		t.Fatalf("AssignKeypadAction error = %v", err)
	}
	if err := eng.UpdateKeypadSlot(c00, "Music Set", "", map[string]any{
		"inputName": "Desktop Audio",
		"mode":      "set",
		"targetPct": 12.5,
	}); err != nil {
		t.Fatalf("UpdateKeypadSlot error = %v", err)
	}
	if err := eng.PressKey(ctx, c00); err != nil {
		t.Fatalf("PressKey(set 12.5%%) error = %v", err)
	}
	dialVis := eng.Snapshot().DialSlots[0].Visual
	if dialVis.Subtitle != "12.5%" || dialVis.Feedback["indicator"] != 12.5 {
		t.Fatalf("expected 12.5%% visual + indicator, got subtitle=%q fb=%+v", dialVis.Subtitle, dialVis.Feedback)
	}

	// Step by +1 tick at 0.5% -> 13%.
	if err := eng.RotateDial(ctx, 0, 1); err != nil {
		t.Fatalf("RotateDial(+1) error = %v", err)
	}
	dialVis = eng.Snapshot().DialSlots[0].Visual
	if dialVis.Subtitle != "13%" || dialVis.Feedback["indicator"] != 13.0 {
		t.Fatalf("expected 13%% visual + indicator, got subtitle=%q fb=%+v", dialVis.Subtitle, dialVis.Feedback)
	}

	// External InputVolumeChanged with nil dB and 0 mul drops dial to 0% cleanly.
	volZeroCh := make(chan string, 4)
	unsub := eng.Subscribe(func(s app.Snapshot) {
		sub := s.DialSlots[0].Visual.Subtitle
		if sub == "0%" {
			select {
			case volZeroCh <- sub:
			default:
			}
		}
	})
	defer unsub()
	srv.BroadcastEvent("InputVolumeChanged", map[string]any{
		"inputName":      "Desktop Audio",
		"inputVolumeDb":  nil,
		"inputVolumeMul": 0.0,
	})
	select {
	case <-volZeroCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for 0% update on nil dB InputVolumeChanged")
	}

	// Dial press with reset_100 restores 100%.
	if err := eng.PressDial(ctx, 0); err != nil {
		t.Fatalf("PressDial(reset_100) error = %v", err)
	}
	dialVis = eng.Snapshot().DialSlots[0].Visual
	if dialVis.Subtitle != "100%" || dialVis.Feedback["indicator"] != 100.0 {
		t.Fatalf("expected 100%% visual after reset_100, got subtitle=%q fb=%+v", dialVis.Subtitle, dialVis.Feedback)
	}

	// Legacy unmigrated settings dispatched directly still work via fallback.
	if err := prov.Dispatch(ctx, plugin.ActionEvent{
		Type:       plugin.EventKeyDown,
		Context:    "legacy-ctx",
		ActionUUID: ActionAudioVolume,
		Settings: map[string]any{
			"inputName": "Desktop Audio",
			"mode":      "step",
			"stepDb":    -2.0,
		},
	}); err != nil {
		t.Fatalf("Dispatch(legacy stepDb) error = %v", err)
	}
	if got := eng.Snapshot().DialSlots[0].Visual.Subtitle; got != "95%" {
		t.Fatalf("expected 95%% after legacy stepDb=-2 (-5%%), got %q", got)
	}
}

func TestWarmSlotStateDoesNotOverwriteNewerVolumeMutation(t *testing.T) {
	srv := newMockOBSServer("")
	defer srv.Close()

	eng, prov, _ := setupOBSTestEngine(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := prov.SaveConnectionConfig(ConnectionConfig{Host: srv.Host, Port: srv.Port}, "", false); err != nil {
		t.Fatalf("SaveConnectionConfig error = %v", err)
	}
	if err := prov.Connect(ctx); err != nil {
		t.Fatalf("Connect error = %v", err)
	}

	if _, err := eng.AssignDialAction(0, ActionAudioVolume); err != nil {
		t.Fatalf("AssignDialAction error = %v", err)
	}
	if err := eng.UpdateDialSlot(0, "Music", "", map[string]any{
		"inputName": "Desktop Audio",
		"stepPct":   5.0,
	}); err != nil {
		t.Fatalf("UpdateDialSlot error = %v", err)
	}

	// Set dial input and keypad button to 100%.
	c00 := deck.Coordinates{Row: 0, Col: 0}
	if _, err := eng.AssignKeypadAction(c00, ActionAudioVolume); err != nil {
		t.Fatalf("AssignKeypadAction error = %v", err)
	}
	if err := eng.UpdateKeypadSlot(c00, "Set 100%", "", map[string]any{
		"inputName": "Desktop Audio",
		"mode":      "set",
		"targetPct": 100.0,
	}); err != nil {
		t.Fatalf("UpdateKeypadSlot error = %v", err)
	}
	if err := eng.PressKey(ctx, c00); err != nil {
		t.Fatalf("PressKey(100%%) error = %v", err)
	}

	// Race background warmSlotState reads against rapid 100% <-> 95% mutations.
	for range 10 {
		go prov.warmSlotState(ActionAudioVolume, map[string]any{"inputName": "Desktop Audio"})
		if err := eng.UpdateDialSlot(0, "Music", "", map[string]any{
			"inputName": "Desktop Audio",
			"stepPct":   -5.0,
		}); err != nil {
			t.Fatalf("UpdateDialSlot error = %v", err)
		}
		if err := prov.Dispatch(ctx, plugin.ActionEvent{
			Type:       plugin.EventKeyDown,
			Context:    "set-100-ctx",
			ActionUUID: ActionAudioVolume,
			Settings: map[string]any{
				"inputName": "Desktop Audio",
				"mode":      "set",
				"targetPct": 100.0,
			},
		}); err != nil {
			t.Fatalf("Dispatch(100%%) error = %v", err)
		}
		if err := prov.Dispatch(ctx, plugin.ActionEvent{
			Type:       plugin.EventKeyDown,
			Context:    "step-ctx",
			ActionUUID: ActionAudioVolume,
			Settings: map[string]any{
				"inputName": "Desktop Audio",
				"mode":      "step",
				"stepPct":   -5.0,
			},
		}); err != nil {
			t.Fatalf("Dispatch(-5%%) error = %v", err)
		}
	}

	time.Sleep(80 * time.Millisecond)
	if got := eng.Snapshot().DialSlots[0].Visual.Subtitle; got != "95%" {
		t.Fatalf("expected 95%% to remain intact after background warmSlotState, got %q", got)
	}
}
