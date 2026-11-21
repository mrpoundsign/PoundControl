package wasmplugin

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"

	"github.com/mrpoundsign/PoundDeck/pkg/plugin"
)

// PluginRunState describes the lifecycle state of a loaded .wasm module.
type PluginRunState string

const (
	StatusRunning PluginRunState = "running"
	StatusError   PluginRunState = "error"
	StatusStopped PluginRunState = "stopped"
)

// PluginStatus summarizes one discovered WebAssembly plugin for Studio status cards.
type PluginStatus struct {
	UUID         string         `json:"uuid"`
	Name         string         `json:"name"`
	Version      string         `json:"version"`
	Author       string         `json:"author,omitempty"`
	Path         string         `json:"path"`
	AllowedHosts []string       `json:"allowedHosts,omitempty"`
	ActionCount  int            `json:"actionCount"`
	State        PluginRunState `json:"state"`
	Error        string         `json:"error,omitempty"`
}

// HostConfig persists user-configured WebAssembly search paths.
type HostConfig struct {
	ExtraPaths []string `json:"extraPaths,omitempty"`
}

// Options configures a WebAssembly plugin Provider.
type Options struct {
	DefaultPluginsDir string
	HTTPClient        *http.Client
	CallTimeout       time.Duration
	OnStatusChange    func()
}

type timerHandle struct {
	cancel context.CancelFunc
}

type wasmInstance struct {
	provider     *Provider
	path         string
	manifest     plugin.Manifest
	allowedHosts []string

	stateMu sync.RWMutex
	state   PluginRunState
	errMsg  string

	callMu  sync.Mutex
	rt      wazero.Runtime
	mod     api.Module
	allocFn api.Function
	freeFn  api.Function
	eventFn api.Function
	timerFn api.Function

	lifeCtx    context.Context
	lifeCancel context.CancelFunc

	timersMu sync.Mutex
	timers   map[string]timerHandle
}

func (inst *wasmInstance) snapshotState() (PluginRunState, string) {
	inst.stateMu.RLock()
	st, msg := inst.state, inst.errMsg
	inst.stateMu.RUnlock()
	return st, msg
}

func (inst *wasmInstance) setState(st PluginRunState, msg string) {
	inst.stateMu.Lock()
	inst.state = st
	inst.errMsg = msg
	inst.stateMu.Unlock()
}

// Provider hosts pure-Go wazero WebAssembly plugins implementing plugin.Provider.
type Provider struct {
	reloadMu sync.Mutex
	mu       sync.RWMutex

	opts        Options
	httpClient  *http.Client
	callTimeout time.Duration
	host        plugin.HostAPI
	closed      bool

	instances      map[string]*wasmInstance // key: manifest UUID or file path on load error
	actionToPlugin map[string]string        // action UUID -> instance key
	manifests      []plugin.Manifest
	actions        []plugin.ActionDescriptor
}

var _ plugin.Provider = (*Provider)(nil)

// NewProvider constructs a sandboxed wazero WebAssembly plugin provider.
func NewProvider(opts Options) *Provider {
	httpCli := opts.HTTPClient
	if httpCli == nil {
		httpCli = &http.Client{Timeout: maxHTTPTimeout}
	}
	timeout := opts.CallTimeout
	if timeout <= 0 {
		timeout = defaultCallTimeout
	}
	return &Provider{
		opts:           opts,
		httpClient:     httpCli,
		callTimeout:    timeout,
		instances:      make(map[string]*wasmInstance),
		actionToPlugin: make(map[string]string),
	}
}

// Kind implements plugin.Provider.
func (p *Provider) Kind() plugin.ProviderKind {
	return plugin.ProviderWasm
}

// Name implements plugin.Provider.
func (p *Provider) Name() string {
	return "WebAssembly (.wasm) Plugins"
}

// DefaultPluginsDir returns the per-user folder scanned automatically for .wasm plugins.
func (p *Provider) DefaultPluginsDir() string {
	return p.opts.DefaultPluginsDir
}

// Manifests implements plugin.Provider.
func (p *Provider) Manifests() []plugin.Manifest {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]plugin.Manifest, len(p.manifests))
	copy(out, p.manifests)
	return out
}

