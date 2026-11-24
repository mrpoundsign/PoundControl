package soundboard

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mrpoundsign/PoundControl/pkg/deck"
	"github.com/mrpoundsign/PoundControl/pkg/plugin"
)

type fakeVoice struct {
	id        uint64
	slotCtx   string
	gain      float32
	remaining time.Duration
}

type fakeAudioEngine struct {
	mu           sync.Mutex
	devices      []OutputDevice
	outputDevice string
	masterVol    float32
	muted        bool
	maxVoices    int
	nextID       uint64
	voices       []fakeVoice
	playCalls    int
	stopAllCalls int
	onChange     func()
}

func newFakeAudioEngine(devices ...OutputDevice) *fakeAudioEngine {
	return &fakeAudioEngine{
		devices:      devices,
		outputDevice: SystemDefaultDevice,
		masterVol:    1.0,
		maxVoices:    DefaultMaxVoices,
	}
}

func (f *fakeAudioEngine) ListOutputDevices() ([]OutputDevice, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.devices), nil
}

func (f *fakeAudioEngine) SetOutputDevice(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if name == "" || name == SystemDefaultDevice {
		f.outputDevice = SystemDefaultDevice
	} else {
		f.outputDevice = name
	}
	return nil
}

func (f *fakeAudioEngine) OutputDevice() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.outputDevice
}

func (f *fakeAudioEngine) Play(slotCtx string, clip *Clip, gain float32) (uint64, error) {
	f.mu.Lock()
	f.nextID++
	id := f.nextID
	f.playCalls++
	rem := clip.Duration
	if rem <= 0 {
		rem = time.Second
	}
	f.voices = append(f.voices, fakeVoice{
		id:        id,
		slotCtx:   slotCtx,
		gain:      gain,
		remaining: rem,
	})
	cb := f.onChange
	f.mu.Unlock()
	if cb != nil {
		cb()
	}
	return id, nil
}

func (f *fakeAudioEngine) StopSlot(slotCtx string, _ time.Duration) {
	f.mu.Lock()
	kept := f.voices[:0]
	changed := false
	for _, v := range f.voices {
		if v.slotCtx == slotCtx {
			changed = true
			continue
		}
		kept = append(kept, v)
	}
	f.voices = kept
	cb := f.onChange
	f.mu.Unlock()
	if changed && cb != nil {
		cb()
	}
}

func (f *fakeAudioEngine) StopOthers(exceptSlotCtx string, _ time.Duration) {
	f.mu.Lock()
	kept := f.voices[:0]
	changed := false
	for _, v := range f.voices {
		if v.slotCtx != exceptSlotCtx {
			changed = true
			continue
		}
		kept = append(kept, v)
	}
	f.voices = kept
	cb := f.onChange
	f.mu.Unlock()
	if changed && cb != nil {
		cb()
	}
}

func (f *fakeAudioEngine) StopAll(_ time.Duration) {
	f.mu.Lock()
	changed := len(f.voices) > 0
	f.voices = nil
	f.stopAllCalls++
	cb := f.onChange
	f.mu.Unlock()
	if changed && cb != nil {
		cb()
	}
}

func (f *fakeAudioEngine) finishAll() {
	f.mu.Lock()
	f.voices = nil
	cb := f.onChange
	f.mu.Unlock()
	if cb != nil {
		cb()
	}
}

func (f *fakeAudioEngine) ActiveForSlot(slotCtx string) (int, time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	count := 0
	var maxRem time.Duration
	for _, v := range f.voices {
		if v.slotCtx == slotCtx {
			count++
			if v.remaining > maxRem {
				maxRem = v.remaining
			}
		}
	}
	return count, maxRem
}

func (f *fakeAudioEngine) TotalActive() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.voices)
}

func (f *fakeAudioEngine) SetMasterVolume(vol float32) {
	f.mu.Lock()
	f.masterVol = clampFloat32(vol, 0.0, 1.0)
	f.mu.Unlock()
}

func (f *fakeAudioEngine) MasterVolume() float32 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.masterVol
}

func (f *fakeAudioEngine) SetMuted(muted bool) {
	f.mu.Lock()
	f.muted = muted
	f.mu.Unlock()
}

func (f *fakeAudioEngine) IsMuted() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.muted
}

func (f *fakeAudioEngine) SetMaxVoices(n int) {
	f.mu.Lock()
	f.maxVoices = n
	f.mu.Unlock()
}

