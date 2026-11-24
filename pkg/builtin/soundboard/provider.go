package soundboard

import (
	"context"
	"fmt"
	"maps"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mrpoundsign/PoundControl/pkg/deck"
	"github.com/mrpoundsign/PoundControl/pkg/plugin"
)

const (
	// PluginUUID identifies the built-in PoundControl Soundboard plugin.
	PluginUUID = "com.github.mrpoundsign.poundcontrol.soundboard"

	ActionPlaySound    = PluginUUID + ".play"
	ActionStopAll      = PluginUUID + ".stopAll"
	ActionMasterVolume = PluginUUID + ".volume"

	// OptionsKeyOutputDevices supplies enumerated audio output devices to Settings -> Soundboard.
	OptionsKeyOutputDevices = "soundboard.outputDevices"

	// MissingBgColor overrides a Soundboard Play tile background when its audio file cannot be found.
	MissingBgColor = "#5C1922"
	// MissingAccentColor highlights missing-file buttons in alert red.
	MissingAccentColor = "#EF4444"
	// MissingSubtitle labels missing-file Soundboard buttons.
	MissingSubtitle = "MISSING"

	defaultFileCheckInterval    = 2 * time.Second
	defaultPlaybackTickInterval = 250 * time.Millisecond
	volumeSaveDebounce          = 300 * time.Millisecond
)

// Options configures a Soundboard plugin Provider (intended primarily for deterministic tests).
type Options struct {
	Engine               AudioEngine
	StatFunc             func(string) (os.FileInfo, error)
	DecodeFunc           func(string) (*Clip, error)
	FileCheckInterval    time.Duration
	PlaybackTickInterval time.Duration
	SyncDecode           bool
}

type slotErrorState struct {
	msg     string
	modUnix int64
	size    int64
}

type trackedSlot struct {
	context    string
	actionUUID string
	controller deck.ControllerType
	settings   map[string]any
	playGen    uint64
	cancelGen  uint64
	errState   *slotErrorState
}

type visualStateKey struct {
	subtitle string
	color    string
	bgColor  string
	state    int
	fbValue  string
	fbIndSet bool
	fbInd    int
}

// Provider implements the built-in PoundControl Soundboard plugin.
type Provider struct {
	mu           sync.RWMutex
	host         plugin.HostAPI
	engine       AudioEngine
	ownsEngine   bool
	statFn       func(string) (os.FileInfo, error)
	cache        *clipCache
	manifest     plugin.Manifest
	slots        map[string]*trackedSlot
	lastVisuals  map[string]visualStateKey
	syncDecode   bool
	fileInterval time.Duration
	playInterval time.Duration

	stopAllGen  atomic.Uint64
	saveMu      sync.Mutex
	saveTimerMu sync.Mutex
	saveTimer   *time.Timer
	savePending bool

	bgMu      sync.Mutex
	closed    bool
	disabled  atomic.Bool
	stopCh    chan struct{}
	closeOnce sync.Once
	wg        sync.WaitGroup
}

// New constructs a built-in Soundboard provider using engine (or a new MalgoEngine if nil).
func New(engine AudioEngine) *Provider {
	return NewWithOptions(Options{Engine: engine})
}

// NewWithOptions constructs a Soundboard provider with injectable audio, stat, and timing hooks.
func NewWithOptions(opts Options) *Provider {
	eng := opts.Engine
	owns := false
	if eng == nil {
		eng = NewMalgoEngine()
		owns = true
	}
	statFn := opts.StatFunc
	if statFn == nil {
		statFn = os.Stat
	}
	fileInt := opts.FileCheckInterval
	if fileInt == 0 {
		fileInt = defaultFileCheckInterval
	}
	playInt := opts.PlaybackTickInterval
	if playInt == 0 {
		playInt = defaultPlaybackTickInterval
	}

	p := &Provider{
		engine:       eng,
		ownsEngine:   owns,
		statFn:       statFn,
		cache:        newClipCache(defaultCacheMaxBytes, opts.DecodeFunc),
		manifest:     buildManifest(),
		slots:        make(map[string]*trackedSlot),
		lastVisuals:  make(map[string]visualStateKey),
		syncDecode:   opts.SyncDecode,
		fileInterval: fileInt,
		playInterval: playInt,
		stopCh:       make(chan struct{}),
	}

	eng.SetOnChange(func() {
		p.RefreshVisiblePlaybackVisuals()
	})

	if fileInt > 0 || playInt > 0 {
		p.startBackgroundLoops(fileInt, playInt)
	}
	return p
}