// Actions implements plugin.Provider.
func (p *Provider) Actions() []plugin.ActionDescriptor {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]plugin.ActionDescriptor, len(p.actions))
	copy(out, p.actions)
	return out
}

// BindHost implements plugin.Provider.
func (p *Provider) BindHost(host plugin.HostAPI) {
	p.mu.Lock()
	p.host = host
	p.mu.Unlock()
}

// HostConfig reads persisted search path settings from the bound Engine.
func (p *Provider) HostConfig() HostConfig {
	p.mu.RLock()
	host := p.host
	p.mu.RUnlock()
	if host == nil {
		return HostConfig{}
	}
	gs := host.GetGlobalSettings(HostPluginUUID)
	if len(gs) == 0 {
		return HostConfig{}
	}

	var cfg HostConfig
	switch raw := gs["extraPaths"].(type) {
	case []string:
		for _, s := range raw {
			if t := strings.TrimSpace(s); t != "" {
				cfg.ExtraPaths = append(cfg.ExtraPaths, t)
			}
		}
	case []any:
		for _, item := range raw {
			if s, ok := item.(string); ok {
				if t := strings.TrimSpace(s); t != "" {
					cfg.ExtraPaths = append(cfg.ExtraPaths, t)
				}
			}
		}
	}
	return cfg
}

// SaveHostConfig persists WebAssembly search path settings into global plugin settings.
func (p *Provider) SaveHostConfig(cfg HostConfig) error {
	p.mu.RLock()
	host := p.host
	p.mu.RUnlock()
	if host == nil {
		return errors.New("wasm plugin host is not bound to an engine")
	}

	cleaned := cleanPathList(cfg.ExtraPaths)
	return host.SaveGlobalSettings(HostPluginUUID, map[string]any{
		"extraPaths": cleaned,
	})
}

// Plugins returns a sorted snapshot of discovered WebAssembly plugin statuses.
func (p *Provider) Plugins() []PluginStatus {
	p.mu.RLock()
	out := make([]PluginStatus, 0, len(p.instances))
	for _, inst := range p.instances {
		name := inst.manifest.Name
		if name == "" {
			name = filepath.Base(inst.path)
		}
		st, errMsg := inst.snapshotState()
		out = append(out, PluginStatus{
			UUID:         inst.manifest.UUID,
			Name:         name,
			Version:      inst.manifest.Version,
			Author:       inst.manifest.Author,
			Path:         inst.path,
			AllowedHosts: slices.Clone(inst.allowedHosts),
			ActionCount:  len(inst.manifest.Actions),
			State:        st,
			Error:        errMsg,
		})
	}
	p.mu.RUnlock()

	slices.SortFunc(out, func(a, b PluginStatus) int {
		if c := cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)); c != 0 {
			return c
		}
		return cmp.Compare(a.Path, b.Path)
	})
	return out
}

// Start discovers and instantiates all configured .wasm plugins.
func (p *Provider) Start() error {
	return p.Reload()
}

// Reload stops active Wasm modules/timers and cleanly reloads all discovered .wasm plugins.
func (p *Provider) Reload() error {
	p.reloadMu.Lock()
	defer p.reloadMu.Unlock()

	p.mu.RLock()
	closed := p.closed
	p.mu.RUnlock()
	if closed {
		return errors.New("wasm plugin provider is closed")
	}

	p.stopAllInstances()

	cfg := p.HostConfig()
	wasmPaths := discoverWasmFiles(p.opts.DefaultPluginsDir, cfg.ExtraPaths)

	newInstances := make(map[string]*wasmInstance, len(wasmPaths))
	newActionToPlugin := make(map[string]string)
	var newManifests []plugin.Manifest
	var newActions []plugin.ActionDescriptor

	for _, path := range wasmPaths {
		inst := p.loadInstance(path)
		key := inst.manifest.UUID
		if key == "" {
			key = "err:" + inst.path
		}
		if _, dup := newInstances[key]; dup {
			inst.closeInstance()
			continue
		}
		newInstances[key] = inst
		if inst.state == StatusRunning {
			newManifests = append(newManifests, inst.manifest)
			for _, act := range inst.manifest.Actions {
				if _, exists := newActionToPlugin[act.UUID]; exists {
					continue
				}
				newActionToPlugin[act.UUID] = key
				newActions = append(newActions, act)
			}
		}
	}

	p.mu.Lock()
	p.instances = newInstances
	p.actionToPlugin = newActionToPlugin
	p.manifests = newManifests
	p.actions = newActions
	cb := p.opts.OnStatusChange
	p.mu.Unlock()

	if cb != nil {
		cb()
	}
	return nil
}

