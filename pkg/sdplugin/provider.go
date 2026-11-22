package sdplugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/websocket"

	"github.com/mrpoundsign/PoundControl/pkg/builtin/apps"
	"github.com/mrpoundsign/PoundControl/pkg/deck"
	"github.com/mrpoundsign/PoundControl/pkg/plugin"
)

const virtualDeviceID = "poundcontrol-virtual-0"

// PluginRunState describes the runtime status of a loaded .sdPlugin bundle.
type PluginRunState string

const (
	StatusRunning   PluginRunState = "running"
	StatusStarting  PluginRunState = "starting"
	StatusNeedsNode PluginRunState = "needs_node"
	StatusError     PluginRunState = "error"
	StatusStopped   PluginRunState = "stopped"
)

// PluginStatus reports runtime and metadata details for one loaded .sdPlugin bundle.
type PluginStatus struct {
	UUID         string         `json:"uuid"`
	Name         string         `json:"name"`
	Version      string         `json:"version"`
	Author       string         `json:"author,omitempty"`
	Dir          string         `json:"dir"`
	IsNode       bool           `json:"isNode"`
	MinNodeMajor int            `json:"minNodeMajor,omitempty"`
	ActionCount  int            `json:"actionCount"`
	State        PluginRunState `json:"state"`
	Connected    bool           `json:"connected"`
	PID          int            `json:"pid,omitempty"`
	Error        string         `json:"error,omitempty"`
}

// HostConfig stores persisted configuration for discovering and running external .sdPlugin bundles.
type HostConfig struct {
	ExtraPaths     []string `json:"extraPaths,omitempty"`
	ScanElgato     bool     `json:"scanElgato,omitempty"`
	CustomNodePath string   `json:"customNodePath,omitempty"`
}

// Options configures a new Elgato .sdPlugin Provider host.
type Options struct {
	DefaultPluginsDir string
	ManagedNodeDir    string
	OpenURL           func(context.Context, string) error
	OnStatusChange    func()
	elgatoDir         string
	nodeManager       *NodeManager
	spawnOverride     func(inst *pluginInstance, port int) error
}

type wsPeer struct {
	conn       *websocket.Conn
	writeMu    sync.Mutex
	kind       string // "plugin" or "pi"
	pluginUUID string
	token      string
	piCtxID    string
	actionUUID string
}

func (p *wsPeer) sendJSON(v any) error {
	if p == nil || p.conn == nil {
		return errors.New("websocket connection closed")
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	p.writeMu.Lock()
	_ = p.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	err = websocket.Message.Send(p.conn, string(raw))
	_ = p.conn.SetWriteDeadline(time.Time{})
	p.writeMu.Unlock()
	return err
}

type pluginInstance struct {
	bundle      *LoadedBundle
	launchToken string
	state       PluginRunState
	errMsg      string
	cmd         *exec.Cmd
	waitDone    chan struct{}
	pid         int
	ws          *wsPeer
}

type piSessionInfo struct {
	token      string
	pluginUUID string
	actionUUID string
	ctxID      string
	controller deck.ControllerType
	coords     deck.Coordinates
	dial       int
	settings   map[string]any
	piRelPath  string
}

// Provider hosts Elgato Stream Deck SDK v2/v3 plugins and HTML Property Inspectors over
// a loopback WebSocket + HTTP server implementing plugin.Provider and plugin.PropertyInspectorProvider.
type Provider struct {
	reloadMu sync.Mutex
	mu       sync.RWMutex

	opts      Options
	elgatoDir string
	nodeMgr   *NodeManager
	openURL   func(context.Context, string) error
	host      plugin.HostAPI
	closed    bool

	listener net.Listener
	httpSrv  *http.Server
	port     int

	plugins        map[string]*pluginInstance
	pluginTokens   map[string]string // launchToken -> pluginUUID
	actionToPlugin map[string]string // actionUUID -> pluginUUID
	manifests      []plugin.Manifest
	actions        []plugin.ActionDescriptor

	activeSlots map[string]plugin.ActionEvent // contextID -> latest state
	piSessions  map[string]*piSessionInfo     // piToken -> session
	piOrder     []string
	piPeers     map[string]*wsPeer // piToken -> connected PI WS
}

var (
	_ plugin.Provider                  = (*Provider)(nil)
	_ plugin.PropertyInspectorProvider = (*Provider)(nil)
)

// NewProvider creates an Elgato .sdPlugin Provider configured by opts.
func NewProvider(opts Options) *Provider {
	nodeMgr := opts.nodeManager
	if nodeMgr == nil {
		nodeMgr = NewNodeManager(opts.ManagedNodeDir)
	}
	openURL := opts.OpenURL
	if openURL == nil {
		drv := apps.DefaultDriver()
		openURL = func(ctx context.Context, rawURL string) error {
			return drv.OpenURL(ctx, rawURL)
		}
	}
	elgatoDir := opts.elgatoDir
	if elgatoDir == "" && opts.nodeManager == nil && opts.spawnOverride == nil {
		elgatoDir = DefaultElgatoPluginsDir()
	}

	return &Provider{
		opts:           opts,
		elgatoDir:      elgatoDir,
		nodeMgr:        nodeMgr,
		openURL:        openURL,
		plugins:        make(map[string]*pluginInstance),
		pluginTokens:   make(map[string]string),
		actionToPlugin: make(map[string]string),
		activeSlots:    make(map[string]plugin.ActionEvent),
		piSessions:     make(map[string]*piSessionInfo),
		piPeers:        make(map[string]*wsPeer),
	}
}

// Kind returns plugin.ProviderStreamDeck.
func (p *Provider) Kind() plugin.ProviderKind {
	return plugin.ProviderStreamDeck
}

// Name returns the display name of the .sdPlugin host provider.
func (p *Provider) Name() string {
	return "Elgato Stream Deck (.sdPlugin) Host"
}

// Manifests returns the plugin manifests currently loaded from discovered .sdPlugin bundles.
func (p *Provider) Manifests() []plugin.Manifest {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return slices.Clone(p.manifests)
}

// Actions returns all actions declared by currently loaded .sdPlugin bundles.
func (p *Provider) Actions() []plugin.ActionDescriptor {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return slices.Clone(p.actions)
}

// BindHost sets the HostAPI used for visual updates, persistence, logging, and deck control.
func (p *Provider) BindHost(host plugin.HostAPI) {
	p.mu.Lock()
	p.host = host
	p.mu.Unlock()
}

// HostConfig loads persisted .sdPlugin discovery and runtime preferences from HostAPI global settings.
func (p *Provider) HostConfig() HostConfig {
	p.mu.RLock()
	host := p.host
	p.mu.RUnlock()
	if host == nil {
		return HostConfig{}
	}
	gs := host.GetGlobalSettings(HostPluginUUID)
	var cfg HostConfig
	if rawExtras, ok := gs["extraPaths"].([]any); ok {
		for _, v := range rawExtras {
			if s := strings.TrimSpace(fmt.Sprint(v)); s != "" {
				cfg.ExtraPaths = append(cfg.ExtraPaths, s)
			}
		}
	} else if strExtras, ok := gs["extraPaths"].([]string); ok {
		for _, s := range strExtras {
			if trimmed := strings.TrimSpace(s); trimmed != "" {
				cfg.ExtraPaths = append(cfg.ExtraPaths, trimmed)
			}
		}
	}
	if b, ok := gs["scanElgato"].(bool); ok {
		cfg.ScanElgato = b
	}
	if s, ok := gs["customNodePath"].(string); ok {
		cfg.CustomNodePath = strings.TrimSpace(s)
	}
	return cfg
}

// SaveHostConfig persists discovery and Node.js preferences into global plugin settings.
func (p *Provider) SaveHostConfig(cfg HostConfig) error {
	p.mu.RLock()
	host := p.host
	p.mu.RUnlock()
	if host == nil {
		return errors.New("sdplugin host is not bound to an engine")
	}

	cleaned := make([]string, 0, len(cfg.ExtraPaths))
	seen := make(map[string]struct{}, len(cfg.ExtraPaths))
	for _, raw := range cfg.ExtraPaths {
		t := strings.TrimSpace(raw)
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
		cleaned = append(cleaned, t)
	}

	return host.SaveGlobalSettings(HostPluginUUID, map[string]any{
		"extraPaths":     cleaned,
		"scanElgato":     cfg.ScanElgato,
		"customNodePath": strings.TrimSpace(cfg.CustomNodePath),
	})
}

// Port returns the active loopback TCP port serving WebSocket and Property Inspector endpoints, or 0 if stopped.
func (p *Provider) Port() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.port
}