func (f *fakeAudioEngine) SetOnChange(fn func()) {
	f.mu.Lock()
	f.onChange = fn
	f.mu.Unlock()
}

func (f *fakeAudioEngine) Close() error { return nil }

type fakeHostAPI struct {
	mu             sync.Mutex
	visuals        map[string][]plugin.VisualPatch
	globalSettings map[string]map[string]any
	logs           []string
}

func newFakeHostAPI() *fakeHostAPI {
	return &fakeHostAPI{
		visuals:        make(map[string][]plugin.VisualPatch),
		globalSettings: make(map[string]map[string]any),
	}
}

func (h *fakeHostAPI) UpdateVisual(ctxID string, patch plugin.VisualPatch) {
	h.mu.Lock()
	h.visuals[ctxID] = append(h.visuals[ctxID], patch)
	h.mu.Unlock()
}

func (h *fakeHostAPI) latestVisual(ctxID string) (plugin.VisualPatch, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	list := h.visuals[ctxID]
	if len(list) == 0 {
		return plugin.VisualPatch{}, false
	}
	return list[len(list)-1], true
}

func (h *fakeHostAPI) SaveSettings(string, map[string]any) error { return nil }

func (h *fakeHostAPI) GetGlobalSettings(pluginUUID string) map[string]any {
	h.mu.Lock()
	defer h.mu.Unlock()
	return cloneSettingsMap(h.globalSettings[pluginUUID])
}

func (h *fakeHostAPI) SaveGlobalSettings(pluginUUID string, settings map[string]any) error {
	h.mu.Lock()
	h.globalSettings[pluginUUID] = cloneSettingsMap(settings)
	h.mu.Unlock()
	return nil
}

func (h *fakeHostAPI) Log(_ string, msg string) {
	h.mu.Lock()
	h.logs = append(h.logs, msg)
	h.mu.Unlock()
}

func (h *fakeHostAPI) Deck() plugin.DeckControl { return nil }

func writeTestWAV(t *testing.T, path string, frames int) {
	t.Helper()
	var pcm = make([]byte, frames*4)
	for i := range frames {
		binary.LittleEndian.PutUint16(pcm[i*4:i*4+2], 4000)
		binary.LittleEndian.PutUint16(pcm[i*4+2:i*4+4], 4000)
	}
	if err := os.WriteFile(path, buildWAVBytes(1, 2, MixerSampleRate, 16, pcm, nil), 0o644); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", path, err)
	}
}

func TestSoundboardManifestDynamicDevicesAndGlobalSettings(t *testing.T) {
	eng := newFakeAudioEngine(
		OutputDevice{ID: "dev1", Name: "Speakers (Realtek)", IsDefault: true},
		OutputDevice{ID: "dev2", Name: "CABLE Input (VB-Audio)"},
	)
	host := newFakeHostAPI()
	host.globalSettings[PluginUUID] = map[string]any{
		"outputDevice": "CABLE Input (VB-Audio)",
		"masterVolume": 70,
		"maxVoices":    8,
	}

	prov := NewWithOptions(Options{
		Engine:               eng,
		FileCheckInterval:    -1,
		PlaybackTickInterval: -1,
		SyncDecode:           true,
	})
	t.Cleanup(func() { _ = prov.Close() })
	prov.BindHost(host)

	for _, m := range prov.Manifests() {
		if err := m.Validate(); err != nil {
			t.Fatalf("manifest.Validate() error = %v", err)
		}
	}

	opts := prov.DynamicOptions(context.Background(), OptionsKeyOutputDevices, nil)
	if len(opts) != 3 || opts[0].Value != SystemDefaultDevice || opts[2].Value != "CABLE Input (VB-Audio)" {
		t.Fatalf("unexpected DynamicOptions: %+v", opts)
	}

	prov.StartFromGlobalSettings()
	if eng.OutputDevice() != "CABLE Input (VB-Audio)" || currentVolumePercent(eng.MasterVolume()) != 70 {
		t.Fatalf("expected global settings applied, device=%q vol=%v", eng.OutputDevice(), eng.MasterVolume())
	}

	if err := prov.OnGlobalSettingsChanged(context.Background(), PluginUUID, map[string]any{
		"outputDevice": SystemDefaultDevice,
		"masterVolume": 45,
		"maxVoices":    12,
	}); err != nil {
		t.Fatalf("OnGlobalSettingsChanged() error = %v", err)
	}
	if eng.OutputDevice() != SystemDefaultDevice || currentVolumePercent(eng.MasterVolume()) != 45 {
		t.Fatalf("expected updated global settings, device=%q vol=%v", eng.OutputDevice(), eng.MasterVolume())
	}
}