// Dispatch implements plugin.Provider by serializing ev as JSON to the owning module's pd_handle_event export.
func (p *Provider) Dispatch(_ context.Context, ev plugin.ActionEvent) error {
	p.mu.RLock()
	key, hasAct := p.actionToPlugin[ev.ActionUUID]
	inst := p.instances[key]
	p.mu.RUnlock()

	if !hasAct || inst == nil {
		return fmt.Errorf("wasmplugin: unknown action %q", ev.ActionUUID)
	}
	if curState, _ := inst.snapshotState(); curState != StatusRunning || inst.eventFn == nil {
		return fmt.Errorf("wasmplugin: plugin %q is not running", inst.manifest.Name)
	}

	raw, err := json.Marshal(ev)
	if err != nil {
		return err
	}

	_, err = inst.callWithBytes(inst.eventFn, raw)
	return err
}

// Close implements plugin.Provider, canceling timers and closing all wazero runtimes.
func (p *Provider) Close() error {
	p.reloadMu.Lock()
	defer p.reloadMu.Unlock()

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	p.mu.Unlock()

	p.stopAllInstances()
	return nil
}

func (p *Provider) stopAllInstances() {
	p.mu.Lock()
	old := p.instances
	p.instances = make(map[string]*wasmInstance)
	p.actionToPlugin = make(map[string]string)
	p.manifests = nil
	p.actions = nil
	p.mu.Unlock()

	for _, inst := range old {
		inst.closeInstance()
	}
}

func (p *Provider) loadInstance(path string) *wasmInstance {
	lifeCtx, lifeCancel := context.WithCancel(context.Background())
	inst := &wasmInstance{
		provider:   p,
		path:       path,
		state:      StatusError,
		lifeCtx:    lifeCtx,
		lifeCancel: lifeCancel,
		timers:     make(map[string]timerHandle),
	}

	wasmBytes, err := os.ReadFile(path)
	if err != nil {
		inst.errMsg = fmt.Sprintf("read wasm file: %v", err)
		return inst
	}

	rtCfg := wazero.NewRuntimeConfig().
		WithMemoryLimitPages(maxMemoryPages).
		WithCloseOnContextDone(true)
	rt := wazero.NewRuntimeWithConfig(lifeCtx, rtCfg)
	inst.rt = rt

	wasi_snapshot_preview1.MustInstantiate(lifeCtx, rt)
	if err := p.instantiateHostModule(lifeCtx, rt, inst); err != nil {
		inst.errMsg = fmt.Sprintf("host module init failed: %v", err)
		inst.closeInstance()
		return inst
	}

	compiled, err := rt.CompileModule(lifeCtx, wasmBytes)
	if err != nil {
		inst.errMsg = fmt.Sprintf("compile wasm: %v", err)
		inst.closeInstance()
		return inst
	}

	modCfg := wazero.NewModuleConfig().
		WithName(filepath.Base(path)).
		WithStartFunctions("_initialize").
		WithSysWalltime().
		WithSysNanotime().
		WithSysNanosleep().
		WithRandSource(rand.Reader)

	initCtx, initCancel := context.WithTimeout(lifeCtx, p.callTimeout)
	defer initCancel()

	mod, err := rt.InstantiateModule(initCtx, compiled, modCfg)
	if err != nil {
		inst.errMsg = fmt.Sprintf("instantiate wasm: %v", err)
		inst.closeInstance()
		return inst
	}

	inst.mod = mod
	inst.allocFn = mod.ExportedFunction("pd_alloc")
	inst.freeFn = mod.ExportedFunction("pd_free")
	manifestFn := mod.ExportedFunction("pd_manifest")
	inst.eventFn = mod.ExportedFunction("pd_handle_event")
	inst.timerFn = mod.ExportedFunction("pd_on_timer")

	if manifestFn == nil || inst.eventFn == nil {
		inst.errMsg = "missing required PoundDeck exports (pd_manifest, pd_handle_event)"
		inst.closeInstance()
		return inst
	}

	gm, err := inst.readManifest(manifestFn)
	if err != nil {
		inst.errMsg = err.Error()
		inst.closeInstance()
		return inst
	}

	pm := gm.toPluginManifest()
	if err := pm.Validate(); err != nil {
		inst.errMsg = fmt.Sprintf("invalid manifest: %v", err)
		inst.closeInstance()
		return inst
	}

	inst.manifest = pm
	inst.allowedHosts = cleanHostList(gm.Permissions.AllowedHosts)
	inst.state = StatusRunning
	return inst
}

