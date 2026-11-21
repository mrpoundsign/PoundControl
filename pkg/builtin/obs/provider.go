package obs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mrpoundsign/PoundDeck/pkg/deck"
	"github.com/mrpoundsign/PoundDeck/pkg/plugin"
	"github.com/mrpoundsign/PoundDeck/pkg/secrets"
)

const (
	// PluginUUID identifies the built-in OBS Studio WebSocket v5 plugin.
	PluginUUID = "com.github.mrpoundsign.pounddeck.obs"
	// SecretKeyPassword is the secrets.Store key holding the OBS WebSocket password.
	SecretKeyPassword = "obs.websocket.password"

	DefaultHost = "127.0.0.1"
	DefaultPort = 4455

	ActionSwitchScene   = PluginUUID + ".scene.switch"
	ActionToggleSource  = PluginUUID + ".source.visibility"
	ActionToggleMute    = PluginUUID + ".audio.mute"
	ActionAudioVolume   = PluginUUID + ".audio.volume"
	ActionStream        = PluginUUID + ".stream.toggle"
	ActionRecord        = PluginUUID + ".record.toggle"
	ActionReplayBuffer  = PluginUUID + ".replay.save"
	ActionStudioMode    = PluginUUID + ".studio.transition"
	ActionCustomRequest = PluginUUID + ".request.custom"
)

// ConnectionConfig holds non-secret OBS connection parameters persisted in PluginSettings.
type ConnectionConfig struct {
	Host        string `json:"host"`
	Port        int    `json:"port"`
	AutoConnect bool   `json:"autoConnect"`
}

type sourceKey struct {
	scene  string
	source string
}

type trackedSlot struct {
	context    string
	actionUUID string
	controller deck.ControllerType
	settings   map[string]any
}

type obsState struct {
	programScene    string
	previewScene    string
	studioMode      bool
	streaming       bool
	recording       bool
	recordingPaused bool
	replayActive    bool
	scenes          []string
	inputs          []string
	sceneSources    map[string][]string
	sourceItemID    map[sourceKey]int
	sourceEnabled   map[sourceKey]bool
	inputMuted      map[string]bool
	inputVolumeDb   map[string]float64
}

func newOBSState() obsState {
	return obsState{
		sceneSources:  make(map[string][]string),
		sourceItemID:  make(map[sourceKey]int),
		sourceEnabled: make(map[sourceKey]bool),
		inputMuted:    make(map[string]bool),
		inputVolumeDb: make(map[string]float64),
	}
}

// Provider implements plugin.Provider and plugin.DynamicOptionsProvider for OBS Studio WebSocket v5.
type Provider struct {
	mu          sync.RWMutex
	host        plugin.HostAPI
	secretStore secrets.Store
	client      *Client
	manifest    plugin.Manifest
	slots       map[string]trackedSlot
	state       obsState
	statusSubs  map[uint64]func(ClientStatus)
	statusSeq   uint64
	closed      bool
}

// New constructs a built-in OBS plugin provider backed by secretStore (or an in-memory store if nil).
func New(secretStore secrets.Store) *Provider {
	if secretStore == nil {
		secretStore = secrets.NewMemoryStore()
	}
	p := &Provider{
		secretStore: secretStore,
		manifest:    buildManifest(),
		slots:       make(map[string]trackedSlot),
		state:       newOBSState(),
		statusSubs:  make(map[uint64]func(ClientStatus)),
	}
	p.client = NewClient(p.handleOBSEvent, p.handleClientStatus)
	return p
}