func buildManifest() plugin.Manifest {
	actions := []plugin.ActionDescriptor{
		{
			UUID:         ActionPlaySound,
			PluginUUID:   PluginUUID,
			Name:         "Play Sound",
			Category:     "Soundboard",
			Tooltip:      "Play an audio file (WAV, MP3, OGG, FLAC) through the Soundboard output device",
			DefaultTitle: "Sound",
			DefaultColor: "#A855F7",
			Controllers:  []deck.ControllerType{deck.ControllerKeypad},
			Provider:     plugin.ProviderBuiltin,
			SettingsSchema: []plugin.SettingField{
				{
					Key:            "filePath",
					Label:          "Audio File",
					Type:           plugin.FieldFile,
					FileExtensions: append([]string(nil), SupportedExtensions...),
					Placeholder:    `C:\Sounds\airhorn.mp3`,
					Required:       true,
					Help:           "Supports WAV, MP3, OGG Vorbis, and FLAC. Files stay where they are on disk.",
				},
				{
					Key:         "volume",
					Label:       "Clip Volume (%)",
					Type:        plugin.FieldNumber,
					Default:     100,
					Placeholder: "100",
					Help:        "Per-button loudness from 0 to 150%.",
				},
				{
					Key:     "mode",
					Label:   "Playback Mode",
					Type:    plugin.FieldSelect,
					Default: "restart",
					Options: []plugin.SelectOption{
						{Label: "Restart (stop & replay)", Value: "restart"},
						{Label: "Overlap (layer multiple)", Value: "overlap"},
						{Label: "Toggle (play / stop)", Value: "toggle"},
						{Label: "Play Once (ignore while playing)", Value: "once"},
					},
				},
				{
					Key:     "stopOthers",
					Label:   "Stop Other Sounds on Play",
					Type:    plugin.FieldBool,
					Default: false,
					Help:    "Stop all other playing soundboard buttons when this clip starts. Leave unchecked to play in parallel.",
				},
				{
					Key:         "fadeOutMs",
					Label:       "Stop Fade Out (ms)",
					Type:        plugin.FieldNumber,
					Section:     plugin.SectionAdvanced,
					Default:     0,
					Placeholder: "0",
					Help:        "Smooth fade-out when stopped early via Toggle, Restart, or Stop All.",
				},
			},
		},
		{
			UUID:         ActionStopAll,
			PluginUUID:   PluginUUID,
			Name:         "Stop All Sounds",
			Category:     "Soundboard",
			Tooltip:      "Immediately stop or fade out all currently playing Soundboard clips",
			DefaultTitle: "Stop All",
			DefaultColor: "#EF4444",
			Controllers:  []deck.ControllerType{deck.ControllerKeypad},
			Provider:     plugin.ProviderBuiltin,
			SettingsSchema: []plugin.SettingField{
				{
					Key:         "fadeOutMs",
					Label:       "Fade Out (ms)",
					Type:        plugin.FieldNumber,
					Default:     0,
					Placeholder: "0",
					Help:        "0 stops immediately; > 0 smoothly fades out all playing clips.",
				},
			},
		},
		{
			UUID:         ActionMasterVolume,
			PluginUUID:   PluginUUID,
			Name:         "Soundboard Volume",
			Category:     "Soundboard",
			Tooltip:      "Adjust Soundboard master volume or mute via keypad button or rotary dial",
			DefaultTitle: "SB Volume",
			DefaultColor: "#38BDF8",
			Controllers:  []deck.ControllerType{deck.ControllerKeypad, deck.ControllerEncoder},
			Provider:     plugin.ProviderBuiltin,
			SettingsSchema: []plugin.SettingField{
				{
					Key:     "mode",
					Label:   "Keypad Mode",
					Type:    plugin.FieldSelect,
					Default: "step",
					Options: []plugin.SelectOption{
						{Label: "Step Delta (%)", Value: "step"},
						{Label: "Set Exact (%)", Value: "set"},
						{Label: "Toggle Mute", Value: "mute"},
					},
				},
				{
					Key:         "stepPct",
					Label:       "Step Delta (%)",
					Type:        plugin.FieldNumber,
					Default:     5,
					Placeholder: "5",
					Help:        "Volume step per keypad press (can be negative) or per dial tick.",
				},
				{
					Key:         "targetPct",
					Label:       "Target Volume (%)",
					Type:        plugin.FieldNumber,
					Default:     100,
					Placeholder: "100",
					Help:        "Exact volume applied when Keypad Mode is Set Exact (%).",
				},
				{
					Key:     "pushAction",
					Label:   "Dial Push Action",
					Type:    plugin.FieldSelect,
					Default: "toggle_mute",
					Options: []plugin.SelectOption{
						{Label: "Toggle Mute", Value: "toggle_mute"},
						{Label: "Reset to 100%", Value: "reset_100"},
						{Label: "Stop All Sounds", Value: "stop_all"},
					},
				},
			},
		},
	}

	return plugin.Manifest{
		UUID:        PluginUUID,
		Name:        "Soundboard",
		Version:     "1.0.0",
		Author:      "PoundControl",
		Description: "Play WAV, MP3, OGG, and FLAC soundboard clips with polyphonic mixing and output device routing.",
		Provider:    plugin.ProviderBuiltin,
		GlobalSettingsSchema: []plugin.SettingField{
			{
				Key:         "outputDevice",
				Label:       "Output Audio Device",
				Type:        plugin.FieldSelect,
				OptionsKey:  OptionsKeyOutputDevices,
				Default:     SystemDefaultDevice,
				Placeholder: SystemDefaultDevice,
				Help:        "Single audio playback device used for all Soundboard buttons.",
			},
			{
				Key:         "masterVolume",
				Label:       "Master Volume (%)",
				Type:        plugin.FieldNumber,
				Default:     100,
				Placeholder: "100",
				Help:        "Global loudness scaling across all Soundboard buttons (0 to 100%).",
			},
			{
				Key:         "maxVoices",
				Label:       "Max Simultaneous Sounds",
				Type:        plugin.FieldNumber,
				Section:     plugin.SectionAdvanced,
				Default:     DefaultMaxVoices,
				Placeholder: strconv.Itoa(DefaultMaxVoices),
				Help:        "Maximum overlapping clips mixed simultaneously (1 to 64).",
			},
		},
		Actions: actions,
	}
}