// DefaultPluginsDir returns the primary per-user folder scanned automatically for .sdPlugin bundles.
func (p *Provider) DefaultPluginsDir() string {
	return p.opts.DefaultPluginsDir
}

// ElgatoPluginsDir returns the detected Elgato local plugins directory if applicable on this OS.
func (p *Provider) ElgatoPluginsDir() string {
	return p.elgatoDir
}

// NodeStatus returns resolved Node.js runtime information meeting requiredMajor (default 20).
func (p *Provider) NodeStatus(requiredMajor int) NodeRuntimeInfo {
	if requiredMajor <= 0 {
		requiredMajor = p.maxRequiredNodeMajor()
	}
	cfg := p.HostConfig()
	return p.nodeMgr.Resolve(cfg.CustomNodePath, requiredMajor)
}

// DownloadNode downloads and verifies the standalone managed Node.js runtime, then reloads waiting plugins.
func (p *Provider) DownloadNode(
	ctx context.Context,
	onProgress func(downloaded, total int64, stage string),
) (NodeRuntimeInfo, error) {
	info, err := p.nodeMgr.DownloadManaged(ctx, onProgress)
	if err != nil {
		return NodeRuntimeInfo{}, err
	}
	_ = p.Reload()
	return info, nil
}

// Plugins returns a sorted snapshot of discovered .sdPlugin statuses.
func (p *Provider) Plugins() []PluginStatus {
	p.mu.RLock()
	out := make([]PluginStatus, 0, len(p.plugins))
	for _, inst := range p.plugins {
		st := PluginStatus{
			UUID:         inst.bundle.UUID,
			Name:         inst.bundle.Manifest.Name,
			Version:      inst.bundle.Manifest.Version,
			Author:       inst.bundle.Manifest.Author,
			Dir:          inst.bundle.Dir,
			IsNode:       inst.bundle.IsNode,
			MinNodeMajor: inst.bundle.MinNodeMajor,
			ActionCount:  len(inst.bundle.Actions),
			State:        inst.state,
			Connected:    inst.ws != nil,
			PID:          inst.pid,
			Error:        inst.errMsg,
		}
		out = append(out, st)
	}
	p.mu.RUnlock()

	slices.SortFunc(out, func(a, b PluginStatus) int {
		if c := strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)); c != 0 {
			return c
		}
		return strings.Compare(a.UUID, b.UUID)
	})
	return out
}

// NeedsNodeForAction reports whether actionUUID belongs to an unloaded Node.js plugin awaiting runtime install.
func (p *Provider) NeedsNodeForAction(actionUUID string) (minMajor int, ok bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	pid, exists := p.actionToPlugin[actionUUID]
	if !exists {
		return 0, false
	}
	inst := p.plugins[pid]
	if inst == nil || inst.state != StatusNeedsNode {
		return 0, false
	}
	return inst.bundle.MinNodeMajor, true
}

func (p *Provider) maxRequiredNodeMajor() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	req := 20
	for _, inst := range p.plugins {
		if inst.bundle.IsNode && inst.bundle.MinNodeMajor > req {
			req = inst.bundle.MinNodeMajor
		}
	}
	return req
}

// Start begins the loopback WS/HTTP bridge and loads configured .sdPlugin bundles.
func (p *Provider) Start() error {
	p.reloadMu.Lock()
	defer p.reloadMu.Unlock()

	p.mu.RLock()
	closed := p.closed
	hasSrv := p.listener != nil
	p.mu.RUnlock()
	if closed {
		return errors.New("sdplugin provider is closed")
	}

	if !hasSrv {
		if err := p.startServer(); err != nil {
			return err
		}
	}
	return p.reloadLocked()
}