func (p *Provider) instantiateHostModule(ctx context.Context, rt wazero.Runtime, inst *wasmInstance) error {
	builder := rt.NewHostModuleBuilder(HostModuleName)

	builder.NewFunctionBuilder().
		WithFunc(func(_ context.Context, m api.Module, ctxPtr, ctxLen, patchPtr, patchLen uint32) int32 {
			ctxBytes, ok1 := readGuestBytes(m, ctxPtr, ctxLen)
			patchBytes, ok2 := readGuestBytes(m, patchPtr, patchLen)
			if !ok1 || !ok2 {
				return -1
			}
			var patch plugin.VisualPatch
			if err := json.Unmarshal(patchBytes, &patch); err != nil {
				return -2
			}
			p.mu.RLock()
			host := p.host
			p.mu.RUnlock()
			if host != nil {
				host.UpdateVisual(string(ctxBytes), patch)
			}
			return 0
		}).
		Export("pd_update_visual")

	builder.NewFunctionBuilder().
		WithFunc(func(_ context.Context, m api.Module, ctxPtr, ctxLen, jsonPtr, jsonLen uint32) int32 {
			ctxBytes, ok1 := readGuestBytes(m, ctxPtr, ctxLen)
			rawJSON, ok2 := readGuestBytes(m, jsonPtr, jsonLen)
			if !ok1 || !ok2 {
				return -1
			}
			var settings map[string]any
			if err := json.Unmarshal(rawJSON, &settings); err != nil {
				return -2
			}
			p.mu.RLock()
			host := p.host
			p.mu.RUnlock()
			if host == nil {
				return -3
			}
			if err := host.SaveSettings(string(ctxBytes), settings); err != nil {
				return -4
			}
			return 0
		}).
		Export("pd_save_settings")

	builder.NewFunctionBuilder().
		WithFunc(func(_ context.Context, m api.Module, outPtr, outCap uint32) int32 {
			p.mu.RLock()
			host := p.host
			p.mu.RUnlock()
			var gs map[string]any
			if host != nil && inst.manifest.UUID != "" {
				gs = host.GetGlobalSettings(inst.manifest.UUID)
			}
			if gs == nil {
				gs = map[string]any{}
			}
			encoded, err := json.Marshal(gs)
			if err != nil {
				return -1
			}
			return writeGuestResponse(m, outPtr, outCap, encoded)
		}).
		Export("pd_get_global_settings")

	builder.NewFunctionBuilder().
		WithFunc(func(_ context.Context, m api.Module, jsonPtr, jsonLen uint32) int32 {
			rawJSON, ok := readGuestBytes(m, jsonPtr, jsonLen)
			if !ok {
				return -1
			}
			var gs map[string]any
			if err := json.Unmarshal(rawJSON, &gs); err != nil {
				return -2
			}
			p.mu.RLock()
			host := p.host
			p.mu.RUnlock()
			if host == nil || inst.manifest.UUID == "" {
				return -3
			}
			if err := host.SaveGlobalSettings(inst.manifest.UUID, gs); err != nil {
				return -4
			}
			return 0
		}).
		Export("pd_save_global_settings")

	builder.NewFunctionBuilder().
		WithFunc(func(_ context.Context, m api.Module, msgPtr, msgLen uint32) {
			msgBytes, ok := readGuestBytes(m, msgPtr, msgLen)
			if !ok {
				return
			}
			p.mu.RLock()
			host := p.host
			p.mu.RUnlock()
			if host != nil {
				pid := inst.manifest.UUID
				if pid == "" {
					pid = filepath.Base(inst.path)
				}
				host.Log(pid, string(msgBytes))
			}
		}).
		Export("pd_log")

	builder.NewFunctionBuilder().
		WithFunc(func(callCtx context.Context, m api.Module, reqPtr, reqLen, respPtr, respCap uint32) int32 {
			reqBytes, ok := readGuestBytes(m, reqPtr, reqLen)
			if !ok {
				return -1
			}
			var req GuestHTTPRequest
			if err := json.Unmarshal(reqBytes, &req); err != nil {
				return writeGuestJSON(m, respPtr, respCap, GuestHTTPResponse{
					Status: http.StatusBadRequest,
					Error:  "invalid HTTP request JSON: " + err.Error(),
				})
			}
			resp := p.executeSandboxedHTTP(callCtx, inst.allowedHosts, req)
			return writeGuestJSON(m, respPtr, respCap, resp)
		}).
		Export("pd_http_request")

	builder.NewFunctionBuilder().
		WithFunc(func(_ context.Context, m api.Module, idPtr, idLen, ctxPtr, ctxLen, intervalMs uint32) int32 {
			idBytes, ok1 := readGuestBytes(m, idPtr, idLen)
			ctxBytes, ok2 := readGuestBytes(m, ctxPtr, ctxLen)
			if !ok1 || !ok2 {
				return -1
			}
			timerID := strings.TrimSpace(string(idBytes))
			if timerID == "" {
				return -2
			}
			if err := inst.configureTimer(timerID, string(ctxBytes), intervalMs); err != nil {
				return -3
			}
			return 0
		}).
		Export("pd_set_timer")

	builder.NewFunctionBuilder().
		WithFunc(func(_ context.Context, m api.Module, cmdPtr, cmdLen uint32) int32 {
			raw, ok := readGuestBytes(m, cmdPtr, cmdLen)
			if !ok {
				return -1
			}
			var cmd GuestNavCommand
			if err := json.Unmarshal(raw, &cmd); err != nil {
				return -2
			}
			p.mu.RLock()
			host := p.host
			p.mu.RUnlock()
			if host == nil || host.Deck() == nil {
				return -3
			}
			deckCtrl := host.Deck()
			var err error
			switch cmd.Action {
			case "nextPage":
				err = deckCtrl.NextPage(true)
			case "prevPage":
				err = deckCtrl.PrevPage(true)
			case "goToPage":
				err = deckCtrl.GoToPage(cmd.Page)
			case "openFolder":
				err = deckCtrl.OpenFolder(cmd.FolderID, cmd.FolderName)
			case "closeFolder":
				err = deckCtrl.CloseFolder()
			case "switchProfile":
				err = deckCtrl.SwitchProfile(cmd.ProfileRef)
			default:
				return -4
			}
			if err != nil {
				return -5
			}
			return 0
		}).
		Export("pd_deck_nav")

	_, err := builder.Instantiate(ctx)
	return err
}