func (p *Provider) startBackgroundLoops(fileInt, playInt time.Duration) {
	if fileInt > 0 {
		p.wg.Go(func() {
			ticker := time.NewTicker(fileInt)
			defer ticker.Stop()
			for {
				select {
				case <-p.stopCh:
					return
				case <-ticker.C:
					if !p.disabled.Load() {
						p.CheckVisibleFiles()
					}
				}
			}
		})
	}
	if playInt > 0 {
		p.wg.Go(func() {
			ticker := time.NewTicker(playInt)
			defer ticker.Stop()
			for {
				select {
				case <-p.stopCh:
					return
				case <-ticker.C:
					if !p.disabled.Load() && p.engine.TotalActive() > 0 {
						p.RefreshVisiblePlaybackVisuals()
					}
				}
			}
		})
	}
}

func (p *Provider) Kind() plugin.ProviderKind    { return plugin.ProviderBuiltin }
func (p *Provider) Name() string                 { return "Soundboard (Built-in)" }
func (p *Provider) Manifests() []plugin.Manifest { return []plugin.Manifest{p.manifest} }
func (p *Provider) Actions() []plugin.ActionDescriptor {
	out := make([]plugin.ActionDescriptor, len(p.manifest.Actions))
	copy(out, p.manifest.Actions)
	return out
}

// BindHost attaches the PoundControl HostAPI to receive logs and visual updates.
func (p *Provider) BindHost(host plugin.HostAPI) {
	p.mu.Lock()
	p.host = host
	p.mu.Unlock()
}

// StartFromGlobalSettings loads persisted Soundboard settings from HostAPI into the audio engine.
func (p *Provider) StartFromGlobalSettings() {
	if p.disabled.Load() {
		return
	}
	p.mu.RLock()
	host := p.host
	p.mu.RUnlock()
	if host == nil {
		return
	}
	cfg := host.GetGlobalSettings(PluginUUID)
	if len(cfg) == 0 {
		return
	}
	_ = p.OnGlobalSettingsChanged(context.Background(), PluginUUID, cfg)
}

// SetPluginEnabled pauses playback and releases the audio device when disabled, and restores settings when enabled.
func (p *Provider) SetPluginEnabled(_ context.Context, pluginUUID string, enabled bool) error {
	if pluginUUID != PluginUUID {
		return nil
	}
	if !enabled {
		p.disabled.Store(true)
		p.stopAllGen.Add(1)
		p.cancelAllSlotAttempts()
		p.engine.StopAll(0)
		p.flushDebouncedMasterVolume()
		if releaser, ok := p.engine.(interface{ ReleaseDevice() }); ok {
			releaser.ReleaseDevice()
		}
		return nil
	}
	p.disabled.Store(false)
	p.StartFromGlobalSettings()
	return nil
}

// OnGlobalSettingsChanged applies updated output device, master volume, and voice cap settings live.
func (p *Provider) OnGlobalSettingsChanged(_ context.Context, pluginUUID string, settings map[string]any) error {
	if pluginUUID != PluginUUID {
		return nil
	}

	deviceName := stringSetting(settings, "outputDevice", SystemDefaultDevice)
	if err := p.engine.SetOutputDevice(deviceName); err != nil {
		p.logf("Set output device %q failed: %v", deviceName, err)
	}

	volPct := clampInt(intSetting(settings, "masterVolume", 100), 0, 100)
	p.engine.SetMasterVolume(float32(volPct) / 100.0)

	maxVoices := clampInt(intSetting(settings, "maxVoices", DefaultMaxVoices), 1, MaxAllowedVoices)
	p.engine.SetMaxVoices(maxVoices)

	p.refreshVolumeSlots()
	return nil
}