// Reload rescans configured .sdPlugin directories, restarts plugin processes cleanly, and refreshes statuses.
func (p *Provider) Reload() error {
	p.reloadMu.Lock()
	defer p.reloadMu.Unlock()

	p.mu.RLock()
	closed := p.closed
	hasSrv := p.listener != nil
	p.mu.RUnlock()
	if closed {
		return errors.New("sdplugin provider is closed")
	}
	if !hasSrv {
		if err := p.startServer(); err != nil {
			return err
		}
	}
	return p.reloadLocked()
}

func (p *Provider) startServer() error {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("listen sdplugin loopback bridge: %w", err)
	}
	addr, _ := ln.Addr().(*net.TCPAddr)
	if addr == nil {
		_ = ln.Close()
		return errors.New("unexpected non-TCP listener address")
	}

	wsSrv := websocket.Server{
		Handshake: func(_ *websocket.Config, _ *http.Request) error { return nil },
		Handler:   p.handleWSConn,
	}

	muxHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/pi/") {
			p.servePropertyInspector(w, r)
			return
		}
		wsSrv.ServeHTTP(w, r)
	})

	srv := &http.Server{
		Handler:           muxHandler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	p.mu.Lock()
	p.listener = ln
	p.httpSrv = srv
	p.port = addr.Port
	p.mu.Unlock()

	go func() {
		_ = srv.Serve(ln)
	}()
	return nil
}

func (p *Provider) reloadLocked() error {
	p.stopAllPlugins()

	cfg := p.HostConfig()
	if p.opts.DefaultPluginsDir != "" {
		_ = os.MkdirAll(p.opts.DefaultPluginsDir, 0o755)
	}

	var searchPaths []string
	if p.opts.DefaultPluginsDir != "" {
		searchPaths = append(searchPaths, p.opts.DefaultPluginsDir)
	}
	if cfg.ScanElgato && p.elgatoDir != "" {
		searchPaths = append(searchPaths, p.elgatoDir)
	}
	searchPaths = append(searchPaths, cfg.ExtraPaths...)

	bundleDirs := DiscoverBundles(searchPaths)

	newPlugins := make(map[string]*pluginInstance, len(bundleDirs))
	newTokens := make(map[string]string, len(bundleDirs))
	newActionToPlugin := make(map[string]string)
	newManifests := make([]plugin.Manifest, 0, len(bundleDirs))
	var newActions []plugin.ActionDescriptor

	p.mu.RLock()
	host := p.host
	port := p.port
	p.mu.RUnlock()

	for _, dir := range bundleDirs {
		b, err := LoadBundle(dir)
		if err != nil {
			if host != nil {
				host.Log(HostPluginUUID, fmt.Sprintf("Failed loading %s: %v", dir, err))
			}
			continue
		}
		if _, dup := newPlugins[b.UUID]; dup {
			continue
		}

		token := strings.ReplaceAll(deck.NewUUID(), "-", "")
		inst := &pluginInstance{
			bundle:      b,
			launchToken: token,
			state:       StatusStarting,
		}
		newPlugins[b.UUID] = inst
		newTokens[token] = b.UUID
		newManifests = append(newManifests, b.ToPluginManifest())
		for _, act := range b.Actions {
			if _, exists := newActionToPlugin[act.UUID]; exists {
				continue
			}
			newActionToPlugin[act.UUID] = b.UUID
			newActions = append(newActions, act)
		}
	}

	p.mu.Lock()
	p.plugins = newPlugins
	p.pluginTokens = newTokens
	p.actionToPlugin = newActionToPlugin
	p.manifests = newManifests
	p.actions = newActions
	p.mu.Unlock()

	for _, inst := range newPlugins {
		p.launchPluginInstance(inst, cfg.CustomNodePath, port)
	}
	p.emitStatusChange()
	return nil
}

func (p *Provider) launchPluginInstance(inst *pluginInstance, customNodePath string, port int) {
	if p.opts.spawnOverride != nil {
		if err := p.opts.spawnOverride(inst, port); err != nil {
			p.mu.Lock()
			inst.state = StatusError
			inst.errMsg = err.Error()
			p.mu.Unlock()
			return
		}
		p.mu.Lock()
		if inst.state == StatusStarting {
			inst.state = StatusRunning
		}
		p.mu.Unlock()
		return
	}

	b := inst.bundle
	var (
		binPath string
		args    []string
	)
	if b.IsNode {
		nodeInfo := p.nodeMgr.Resolve(customNodePath, b.MinNodeMajor)
		if !nodeInfo.Usable {
			msg := nodeInfo.Error
			if msg == "" {
				msg = fmt.Sprintf("Requires Node.js >= v%d", b.MinNodeMajor)
			}
			p.mu.Lock()
			inst.state = StatusNeedsNode
			inst.errMsg = msg
			p.mu.Unlock()
			return
		}
		binPath = nodeInfo.Path
		args = append(args, b.CodeAbsPath)
	} else {
		binPath = b.CodeAbsPath
	}

	infoBytes, _ := json.Marshal(buildInfoObject(b.UUID, b.Manifest.Version))
	args = append(args,
		"-port", strconv.Itoa(port),
		"-pluginUUID", inst.launchToken,
		"-registerEvent", "registerPlugin",
		"-info", string(infoBytes),
	)

	cmd := exec.Command(binPath, args...)
	cmd.Dir = b.Dir
	configureBackgroundCmd(cmd)

	if err := cmd.Start(); err != nil {
		p.mu.Lock()
		inst.state = StatusError
		inst.errMsg = fmt.Sprintf("start failed: %v", err)
		p.mu.Unlock()
		return
	}

	waitDone := make(chan struct{})
	pid := 0
	if cmd.Process != nil {
		pid = cmd.Process.Pid
	}

	p.mu.Lock()
	inst.cmd = cmd
	inst.waitDone = waitDone
	inst.pid = pid
	inst.state = StatusRunning
	p.mu.Unlock()

	go func(c *exec.Cmd, done chan struct{}, plugUUID string) {
		err := c.Wait()
		close(done)

		p.mu.Lock()
		cur := p.plugins[plugUUID]
		if cur == inst && !p.closed && cur.cmd == c {
			cur.pid = 0
			cur.ws = nil
			if err != nil {
				cur.state = StatusError
				cur.errMsg = fmt.Sprintf("plugin exited: %v", err)
			} else {
				cur.state = StatusStopped
			}
		}
		p.mu.Unlock()
		p.emitStatusChange()
	}(cmd, waitDone, b.UUID)
}