func TestSoundboardPlayAndMissingFileBackgroundWatcher(t *testing.T) {
	dir := t.TempDir()
	wavPath := filepath.Join(dir, "airhorn.wav")

	eng := newFakeAudioEngine()
	host := newFakeHostAPI()
	prov := NewWithOptions(Options{
		Engine:               eng,
		FileCheckInterval:    -1,
		PlaybackTickInterval: -1,
		SyncDecode:           true,
	})
	t.Cleanup(func() { _ = prov.Close() })
	prov.BindHost(host)

	playCtx := "slot-play"
	stopCtx := "slot-stop"
	settings := map[string]any{"filePath": wavPath, "volume": 100, "mode": "restart"}

	// 1. Appearing with a missing file sets crimson alert background and MISSING subtitle.
	_ = prov.Dispatch(context.Background(), plugin.ActionEvent{
		Type:       plugin.EventWillAppear,
		Context:    playCtx,
		ActionUUID: ActionPlaySound,
		Settings:   settings,
	})
	_ = prov.Dispatch(context.Background(), plugin.ActionEvent{
		Type:       plugin.EventWillAppear,
		Context:    stopCtx,
		ActionUUID: ActionStopAll,
	})

	vis, ok := host.latestVisual(playCtx)
	if !ok || vis.BackgroundColor == nil || *vis.BackgroundColor != MissingBgColor ||
		vis.Color == nil || *vis.Color != MissingAccentColor ||
		vis.Subtitle == nil || *vis.Subtitle != MissingSubtitle {
		t.Fatalf("expected missing-file visual on appear, got %+v", vis)
	}

	// 2. Pressing missing sound returns error and flashes alert while preserving crimson background.
	if err := prov.Dispatch(context.Background(), plugin.ActionEvent{
		Type:       plugin.EventKeyDown,
		Context:    playCtx,
		ActionUUID: ActionPlaySound,
		Settings:   settings,
	}); err == nil {
		t.Fatal("expected error when pressing missing sound button")
	}
	vis, _ = host.latestVisual(playCtx)
	if vis.Flash != "alert" || vis.BackgroundColor == nil || *vis.BackgroundColor != MissingBgColor {
		t.Fatalf("expected alert flash and crimson background on missing keyDown, got %+v", vis)
	}

	// 3. Restoring the file on disk and running CheckVisibleFiles restores normal background.
	writeTestWAV(t, wavPath, MixerSampleRate*2) // 2 seconds
	prov.CheckVisibleFiles()
	vis, _ = host.latestVisual(playCtx)
	if vis.BackgroundColor == nil || *vis.BackgroundColor != "" ||
		vis.Color == nil || *vis.Color != "" ||
		vis.Subtitle == nil || *vis.Subtitle != "" {
		t.Fatalf("expected restored normal background & empty subtitle, got %+v", vis)
	}

	// 4. Deleting the file again turns background crimson automatically on CheckVisibleFiles.
	if err := os.Remove(wavPath); err != nil {
		t.Fatalf("Remove(wav) error = %v", err)
	}
	prov.CheckVisibleFiles()
	vis, _ = host.latestVisual(playCtx)
	if vis.BackgroundColor == nil || *vis.BackgroundColor != MissingBgColor || *vis.Subtitle != MissingSubtitle {
		t.Fatalf("expected missing-file background after delete, got %+v", vis)
	}

	// 5. Recreate file and press Play -> active glow (State: 1) + countdown + Stop All active.
	writeTestWAV(t, wavPath, MixerSampleRate*2)
	prov.CheckVisibleFiles()
	if err := prov.Dispatch(context.Background(), plugin.ActionEvent{
		Type:       plugin.EventKeyDown,
		Context:    playCtx,
		ActionUUID: ActionPlaySound,
		Settings:   settings,
	}); err != nil {
		t.Fatalf("KeyDown(play) error = %v", err)
	}
	vis, _ = host.latestVisual(playCtx)
	if vis.State == nil || *vis.State != 1 || vis.Subtitle == nil || *vis.Subtitle != "0:02" {
		t.Fatalf("expected playing state=1 subtitle=0:02, got state=%v sub=%v", vis.State, vis.Subtitle)
	}
	stopVis, _ := host.latestVisual(stopCtx)
	if stopVis.State == nil || *stopVis.State != 1 || stopVis.Subtitle == nil || *stopVis.Subtitle != "1 PLAYING" {
		t.Fatalf("expected Stop All state=1 subtitle=1 PLAYING, got %+v", stopVis)
	}

	// 6. Natural completion resets both tiles to idle.
	eng.finishAll()
	vis, _ = host.latestVisual(playCtx)
	if vis.State == nil || *vis.State != 0 || vis.Subtitle == nil || *vis.Subtitle != "" {
		t.Fatalf("expected play tile reset to idle after completion, got %+v", vis)
	}
	stopVis, _ = host.latestVisual(stopCtx)
	if stopVis.State == nil || *stopVis.State != 0 {
		t.Fatalf("expected Stop All tile reset to idle, got %+v", stopVis)
	}
}