// DynamicOptions supplies live playback devices for Settings -> Soundboard.
func (p *Provider) DynamicOptions(_ context.Context, optionsKey string, _ map[string]any) []plugin.SelectOption {
	if optionsKey != OptionsKeyOutputDevices {
		return nil
	}
	out := []plugin.SelectOption{
		{Label: SystemDefaultDevice, Value: SystemDefaultDevice},
	}
	devices, err := p.engine.ListOutputDevices()
	if err != nil {
		p.logf("Enumerate playback devices failed: %v", err)
		return out
	}
	for _, dev := range sortDeviceNames(devices) {
		name := strings.TrimSpace(dev.Name)
		if name == "" || strings.EqualFold(name, SystemDefaultDevice) {
			continue
		}
		out = append(out, plugin.SelectOption{Label: name, Value: name})
	}
	return out
}

// Dispatch processes Soundboard lifecycle events and user interactions.
func (p *Provider) Dispatch(_ context.Context, ev plugin.ActionEvent) error {
	switch ev.Type {
	case plugin.EventWillAppear:
		p.trackSlot(ev, true)
		p.refreshSlotVisual(ev.Context, false, "")
		return nil

	case plugin.EventSettingsChanged:
		p.trackSlot(ev, true)
		p.refreshSlotVisual(ev.Context, true, "")
		return nil

	case plugin.EventWillDisappear:
		p.untrackSlot(ev.Context)
		return nil

	case plugin.EventKeyDown:
		return p.handleKeyDown(ev)

	case plugin.EventDialRotate:
		if ev.ActionUUID == ActionMasterVolume {
			return p.handleDialRotate(ev)
		}
		return nil

	case plugin.EventDialPress:
		if ev.ActionUUID == ActionMasterVolume {
			return p.handleDialPress(ev)
		}
		return nil

	default:
		return nil
	}
}

func (p *Provider) trackSlot(ev plugin.ActionEvent, clearVisualCache bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	settings := cloneSettingsMap(ev.Settings)
	if existing, ok := p.slots[ev.Context]; ok {
		oldFile := stringSetting(existing.settings, "filePath", "")
		newFile := stringSetting(settings, "filePath", "")
		existing.actionUUID = ev.ActionUUID
		existing.controller = ev.Controller
		existing.settings = settings
		if oldFile != newFile || clearVisualCache {
			existing.errState = nil
		}
	} else {
		p.slots[ev.Context] = &trackedSlot{
			context:    ev.Context,
			actionUUID: ev.ActionUUID,
			controller: ev.Controller,
			settings:   settings,
		}
	}
	if clearVisualCache {
		delete(p.lastVisuals, ev.Context)
	}
}

func (p *Provider) untrackSlot(ctxID string) {
	p.mu.Lock()
	delete(p.slots, ctxID)
	delete(p.lastVisuals, ctxID)
	p.mu.Unlock()
}

func (p *Provider) handleKeyDown(ev plugin.ActionEvent) error {
	p.trackSlot(ev, false)

	switch ev.ActionUUID {
	case ActionPlaySound:
		return p.handlePlaySoundKey(ev)
	case ActionStopAll:
		return p.handleStopAllKey(ev)
	case ActionMasterVolume:
		return p.handleVolumeKey(ev)
	default:
		return fmt.Errorf("unknown soundboard action %q", ev.ActionUUID)
	}
}