func buildManifest() plugin.Manifest {
	actions := []plugin.ActionDescriptor{
		{
			UUID:         ActionSwitchScene,
			PluginUUID:   PluginUUID,
			Name:         "Switch Scene",
			Category:     "OBS Studio",
			Tooltip:      "Switch the OBS program or preview scene",
			DefaultTitle: "Scene",
			DefaultColor: "#38BDF8",
			Controllers:  []deck.ControllerType{deck.ControllerKeypad},
			Provider:     plugin.ProviderBuiltin,
			SettingsSchema: []plugin.SettingField{
				{
					Key:         "sceneName",
					Label:       "Scene Name",
					Type:        plugin.FieldSelect,
					OptionsKey:  "obs.scenes",
					Placeholder: "Main Scene",
					Required:    true,
				},
				{
					Key:     "target",
					Label:   "Target Output",
					Type:    plugin.FieldSelect,
					Default: "program",
					Options: []plugin.SelectOption{
						{Label: "Program (Live)", Value: "program"},
						{Label: "Preview (Studio Mode)", Value: "preview"},
					},
				},
			},
		},
		{
			UUID:         ActionToggleSource,
			PluginUUID:   PluginUUID,
			Name:         "Source Visibility",
			Category:     "OBS Studio",
			Tooltip:      "Show, hide, or toggle an OBS source inside a scene",
			DefaultTitle: "Source",
			DefaultColor: "#38BDF8",
			Controllers:  []deck.ControllerType{deck.ControllerKeypad},
			Provider:     plugin.ProviderBuiltin,
			SettingsSchema: []plugin.SettingField{
				{
					Key:         "sceneName",
					Label:       "Scene Name",
					Type:        plugin.FieldSelect,
					OptionsKey:  "obs.scenes",
					Placeholder: "Active Scene (or specify)",
				},
				{
					Key:         "sourceName",
					Label:       "Source Name",
					Type:        plugin.FieldSelect,
					OptionsKey:  "obs.sources",
					Placeholder: "Webcam",
					Required:    true,
				},
				{
					Key:     "mode",
					Label:   "Mode",
					Type:    plugin.FieldSelect,
					Default: "toggle",
					Options: []plugin.SelectOption{
						{Label: "Toggle", Value: "toggle"},
						{Label: "Show", Value: "show"},
						{Label: "Hide", Value: "hide"},
					},
				},
			},
		},
		{
			UUID:         ActionToggleMute,
			PluginUUID:   PluginUUID,
			Name:         "Audio Mute",
			Category:     "OBS Studio",
			Tooltip:      "Mute, unmute, or toggle an OBS audio input",
			DefaultTitle: "Mic Mute",
			DefaultColor: "#22C55E",
			Controllers:  []deck.ControllerType{deck.ControllerKeypad},
			Provider:     plugin.ProviderBuiltin,
			SettingsSchema: []plugin.SettingField{
				{
					Key:         "inputName",
					Label:       "Audio Input",
					Type:        plugin.FieldSelect,
					OptionsKey:  "obs.inputs",
					Placeholder: "Mic/Aux",
					Required:    true,
				},
				{
					Key:     "mode",
					Label:   "Mode",
					Type:    plugin.FieldSelect,
					Default: "toggle",
					Options: []plugin.SelectOption{
						{Label: "Toggle", Value: "toggle"},
						{Label: "Mute", Value: "mute"},
						{Label: "Unmute", Value: "unmute"},
					},
				},
			},
		},
		{
			UUID:         ActionAudioVolume,
			PluginUUID:   PluginUUID,
			Name:         "Audio Volume",
			Category:     "OBS Studio",
			Tooltip:      "Adjust OBS audio input gain (dB) via keypad or rotary dial",
			DefaultTitle: "Volume",
			DefaultColor: "#38BDF8",
			Controllers:  []deck.ControllerType{deck.ControllerKeypad, deck.ControllerEncoder},
			Provider:     plugin.ProviderBuiltin,
			SettingsSchema: []plugin.SettingField{
				{
					Key:         "inputName",
					Label:       "Audio Input",
					Type:        plugin.FieldSelect,
					OptionsKey:  "obs.inputs",
					Placeholder: "Desktop Audio",
					Required:    true,
				},
				{
					Key:     "mode",
					Label:   "Keypad Mode",
					Type:    plugin.FieldSelect,
					Default: "step",
					Options: []plugin.SelectOption{
						{Label: "Step Delta (dB)", Value: "step"},
						{Label: "Set Exact (dB)", Value: "set"},
					},
				},
				{
					Key:         "stepDb",
					Label:       "Step Delta (dB)",
					Type:        plugin.FieldNumber,
					Default:     2,
					Placeholder: "2",
				},
				{
					Key:         "targetDb",
					Label:       "Target Volume (dB)",
					Type:        plugin.FieldNumber,
					Default:     0,
					Placeholder: "0",
				},
				{
					Key:     "pushAction",
					Label:   "Dial Push Action",
					Type:    plugin.FieldSelect,
					Default: "toggle_mute",
					Options: []plugin.SelectOption{
						{Label: "Toggle Mute", Value: "toggle_mute"},
						{Label: "Reset to 0 dB", Value: "reset_0db"},
						{Label: "Step Delta (dB)", Value: "step"},
					},
				},
			},
		},
		{
			UUID:         ActionStream,
			PluginUUID:   PluginUUID,
			Name:         "Stream Toggle",
			Category:     "OBS Studio",
			Tooltip:      "Start, stop, or toggle OBS live streaming",
			DefaultTitle: "Stream",
			DefaultColor: "#EF4444",
			Controllers:  []deck.ControllerType{deck.ControllerKeypad},
			Provider:     plugin.ProviderBuiltin,
			SettingsSchema: []plugin.SettingField{
				{
					Key:     "mode",
					Label:   "Action",
					Type:    plugin.FieldSelect,
					Default: "toggle",
					Options: []plugin.SelectOption{
						{Label: "Toggle", Value: "toggle"},
						{Label: "Start Stream", Value: "start"},
						{Label: "Stop Stream", Value: "stop"},
					},
				},
			},
		},
		{
			UUID:         ActionRecord,
			PluginUUID:   PluginUUID,
			Name:         "Record Toggle",
			Category:     "OBS Studio",
			Tooltip:      "Start, stop, toggle, or pause OBS recording",
			DefaultTitle: "Record",
			DefaultColor: "#EF4444",
			Controllers:  []deck.ControllerType{deck.ControllerKeypad},
			Provider:     plugin.ProviderBuiltin,
			SettingsSchema: []plugin.SettingField{
				{
					Key:     "mode",
					Label:   "Action",
					Type:    plugin.FieldSelect,
					Default: "toggle",
					Options: []plugin.SelectOption{
						{Label: "Toggle", Value: "toggle"},
						{Label: "Start Recording", Value: "start"},
						{Label: "Stop Recording", Value: "stop"},
						{Label: "Toggle Pause", Value: "pause_toggle"},
					},
				},
			},
		},
		{
			UUID:         ActionReplayBuffer,
			PluginUUID:   PluginUUID,
			Name:         "Save Replay Buffer",
			Category:     "OBS Studio",
			Tooltip:      "Save, start, stop, or toggle the OBS Replay Buffer",
			DefaultTitle: "Replay",
			DefaultColor: "#22C55E",
			Controllers:  []deck.ControllerType{deck.ControllerKeypad},
			Provider:     plugin.ProviderBuiltin,
			SettingsSchema: []plugin.SettingField{
				{
					Key:     "mode",
					Label:   "Action",
					Type:    plugin.FieldSelect,
					Default: "save",
					Options: []plugin.SelectOption{
						{Label: "Save Replay", Value: "save"},
						{Label: "Toggle Buffer", Value: "toggle"},
						{Label: "Start Buffer", Value: "start"},
						{Label: "Stop Buffer", Value: "stop"},
					},
				},
			},
		},
		{
			UUID:         ActionStudioMode,
			PluginUUID:   PluginUUID,
			Name:         "Studio Mode",
			Category:     "OBS Studio",
			Tooltip:      "Trigger Studio Mode transition or toggle Studio Mode",
			DefaultTitle: "Studio",
			DefaultColor: "#38BDF8",
			Controllers:  []deck.ControllerType{deck.ControllerKeypad},
			Provider:     plugin.ProviderBuiltin,
			SettingsSchema: []plugin.SettingField{
				{
					Key:     "mode",
					Label:   "Action",
					Type:    plugin.FieldSelect,
					Default: "transition",
					Options: []plugin.SelectOption{
						{Label: "Trigger Transition", Value: "transition"},
						{Label: "Toggle Studio Mode", Value: "toggle"},
						{Label: "Enable Studio Mode", Value: "enable"},
						{Label: "Disable Studio Mode", Value: "disable"},
					},
				},
			},
		},
		{
			UUID:         ActionCustomRequest,
			PluginUUID:   PluginUUID,
			Name:         "Custom OBS Request",
			Category:     "OBS Studio",
			Tooltip:      "Invoke any OBS WebSocket v5 requestType with JSON requestData",
			DefaultTitle: "OBS Req",
			DefaultColor: "#A855F7",
			Controllers:  []deck.ControllerType{deck.ControllerKeypad},
			Provider:     plugin.ProviderBuiltin,
			SettingsSchema: []plugin.SettingField{
				{
					Key:         "requestType",
					Label:       "Request Type",
					Type:        plugin.FieldText,
					Placeholder: "TriggerHotkeyByName",
					Required:    true,
				},
				{
					Key:         "requestData",
					Label:       "Request Data (JSON)",
					Type:        plugin.FieldMultiline,
					Default:     "{}",
					Placeholder: `{"hotkeyName":"OBSBasic.Screenshot"}`,
				},
			},
		},
	}

	return plugin.Manifest{
		UUID:        PluginUUID,
		Name:        "OBS Studio (WebSocket v5)",
		Version:     "0.1.0",
		Author:      "PoundDeck",
		Description: "Control OBS Studio scenes, sources, audio, stream/record, and replay buffer via WebSocket v5.",
		Provider:    plugin.ProviderBuiltin,
		Actions:     actions,
	}
}