func TestSoundboardEmptyAndCorruptAudioFiles(t *testing.T) {
	dir := t.TempDir()
	badPath := filepath.Join(dir, "broken.mp3")
	if err := os.WriteFile(badPath, []byte("corrupt bytes"), 0o644); err != nil {
		t.Fatalf("WriteFile(bad) error = %v", err)
	}

	eng := newFakeAudioEngine()
	host := newFakeHostAPI()
	prov := NewWithOptions(Options{
		Engine:               eng,
		FileCheckInterval:    -1,
		PlaybackTickInterval: -1,
		SyncDecode:           true,
	})
	t.Cleanup(func() { _ = prov.Close() })
	prov.BindHost(host)

	_ = prov.Dispatch(context.Background(), plugin.ActionEvent{
		Type:       plugin.EventKeyDown,
		Context:    "empty-slot",
		ActionUUID: ActionPlaySound,
		Settings:   map[string]any{"filePath": ""},
	})
	visEmpty, _ := host.latestVisual("empty-slot")
	if *visEmpty.Subtitle != "NO FILE" || *visEmpty.BackgroundColor != "" || visEmpty.Flash != "alert" {
		t.Fatalf("unexpected empty file visual: %+v", visEmpty)
	}

	if err := prov.Dispatch(context.Background(), plugin.ActionEvent{
		Type:       plugin.EventKeyDown,
		Context:    "bad-slot",
		ActionUUID: ActionPlaySound,
		Settings:   map[string]any{"filePath": badPath},
	}); err == nil {
		t.Fatal("expected corrupt audio decode error")
	}
	visBad, _ := host.latestVisual("bad-slot")
	if *visBad.Subtitle != "BAD FILE" || *visBad.BackgroundColor != "" || visBad.Flash != "alert" {
		t.Fatalf("unexpected corrupt file visual: %+v", visBad)
	}
}

func TestSoundboardPlaybackModesAndPageSwitch(t *testing.T) {
	dir := t.TempDir()
	wavPath := filepath.Join(dir, "fx.wav")
	writeTestWAV(t, wavPath, MixerSampleRate)

	eng := newFakeAudioEngine()
	host := newFakeHostAPI()
	prov := NewWithOptions(Options{
		Engine:               eng,
		FileCheckInterval:    -1,
		PlaybackTickInterval: -1,
		SyncDecode:           true,
	})
	t.Cleanup(func() { _ = prov.Close() })
	prov.BindHost(host)

	press := func(ctxID, mode string) {
		if err := prov.Dispatch(context.Background(), plugin.ActionEvent{
			Type:       plugin.EventKeyDown,
			Context:    ctxID,
			ActionUUID: ActionPlaySound,
			Settings:   map[string]any{"filePath": wavPath, "mode": mode},
		}); err != nil {
			t.Fatalf("KeyDown(%s) error = %v", mode, err)
		}
	}

	// Overlap layers multiple voices and shows (2x).
	press("s1", "overlap")
	press("s1", "overlap")
	if c, _ := eng.ActiveForSlot("s1"); c != 2 {
		t.Fatalf("expected 2 overlapping voices, got %d", c)
	}
	vis, _ := host.latestVisual("s1")
	if *vis.Subtitle != "0:01 (2x)" {
		t.Fatalf("expected overlap subtitle '0:01 (2x)', got %q", *vis.Subtitle)
	}

	// Restart replaces slot voices with 1 new voice.
	press("s1", "restart")
	if c, _ := eng.ActiveForSlot("s1"); c != 1 {
		t.Fatalf("expected restart to replace voices leaving 1, got %d", c)
	}

	// Once ignores second press while active.
	beforeCalls := eng.playCalls
	press("s1", "once")
	if eng.playCalls != beforeCalls {
		t.Fatalf("expected 'once' mode to ignore press while playing")
	}

	// Toggle stops active voice on press.
	press("s1", "toggle")
	if c, _ := eng.ActiveForSlot("s1"); c != 0 {
		t.Fatalf("expected 'toggle' mode to stop voice, got %d", c)
	}

	// Switching pages (EventWillDisappear) keeps sound playing and restores State: 1 on WillAppear.
	press("s2", "restart")
	_ = prov.Dispatch(context.Background(), plugin.ActionEvent{
		Type:       plugin.EventWillDisappear,
		Context:    "s2",
		ActionUUID: ActionPlaySound,
	})
	if c, _ := eng.ActiveForSlot("s2"); c != 1 {
		t.Fatalf("expected voice to keep playing across WillDisappear, got %d", c)
	}
	_ = prov.Dispatch(context.Background(), plugin.ActionEvent{
		Type:       plugin.EventWillAppear,
		Context:    "s2",
		ActionUUID: ActionPlaySound,
		Settings:   map[string]any{"filePath": wavPath},
	})
	vis2, _ := host.latestVisual("s2")
	if *vis2.State != 1 {
		t.Fatalf("expected restored State=1 on WillAppear, got %+v", vis2)
	}

	// Stop All silences everything.
	_ = prov.Dispatch(context.Background(), plugin.ActionEvent{
		Type:       plugin.EventKeyDown,
		Context:    "stop",
		ActionUUID: ActionStopAll,
	})
	if eng.TotalActive() != 0 {
		t.Fatalf("expected 0 active voices after Stop All, got %d", eng.TotalActive())
	}
}