func (p *Provider) handlePlaySoundKey(ev plugin.ActionEvent) error {
	filePath := stringSetting(ev.Settings, "filePath", "")
	if filePath == "" {
		p.clearSlotError(ev.Context)
		p.refreshSlotVisual(ev.Context, true, "alert")
		return nil
	}

	info, statErr := p.statFn(filePath)
	if statErr != nil || info.IsDir() {
		p.cache.invalidatePath(filePath)
		p.clearSlotError(ev.Context)
		p.refreshSlotVisual(ev.Context, true, "alert")
		if statErr == nil {
			statErr = fmt.Errorf("%q is a directory, expected audio file", filePath)
		}
		p.logf("Missing audio file %q: %v", filePath, statErr)
		return fmt.Errorf("audio file not found: %s", filePath)
	}

	mode := strings.ToLower(stringSetting(ev.Settings, "mode", "restart"))
	stopOthers := boolSetting(ev.Settings, "stopOthers", false)
	volPct := clampInt(intSetting(ev.Settings, "volume", 100), 0, 150)
	gain := float32(volPct) / 100.0
	fadeOut := time.Duration(clampInt(intSetting(ev.Settings, "fadeOutMs", 0), 0, 30000)) * time.Millisecond

	activeCount, _ := p.engine.ActiveForSlot(ev.Context)
	switch mode {
	case "toggle":
		if activeCount > 0 {
			p.cancelSlotAttempt(ev.Context)
			p.engine.StopSlot(ev.Context, fadeOut)
			p.clearSlotError(ev.Context)
			p.RefreshVisiblePlaybackVisuals()
			return nil
		}
	case "once":
		if activeCount > 0 {
			return nil
		}
	case "overlap":
		// Layer new clip on top of active voices for this slot.
	default: // "restart"
		if !stopOthers {
			p.engine.StopSlot(ev.Context, fadeOut)
		}
	}

	if stopOthers {
		p.cancelOtherSlotAttempts(ev.Context)
		if mode == "overlap" {
			p.engine.StopOthers(ev.Context, fadeOut)
		} else {
			p.engine.StopAll(fadeOut)
		}
		p.RefreshVisiblePlaybackVisuals()
	}

	myGen, myCancelGen := p.beginPlayAttempt(ev.Context)
	myStopGen := p.stopAllGen.Load()

	if clip, ok := p.cache.lookup(filePath, info); ok {
		return p.startClipForSlot(ev.Context, filePath, info, clip, gain, mode, myGen, myCancelGen, myStopGen)
	}

	if p.syncDecode {
		clip, decErr := p.decodeAndCache(filePath, info)
		if decErr != nil {
			p.recordSlotError(ev.Context, info, decErr.Error())
			p.refreshSlotVisual(ev.Context, true, "alert")
			p.logf("Decode failed for %q: %v", filePath, decErr)
			return decErr
		}
		return p.startClipForSlot(ev.Context, filePath, info, clip, gain, mode, myGen, myCancelGen, myStopGen)
	}

	p.launchAsyncPlay(ev.Context, filePath, info, gain, mode, myGen, myCancelGen, myStopGen)
	return nil
}

func (p *Provider) decodeAndCache(filePath string, info os.FileInfo) (*Clip, error) {
	clip, err := p.cache.decode(filePath)
	if err != nil {
		return nil, err
	}
	p.cache.store(filePath, info, clip)
	return clip, nil
}

func (p *Provider) launchAsyncPlay(
	ctxID, filePath string,
	info os.FileInfo,
	gain float32,
	mode string,
	myGen, myCancelGen, myStopGen uint64,
) {
	p.bgMu.Lock()
	if p.closed {
		p.bgMu.Unlock()
		return
	}
	p.wg.Go(func() {
		clip, decErr := p.decodeAndCache(filePath, info)
		if p.isCancelled() || p.stopAllGen.Load() != myStopGen {
			return
		}
		if decErr != nil {
			if p.matchesPlayAttempt(ctxID, filePath, myGen, myCancelGen, mode) {
				p.recordSlotError(ctxID, info, decErr.Error())
				p.refreshSlotVisual(ctxID, true, "alert")
				p.logf("Decode failed for %q: %v", filePath, decErr)
			}
			return
		}
		if err := p.startClipForSlot(ctxID, filePath, info, clip, gain, mode, myGen, myCancelGen, myStopGen); err != nil {
			p.logf("Play failed for %q: %v", filePath, err)
		}
	})
	p.bgMu.Unlock()
}

func (p *Provider) startClipForSlot(
	ctxID, filePath string,
	_ os.FileInfo,
	clip *Clip,
	gain float32,
	mode string,
	myGen, myCancelGen, myStopGen uint64,
) error {
	if p.isCancelled() || p.stopAllGen.Load() != myStopGen {
		return nil
	}
	if !p.matchesPlayAttempt(ctxID, filePath, myGen, myCancelGen, mode) {
		return nil
	}
	if _, err := p.engine.Play(ctxID, clip, gain); err != nil {
		p.refreshSlotVisual(ctxID, true, "alert")
		p.logf("Playback failed for %q: %v", filePath, err)
		return err
	}
	p.clearSlotError(ctxID)
	p.RefreshVisiblePlaybackVisuals()
	return nil
}

func (p *Provider) cancelSlotAttempt(ctxID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if slot, ok := p.slots[ctxID]; ok {
		slot.playGen++
		slot.cancelGen++
	}
}

func (p *Provider) cancelOtherSlotAttempts(exceptCtxID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, slot := range p.slots {
		if id != exceptCtxID {
			slot.cancelGen++
		}
	}
}

func (p *Provider) cancelAllSlotAttempts() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, slot := range p.slots {
		slot.playGen++
		slot.cancelGen++
	}
}

func (p *Provider) beginPlayAttempt(ctxID string) (playGen, cancelGen uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if slot, ok := p.slots[ctxID]; ok {
		slot.playGen++
		return slot.playGen, slot.cancelGen
	}
	return 0, 0
}