func (p *Provider) stopAllPlugins() {
	p.mu.Lock()
	instances := make([]*pluginInstance, 0, len(p.plugins))
	var conns []*websocket.Conn
	for _, inst := range p.plugins {
		instances = append(instances, inst)
		if inst.ws != nil && inst.ws.conn != nil {
			conns = append(conns, inst.ws.conn)
		}
		inst.ws = nil
	}
	for _, pi := range p.piPeers {
		if pi.conn != nil {
			conns = append(conns, pi.conn)
		}
	}
	clear(p.piPeers)
	p.mu.Unlock()

	for _, c := range conns {
		_ = c.Close()
	}

	for _, inst := range instances {
		p.stopInstance(inst)
	}
}

func (p *Provider) stopInstance(inst *pluginInstance) {
	if inst == nil {
		return
	}
	cmd := inst.cmd
	done := inst.waitDone
	if cmd == nil || cmd.Process == nil {
		return
	}

	if done != nil {
		select {
		case <-done:
			return
		case <-time.After(350 * time.Millisecond):
		}
	}
	_ = cmd.Process.Kill()
	if done != nil {
		select {
		case <-done:
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// Close shuts down all running plugin processes and closes the loopback WebSocket/HTTP server.
func (p *Provider) Close() error {
	p.reloadMu.Lock()
	defer p.reloadMu.Unlock()

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	srv := p.httpSrv
	ln := p.listener
	p.httpSrv = nil
	p.listener = nil
	p.port = 0
	p.mu.Unlock()

	p.stopAllPlugins()

	var errs []error
	if srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		if err := srv.Shutdown(ctx); err != nil {
			errs = append(errs, err)
		}
		cancel()
	}
	if ln != nil {
		_ = ln.Close()
	}
	return errors.Join(errs...)
}

// Dispatch routes Engine lifecycle and input events to the external .sdPlugin owning ev.ActionUUID.
func (p *Provider) Dispatch(_ context.Context, ev plugin.ActionEvent) error {
	p.mu.Lock()
	pid, hasAct := p.actionToPlugin[ev.ActionUUID]
	if !hasAct {
		p.mu.Unlock()
		return fmt.Errorf("sdplugin: unknown action %q", ev.ActionUUID)
	}
	inst := p.plugins[pid]
	switch ev.Type {
	case plugin.EventWillDisappear:
		delete(p.activeSlots, ev.Context)
	case plugin.EventWillAppear, plugin.EventSettingsChanged:
		p.activeSlots[ev.Context] = cloneActionEvent(ev)
	default:
		if cur, ok := p.activeSlots[ev.Context]; ok {
			cur.Settings = maps.Clone(ev.Settings)
			cur.State = ev.State
			p.activeSlots[ev.Context] = cur
		}
	}
	var peer *wsPeer
	var sdkVer int
	var state PluginRunState
	if inst != nil {
		peer = inst.ws
		sdkVer = inst.bundle.Manifest.SDKVersion
		state = inst.state
	}
	p.mu.Unlock()

	if state == StatusNeedsNode {
		if ev.Type == plugin.EventWillAppear || ev.Type == plugin.EventSettingsChanged || ev.Type == plugin.EventWillDisappear {
			return nil
		}
		return fmt.Errorf("%s requires Node.js runtime (install in Settings -> Plugins)", inst.bundle.Manifest.Name)
	}

	if peer == nil {
		// Lifecycle events emitted while a plugin subprocess is still booting will be replayed on registerPlugin.
		if ev.Type == plugin.EventWillAppear || ev.Type == plugin.EventSettingsChanged || ev.Type == plugin.EventWillDisappear {
			return nil
		}
		return fmt.Errorf("%s plugin is not connected", inst.bundle.Manifest.Name)
	}

	return sendSDKActionEvents(peer, ev, sdkVer)
}

func sendSDKActionEvents(peer *wsPeer, ev plugin.ActionEvent, sdkVer int) error {
	controllerName := "Keypad"
	if ev.Controller == deck.ControllerEncoder {
		controllerName = "Encoder"
	}
	coords := map[string]int{
		"column": ev.Coordinates.Col,
		"row":    ev.Coordinates.Row,
	}
	if ev.Controller == deck.ControllerEncoder {
		coords = map[string]int{
			"column": ev.DialIndex,
			"row":    0,
		}
	}

	payload := map[string]any{
		"settings":        nonNilMap(ev.Settings),
		"coordinates":     coords,
		"state":           ev.State,
		"isInMultiAction": false,
		"controller":      controllerName,
	}

	eventName := string(ev.Type)
	if ev.Type == plugin.EventDialRotate {
		payload["ticks"] = ev.Ticks
		payload["pressed"] = false
	} else if ev.Type == plugin.EventDialPress && sdkVer >= 3 {
		for _, name := range []string{"dialDown", "dialUp"} {
			if err := peer.sendJSON(map[string]any{
				"action":  ev.ActionUUID,
				"event":   name,
				"context": ev.Context,
				"device":  virtualDeviceID,
				"payload": payload,
			}); err != nil {
				return err
			}
		}
		return nil
	}

	return peer.sendJSON(map[string]any{
		"action":  ev.ActionUUID,
		"event":   eventName,
		"context": ev.Context,
		"device":  virtualDeviceID,
		"payload": payload,
	})
}

// PropertyInspectorURL returns a loopback HTTP URL serving the HTML Property Inspector for actionUUID.
func (p *Provider) PropertyInspectorURL(
	ctxID string,
	actionUUID string,
	controller deck.ControllerType,
	coords deck.Coordinates,
	dial int,
	settings map[string]any,
) (plugin.PropertyInspectorSession, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.port <= 0 {
		return plugin.PropertyInspectorSession{}, false
	}
	pid, ok := p.actionToPlugin[actionUUID]
	if !ok {
		return plugin.PropertyInspectorSession{}, false
	}
	inst := p.plugins[pid]
	if inst == nil {
		return plugin.PropertyInspectorSession{}, false
	}
	piRel := inst.bundle.ActionPI[actionUUID]
	if piRel == "" {
		return plugin.PropertyInspectorSession{}, false
	}

	token := strings.ReplaceAll(deck.NewUUID(), "-", "")
	sess := &piSessionInfo{
		token:      token,
		pluginUUID: pid,
		actionUUID: actionUUID,
		ctxID:      ctxID,
		controller: controller,
		coords:     coords,
		dial:       dial,
		settings:   maps.Clone(settings),
		piRelPath:  piRel,
	}
	p.piSessions[token] = sess
	p.piOrder = append(p.piOrder, token)
	if len(p.piOrder) > 32 {
		evict := p.piOrder[0]
		p.piOrder = slices.Delete(p.piOrder, 0, 1)
		delete(p.piSessions, evict)
	}

	title := inst.bundle.Manifest.Name
	for _, a := range inst.bundle.Actions {
		if a.UUID == actionUUID {
			title = a.Name + " — Property Inspector"
			break
		}
	}

	rawURL := fmt.Sprintf("http://127.0.0.1:%d/pi/%s/%s?token=%s",
		p.port,
		url.PathEscape(pid),
		escapeURLPathSegments(piRel),
		url.QueryEscape(token),
	)
	return plugin.PropertyInspectorSession{
		Title: title,
		URL:   rawURL,
	}, true
}

func escapeURLPathSegments(rel string) string {
	parts := strings.Split(rel, "/")
	for i, seg := range parts {
		parts[i] = url.PathEscape(seg)
	}
	return strings.Join(parts, "/")
}

func (p *Provider) servePropertyInspector(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	trimmed := strings.TrimPrefix(r.URL.Path, "/pi/")
	slashIdx := strings.IndexByte(trimmed, '/')
	if slashIdx <= 0 || slashIdx >= len(trimmed)-1 {
		http.NotFound(w, r)
		return
	}
	pluginUUID, err := url.PathUnescape(trimmed[:slashIdx])
	if err != nil {
		http.NotFound(w, r)
		return
	}
	relPath, err := url.PathUnescape(trimmed[slashIdx+1:])
	if err != nil {
		http.NotFound(w, r)
		return
	}

	p.mu.RLock()
	inst := p.plugins[pluginUUID]
	port := p.port
	var sessCopy *piSessionInfo
	if tok := strings.TrimSpace(r.URL.Query().Get("token")); tok != "" {
		if s := p.piSessions[tok]; s != nil && s.pluginUUID == pluginUUID {
			cp := *s
			if curSlot, ok := p.activeSlots[cp.ctxID]; ok {
				cp.settings = maps.Clone(curSlot.Settings)
			} else {
				cp.settings = maps.Clone(s.settings)
			}
			sessCopy = &cp
		}
	}
	p.mu.RUnlock()

	if inst == nil {
		http.NotFound(w, r)
		return
	}

	absPath, safe := ResolveAssetPath(inst.bundle.Dir, relPath)
	if !safe {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	data, err := os.ReadFile(absPath)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	ext := strings.ToLower(filepath.Ext(absPath))
	if ctype := mime.TypeByExtension(ext); ctype != "" {
		w.Header().Set("Content-Type", ctype)
	}
	if ext == ".html" || ext == ".htm" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if sessCopy != nil {
			data = injectPIBootstrap(data, port, inst.bundle, sessCopy)
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

func injectPIBootstrap(htmlBytes []byte, port int, b *LoadedBundle, sess *piSessionInfo) []byte {
	infoJSON, _ := json.Marshal(buildInfoObject(b.UUID, b.Manifest.Version))
	controllerName := "Keypad"
	col, row := sess.coords.Col, sess.coords.Row
	if sess.controller == deck.ControllerEncoder {
		controllerName = "Encoder"
		col, row = sess.dial, 0
	}
	actionInfoObj := map[string]any{
		"action":  sess.actionUUID,
		"context": sess.ctxID,
		"device":  virtualDeviceID,
		"payload": map[string]any{
			"settings":    nonNilMap(sess.settings),
			"coordinates": map[string]int{"column": col, "row": row},
			"controller":  controllerName,
		},
	}
	actionInfoJSON, _ := json.Marshal(actionInfoObj)

	tokJS, _ := json.Marshal(sess.token)
	regJS, _ := json.Marshal("registerPropertyInspector")
	infoArgJS, _ := json.Marshal(string(infoJSON))
	actionArgJS, _ := json.Marshal(string(actionInfoJSON))

	script := fmt.Sprintf(
		`<script>(function(){var p=%d,u=%s,e=%s,i=%s,a=%s;`+
			`function boot(){if(typeof window.connectElgatoStreamDeckSocket==="function"){`+
			`window.connectElgatoStreamDeckSocket(p,u,e,i,a);}}`+
			`if(document.readyState==="loading"){document.addEventListener("DOMContentLoaded",boot);}`+
			`else{setTimeout(boot,0);}})();</script>`,
		port, tokJS, regJS, infoArgJS, actionArgJS,
	)

	lower := strings.ToLower(string(htmlBytes))
	if idx := strings.LastIndex(lower, "</body>"); idx >= 0 {
		var out bytes.Buffer
		out.Grow(len(htmlBytes) + len(script))
		out.Write(htmlBytes[:idx])
		out.WriteString(script)
		out.Write(htmlBytes[idx:])
		return out.Bytes()
	}
	return append(slices.Clone(htmlBytes), []byte(script)...)
}

type sdkInboundMessage struct {
	Event   string          `json:"event"`
	UUID    string          `json:"uuid,omitempty"`
	Context string          `json:"context,omitempty"`
	Action  string          `json:"action,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

func (p *Provider) handleWSConn(conn *websocket.Conn) {
	peer := &wsPeer{conn: conn}
	defer func() {
		p.handlePeerDisconnect(peer)
		_ = conn.Close()
	}()

	for {
		var rawText string
		if err := websocket.Message.Receive(conn, &rawText); err != nil {
			if !errors.Is(err, io.EOF) {
				return
			}
			return
		}
		var msg sdkInboundMessage
		if err := json.Unmarshal([]byte(rawText), &msg); err != nil {
			continue
		}
		p.dispatchSDKInbound(peer, msg)
	}
}

func (p *Provider) handlePeerDisconnect(peer *wsPeer) {
	if peer == nil || peer.kind == "" {
		return
	}
	switch peer.kind {
	case "plugin":
		p.mu.Lock()
		inst := p.plugins[peer.pluginUUID]
		if inst != nil && inst.ws == peer {
			inst.ws = nil
		}
		p.mu.Unlock()
		p.emitStatusChange()

	case "pi":
		p.mu.Lock()
		if cur := p.piPeers[peer.token]; cur == peer {
			delete(p.piPeers, peer.token)
		}
		inst := p.plugins[peer.pluginUUID]
		var plugPeer *wsPeer
		if inst != nil {
			plugPeer = inst.ws
		}
		ctxID := peer.piCtxID
		actUUID := peer.actionUUID
		p.mu.Unlock()

		if plugPeer != nil {
			_ = plugPeer.sendJSON(map[string]any{
				"action":  actUUID,
				"event":   "propertyInspectorDidDisappear",
				"context": ctxID,
				"device":  virtualDeviceID,
			})
		}
	}
}

func (p *Provider) dispatchSDKInbound(peer *wsPeer, msg sdkInboundMessage) {
	switch msg.Event {
	case "registerPlugin":
		p.onRegisterPlugin(peer, msg.UUID)
	case "registerPropertyInspector":
		p.onRegisterPI(peer, msg.UUID)
	case "getSettings":
		p.onGetSettings(peer, msg.Context)
	case "setSettings":
		p.onSetSettings(peer, msg.Context, msg.Payload)
	case "getGlobalSettings":
		p.onGetGlobalSettings(peer)
	case "setGlobalSettings":
		p.onSetGlobalSettings(peer, msg.Payload)
	case "setTitle":
		p.onSetTitle(peer, msg.Context, msg.Payload)
	case "setImage":
		p.onSetImage(peer, msg.Context, msg.Payload)
	case "setState":
		p.onSetState(msg.Context, msg.Payload)
	case "setFeedback":
		p.onSetFeedback(msg.Context, msg.Payload)
	case "showAlert":
		p.onFlash(msg.Context, "alert")
	case "showOk":
		p.onFlash(msg.Context, "ok")
	case "sendToPlugin":
		p.onForwardToPlugin(peer, msg.Action, msg.Context, msg.Payload)
	case "sendToPropertyInspector":
		p.onForwardToPI(peer, msg.Action, msg.Context, msg.Payload)
	case "openUrl":
		p.onOpenURL(msg.Payload)
	case "logMessage":
		p.onLogMessage(peer, msg.Payload)
	case "switchToProfile":
		p.onSwitchProfile(msg.Payload)
	}
}

func (p *Provider) onRegisterPlugin(peer *wsPeer, regUUID string) {
	tok := strings.TrimSpace(regUUID)

	p.mu.Lock()
	plugUUID, ok := p.pluginTokens[tok]
	if !ok {
		if _, direct := p.plugins[tok]; direct {
			plugUUID = tok
			ok = true
		}
	}
	if !ok {
		p.mu.Unlock()
		return
	}
	inst := p.plugins[plugUUID]
	if inst == nil {
		p.mu.Unlock()
		return
	}
	peer.kind = "plugin"
	peer.pluginUUID = plugUUID
	peer.token = tok
	inst.ws = peer
	inst.state = StatusRunning
	inst.errMsg = ""

	sdkVer := inst.bundle.Manifest.SDKVersion
	host := p.host
	var replay []plugin.ActionEvent
	for _, ev := range p.activeSlots {
		if p.actionToPlugin[ev.ActionUUID] == plugUUID {
			replay = append(replay, cloneActionEvent(ev))
		}
	}
	p.mu.Unlock()

	var gs map[string]any
	if host != nil {
		gs = host.GetGlobalSettings(plugUUID)
	}
	_ = peer.sendJSON(map[string]any{
		"event":   "didReceiveGlobalSettings",
		"payload": map[string]any{"settings": nonNilMap(gs)},
	})
	for _, ev := range replay {
		_ = sendSDKActionEvents(peer, ev, sdkVer)
	}
	p.emitStatusChange()
}

func (p *Provider) onRegisterPI(peer *wsPeer, regUUID string) {
	tok := strings.TrimSpace(regUUID)

	p.mu.Lock()
	sess := p.piSessions[tok]
	if sess == nil {
		p.mu.Unlock()
		return
	}
	peer.kind = "pi"
	peer.pluginUUID = sess.pluginUUID
	peer.token = tok
	peer.piCtxID = sess.ctxID
	peer.actionUUID = sess.actionUUID
	p.piPeers[tok] = peer

	inst := p.plugins[sess.pluginUUID]
	var plugPeer *wsPeer
	if inst != nil {
		plugPeer = inst.ws
	}
	actUUID := sess.actionUUID
	ctxID := sess.ctxID
	p.mu.Unlock()

	if plugPeer != nil {
		_ = plugPeer.sendJSON(map[string]any{
			"action":  actUUID,
			"event":   "propertyInspectorDidAppear",
			"context": ctxID,
			"device":  virtualDeviceID,
		})
	}
}

func (p *Provider) onGetSettings(peer *wsPeer, ctxID string) {
	if ctxID == "" {
		return
	}
	p.mu.RLock()
	ev, hasSlot := p.activeSlots[ctxID]
	var sess *piSessionInfo
	if !hasSlot && peer.kind == "pi" {
		sess = p.piSessions[peer.token]
	}
	p.mu.RUnlock()

	var (
		actUUID string
		coords  map[string]int
		ctrl    string
		setMap  map[string]any
	)
	switch {
	case hasSlot:
		actUUID = ev.ActionUUID
		ctrl = controllerLabel(ev.Controller)
		coords = slotCoordsPayload(ev.Controller, ev.Coordinates, ev.DialIndex)
		setMap = nonNilMap(ev.Settings)
	case sess != nil:
		actUUID = sess.actionUUID
		ctrl = controllerLabel(sess.controller)
		coords = slotCoordsPayload(sess.controller, sess.coords, sess.dial)
		setMap = nonNilMap(sess.settings)
	default:
		return
	}

	_ = peer.sendJSON(map[string]any{
		"action":  actUUID,
		"event":   "didReceiveSettings",
		"context": ctxID,
		"device":  virtualDeviceID,
		"payload": map[string]any{
			"settings":        setMap,
			"coordinates":     coords,
			"isInMultiAction": false,
			"controller":      ctrl,
		},
	})
}

func (p *Provider) onSetSettings(sender *wsPeer, ctxID string, raw json.RawMessage) {
	if ctxID == "" {
		return
	}
	settings := parseJSONObject(raw)

	p.mu.Lock()
	ev, hasSlot := p.activeSlots[ctxID]
	if hasSlot {
		ev.Settings = maps.Clone(settings)
		p.activeSlots[ctxID] = ev
	}
	for _, s := range p.piSessions {
		if s.ctxID == ctxID {
			s.settings = maps.Clone(settings)
		}
	}
	host := p.host
	plugUUID := sender.pluginUUID
	if hasSlot {
		if owner, ok := p.actionToPlugin[ev.ActionUUID]; ok {
			plugUUID = owner
		}
	}
	var plugPeer *wsPeer
	if inst := p.plugins[plugUUID]; inst != nil {
		plugPeer = inst.ws
	}
	var openPIs []*wsPeer
	for _, pi := range p.piPeers {
		if pi.piCtxID == ctxID {
			openPIs = append(openPIs, pi)
		}
	}
	p.mu.Unlock()

	if host != nil {
		_ = host.SaveSettings(ctxID, settings)
	}

	msg := map[string]any{
		"action":  ev.ActionUUID,
		"event":   "didReceiveSettings",
		"context": ctxID,
		"device":  virtualDeviceID,
		"payload": map[string]any{
			"settings":        nonNilMap(settings),
			"coordinates":     slotCoordsPayload(ev.Controller, ev.Coordinates, ev.DialIndex),
			"isInMultiAction": false,
			"controller":      controllerLabel(ev.Controller),
		},
	}
	if sender.kind != "plugin" && plugPeer != nil {
		_ = plugPeer.sendJSON(msg)
	}
	for _, pi := range openPIs {
		if pi != sender {
			_ = pi.sendJSON(msg)
		}
	}
}

func (p *Provider) onGetGlobalSettings(peer *wsPeer) {
	if peer.pluginUUID == "" {
		return
	}
	p.mu.RLock()
	host := p.host
	p.mu.RUnlock()

	var gs map[string]any
	if host != nil {
		gs = host.GetGlobalSettings(peer.pluginUUID)
	}
	_ = peer.sendJSON(map[string]any{
		"event":   "didReceiveGlobalSettings",
		"payload": map[string]any{"settings": nonNilMap(gs)},
	})
}

func (p *Provider) onSetGlobalSettings(sender *wsPeer, raw json.RawMessage) {
	if sender.pluginUUID == "" {
		return
	}
	settings := parseJSONObject(raw)

	p.mu.RLock()
	host := p.host
	inst := p.plugins[sender.pluginUUID]
	var plugPeer *wsPeer
	if inst != nil {
		plugPeer = inst.ws
	}
	var pis []*wsPeer
	for _, pi := range p.piPeers {
		if pi.pluginUUID == sender.pluginUUID {
			pis = append(pis, pi)
		}
	}
	p.mu.RUnlock()

	if host != nil {
		_ = host.SaveGlobalSettings(sender.pluginUUID, settings)
	}

	msg := map[string]any{
		"event":   "didReceiveGlobalSettings",
		"payload": map[string]any{"settings": nonNilMap(settings)},
	}
	if plugPeer != nil && plugPeer != sender {
		_ = plugPeer.sendJSON(msg)
	}
	for _, pi := range pis {
		if pi != sender {
			_ = pi.sendJSON(msg)
		}
	}
}

func (p *Provider) onSetTitle(peer *wsPeer, ctxID string, raw json.RawMessage) {
	if ctxID == "" {
		return
	}
	var payload struct {
		Title string `json:"title"`
		State *int   `json:"state,omitempty"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return
	}

	p.mu.RLock()
	host := p.host
	ev, hasSlot := p.activeSlots[ctxID]
	p.mu.RUnlock()
	if host == nil {
		return
	}
	if payload.State != nil && hasSlot && *payload.State != ev.State {
		return
	}
	_ = peer
	host.UpdateVisual(ctxID, plugin.VisualPatch{
		Title: new(payload.Title),
	})
}

func (p *Provider) onSetImage(peer *wsPeer, ctxID string, raw json.RawMessage) {
	if ctxID == "" {
		return
	}
	var payload struct {
		Image string `json:"image"`
		State *int   `json:"state,omitempty"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return
	}

	p.mu.RLock()
	host := p.host
	inst := p.plugins[peer.pluginUUID]
	ev, hasSlot := p.activeSlots[ctxID]
	p.mu.RUnlock()
	if host == nil {
		return
	}
	if payload.State != nil && hasSlot && *payload.State != ev.State {
		return
	}

	imgURI := strings.TrimSpace(payload.Image)
	if imgURI != "" && !strings.HasPrefix(strings.ToLower(imgURI), "data:") && inst != nil {
		if absImg, safe := ResolveAssetPath(inst.bundle.Dir, imgURI); safe {
			imgURI = absImg
		} else {
			imgURI = ""
		}
	}
	host.UpdateVisual(ctxID, plugin.VisualPatch{
		ImageURI: new(imgURI),
	})
}

func (p *Provider) onSetState(ctxID string, raw json.RawMessage) {
	if ctxID == "" {
		return
	}
	var payload struct {
		State int `json:"state"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return
	}

	p.mu.Lock()
	if cur, ok := p.activeSlots[ctxID]; ok {
		cur.State = payload.State
		p.activeSlots[ctxID] = cur
	}
	host := p.host
	p.mu.Unlock()
	if host != nil {
		host.UpdateVisual(ctxID, plugin.VisualPatch{
			State: new(payload.State),
		})
	}
}

func (p *Provider) onSetFeedback(ctxID string, raw json.RawMessage) {
	if ctxID == "" {
		return
	}
	fb := parseJSONObject(raw)
	if len(fb) == 0 {
		return
	}

	p.mu.RLock()
	host := p.host
	p.mu.RUnlock()
	if host == nil {
		return
	}

	patch := plugin.VisualPatch{Feedback: fb}
	if valStr, ok := fb["value"].(string); ok {
		patch.Subtitle = new(valStr)
	}
	host.UpdateVisual(ctxID, patch)
}

func (p *Provider) onFlash(ctxID, flash string) {
	if ctxID == "" {
		return
	}
	p.mu.RLock()
	host := p.host
	p.mu.RUnlock()
	if host != nil {
		host.UpdateVisual(ctxID, plugin.VisualPatch{Flash: flash})
	}
}

func (p *Provider) onForwardToPlugin(sender *wsPeer, actionUUID, ctxID string, payload json.RawMessage) {
	if sender.pluginUUID == "" {
		return
	}
	p.mu.RLock()
	inst := p.plugins[sender.pluginUUID]
	var plugPeer *wsPeer
	if inst != nil {
		plugPeer = inst.ws
	}
	p.mu.RUnlock()
	if plugPeer == nil {
		return
	}
	_ = plugPeer.sendJSON(map[string]any{
		"action":  actionUUID,
		"event":   "sendToPlugin",
		"context": ctxID,
		"payload": jsonRawOrEmptyObj(payload),
	})
}

func (p *Provider) onForwardToPI(sender *wsPeer, actionUUID, ctxID string, payload json.RawMessage) {
	p.mu.RLock()
	var targets []*wsPeer
	for _, pi := range p.piPeers {
		if pi.pluginUUID == sender.pluginUUID && (ctxID == "" || pi.piCtxID == ctxID) {
			targets = append(targets, pi)
		}
	}
	p.mu.RUnlock()

	msg := map[string]any{
		"action":  actionUUID,
		"event":   "sendToPropertyInspector",
		"context": ctxID,
		"payload": jsonRawOrEmptyObj(payload),
	}
	for _, pi := range targets {
		_ = pi.sendJSON(msg)
	}
}

func (p *Provider) onOpenURL(raw json.RawMessage) {
	var payload struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return
	}
	target := strings.TrimSpace(payload.URL)
	u, err := url.Parse(target)
	if err != nil || (!strings.EqualFold(u.Scheme, "http") && !strings.EqualFold(u.Scheme, "https")) {
		return
	}
	if p.openURL != nil {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = p.openURL(ctx, target)
		}()
	}
}

func (p *Provider) onLogMessage(peer *wsPeer, raw json.RawMessage) {
	var payload struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil || payload.Message == "" {
		return
	}
	p.mu.RLock()
	host := p.host
	p.mu.RUnlock()
	if host != nil {
		host.Log(peer.pluginUUID, payload.Message)
	}
}

func (p *Provider) onSwitchProfile(raw json.RawMessage) {
	var payload struct {
		Profile string `json:"profile"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil || strings.TrimSpace(payload.Profile) == "" {
		return
	}
	p.mu.RLock()
	host := p.host
	p.mu.RUnlock()
	if host != nil && host.Deck() != nil {
		_ = host.Deck().SwitchProfile(payload.Profile)
	}
}

func (p *Provider) emitStatusChange() {
	if p.opts.OnStatusChange != nil {
		p.opts.OnStatusChange()
	}
}

func buildInfoObject(pluginUUID, version string) map[string]any {
	platform := runtime.GOOS
	if platform == "darwin" {
		platform = "mac"
	}
	return map[string]any{
		"application": map[string]any{
			"font":            "Segoe UI",
			"language":        "en",
			"platform":        platform,
			"platformVersion": "10",
			"version":         "6.5.0",
		},
		"plugin": map[string]any{
			"uuid":    pluginUUID,
			"version": version,
		},
		"devicePixelRatio": 2,
		"colors": map[string]any{
			"buttonPressedBackgroundColor": "#1E293B",
			"buttonPressedBorderColor":     "#38BDF8",
			"buttonPressedTextColor":       "#FFFFFF",
			"highlightColor":               "#38BDF8",
		},
		"devices": []map[string]any{
			{
				"id":   virtualDeviceID,
				"name": "PoundControl Studio",
				"size": map[string]int{"columns": 8, "rows": 4},
				"type": 7,
			},
		},
	}
}

func controllerLabel(ctrl deck.ControllerType) string {
	if ctrl == deck.ControllerEncoder {
		return "Encoder"
	}
	return "Keypad"
}

func slotCoordsPayload(ctrl deck.ControllerType, c deck.Coordinates, dial int) map[string]int {
	if ctrl == deck.ControllerEncoder {
		return map[string]int{"column": dial, "row": 0}
	}
	return map[string]int{"column": c.Col, "row": c.Row}
}

func parseJSONObject(raw json.RawMessage) map[string]any {
	if len(raw) == 0 {
		return make(map[string]any)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil || m == nil {
		return make(map[string]any)
	}
	return m
}

func jsonRawOrEmptyObj(raw json.RawMessage) json.RawMessage {
	if len(bytes.TrimSpace(raw)) == 0 {
		return json.RawMessage(`{}`)
	}
	return raw
}

func nonNilMap(m map[string]any) map[string]any {
	if m == nil {
		return make(map[string]any)
	}
	return m
}

func cloneActionEvent(ev plugin.ActionEvent) plugin.ActionEvent {
	cp := ev
	cp.Settings = maps.Clone(ev.Settings)
	return cp
}