func TestSoundboardMasterVolumeKeypadDialAndFlush(t *testing.T) {
	eng := newFakeAudioEngine()
	host := newFakeHostAPI()
	host.globalSettings[PluginUUID] = map[string]any{
		"outputDevice": "Speakers (Realtek)",
		"masterVolume": 100,
		"maxVoices":    10,
	}

	prov := NewWithOptions(Options{
		Engine:               eng,
		FileCheckInterval:    -1,
		PlaybackTickInterval: -1,
		SyncDecode:           true,
	})
	prov.BindHost(host)
	prov.StartFromGlobalSettings()

	dialCtx := "dial-vol"
	_ = prov.Dispatch(context.Background(), plugin.ActionEvent{
		Type:       plugin.EventWillAppear,
		Context:    dialCtx,
		ActionUUID: ActionMasterVolume,
		Controller: deck.ControllerEncoder,
		Settings:   map[string]any{"stepPct": 5, "pushAction": "toggle_mute"},
	})

	// Dial rotate down 3 ticks -> 85%.
	if err := prov.Dispatch(context.Background(), plugin.ActionEvent{
		Type:       plugin.EventDialRotate,
		Context:    dialCtx,
		ActionUUID: ActionMasterVolume,
		Ticks:      -3,
		Settings:   map[string]any{"stepPct": 5},
	}); err != nil {
		t.Fatalf("DialRotate error = %v", err)
	}
	if got := currentVolumePercent(eng.MasterVolume()); got != 85 {
		t.Fatalf("expected 85%% volume, got %d%%", got)
	}
	vis, _ := host.latestVisual(dialCtx)
	if vis.Feedback["value"] != "85%" || vis.Feedback["indicator"] != 85 {
		t.Fatalf("unexpected dial feedback: %+v", vis.Feedback)
	}

	// Keypad set exact volume immediately persists while preserving outputDevice & maxVoices.
	if err := prov.Dispatch(context.Background(), plugin.ActionEvent{
		Type:       plugin.EventKeyDown,
		Context:    "key-set",
		ActionUUID: ActionMasterVolume,
		Settings:   map[string]any{"mode": "set", "targetPct": 40},
	}); err != nil {
		t.Fatalf("KeyDown(set) error = %v", err)
	}
	gs := host.GetGlobalSettings(PluginUUID)
	if gs["masterVolume"] != 40 || gs["outputDevice"] != "Speakers (Realtek)" || gs["maxVoices"] != 10 {
		t.Fatalf("expected merged global settings preserved, got %+v", gs)
	}

	// Dial press toggle_mute mutes output and reflects MUTED feedback.
	if err := prov.Dispatch(context.Background(), plugin.ActionEvent{
		Type:       plugin.EventDialPress,
		Context:    dialCtx,
		ActionUUID: ActionMasterVolume,
		Settings:   map[string]any{"pushAction": "toggle_mute"},
	}); err != nil {
		t.Fatalf("DialPress error = %v", err)
	}
	visMuted, _ := host.latestVisual(dialCtx)
	if !eng.IsMuted() || visMuted.Feedback["value"] != "MUTED" || visMuted.Feedback["indicator"] != 0 {
		t.Fatalf("expected muted feedback, muted=%v fb=%+v", eng.IsMuted(), visMuted.Feedback)
	}

	// Rotate dial up -> unmutes & sets 50%, then Close() flushes debounced persistence cleanly.
	_ = prov.Dispatch(context.Background(), plugin.ActionEvent{
		Type:       plugin.EventDialRotate,
		Context:    dialCtx,
		ActionUUID: ActionMasterVolume,
		Ticks:      2,
		Settings:   map[string]any{"stepPct": 5},
	})
	if err := prov.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if gsFinal := host.GetGlobalSettings(PluginUUID); gsFinal["masterVolume"] != 50 {
		t.Fatalf("expected Close() to flush debounced masterVolume=50, got %+v", gsFinal)
	}
}