func (p *Provider) matchesPlayAttempt(ctxID, filePath string, expectedGen, expectedCancelGen uint64, mode string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	slot, ok := p.slots[ctxID]
	if !ok {
		return false
	}
	if slot.cancelGen != expectedCancelGen {
		return false
	}
	if mode != "overlap" && slot.playGen != expectedGen {
		return false
	}
	return stringSetting(slot.settings, "filePath", "") == filePath
}

func (p *Provider) recordSlotError(ctxID string, info os.FileInfo, msg string) {
	p.mu.Lock()
	if slot, ok := p.slots[ctxID]; ok && info != nil {
		slot.errState = &slotErrorState{
			msg:     msg,
			modUnix: info.ModTime().UnixNano(),
			size:    info.Size(),
		}
	}
	p.mu.Unlock()
}

func (p *Provider) clearSlotError(ctxID string) {
	p.mu.Lock()
	if slot, ok := p.slots[ctxID]; ok {
		slot.errState = nil
	}
	p.mu.Unlock()
}

func (p *Provider) handleStopAllKey(ev plugin.ActionEvent) error {
	p.stopAllGen.Add(1)
	fadeOut := time.Duration(clampInt(intSetting(ev.Settings, "fadeOutMs", 0), 0, 30000)) * time.Millisecond
	p.engine.StopAll(fadeOut)
	p.RefreshVisiblePlaybackVisuals()
	p.flashSlot(ev.Context, "ok")
	return nil
}

func (p *Provider) handleVolumeKey(ev plugin.ActionEvent) error {
	mode := strings.ToLower(stringSetting(ev.Settings, "mode", "step"))
	switch mode {
	case "mute":
		p.engine.SetMuted(!p.engine.IsMuted())
	case "set":
		target := clampInt(intSetting(ev.Settings, "targetPct", 100), 0, 100)
		p.engine.SetMasterVolume(float32(target) / 100.0)
		if target > 0 {
			p.engine.SetMuted(false)
		}
		p.persistMasterVolumeNow(target)
	default: // "step"
		step := intSetting(ev.Settings, "stepPct", 5)
		if step == 0 {
			step = 5
		}
		cur := currentVolumePercent(p.engine.MasterVolume())
		next := clampInt(cur+step, 0, 100)
		p.engine.SetMasterVolume(float32(next) / 100.0)
		if step > 0 && next > 0 {
			p.engine.SetMuted(false)
		}
		p.persistMasterVolumeNow(next)
	}
	p.refreshVolumeSlots()
	p.flashSlot(ev.Context, "ok")
	return nil
}

func (p *Provider) handleDialRotate(ev plugin.ActionEvent) error {
	ticks := ev.Ticks
	if ticks == 0 {
		return nil
	}
	step := absInt(intSetting(ev.Settings, "stepPct", 5))
	if step == 0 {
		step = 5
	}
	cur := currentVolumePercent(p.engine.MasterVolume())
	next := clampInt(cur+ticks*step, 0, 100)
	p.engine.SetMasterVolume(float32(next) / 100.0)
	if ticks > 0 && next > 0 {
		p.engine.SetMuted(false)
	}
	p.schedulePersistMasterVolume()
	p.refreshVolumeSlots()
	return nil
}

func (p *Provider) handleDialPress(ev plugin.ActionEvent) error {
	action := strings.ToLower(stringSetting(ev.Settings, "pushAction", "toggle_mute"))
	switch action {
	case "reset_100":
		p.engine.SetMasterVolume(1.0)
		p.engine.SetMuted(false)
		p.persistMasterVolumeNow(100)
		p.refreshVolumeSlots()
	case "stop_all":
		p.stopAllGen.Add(1)
		p.engine.StopAll(0)
		p.RefreshVisiblePlaybackVisuals()
	default: // "toggle_mute"
		p.engine.SetMuted(!p.engine.IsMuted())
		p.refreshVolumeSlots()
	}
	p.flashSlot(ev.Context, "ok")
	return nil
}

func (p *Provider) persistMasterVolumeNow(pct int) {
	p.saveMu.Lock()
	defer p.saveMu.Unlock()

	p.saveTimerMu.Lock()
	if p.saveTimer != nil {
		p.saveTimer.Stop()
		p.saveTimer = nil
	}
	p.savePending = false
	p.saveTimerMu.Unlock()

	p.writeMasterVolumeGlobalSetting(pct)
}

func (p *Provider) schedulePersistMasterVolume() {
	p.saveTimerMu.Lock()
	defer p.saveTimerMu.Unlock()
	if p.isCancelled() {
		return
	}
	p.savePending = true
	if p.saveTimer == nil {
		p.saveTimer = time.AfterFunc(volumeSaveDebounce, p.flushDebouncedMasterVolume)
		return
	}
	p.saveTimer.Reset(volumeSaveDebounce)
}