// Kind returns plugin.ProviderBuiltin.
func (p *Provider) Kind() plugin.ProviderKind {
	return plugin.ProviderBuiltin
}

// Name returns the display name of the OBS provider.
func (p *Provider) Name() string {
	return p.manifest.Name
}

// Manifests returns the OBS plugin manifest.
func (p *Provider) Manifests() []plugin.Manifest {
	return []plugin.Manifest{p.manifest}
}

// Actions returns all OBS action descriptors.
func (p *Provider) Actions() []plugin.ActionDescriptor {
	return slices.Clone(p.manifest.Actions)
}

// BindHost sets the HostAPI used to update tiles and read/save global plugin settings.
func (p *Provider) BindHost(host plugin.HostAPI) {
	p.mu.Lock()
	p.host = host
	p.mu.Unlock()
}

// Status returns the current OBS WebSocket connection status.
func (p *Provider) Status() ClientStatus {
	return p.client.Status()
}

// SubscribeStatus registers fn to be called whenever OBS WebSocket connection status changes.
func (p *Provider) SubscribeStatus(fn func(ClientStatus)) func() {
	if fn == nil {
		return func() {}
	}
	p.mu.Lock()
	p.statusSeq++
	id := p.statusSeq
	p.statusSubs[id] = fn
	p.mu.Unlock()

	return func() {
		p.mu.Lock()
		delete(p.statusSubs, id)
		p.mu.Unlock()
	}
}

// ConnectionConfig returns the saved OBS host, port, and autoConnect preferences from HostAPI global settings.
func (p *Provider) ConnectionConfig() ConnectionConfig {
	cfg := ConnectionConfig{
		Host: DefaultHost,
		Port: DefaultPort,
	}
	p.mu.RLock()
	host := p.host
	p.mu.RUnlock()
	if host == nil {
		return cfg
	}

	gs := host.GetGlobalSettings(PluginUUID)
	if h := stringSetting(gs, "host", ""); h != "" {
		cfg.Host = h
	}
	if port := intSetting(gs, "port", DefaultPort); port > 0 && port <= 65535 {
		cfg.Port = port
	}
	cfg.AutoConnect = boolSetting(gs, "autoConnect", false)
	return cfg
}

// HasPassword reports whether an OBS password is stored in the secret store.
func (p *Provider) HasPassword() bool {
	pw, err := p.secretStore.Get(SecretKeyPassword)
	return err == nil && pw != ""
}

// Password returns the stored OBS WebSocket password, or "" if none is set.
func (p *Provider) Password() string {
	pw, err := p.secretStore.Get(SecretKeyPassword)
	if err != nil {
		return ""
	}
	return pw
}

// SaveConnectionConfig persists non-secret settings into PluginSettings and optionally updates the keyring password.
func (p *Provider) SaveConnectionConfig(cfg ConnectionConfig, password string, updatePassword bool) error {
	hostStr := strings.TrimSpace(cfg.Host)
	if hostStr == "" {
		hostStr = DefaultHost
	}
	port := cfg.Port
	if port <= 0 || port > 65535 {
		port = DefaultPort
	}

	if updatePassword {
		if password == "" {
			if err := p.secretStore.Delete(SecretKeyPassword); err != nil {
				return err
			}
		} else {
			if err := p.secretStore.Set(SecretKeyPassword, password); err != nil {
				return err
			}
		}
	}

	p.mu.RLock()
	host := p.host
	p.mu.RUnlock()
	if host != nil {
		return host.SaveGlobalSettings(PluginUUID, map[string]any{
			"host":        hostStr,
			"port":        port,
			"autoConnect": cfg.AutoConnect,
		})
	}
	return nil
}

// Connect establishes a synchronous OBS WebSocket v5 session and hydrates live OBS state.
func (p *Provider) Connect(ctx context.Context) error {
	cfg := p.ConnectionConfig()
	if err := p.client.Connect(ctx, cfg.Host, cfg.Port, p.Password()); err != nil {
		p.refreshAllSlots()
		return err
	}
	p.hydrateState(ctx)
	return nil
}

// ConnectAndMaintain starts a background supervisor keeping the OBS WebSocket session connected.
func (p *Provider) ConnectAndMaintain() {
	cfg := p.ConnectionConfig()
	p.client.StartAutoReconnect(cfg.Host, cfg.Port, p.Password())
}

// Disconnect terminates the OBS connection and stops background reconnection.
func (p *Provider) Disconnect() {
	p.client.Disconnect()
	p.resetState()
	p.refreshAllSlots()
}