func TestSoundboardAsyncDecodeCancelledByStopAll(t *testing.T) {
	dir := t.TempDir()
	wavPath := filepath.Join(dir, "slow.wav")
	writeTestWAV(t, wavPath, MixerSampleRate)

	startedCh := make(chan struct{})
	releaseCh := make(chan struct{})
	var decodeCalls atomic.Int32

	eng := newFakeAudioEngine()
	host := newFakeHostAPI()
	prov := NewWithOptions(Options{
		Engine:               eng,
		FileCheckInterval:    -1,
		PlaybackTickInterval: -1,
		SyncDecode:           false,
		DecodeFunc: func(string) (*Clip, error) {
			decodeCalls.Add(1)
			close(startedCh)
			<-releaseCh
			return makeConstClip(100, 0.2, 0.2), nil
		},
	})
	t.Cleanup(func() { _ = prov.Close() })
	prov.BindHost(host)

	if err := prov.Dispatch(context.Background(), plugin.ActionEvent{
		Type:       plugin.EventKeyDown,
		Context:    "async-slot",
		ActionUUID: ActionPlaySound,
		Settings:   map[string]any{"filePath": wavPath},
	}); err != nil {
		t.Fatalf("KeyDown(async) error = %v", err)
	}

	<-startedCh
	// Stop All cancels the in-flight decode before it finishes.
	_ = prov.Dispatch(context.Background(), plugin.ActionEvent{
		Type:       plugin.EventKeyDown,
		Context:    "stop-slot",
		ActionUUID: ActionStopAll,
	})
	close(releaseCh)

	// Shut down cleanly so goroutine finishes, then verify clip was never queued.
	_ = prov.Close()
	if decodeCalls.Load() != 1 {
		t.Fatalf("expected 1 async decode call, got %d", decodeCalls.Load())
	}
	if eng.playCalls != 0 {
		t.Fatalf("expected cancelled async decode never to call Play(), got playCalls=%d", eng.playCalls)
	}
}