func (p *Provider) flushDebouncedMasterVolume() {
	p.saveMu.Lock()
	defer p.saveMu.Unlock()

	p.saveTimerMu.Lock()
	shouldSave := p.savePending
	p.savePending = false
	p.saveTimer = nil
	p.saveTimerMu.Unlock()

	if !shouldSave {
		return
	}
	p.writeMasterVolumeGlobalSetting(currentVolumePercent(p.engine.MasterVolume()))
}

func (p *Provider) writeMasterVolumeGlobalSetting(pct int) {
	p.mu.RLock()
	host := p.host
	p.mu.RUnlock()
	if host == nil {
		return
	}
	merged := cloneSettingsMap(host.GetGlobalSettings(PluginUUID))
	if _, exists := merged["outputDevice"]; !exists {
		merged["outputDevice"] = SystemDefaultDevice
	}
	if _, exists := merged["maxVoices"]; !exists {
		merged["maxVoices"] = DefaultMaxVoices
	}
	merged["masterVolume"] = clampInt(pct, 0, 100)
	if err := host.SaveGlobalSettings(PluginUUID, merged); err != nil {
		p.logf("Save master volume setting failed: %v", err)
	}
}

// CheckVisibleFiles re-checks every visible Play Sound button's file path on disk and updates tile visuals.
func (p *Provider) CheckVisibleFiles() {
	if p.disabled.Load() {
		return
	}
	p.refreshSlotsMatching(false, "", func(s *trackedSlot) bool {
		return s.actionUUID == ActionPlaySound
	})
}

// RefreshVisiblePlaybackVisuals updates all visible Play Sound and Stop All slots whose visual state changed.
func (p *Provider) RefreshVisiblePlaybackVisuals() {
	if p.disabled.Load() {
		return
	}
	p.refreshSlotsMatching(false, "", func(s *trackedSlot) bool {
		return s.actionUUID == ActionPlaySound || s.actionUUID == ActionStopAll
	})
}

func (p *Provider) refreshVolumeSlots() {
	p.refreshSlotsMatching(false, "", func(s *trackedSlot) bool {
		return s.actionUUID == ActionMasterVolume
	})
}

func (p *Provider) refreshSlotVisual(ctxID string, force bool, flash string) {
	p.refreshSlotsMatching(force, flash, func(s *trackedSlot) bool {
		return s.context == ctxID
	})
}

type slotVisualPush struct {
	ctxID string
	patch plugin.VisualPatch
}

func (p *Provider) refreshSlotsMatching(force bool, flash string, match func(*trackedSlot) bool) {
	p.mu.RLock()
	host := p.host
	if host == nil {
		p.mu.RUnlock()
		return
	}
	snapshots := make([]*trackedSlot, 0, len(p.slots))
	for _, s := range p.slots {
		if match(s) {
			snapshots = append(snapshots, &trackedSlot{
				context:    s.context,
				actionUUID: s.actionUUID,
				controller: s.controller,
				settings:   cloneSettingsMap(s.settings),
				playGen:    s.playGen,
				errState:   cloneSlotError(s.errState),
			})
		}
	}
	p.mu.RUnlock()

	if len(snapshots) == 0 {
		return
	}

	var pushes []slotVisualPush
	for _, slot := range snapshots {
		patch, key := p.computeSlotPatch(slot, flash)
		p.mu.Lock()
		cur := p.slots[slot.context]
		stillCurrent := cur != nil && cur.playGen == slot.playGen &&
			stringSetting(cur.settings, "filePath", "") == stringSetting(slot.settings, "filePath", "")
		prevKey, hadPrev := p.lastVisuals[slot.context]
		changed := force || !hadPrev || key != prevKey || flash != ""
		if changed && stillCurrent {
			p.lastVisuals[slot.context] = key
			if slot.errState == nil && cur.errState != nil {
				cur.errState = nil
			}
		}
		p.mu.Unlock()

		if changed && stillCurrent {
			pushes = append(pushes, slotVisualPush{ctxID: slot.context, patch: patch})
		}
	}

	for _, push := range pushes {
		host.UpdateVisual(push.ctxID, push.patch)
	}
}