func (p *Provider) executeSandboxedHTTP(ctx context.Context, allowedHosts []string, req GuestHTTPRequest) GuestHTTPResponse {
	if !isHostAllowed(req.URL, allowedHosts) {
		return GuestHTTPResponse{
			Status: http.StatusForbidden,
			Error:  "URL blocked by WebAssembly plugin permissions.allowedHosts sandbox",
		}
	}

	method := strings.ToUpper(strings.TrimSpace(req.Method))
	if method == "" {
		method = http.MethodGet
	}

	timeout := defaultHTTPTimeout
	if req.TimeoutMs > 0 {
		timeout = min(time.Duration(req.TimeoutMs)*time.Millisecond, maxHTTPTimeout)
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var bodyReader io.Reader
	if req.Body != "" {
		bodyReader = strings.NewReader(req.Body)
	}
	httpReq, err := http.NewRequestWithContext(reqCtx, method, req.URL, bodyReader)
	if err != nil {
		return GuestHTTPResponse{Status: 0, Error: err.Error()}
	}
	for k, v := range req.Headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return GuestHTTPResponse{Status: 0, Error: err.Error()}
	}
	defer func() { _ = resp.Body.Close() }()

	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, maxHTTPResponseBytes))
	if err != nil {
		return GuestHTTPResponse{Status: resp.StatusCode, Error: err.Error()}
	}

	hdrs := make(map[string]string, len(resp.Header))
	for k, vals := range resp.Header {
		if len(vals) > 0 {
			hdrs[k] = vals[0]
		}
	}
	return GuestHTTPResponse{
		Status:  resp.StatusCode,
		Headers: hdrs,
		Body:    string(bodyBytes),
	}
}