// StartFromGlobalSettings connects automatically if AutoConnect is enabled in saved global settings.
func (p *Provider) StartFromGlobalSettings() {
	if p.ConnectionConfig().AutoConnect {
		p.ConnectAndMaintain()
	}
}

// Close disconnects OBS, clears tracked state, and releases subscribers.
func (p *Provider) Close() error {
	p.mu.Lock()
	p.closed = true
	clear(p.slots)
	clear(p.statusSubs)
	p.host = nil
	p.mu.Unlock()

	p.client.Disconnect()
	return nil
}

// DynamicOptions supplies live OBS scene, audio input, and scene source names for the inspector.
func (p *Provider) DynamicOptions(ctx context.Context, optionsKey string, currentSettings map[string]any) []plugin.SelectOption {
	switch optionsKey {
	case "obs.scenes":
		p.mu.RLock()
		scenes := slices.Clone(p.state.scenes)
		p.mu.RUnlock()
		if len(scenes) == 0 && p.client.IsConnected() {
			p.refreshScenes(ctx)
			p.mu.RLock()
			scenes = slices.Clone(p.state.scenes)
			p.mu.RUnlock()
		}
		return toSelectOptions(scenes)

	case "obs.inputs":
		p.mu.RLock()
		inputs := slices.Clone(p.state.inputs)
		p.mu.RUnlock()
		if len(inputs) == 0 && p.client.IsConnected() {
			p.refreshInputs(ctx)
			p.mu.RLock()
			inputs = slices.Clone(p.state.inputs)
			p.mu.RUnlock()
		}
		return toSelectOptions(inputs)

	case "obs.sources":
		p.mu.RLock()
		scene := stringSetting(currentSettings, "sceneName", p.state.programScene)
		sources := slices.Clone(p.state.sceneSources[scene])
		p.mu.RUnlock()
		if len(sources) == 0 && scene != "" && p.client.IsConnected() {
			p.refreshSceneItems(ctx, scene)
			p.mu.RLock()
			sources = slices.Clone(p.state.sceneSources[scene])
			p.mu.RUnlock()
		}
		return toSelectOptions(sources)

	default:
		return nil
	}
}

// Dispatch handles slot lifecycle events and executes OBS actions on user input.
func (p *Provider) Dispatch(ctx context.Context, ev plugin.ActionEvent) error {
	switch ev.Type {
	case plugin.EventWillDisappear:
		p.mu.Lock()
		delete(p.slots, ev.Context)
		p.mu.Unlock()
		return nil

	case plugin.EventWillAppear, plugin.EventSettingsChanged:
		p.mu.Lock()
		p.slots[ev.Context] = trackedSlot{
			context:    ev.Context,
			actionUUID: ev.ActionUUID,
			controller: ev.Controller,
			settings:   maps.Clone(ev.Settings),
		}
		p.mu.Unlock()
		p.refreshContext(ev.Context, "")
		if p.client.IsConnected() {
			go p.warmSlotState(ev.ActionUUID, maps.Clone(ev.Settings))
		}
		return nil

	case plugin.EventKeyUp:
		return nil
	}

	if !p.client.IsConnected() {
		p.refreshContext(ev.Context, "alert")
		return errors.New("OBS is not connected")
	}

	err := p.executeAction(ctx, ev)
	if err != nil {
		p.refreshContext(ev.Context, "alert")
		return err
	}
	return nil
}

func (p *Provider) executeAction(ctx context.Context, ev plugin.ActionEvent) error {
	switch ev.ActionUUID {
	case ActionSwitchScene:
		return p.execSwitchScene(ctx, ev)
	case ActionToggleSource:
		return p.execToggleSource(ctx, ev)
	case ActionToggleMute:
		return p.execToggleMute(ctx, ev)
	case ActionAudioVolume:
		return p.execAudioVolume(ctx, ev)
	case ActionStream:
		return p.execStream(ctx, ev)
	case ActionRecord:
		return p.execRecord(ctx, ev)
	case ActionReplayBuffer:
		return p.execReplayBuffer(ctx, ev)
	case ActionStudioMode:
		return p.execStudioMode(ctx, ev)
	case ActionCustomRequest:
		return p.execCustomRequest(ctx, ev)
	default:
		return fmt.Errorf("obs provider: unsupported action %q", ev.ActionUUID)
	}
}

func (p *Provider) execSwitchScene(ctx context.Context, ev plugin.ActionEvent) error {
	scene := stringSetting(ev.Settings, "sceneName", "")
	if scene == "" {
		return errors.New("switch scene requires sceneName")
	}
	target := stringSetting(ev.Settings, "target", "program")
	if target == "preview" {
		if _, err := p.client.Call(ctx, "SetCurrentPreviewScene", map[string]any{"sceneName": scene}); err != nil {
			return err
		}
		p.mu.Lock()
		p.state.previewScene = scene
		p.mu.Unlock()
	} else {
		if _, err := p.client.Call(ctx, "SetCurrentProgramScene", map[string]any{"sceneName": scene}); err != nil {
			return err
		}
		p.mu.Lock()
		p.state.programScene = scene
		p.mu.Unlock()
	}
	p.refreshAllSlots()
	return nil
}

func (p *Provider) execToggleSource(ctx context.Context, ev plugin.ActionEvent) error {
	p.mu.RLock()
	fallbackScene := p.state.programScene
	p.mu.RUnlock()

	scene := stringSetting(ev.Settings, "sceneName", fallbackScene)
	source := stringSetting(ev.Settings, "sourceName", "")
	if scene == "" || source == "" {
		return errors.New("source visibility requires sceneName and sourceName")
	}

	itemID, err := p.resolveSceneItemID(ctx, scene, source)
	if err != nil {
		return err
	}

	mode := stringSetting(ev.Settings, "mode", "toggle")
	var desired bool
	switch mode {
	case "show":
		desired = true
	case "hide":
		desired = false
	default:
		resp, getErr := p.client.Call(ctx, "GetSceneItemEnabled", map[string]any{
			"sceneName":   scene,
			"sceneItemId": itemID,
		})
		if getErr != nil {
			return getErr
		}
		desired = !boolMapVal(resp, "sceneItemEnabled", false)
	}

	if _, err := p.client.Call(ctx, "SetSceneItemEnabled", map[string]any{
		"sceneName":        scene,
		"sceneItemId":      itemID,
		"sceneItemEnabled": desired,
	}); err != nil {
		return err
	}

	p.mu.Lock()
	p.state.sourceEnabled[sourceKey{scene: scene, source: source}] = desired
	p.mu.Unlock()
	p.refreshAllSlots()
	return nil
}