func TestSoundboardParallelVsStopOthersOverride(t *testing.T) {
	dir := t.TempDir()
	wavPath := filepath.Join(dir, "clip.wav")
	writeTestWAV(t, wavPath, MixerSampleRate)

	eng := newFakeAudioEngine()
	host := newFakeHostAPI()
	prov := NewWithOptions(Options{
		Engine:               eng,
		FileCheckInterval:    -1,
		PlaybackTickInterval: -1,
		SyncDecode:           true,
	})
	t.Cleanup(func() { _ = prov.Close() })
	prov.BindHost(host)

	appear := func(ctxID string, settings map[string]any) {
		_ = prov.Dispatch(context.Background(), plugin.ActionEvent{
			Type:       plugin.EventWillAppear,
			Context:    ctxID,
			ActionUUID: ActionPlaySound,
			Settings:   settings,
		})
	}
	press := func(ctxID string, settings map[string]any) {
		if err := prov.Dispatch(context.Background(), plugin.ActionEvent{
			Type:       plugin.EventKeyDown,
			Context:    ctxID,
			ActionUUID: ActionPlaySound,
			Settings:   settings,
		}); err != nil {
			t.Fatalf("KeyDown(%s) error = %v", ctxID, err)
		}
	}

	sParallelA := map[string]any{"filePath": wavPath, "mode": "restart", "stopOthers": false}
	sParallelB := map[string]any{"filePath": wavPath, "mode": "restart", "stopOthers": false}
	sOverrideC := map[string]any{"filePath": wavPath, "mode": "restart", "stopOthers": true}
	sOverrideOverlapD := map[string]any{"filePath": wavPath, "mode": "overlap", "stopOthers": true}
	sOverrideToggleE := map[string]any{"filePath": wavPath, "mode": "toggle", "stopOthers": true}

	appear("btnA", sParallelA)
	appear("btnB", sParallelB)
	appear("btnC", sOverrideC)
	appear("btnD", sOverrideOverlapD)
	appear("btnE", sOverrideToggleE)

	// Parallel (stopOthers=false): A and B play simultaneously.
	press("btnA", sParallelA)
	press("btnB", sParallelB)
	if eng.TotalActive() != 2 {
		t.Fatalf("expected 2 parallel voices, got %d", eng.TotalActive())
	}

	// Override + restart (stopOthers=true): C stops A and B cleanly and plays alone.
	press("btnC", sOverrideC)
	if cA, _ := eng.ActiveForSlot("btnA"); cA != 0 {
		t.Fatalf("expected btnA stopped by override, got %d", cA)
	}
	if cB, _ := eng.ActiveForSlot("btnB"); cB != 0 {
		t.Fatalf("expected btnB stopped by override, got %d", cB)
	}
	if cC, _ := eng.ActiveForSlot("btnC"); cC != 1 || eng.TotalActive() != 1 {
		t.Fatalf("expected only btnC active, got cC=%d total=%d", cC, eng.TotalActive())
	}
	if vA, _ := host.latestVisual("btnA"); vA.State == nil || *vA.State != 0 {
		t.Fatalf("expected btnA visual reset to State=0, got %+v", vA)
	}

	// Override + overlap (stopOthers=true): D stops C, then layers 2 voices on D.
	press("btnD", sOverrideOverlapD)
	press("btnD", sOverrideOverlapD)
	if cC, _ := eng.ActiveForSlot("btnC"); cC != 0 {
		t.Fatalf("expected btnC stopped by btnD override, got %d", cC)
	}
	if cD, _ := eng.ActiveForSlot("btnD"); cD != 2 || eng.TotalActive() != 2 {
		t.Fatalf("expected 2 layered voices on btnD, got cD=%d total=%d", cD, eng.TotalActive())
	}

	// Override + toggle: starting E stops D; starting parallel B, then toggling E off stops ONLY E while keeping B playing.
	press("btnE", sOverrideToggleE)
	if cD, _ := eng.ActiveForSlot("btnD"); cD != 0 {
		t.Fatalf("expected btnD stopped by toggle override btnE, got %d", cD)
	}
	press("btnB", sParallelB)
	if eng.TotalActive() != 2 {
		t.Fatalf("expected btnE + parallel btnB active, got %d", eng.TotalActive())
	}
	press("btnE", sOverrideToggleE) // toggle E off
	if cE, _ := eng.ActiveForSlot("btnE"); cE != 0 {
		t.Fatalf("expected btnE toggled off, got %d", cE)
	}
	if cB, _ := eng.ActiveForSlot("btnB"); cB != 1 {
		t.Fatalf("expected parallel btnB kept playing when toggling btnE off, got %d", cB)
	}
}