func (inst *wasmInstance) readManifest(manifestFn api.Function) (GuestManifest, error) {
	inst.callMu.Lock()
	defer inst.callMu.Unlock()

	ctx, cancel := context.WithTimeout(inst.lifeCtx, inst.provider.callTimeout)
	defer cancel()

	res, err := manifestFn.Call(ctx)
	if err != nil {
		return GuestManifest{}, fmt.Errorf("pd_manifest failed: %w", err)
	}
	if len(res) == 0 {
		return GuestManifest{}, errors.New("pd_manifest returned no pointer/length")
	}
	packed := res[0]
	ptr := uint32(packed >> 32)
	size := uint32(packed & 0xffffffff)
	if ptr == 0 || size == 0 || size > maxGuestPayloadBytes {
		return GuestManifest{}, fmt.Errorf("pd_manifest returned invalid region ptr=%d len=%d", ptr, size)
	}

	raw, ok := readGuestBytes(inst.mod, ptr, size)
	if inst.freeFn != nil {
		_, _ = inst.freeFn.Call(ctx, uint64(ptr))
	}
	if !ok {
		return GuestManifest{}, errors.New("pd_manifest memory region out of bounds")
	}

	var gm GuestManifest
	if err := json.Unmarshal(raw, &gm); err != nil {
		return GuestManifest{}, fmt.Errorf("decode pd_manifest JSON: %w", err)
	}
	return gm, nil
}

func (inst *wasmInstance) callWithBytes(fn api.Function, payload []byte) (int32, error) {
	inst.callMu.Lock()
	defer inst.callMu.Unlock()

	if curState, _ := inst.snapshotState(); curState != StatusRunning || inst.mod == nil || inst.allocFn == nil || fn == nil {
		return -1, errors.New("wasm module cannot accept calls")
	}

	ctx, cancel := context.WithTimeout(inst.lifeCtx, inst.provider.callTimeout)
	defer cancel()

	allocRes, err := inst.allocFn.Call(ctx, uint64(len(payload)))
	if err != nil || len(allocRes) == 0 || allocRes[0] == 0 {
		inst.failLocked("pd_alloc failed: " + errString(err))
		return -1, fmt.Errorf("pd_alloc failed: %w", err)
	}
	ptr := uint32(allocRes[0])
	defer func() {
		if inst.freeFn != nil && inst.mod != nil {
			freeCtx, freeCancel := context.WithTimeout(inst.lifeCtx, time.Second)
			_, _ = inst.freeFn.Call(freeCtx, uint64(ptr))
			freeCancel()
		}
	}()

	mem := inst.mod.Memory()
	if mem == nil || !mem.Write(ptr, payload) {
		return -1, errors.New("failed writing payload to guest linear memory")
	}

	res, err := fn.Call(ctx, uint64(ptr), uint64(len(payload)))
	if err != nil {
		inst.failLocked(err.Error())
		return -1, err
	}
	if len(res) > 0 {
		return int32(res[0]), nil
	}
	return 0, nil
}

func (inst *wasmInstance) failLocked(msg string) {
	inst.setState(StatusError, msg)
	inst.stopTimers()
	if inst.provider != nil && inst.provider.opts.OnStatusChange != nil {
		go inst.provider.opts.OnStatusChange()
	}
}

func (inst *wasmInstance) configureTimer(timerID, slotCtx string, intervalMs uint32) error {
	inst.timersMu.Lock()
	if existing, ok := inst.timers[timerID]; ok {
		existing.cancel()
		delete(inst.timers, timerID)
	}
	if intervalMs == 0 {
		inst.timersMu.Unlock()
		return nil
	}
	if len(inst.timers) >= maxTimersPerPlugin {
		inst.timersMu.Unlock()
		return fmt.Errorf("maximum active timers (%d) reached", maxTimersPerPlugin)
	}

	interval := max(time.Duration(intervalMs)*time.Millisecond, minTimerInterval)
	tCtx, tCancel := context.WithCancel(inst.lifeCtx)
	inst.timers[timerID] = timerHandle{cancel: tCancel}
	inst.timersMu.Unlock()

	payload, _ := json.Marshal(GuestTimerEvent{
		TimerID: timerID,
		Context: slotCtx,
	})

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-tCtx.Done():
				return
			case <-ticker.C:
				if inst.timerFn == nil {
					return
				}
				if _, err := inst.callWithBytes(inst.timerFn, payload); err != nil {
					return
				}
			}
		}
	}()
	return nil
}