func (p *Provider) resolveSceneItemID(ctx context.Context, scene, source string) (int, error) {
	key := sourceKey{scene: scene, source: source}
	p.mu.RLock()
	id, ok := p.state.sourceItemID[key]
	p.mu.RUnlock()
	if ok && id > 0 {
		return id, nil
	}

	resp, err := p.client.Call(ctx, "GetSceneItemId", map[string]any{
		"sceneName":  scene,
		"sourceName": source,
	})
	if err != nil {
		return 0, err
	}
	id = intMapVal(resp, "sceneItemId", 0)
	if id <= 0 {
		return 0, fmt.Errorf("scene item %q not found in scene %q", source, scene)
	}
	p.mu.Lock()
	p.state.sourceItemID[key] = id
	p.mu.Unlock()
	return id, nil
}

func (p *Provider) execToggleMute(ctx context.Context, ev plugin.ActionEvent) error {
	input := stringSetting(ev.Settings, "inputName", "")
	if input == "" {
		return errors.New("audio mute requires inputName")
	}

	mode := stringSetting(ev.Settings, "mode", "toggle")
	var muted bool
	if mode == "mute" || mode == "unmute" {
		muted = mode == "mute"
		if _, err := p.client.Call(ctx, "SetInputMute", map[string]any{
			"inputName":  input,
			"inputMuted": muted,
		}); err != nil {
			return err
		}
	} else {
		resp, err := p.client.Call(ctx, "ToggleInputMute", map[string]any{
			"inputName": input,
		})
		if err != nil {
			return err
		}
		muted = boolMapVal(resp, "inputMuted", false)
	}

	p.mu.Lock()
	p.state.inputMuted[input] = muted
	p.mu.Unlock()
	p.refreshAllSlots()
	return nil
}

func (p *Provider) execAudioVolume(ctx context.Context, ev plugin.ActionEvent) error {
	input := stringSetting(ev.Settings, "inputName", "")
	if input == "" {
		return errors.New("audio volume requires inputName")
	}

	stepDb := floatSetting(ev.Settings, "stepDb", 2.0)
	targetDb := floatSetting(ev.Settings, "targetDb", 0.0)
	mode := stringSetting(ev.Settings, "mode", "step")
	pushAction := stringSetting(ev.Settings, "pushAction", "toggle_mute")

	if ev.Type == plugin.EventDialPress && pushAction == "toggle_mute" {
		return p.execToggleMute(ctx, plugin.ActionEvent{
			Context:    ev.Context,
			ActionUUID: ActionToggleMute,
			Settings:   map[string]any{"inputName": input, "mode": "toggle"},
		})
	}

	var newDb float64
	switch {
	case ev.Type == plugin.EventDialRotate:
		curDb, err := p.ensureInputVolumeDb(ctx, input)
		if err != nil {
			return err
		}
		newDb = clampDb(curDb + float64(ev.Ticks)*stepDb)
	case ev.Type == plugin.EventDialPress && pushAction == "reset_0db":
		newDb = 0.0
	case mode == "set":
		newDb = clampDb(targetDb)
	default:
		curDb, err := p.ensureInputVolumeDb(ctx, input)
		if err != nil {
			return err
		}
		newDb = clampDb(curDb + stepDb)
	}

	if _, err := p.client.Call(ctx, "SetInputVolume", map[string]any{
		"inputName":     input,
		"inputVolumeDb": newDb,
	}); err != nil {
		return err
	}

	p.mu.Lock()
	p.state.inputVolumeDb[input] = newDb
	p.mu.Unlock()
	p.refreshAllSlots()
	return nil
}

func (p *Provider) ensureInputVolumeDb(ctx context.Context, input string) (float64, error) {
	p.mu.RLock()
	curDb, ok := p.state.inputVolumeDb[input]
	p.mu.RUnlock()
	if ok {
		return curDb, nil
	}
	resp, err := p.client.Call(ctx, "GetInputVolume", map[string]any{"inputName": input})
	if err != nil {
		return 0, err
	}
	curDb = floatMapVal(resp, "inputVolumeDb", 0)
	p.mu.Lock()
	p.state.inputVolumeDb[input] = curDb
	p.mu.Unlock()
	return curDb, nil
}

func (p *Provider) execStream(ctx context.Context, ev plugin.ActionEvent) error {
	mode := stringSetting(ev.Settings, "mode", "toggle")
	switch mode {
	case "start":
		if _, err := p.client.Call(ctx, "StartStream", nil); err != nil {
			return err
		}
		p.mu.Lock()
		p.state.streaming = true
		p.mu.Unlock()
	case "stop":
		if _, err := p.client.Call(ctx, "StopStream", nil); err != nil {
			return err
		}
		p.mu.Lock()
		p.state.streaming = false
		p.mu.Unlock()
	default:
		resp, err := p.client.Call(ctx, "ToggleStream", nil)
		if err != nil {
			return err
		}
		p.mu.Lock()
		p.state.streaming = boolMapVal(resp, "outputActive", !p.state.streaming)
		p.mu.Unlock()
	}
	p.refreshAllSlots()
	return nil
}