func TestSoundboardAsyncOverrideCancelsInFlightOtherSlot(t *testing.T) {
	dir := t.TempDir()
	slowPath := filepath.Join(dir, "slow.wav")
	fastPath := filepath.Join(dir, "override.wav")
	writeTestWAV(t, slowPath, MixerSampleRate)
	writeTestWAV(t, fastPath, MixerSampleRate)

	startedCh := make(chan struct{})
	releaseCh := make(chan struct{})
	playedFast := make(chan struct{}, 1)

	eng := newFakeAudioEngine()
	host := newFakeHostAPI()
	prov := NewWithOptions(Options{
		Engine:               eng,
		FileCheckInterval:    -1,
		PlaybackTickInterval: -1,
		SyncDecode:           false,
		DecodeFunc: func(path string) (*Clip, error) {
			if path == slowPath {
				close(startedCh)
				<-releaseCh
				return makeConstClip(100, 0.2, 0.2), nil
			}
			select {
			case playedFast <- struct{}{}:
			default:
			}
			return makeConstClip(100, 0.3, 0.3), nil
		},
	})
	t.Cleanup(func() { _ = prov.Close() })
	prov.BindHost(host)

	sSlow := map[string]any{"filePath": slowPath, "mode": "overlap", "stopOthers": false}
	sFast := map[string]any{"filePath": fastPath, "mode": "restart", "stopOthers": true}
	_ = prov.Dispatch(context.Background(), plugin.ActionEvent{
		Type:       plugin.EventWillAppear,
		Context:    "slow-btn",
		ActionUUID: ActionPlaySound,
		Settings:   sSlow,
	})
	_ = prov.Dispatch(context.Background(), plugin.ActionEvent{
		Type:       plugin.EventWillAppear,
		Context:    "fast-btn",
		ActionUUID: ActionPlaySound,
		Settings:   sFast,
	})

	if err := prov.Dispatch(context.Background(), plugin.ActionEvent{
		Type:       plugin.EventKeyDown,
		Context:    "slow-btn",
		ActionUUID: ActionPlaySound,
		Settings:   sSlow,
	}); err != nil {
		t.Fatalf("KeyDown(slow) error = %v", err)
	}
	<-startedCh

	// Trigger override button while slow-btn is still decoding.
	if err := prov.Dispatch(context.Background(), plugin.ActionEvent{
		Type:       plugin.EventKeyDown,
		Context:    "fast-btn",
		ActionUUID: ActionPlaySound,
		Settings:   sFast,
	}); err != nil {
		t.Fatalf("KeyDown(fast) error = %v", err)
	}
	<-playedFast
	// Wait briefly until fast-btn finishes async queue before releasing slow-btn.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if c, _ := eng.ActiveForSlot("fast-btn"); c == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if c, _ := eng.ActiveForSlot("fast-btn"); c != 1 {
		t.Fatalf("expected fast-btn active, got %d", c)
	}

	close(releaseCh)
	_ = prov.Close()

	if cSlow, _ := eng.ActiveForSlot("slow-btn"); cSlow != 0 || eng.playCalls != 1 {
		t.Fatalf("expected cancelled slow-btn never played after override, cSlow=%d playCalls=%d", cSlow, eng.playCalls)
	}
}

func TestSoundboardSetPluginEnabledStopsAudioAndResumes(t *testing.T) {
	dir := t.TempDir()
	wavPath := filepath.Join(dir, "clip.wav")
	writeTestWAV(t, wavPath, MixerSampleRate)

	eng := newFakeAudioEngine()
	host := newFakeHostAPI()
	host.globalSettings[PluginUUID] = map[string]any{
		"outputDevice": "Speakers (Realtek)",
		"masterVolume": 60,
		"maxVoices":    6,
	}

	prov := NewWithOptions(Options{
		Engine:               eng,
		FileCheckInterval:    -1,
		PlaybackTickInterval: -1,
		SyncDecode:           true,
	})
	t.Cleanup(func() { _ = prov.Close() })
	prov.BindHost(host)
	prov.StartFromGlobalSettings()

	if err := prov.Dispatch(context.Background(), plugin.ActionEvent{
		Type:       plugin.EventKeyDown,
		Context:    "sb-slot",
		ActionUUID: ActionPlaySound,
		Settings:   map[string]any{"filePath": wavPath},
	}); err != nil {
		t.Fatalf("KeyDown(play) error = %v", err)
	}
	if eng.TotalActive() != 1 {
		t.Fatalf("expected active sound before disable, got %d", eng.TotalActive())
	}

	if err := prov.SetPluginEnabled(context.Background(), PluginUUID, false); err != nil {
		t.Fatalf("SetPluginEnabled(false) error = %v", err)
	}
	if eng.TotalActive() != 0 || !prov.disabled.Load() {
		t.Fatalf("expected audio stopped and disabled=true, active=%d disabled=%v", eng.TotalActive(), prov.disabled.Load())
	}

	// Update saved volume while disabled; re-enabling applies global settings.
	host.globalSettings[PluginUUID]["masterVolume"] = 80
	if err := prov.SetPluginEnabled(context.Background(), PluginUUID, true); err != nil {
		t.Fatalf("SetPluginEnabled(true) error = %v", err)
	}
	if prov.disabled.Load() || currentVolumePercent(eng.MasterVolume()) != 80 {
		t.Fatalf("expected enabled and 80%% volume applied, disabled=%v vol=%v", prov.disabled.Load(), eng.MasterVolume())
	}
}