func (p *Provider) computeSlotPatch(slot *trackedSlot, flash string) (plugin.VisualPatch, visualStateKey) {
	var (
		sub   string
		col   string
		bgCol string
		state int
		fb    map[string]any
		key   visualStateKey
	)

	switch slot.actionUUID {
	case ActionPlaySound:
		filePath := stringSetting(slot.settings, "filePath", "")
		activeCount, maxRem := p.engine.ActiveForSlot(slot.context)

		switch {
		case activeCount > 0:
			sub = formatPlaybackRemaining(activeCount, maxRem)
			state = 1
			slot.errState = nil
		case filePath == "":
			sub = "NO FILE"
			slot.errState = nil
		default:
			info, err := p.statFn(filePath)
			switch {
			case err != nil || info.IsDir():
				p.cache.invalidatePath(filePath)
				sub = MissingSubtitle
				col = MissingAccentColor
				bgCol = MissingBgColor
				slot.errState = nil
			case slot.errState != nil &&
				slot.errState.modUnix == info.ModTime().UnixNano() &&
				slot.errState.size == info.Size():
				sub = "BAD FILE"
				col = MissingAccentColor
			default:
				slot.errState = nil
			}
		}

	case ActionStopAll:
		total := p.engine.TotalActive()
		if total > 0 {
			sub = strconv.Itoa(total) + " PLAYING"
			state = 1
		}

	case ActionMasterVolume:
		if p.engine.IsMuted() {
			sub = "MUTED"
			col = MissingAccentColor
			state = 1
			fb = map[string]any{
				"value":     "MUTED",
				"indicator": 0,
			}
			key.fbValue = "MUTED"
			key.fbIndSet = true
			key.fbInd = 0
		} else {
			pct := currentVolumePercent(p.engine.MasterVolume())
			pctText := strconv.Itoa(pct) + "%"
			sub = pctText
			fb = map[string]any{
				"value":     pctText,
				"indicator": pct,
			}
			key.fbValue = pctText
			key.fbIndSet = true
			key.fbInd = pct
		}
	}

	key.subtitle = sub
	key.color = col
	key.bgColor = bgCol
	key.state = state

	patch := plugin.VisualPatch{
		Subtitle:        new(sub),
		Color:           new(col),
		BackgroundColor: new(bgCol),
		State:           new(state),
		Feedback:        fb,
		Flash:           flash,
	}
	return patch, key
}

func (p *Provider) flashSlot(ctxID, flash string) {
	p.refreshSlotVisual(ctxID, true, flash)
}

func (p *Provider) isCancelled() bool {
	p.bgMu.Lock()
	defer p.bgMu.Unlock()
	return p.closed
}

func (p *Provider) logf(format string, args ...any) {
	p.mu.RLock()
	host := p.host
	p.mu.RUnlock()
	if host != nil {
		host.Log(PluginUUID, fmt.Sprintf(format, args...))
	}
}

// Close stops background watchers, flushes pending global setting writes, and shuts down the audio engine.
func (p *Provider) Close() error {
	p.closeOnce.Do(func() {
		p.bgMu.Lock()
		p.closed = true
		close(p.stopCh)
		p.bgMu.Unlock()

		p.flushDebouncedMasterVolume()
		p.wg.Wait()

		p.engine.SetOnChange(nil)
		if p.ownsEngine {
			_ = p.engine.Close()
		}
	})
	return nil
}

func formatPlaybackRemaining(activeCount int, rem time.Duration) string {
	var base string
	if rem <= 0 {
		base = "PLAYING"
	} else {
		totalSec := max(int(math.Ceil(rem.Seconds())), 1)
		mins := totalSec / 60
		secs := totalSec % 60
		base = fmt.Sprintf("%d:%02d", mins, secs)
	}
	if activeCount > 1 {
		return fmt.Sprintf("%s (%dx)", base, activeCount)
	}
	return base
}

func currentVolumePercent(vol float32) int {
	return clampInt(int(math.Round(float64(vol)*100.0)), 0, 100)
}

func cloneSlotError(err *slotErrorState) *slotErrorState {
	if err == nil {
		return nil
	}
	cp := *err
	return &cp
}

func cloneSettingsMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return make(map[string]any)
	}
	return maps.Clone(in)
}

func stringSetting(settings map[string]any, key, def string) string {
	if settings == nil {
		return def
	}
	v, ok := settings[key]
	if !ok || v == nil {
		return def
	}
	s := strings.TrimSpace(fmt.Sprint(v))
	if s == "" {
		return def
	}
	return s
}

func boolSetting(settings map[string]any, key string, def bool) bool {
	if settings == nil {
		return def
	}
	v, ok := settings[key]
	if !ok || v == nil {
		return def
	}
	switch b := v.(type) {
	case bool:
		return b
	case string:
		if parsed, err := strconv.ParseBool(strings.TrimSpace(b)); err == nil {
			return parsed
		}
		return def
	default:
		return def
	}
}

func intSetting(settings map[string]any, key string, def int) int {
	if settings == nil {
		return def
	}
	v, ok := settings[key]
	if !ok || v == nil {
		return def
	}
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	case string:
		if parsed, err := strconv.Atoi(strings.TrimSpace(n)); err == nil {
			return parsed
		}
		return def
	default:
		return def
	}
}

func clampInt(v, lo, hi int) int {
	return min(max(v, lo), hi)
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