func (p *Provider) execRecord(ctx context.Context, ev plugin.ActionEvent) error {
	mode := stringSetting(ev.Settings, "mode", "toggle")
	switch mode {
	case "start":
		if _, err := p.client.Call(ctx, "StartRecord", nil); err != nil {
			return err
		}
		p.mu.Lock()
		p.state.recording = true
		p.state.recordingPaused = false
		p.mu.Unlock()
	case "stop":
		if _, err := p.client.Call(ctx, "StopRecord", nil); err != nil {
			return err
		}
		p.mu.Lock()
		p.state.recording = false
		p.state.recordingPaused = false
		p.mu.Unlock()
	case "pause_toggle":
		resp, err := p.client.Call(ctx, "ToggleRecordPause", nil)
		if err != nil {
			return err
		}
		p.mu.Lock()
		p.state.recordingPaused = boolMapVal(resp, "outputPaused", !p.state.recordingPaused)
		p.mu.Unlock()
	default:
		resp, err := p.client.Call(ctx, "ToggleRecord", nil)
		if err != nil {
			return err
		}
		p.mu.Lock()
		p.state.recording = boolMapVal(resp, "outputActive", !p.state.recording)
		p.state.recordingPaused = false
		p.mu.Unlock()
	}
	p.refreshAllSlots()
	return nil
}

func (p *Provider) execReplayBuffer(ctx context.Context, ev plugin.ActionEvent) error {
	mode := stringSetting(ev.Settings, "mode", "save")
	switch mode {
	case "start":
		if _, err := p.client.Call(ctx, "StartReplayBuffer", nil); err != nil {
			return err
		}
		p.mu.Lock()
		p.state.replayActive = true
		p.mu.Unlock()
		p.refreshAllSlots()
	case "stop":
		if _, err := p.client.Call(ctx, "StopReplayBuffer", nil); err != nil {
			return err
		}
		p.mu.Lock()
		p.state.replayActive = false
		p.mu.Unlock()
		p.refreshAllSlots()
	case "toggle":
		resp, err := p.client.Call(ctx, "ToggleReplayBuffer", nil)
		if err != nil {
			return err
		}
		p.mu.Lock()
		p.state.replayActive = boolMapVal(resp, "outputActive", !p.state.replayActive)
		p.mu.Unlock()
		p.refreshAllSlots()
	default:
		if _, err := p.client.Call(ctx, "SaveReplayBuffer", nil); err != nil {
			return err
		}
		p.refreshContext(ev.Context, "ok")
	}
	return nil
}

func (p *Provider) execStudioMode(ctx context.Context, ev plugin.ActionEvent) error {
	mode := stringSetting(ev.Settings, "mode", "transition")
	switch mode {
	case "enable":
		if _, err := p.client.Call(ctx, "SetStudioModeEnabled", map[string]any{"studioModeEnabled": true}); err != nil {
			return err
		}
		p.mu.Lock()
		p.state.studioMode = true
		p.mu.Unlock()
		p.refreshAllSlots()
	case "disable":
		if _, err := p.client.Call(ctx, "SetStudioModeEnabled", map[string]any{"studioModeEnabled": false}); err != nil {
			return err
		}
		p.mu.Lock()
		p.state.studioMode = false
		p.mu.Unlock()
		p.refreshAllSlots()
	case "toggle":
		p.mu.RLock()
		next := !p.state.studioMode
		p.mu.RUnlock()
		if _, err := p.client.Call(ctx, "SetStudioModeEnabled", map[string]any{"studioModeEnabled": next}); err != nil {
			return err
		}
		p.mu.Lock()
		p.state.studioMode = next
		p.mu.Unlock()
		p.refreshAllSlots()
	default:
		if _, err := p.client.Call(ctx, "TriggerStudioModeTransition", nil); err != nil {
			return err
		}
		p.refreshContext(ev.Context, "ok")
	}
	return nil
}

func (p *Provider) execCustomRequest(ctx context.Context, ev plugin.ActionEvent) error {
	reqType := stringSetting(ev.Settings, "requestType", "")
	if reqType == "" {
		return errors.New("custom OBS request requires requestType")
	}
	rawJSON := stringSetting(ev.Settings, "requestData", "")
	var reqData map[string]any
	if rawJSON != "" && rawJSON != "{}" {
		if err := json.Unmarshal([]byte(rawJSON), &reqData); err != nil {
			return fmt.Errorf("invalid OBS requestData JSON: %w", err)
		}
	}
	if _, err := p.client.Call(ctx, reqType, reqData); err != nil {
		return err
	}
	p.refreshContext(ev.Context, "ok")
	return nil
}

func (p *Provider) handleClientStatus(st ClientStatus) {
	p.mu.RLock()
	closed := p.closed
	subs := make([]func(ClientStatus), 0, len(p.statusSubs))
	for _, fn := range p.statusSubs {
		subs = append(subs, fn)
	}
	p.mu.RUnlock()

	if closed {
		return
	}
	switch st.State {
	case StateConnected:
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			p.hydrateState(ctx)
		}()
	case StateDisconnected, StateError:
		p.resetState()
		p.refreshAllSlots()
	}

	for _, fn := range subs {
		fn(st)
	}
}

func (p *Provider) hydrateState(ctx context.Context) {
	p.refreshScenes(ctx)
	p.refreshInputs(ctx)

	if resp, err := p.client.Call(ctx, "GetStreamStatus", nil); err == nil {
		p.mu.Lock()
		p.state.streaming = boolMapVal(resp, "outputActive", false)
		p.mu.Unlock()
	}
	if resp, err := p.client.Call(ctx, "GetRecordStatus", nil); err == nil {
		p.mu.Lock()
		p.state.recording = boolMapVal(resp, "outputActive", false)
		p.state.recordingPaused = boolMapVal(resp, "outputPaused", false)
		p.mu.Unlock()
	}
	if resp, err := p.client.Call(ctx, "GetReplayBufferStatus", nil); err == nil {
		p.mu.Lock()
		p.state.replayActive = boolMapVal(resp, "outputActive", false)
		p.mu.Unlock()
	}
	if resp, err := p.client.Call(ctx, "GetStudioModeEnabled", nil); err == nil {
		p.mu.Lock()
		p.state.studioMode = boolMapVal(resp, "studioModeEnabled", false)
		p.mu.Unlock()
	}

	p.mu.RLock()
	slots := make([]trackedSlot, 0, len(p.slots))
	for _, s := range p.slots {
		slots = append(slots, s)
	}
	p.mu.RUnlock()

	for _, s := range slots {
		p.warmSlotState(s.actionUUID, s.settings)
	}
	p.refreshAllSlots()
}