func (inst *wasmInstance) stopTimers() {
	inst.timersMu.Lock()
	for id, h := range inst.timers {
		h.cancel()
		delete(inst.timers, id)
	}
	inst.timersMu.Unlock()
}

func (inst *wasmInstance) closeInstance() {
	inst.stopTimers()
	if inst.lifeCancel != nil {
		inst.lifeCancel()
	}
	inst.callMu.Lock()
	if curState, _ := inst.snapshotState(); curState == StatusRunning {
		inst.setState(StatusStopped, "")
	}
	if inst.rt != nil {
		_ = inst.rt.Close(context.Background())
		inst.rt = nil
	}
	inst.mod = nil
	inst.callMu.Unlock()
}

func readGuestBytes(m api.Module, ptr, size uint32) ([]byte, bool) {
	if size == 0 {
		return nil, true
	}
	if size > maxGuestPayloadBytes || m == nil {
		return nil, false
	}
	mem := m.Memory()
	if mem == nil {
		return nil, false
	}
	raw, ok := mem.Read(ptr, size)
	if !ok {
		return nil, false
	}
	// Copy out immediately so subsequent memory writes cannot mutate our slice view.
	cp := make([]byte, len(raw))
	copy(cp, raw)
	return cp, true
}

func writeGuestResponse(m api.Module, outPtr, outCap uint32, data []byte) int32 {
	if len(data) > int(outCap) {
		return -int32(len(data))
	}
	if m == nil || m.Memory() == nil || !m.Memory().Write(outPtr, data) {
		return -1
	}
	return int32(len(data))
}

func writeGuestJSON(m api.Module, outPtr, outCap uint32, v any) int32 {
	encoded, err := json.Marshal(v)
	if err != nil {
		return -1
	}
	return writeGuestResponse(m, outPtr, outCap, encoded)
}

func discoverWasmFiles(defaultDir string, extraPaths []string) []string {
	candidates := make([]string, 0, len(extraPaths)+1)
	if t := strings.TrimSpace(defaultDir); t != "" {
		candidates = append(candidates, t)
	}
	candidates = append(candidates, extraPaths...)

	var files []string
	seen := make(map[string]struct{})
	addFile := func(p string) {
		abs, err := filepath.Abs(p)
		if err != nil {
			abs = filepath.Clean(p)
		}
		key := abs
		if runtime.GOOS == "windows" {
			key = strings.ToLower(abs)
		}
		if _, dup := seen[key]; dup {
			return
		}
		seen[key] = struct{}{}
		files = append(files, abs)
	}

	for _, root := range candidates {
		info, err := os.Stat(root)
		if err != nil {
			continue
		}
		if !info.IsDir() {
			if strings.EqualFold(filepath.Ext(root), ".wasm") {
				addFile(root)
			}
			continue
		}
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			full := filepath.Join(root, entry.Name())
			if !entry.IsDir() {
				if strings.EqualFold(filepath.Ext(entry.Name()), ".wasm") {
					addFile(full)
				}
				continue
			}
			subEntries, subErr := os.ReadDir(full)
			if subErr != nil {
				continue
			}
			for _, sub := range subEntries {
				if !sub.IsDir() && strings.EqualFold(filepath.Ext(sub.Name()), ".wasm") {
					addFile(filepath.Join(full, sub.Name()))
				}
			}
		}
	}

	slices.SortFunc(files, func(a, b string) int {
		return cmp.Compare(strings.ToLower(a), strings.ToLower(b))
	})
	return files
}

func cleanPathList(raw []string) []string {
	out := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, s := range raw {
		t := strings.TrimSpace(s)
		if t == "" {
			continue
		}
		key := t
		if runtime.GOOS == "windows" {
			key = strings.ToLower(t)
		}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, t)
	}
	return out
}

func cleanHostList(raw []string) []string {
	out := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, s := range raw {
		t := strings.ToLower(strings.TrimSpace(s))
		if t == "" {
			continue
		}
		if _, dup := seen[t]; dup {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return out
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