func (p *Provider) warmSlotState(actionUUID string, settings map[string]any) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	switch actionUUID {
	case ActionToggleMute, ActionAudioVolume:
		input := stringSetting(settings, "inputName", "")
		if input == "" {
			return
		}
		if resp, err := p.client.Call(ctx, "GetInputMute", map[string]any{"inputName": input}); err == nil {
			p.mu.Lock()
			p.state.inputMuted[input] = boolMapVal(resp, "inputMuted", false)
			p.mu.Unlock()
		}
		if resp, err := p.client.Call(ctx, "GetInputVolume", map[string]any{"inputName": input}); err == nil {
			p.mu.Lock()
			p.state.inputVolumeDb[input] = floatMapVal(resp, "inputVolumeDb", 0)
			p.mu.Unlock()
		}
		p.refreshAllSlots()

	case ActionToggleSource:
		p.mu.RLock()
		scene := stringSetting(settings, "sceneName", p.state.programScene)
		p.mu.RUnlock()
		if scene != "" {
			p.refreshSceneItems(ctx, scene)
			p.refreshAllSlots()
		}
	}
}

func (p *Provider) refreshScenes(ctx context.Context) {
	resp, err := p.client.Call(ctx, "GetSceneList", nil)
	if err != nil {
		return
	}
	scenes := extractNames(resp["scenes"], "sceneName")
	slices.Reverse(scenes) // OBS GetSceneList returns bottom-to-top UI order.

	p.mu.Lock()
	p.state.scenes = scenes
	if s := stringMapVal(resp, "currentProgramSceneName"); s != "" {
		p.state.programScene = s
	}
	if s := stringMapVal(resp, "currentPreviewSceneName"); s != "" {
		p.state.previewScene = s
	}
	p.mu.Unlock()
}

func (p *Provider) refreshInputs(ctx context.Context) {
	resp, err := p.client.Call(ctx, "GetInputList", nil)
	if err != nil {
		return
	}
	inputs := extractNames(resp["inputs"], "inputName")
	p.mu.Lock()
	p.state.inputs = inputs
	p.mu.Unlock()
}

func (p *Provider) refreshSceneItems(ctx context.Context, sceneName string) {
	resp, err := p.client.Call(ctx, "GetSceneItemList", map[string]any{"sceneName": sceneName})
	if err != nil {
		return
	}
	rawItems, ok := resp["sceneItems"].([]any)
	if !ok {
		return
	}
	names := make([]string, 0, len(rawItems))
	p.mu.Lock()
	for _, entry := range rawItems {
		item, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		src := stringMapVal(item, "sourceName")
		if src == "" {
			continue
		}
		names = append(names, src)
		key := sourceKey{scene: sceneName, source: src}
		if id := intMapVal(item, "sceneItemId", 0); id > 0 {
			p.state.sourceItemID[key] = id
		}
		p.state.sourceEnabled[key] = boolMapVal(item, "sceneItemEnabled", true)
	}
	p.state.sceneSources[sceneName] = names
	p.mu.Unlock()
}

func (p *Provider) handleOBSEvent(ev Event) {
	needsRefresh := true
	switch ev.Type {
	case "CurrentProgramSceneChanged":
		p.mu.Lock()
		p.state.programScene = stringMapVal(ev.Data, "sceneName")
		p.mu.Unlock()
	case "CurrentPreviewSceneChanged":
		p.mu.Lock()
		p.state.previewScene = stringMapVal(ev.Data, "sceneName")
		p.mu.Unlock()
	case "SceneListChanged":
		p.mu.Lock()
		p.state.scenes = extractNames(ev.Data["scenes"], "sceneName")
		p.mu.Unlock()
	case "InputCreated", "InputRemoved", "InputNameChanged":
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			p.refreshInputs(ctx)
		}()
		needsRefresh = false
	case "InputMuteStateChanged":
		name := stringMapVal(ev.Data, "inputName")
		if name != "" {
			p.mu.Lock()
			p.state.inputMuted[name] = boolMapVal(ev.Data, "inputMuted", false)
			p.mu.Unlock()
		}
	case "InputVolumeChanged":
		name := stringMapVal(ev.Data, "inputName")
		if name != "" {
			p.mu.Lock()
			p.state.inputVolumeDb[name] = floatMapVal(ev.Data, "inputVolumeDb", 0)
			p.mu.Unlock()
		}
	case "SceneItemEnableStateChanged":
		scene := stringMapVal(ev.Data, "sceneName")
		itemID := intMapVal(ev.Data, "sceneItemId", 0)
		enabled := boolMapVal(ev.Data, "sceneItemEnabled", false)
		p.mu.Lock()
		for k, id := range p.state.sourceItemID {
			if k.scene == scene && id == itemID {
				p.state.sourceEnabled[k] = enabled
			}
		}
		p.mu.Unlock()
	case "StreamStateChanged":
		p.mu.Lock()
		p.state.streaming = boolMapVal(ev.Data, "outputActive", false)
		p.mu.Unlock()
	case "RecordStateChanged":
		p.mu.Lock()
		p.state.recording = boolMapVal(ev.Data, "outputActive", false)
		if stateStr := stringMapVal(ev.Data, "outputState"); stateStr == "OBS_WEBSOCKET_OUTPUT_PAUSED" {
			p.state.recordingPaused = true
		} else if stateStr != "" {
			p.state.recordingPaused = false
		}
		p.mu.Unlock()
	case "ReplayBufferStateChanged":
		p.mu.Lock()
		p.state.replayActive = boolMapVal(ev.Data, "outputActive", false)
		p.mu.Unlock()
	case "StudioModeStateChanged":
		p.mu.Lock()
		p.state.studioMode = boolMapVal(ev.Data, "studioModeEnabled", false)
		p.mu.Unlock()
	default:
		needsRefresh = false
	}

	if needsRefresh {
		p.refreshAllSlots()
	}
}

func (p *Provider) resetState() {
	p.mu.Lock()
	p.state = newOBSState()
	p.mu.Unlock()
}

func (p *Provider) refreshAllSlots() {
	p.mu.RLock()
	ctxIDs := make([]string, 0, len(p.slots))
	for id := range p.slots {
		ctxIDs = append(ctxIDs, id)
	}
	p.mu.RUnlock()

	for _, id := range ctxIDs {
		p.refreshContext(id, "")
	}
}

func (p *Provider) refreshContext(ctxID, flash string) {
	connected := p.client.IsConnected()

	p.mu.RLock()
	host := p.host
	slot, ok := p.slots[ctxID]
	st := p.state
	p.mu.RUnlock()

	if !ok || host == nil {
		return
	}

	patch := buildVisualPatch(slot, st, connected, flash)
	host.UpdateVisual(ctxID, patch)
}

func buildVisualPatch(slot trackedSlot, st obsState, connected bool, flash string) plugin.VisualPatch {
	if !connected {
		return plugin.VisualPatch{
			Subtitle: new("Offline"),
			Color:    new("#64748B"),
			Feedback: map[string]any{"value": "Offline"},
			Flash:    flash,
		}
	}

	var sub, col string
	var fb map[string]any

	switch slot.actionUUID {
	case ActionSwitchScene:
		scene := stringSetting(slot.settings, "sceneName", "")
		target := stringSetting(slot.settings, "target", "program")
		if target == "preview" {
			if scene != "" && strings.EqualFold(scene, st.previewScene) {
				sub, col = "PREVIEW", "#FBBF24"
			} else {
				sub, col = "Ready", "#38BDF8"
			}
		} else {
			if scene != "" && strings.EqualFold(scene, st.programScene) {
				sub, col = "ACTIVE", "#22C55E"
			} else {
				sub, col = "Ready", "#38BDF8"
			}
		}

	case ActionToggleSource:
		scene := stringSetting(slot.settings, "sceneName", st.programScene)
		source := stringSetting(slot.settings, "sourceName", "")
		enabled, cached := st.sourceEnabled[sourceKey{scene: scene, source: source}]
		switch {
		case !cached:
			sub, col = "Ready", "#38BDF8"
		case enabled:
			sub, col = "SHOWN", "#22C55E"
		default:
			sub, col = "HIDDEN", "#64748B"
		}

	case ActionToggleMute:
		input := stringSetting(slot.settings, "inputName", "")
		muted, cached := st.inputMuted[input]
		switch {
		case !cached:
			sub, col = "Ready", "#38BDF8"
		case muted:
			sub, col = "MUTED", "#EF4444"
		default:
			sub, col = "LIVE", "#22C55E"
		}

	case ActionAudioVolume:
		input := stringSetting(slot.settings, "inputName", "")
		if st.inputMuted[input] {
			sub, col = "MUTED", "#EF4444"
		} else if db, cached := st.inputVolumeDb[input]; cached {
			sub, col = fmt.Sprintf("%.1f dB", db), "#38BDF8"
		} else {
			sub, col = "Ready", "#38BDF8"
		}
		fb = map[string]any{"value": sub}

	case ActionStream:
		if st.streaming {
			sub, col = "ON AIR", "#EF4444"
		} else {
			sub, col = "OFFLINE", "#64748B"
		}

	case ActionRecord:
		switch {
		case st.recordingPaused:
			sub, col = "PAUSED", "#FBBF24"
		case st.recording:
			sub, col = "REC", "#EF4444"
		default:
			sub, col = "IDLE", "#64748B"
		}

	case ActionReplayBuffer:
		if st.replayActive {
			sub, col = "ACTIVE", "#22C55E"
		} else {
			sub, col = "OFF", "#64748B"
		}

	case ActionStudioMode:
		if st.studioMode {
			sub, col = "STUDIO ON", "#38BDF8"
		} else {
			sub, col = "OFF", "#64748B"
		}

	case ActionCustomRequest:
		reqType := stringSetting(slot.settings, "requestType", "")
		if reqType != "" {
			sub = reqType
		} else {
			sub = "Ready"
		}
		col = "#A855F7"
	}

	return plugin.VisualPatch{
		Subtitle: new(sub),
		Color:    new(col),
		Feedback: fb,
		Flash:    flash,
	}
}

func toSelectOptions(values []string) []plugin.SelectOption {
	if len(values) == 0 {
		return nil
	}
	out := make([]plugin.SelectOption, len(values))
	for i, v := range values {
		out[i] = plugin.SelectOption{Label: v, Value: v}
	}
	return out
}

func extractNames(raw any, field string) []string {
	arr, ok := raw.([]any)
	if !ok {
		return nil
	}
	names := make([]string, 0, len(arr))
	for _, entry := range arr {
		m, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		if name := stringMapVal(m, field); name != "" {
			names = append(names, name)
		}
	}
	return names
}

func clampDb(db float64) float64 {
	return min(max(db, -100.0), 26.0)
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

func floatSetting(settings map[string]any, key string, def float64) float64 {
	if settings == nil {
		return def
	}
	v, ok := settings[key]
	if !ok || v == nil {
		return def
	}
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case string:
		if parsed, err := strconv.ParseFloat(strings.TrimSpace(n), 64); err == nil {
			return parsed
		}
		return def
	default:
		return def
	}
}

func stringMapVal(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	s, _ := v.(string)
	return s
}

func boolMapVal(m map[string]any, key string, def bool) bool {
	if m == nil {
		return def
	}
	v, ok := m[key]
	if !ok || v == nil {
		return def
	}
	b, ok := v.(bool)
	if !ok {
		return def
	}
	return b
}

func intMapVal(m map[string]any, key string, def int) int {
	if m == nil {
		return def
	}
	v, ok := m[key]
	if !ok || v == nil {
		return def
	}
	switch n := v.(type) {
	case int:
		return n
	case float64:
		return int(n)
	case int64:
		return int(n)
	default:
		return def
	}
}

func floatMapVal(m map[string]any, key string, def float64) float64 {
	if m == nil {
		return def
	}
	v, ok := m[key]
	if !ok || v == nil {
		return def
	}
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	default:
		return def
	}
}
