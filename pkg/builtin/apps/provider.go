package apps

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/websocket"

	"github.com/mrpoundsign/PoundDeck/pkg/deck"
	"github.com/mrpoundsign/PoundDeck/pkg/plugin"
)

const (
	// PluginUUID identifies the built-in App & System Control plugin.
	PluginUUID = "com.github.mrpoundsign.pounddeck.apps"

	ActionExec      = PluginUUID + ".exec"
	ActionOpenURL   = PluginUUID + ".open_url"
	ActionHTTP      = PluginUUID + ".http"
	ActionWebSocket = PluginUUID + ".websocket"
	ActionHotkey    = PluginUUID + ".hotkey"
	ActionTypeText  = PluginUUID + ".type_text"
	ActionMedia     = PluginUUID + ".media"
)

// Provider implements plugin.Provider for launching apps, URLs, webhooks, hotkeys, text macros, and media keys.
type Provider struct {
	mu       sync.RWMutex
	host     plugin.HostAPI
	driver   OSDriver
	httpCli  *http.Client
	manifest plugin.Manifest
}

// New creates a built-in apps plugin provider using driver (or DefaultDriver() if nil).
func New(driver OSDriver) *Provider {
	if driver == nil {
		driver = DefaultDriver()
	}
	return &Provider{
		driver:   driver,
		httpCli:  &http.Client{},
		manifest: buildManifest(),
	}
}

func buildManifest() plugin.Manifest {
	actions := []plugin.ActionDescriptor{
		{
			UUID:         ActionExec,
			PluginUUID:   PluginUUID,
			Name:         "Launch App / Script",
			Category:     "App & System Control",
			Tooltip:      "Run an executable, batch file, or shell script",
			DefaultTitle: "Run App",
			DefaultColor: "#38BDF8",
			Controllers:  []deck.ControllerType{deck.ControllerKeypad},
			Provider:     plugin.ProviderBuiltin,
			SettingsSchema: []plugin.SettingField{
				{
					Key:         "command",
					Label:       "Executable / Script",
					Type:        plugin.FieldFile,
					Placeholder: `C:\Program Files\obs-studio\bin\64bit\obs64.exe`,
					Required:    true,
				},
				{
					Key:         "args",
					Label:       "Arguments",
					Type:        plugin.FieldText,
					Placeholder: `--startreplaybuffer`,
				},
				{
					Key:         "workingDir",
					Label:       "Working Directory",
					Type:        plugin.FieldText,
					Placeholder: "Optional working directory",
				},
				{
					Key:     "detached",
					Label:   "Launch Detached (do not wait for exit)",
					Type:    plugin.FieldBool,
					Default: true,
				},
				{
					Key:     "hideWindow",
					Label:   "Hide Console Window (Windows)",
					Type:    plugin.FieldBool,
					Default: true,
				},
			},
		},
		{
			UUID:         ActionOpenURL,
			PluginUUID:   PluginUUID,
			Name:         "Open URL / File",
			Category:     "App & System Control",
			Tooltip:      "Open a website URL or local document with the default OS handler",
			DefaultTitle: "Open URL",
			DefaultColor: "#38BDF8",
			Controllers:  []deck.ControllerType{deck.ControllerKeypad},
			Provider:     plugin.ProviderBuiltin,
			SettingsSchema: []plugin.SettingField{
				{
					Key:         "url",
					Label:       "URL or Path",
					Type:        plugin.FieldText,
					Placeholder: "https://dashboard.twitch.tv",
					Required:    true,
				},
			},
		},
		{
			UUID:         ActionHTTP,
			PluginUUID:   PluginUUID,
			Name:         "HTTP Request",
			Category:     "App & System Control",
			Tooltip:      "Call a local or remote REST endpoint / webhook",
			DefaultTitle: "HTTP",
			DefaultColor: "#22C55E",
			Controllers:  []deck.ControllerType{deck.ControllerKeypad},
			Provider:     plugin.ProviderBuiltin,
			SettingsSchema: []plugin.SettingField{
				{
					Key:     "method",
					Label:   "HTTP Method",
					Type:    plugin.FieldSelect,
					Default: http.MethodPost,
					Options: []plugin.SelectOption{
						{Label: "POST", Value: http.MethodPost},
						{Label: "GET", Value: http.MethodGet},
						{Label: "PUT", Value: http.MethodPut},
						{Label: "PATCH", Value: http.MethodPatch},
						{Label: "DELETE", Value: http.MethodDelete},
					},
				},
				{
					Key:         "url",
					Label:       "Endpoint URL",
					Type:        plugin.FieldText,
					Placeholder: "http://127.0.0.1:8765/api/panic",
					Required:    true,
				},
				{
					Key:         "headers",
					Label:       "Headers (JSON object)",
					Type:        plugin.FieldMultiline,
					Default:     "{}",
					Placeholder: `{"Authorization":"Bearer token","Content-Type":"application/json"}`,
				},
				{
					Key:         "body",
					Label:       "Request Body",
					Type:        plugin.FieldMultiline,
					Placeholder: `{"action":"toggle"}`,
				},
				{
					Key:         "timeoutSec",
					Label:       "Timeout (seconds)",
					Type:        plugin.FieldNumber,
					Default:     10,
					Placeholder: "10",
				},
			},
		},
		{
			UUID:         ActionWebSocket,
			PluginUUID:   PluginUUID,
			Name:         "WebSocket Message",
			Category:     "App & System Control",
			Tooltip:      "Connect to a WebSocket endpoint, send a message, and close",
			DefaultTitle: "WS Send",
			DefaultColor: "#A855F7",
			Controllers:  []deck.ControllerType{deck.ControllerKeypad},
			Provider:     plugin.ProviderBuiltin,
			SettingsSchema: []plugin.SettingField{
				{
					Key:         "url",
					Label:       "WebSocket URL",
					Type:        plugin.FieldText,
					Placeholder: "ws://127.0.0.1:8765/ws",
					Required:    true,
				},
				{
					Key:         "message",
					Label:       "Payload",
					Type:        plugin.FieldMultiline,
					Placeholder: `{"action":"cycle_tts_mode"}`,
					Required:    true,
				},
				{
					Key:         "timeoutSec",
					Label:       "Timeout (seconds)",
					Type:        plugin.FieldNumber,
					Default:     5,
					Placeholder: "5",
				},
			},
		},
		{
			UUID:         ActionHotkey,
			PluginUUID:   PluginUUID,
			Name:         "Send Hotkey",
			Category:     "App & System Control",
			Tooltip:      "Synthesize a keyboard shortcut chord to the active window",
			DefaultTitle: "Hotkey",
			DefaultColor: "#FBBF24",
			Controllers:  []deck.ControllerType{deck.ControllerKeypad},
			Provider:     plugin.ProviderBuiltin,
			SettingsSchema: []plugin.SettingField{
				{
					Key:         "chord",
					Label:       "Shortcut Chord",
					Type:        plugin.FieldHotkey,
					Placeholder: "Ctrl+Shift+M",
					Required:    true,
					Help:        "Examples: Ctrl+Shift+M, Alt+F9, F13, Ctrl+Alt+Space",
				},
			},
		},
		{
			UUID:         ActionTypeText,
			PluginUUID:   PluginUUID,
			Name:         "Type Text Macro",
			Category:     "App & System Control",
			Tooltip:      "Type a canned text snippet into the focused application",
			DefaultTitle: "Text",
			DefaultColor: "#FBBF24",
			Controllers:  []deck.ControllerType{deck.ControllerKeypad},
			Provider:     plugin.ProviderBuiltin,
			SettingsSchema: []plugin.SettingField{
				{
					Key:         "text",
					Label:       "Text Snippet",
					Type:        plugin.FieldMultiline,
					Placeholder: "gg everyone!",
					Required:    true,
				},
			},
		},
		{
			UUID:         ActionMedia,
			PluginUUID:   PluginUUID,
			Name:         "Media & System Volume",
			Category:     "App & System Control",
			Tooltip:      "Send OS media keys or adjust system volume via keypad or dial",
			DefaultTitle: "Media",
			DefaultColor: "#38BDF8",
			Controllers:  []deck.ControllerType{deck.ControllerKeypad, deck.ControllerEncoder},
			Provider:     plugin.ProviderBuiltin,
			SettingsSchema: []plugin.SettingField{
				{
					Key:     "command",
					Label:   "Keypad Command",
					Type:    plugin.FieldSelect,
					Default: string(MediaPlayPause),
					Options: []plugin.SelectOption{
						{Label: "Play / Pause", Value: string(MediaPlayPause)},
						{Label: "Next Track", Value: string(MediaNext)},
						{Label: "Previous Track", Value: string(MediaPrev)},
						{Label: "Stop", Value: string(MediaStop)},
						{Label: "Volume Up", Value: string(MediaVolumeUp)},
						{Label: "Volume Down", Value: string(MediaVolumeDown)},
						{Label: "Volume Mute", Value: string(MediaVolumeMute)},
					},
				},
				{
					Key:     "pushAction",
					Label:   "Dial Push Command",
					Type:    plugin.FieldSelect,
					Default: string(MediaVolumeMute),
					Options: []plugin.SelectOption{
						{Label: "Volume Mute", Value: string(MediaVolumeMute)},
						{Label: "Play / Pause", Value: string(MediaPlayPause)},
					},
				},
			},
		},
	}

	return plugin.Manifest{
		UUID:        PluginUUID,
		Name:        "App & System Control",
		Version:     "0.1.0",
		Author:      "PoundDeck",
		Description: "Launch apps/scripts, open URLs, call HTTP/WebSocket webhooks, send hotkeys/text, and control media.",
		Provider:    plugin.ProviderBuiltin,
		Actions:     actions,
	}
}

// Kind returns plugin.ProviderBuiltin.
func (p *Provider) Kind() plugin.ProviderKind {
	return plugin.ProviderBuiltin
}

// Name returns the display name of the apps plugin.
func (p *Provider) Name() string {
	return p.manifest.Name
}

// Manifests returns the apps plugin manifest.
func (p *Provider) Manifests() []plugin.Manifest {
	return []plugin.Manifest{p.manifest}
}

// Actions returns all app & system control action descriptors.
func (p *Provider) Actions() []plugin.ActionDescriptor {
	return slices.Clone(p.manifest.Actions)
}

// BindHost sets the HostAPI used for visual updates and logging.
func (p *Provider) BindHost(host plugin.HostAPI) {
	p.mu.Lock()
	p.host = host
	p.mu.Unlock()
}

// Close releases references held by the provider.
func (p *Provider) Close() error {
	p.mu.Lock()
	p.host = nil
	p.mu.Unlock()
	return nil
}

// Dispatch routes lifecycle and trigger events for apps actions.
func (p *Provider) Dispatch(ctx context.Context, ev plugin.ActionEvent) error {
	if ev.Type == plugin.EventWillAppear || ev.Type == plugin.EventSettingsChanged {
		p.updateInitialSubtitle(ev)
		return nil
	}
	if ev.Type == plugin.EventWillDisappear || ev.Type == plugin.EventKeyUp {
		return nil
	}

	var err error
	switch ev.ActionUUID {
	case ActionExec:
		err = p.execApp(ctx, ev)
	case ActionOpenURL:
		err = p.openURL(ctx, ev)
	case ActionHTTP:
		return p.execHTTP(ctx, ev)
	case ActionWebSocket:
		err = p.execWebSocket(ctx, ev)
	case ActionHotkey:
		err = p.sendHotkey(ctx, ev)
	case ActionTypeText:
		err = p.typeText(ctx, ev)
	case ActionMedia:
		err = p.execMedia(ctx, ev)
	default:
		return fmt.Errorf("apps provider: unsupported action %q", ev.ActionUUID)
	}

	if err != nil {
		p.flash(ev.Context, "alert")
		return err
	}
	p.flash(ev.Context, "ok")
	return nil
}

func (p *Provider) updateInitialSubtitle(ev plugin.ActionEvent) {
	var sub string
	switch ev.ActionUUID {
	case ActionHTTP:
		sub = stringSetting(ev.Settings, "method", http.MethodPost)
	case ActionHotkey:
		sub = stringSetting(ev.Settings, "chord", "")
	case ActionMedia:
		sub = stringSetting(ev.Settings, "command", string(MediaPlayPause))
	}
	if sub == "" {
		return
	}
	p.mu.RLock()
	host := p.host
	p.mu.RUnlock()
	if host != nil {
		host.UpdateVisual(ev.Context, plugin.VisualPatch{
			Subtitle: new(sub),
		})
	}
}

func (p *Provider) flash(ctxID, kind string) {
	p.mu.RLock()
	host := p.host
	p.mu.RUnlock()
	if host != nil {
		host.UpdateVisual(ctxID, plugin.VisualPatch{Flash: kind})
	}
}

func (p *Provider) execApp(ctx context.Context, ev plugin.ActionEvent) error {
	cmd := stringSetting(ev.Settings, "command", "")
	if cmd == "" {
		return errors.New("launch app requires a command path")
	}
	req := ExecRequest{
		Command:    cmd,
		Args:       SplitArgs(stringSetting(ev.Settings, "args", "")),
		WorkingDir: stringSetting(ev.Settings, "workingDir", ""),
		Detached:   boolSetting(ev.Settings, "detached", true),
		HideWindow: boolSetting(ev.Settings, "hideWindow", true),
	}
	return p.driver.Exec(ctx, req)
}

func (p *Provider) openURL(ctx context.Context, ev plugin.ActionEvent) error {
	target := stringSetting(ev.Settings, "url", "")
	if target == "" {
		return errors.New("open URL requires a target URL or path")
	}
	return p.driver.OpenURL(ctx, target)
}

func (p *Provider) execHTTP(ctx context.Context, ev plugin.ActionEvent) error {
	rawURL := stringSetting(ev.Settings, "url", "")
	if rawURL == "" {
		p.flash(ev.Context, "alert")
		return errors.New("HTTP request requires a URL")
	}
	method := strings.ToUpper(stringSetting(ev.Settings, "method", http.MethodPost))
	timeoutSec := max(1, intSetting(ev.Settings, "timeoutSec", 10))
	reqCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()

	body := stringSettingRaw(ev.Settings, "body")
	var bodyReader io.Reader
	if body != "" {
		bodyReader = bytes.NewBufferString(body)
	}

	req, err := http.NewRequestWithContext(reqCtx, method, rawURL, bodyReader)
	if err != nil {
		p.flash(ev.Context, "alert")
		return fmt.Errorf("build HTTP request: %w", err)
	}

	rawHeaders := stringSetting(ev.Settings, "headers", "")
	if rawHeaders != "" && rawHeaders != "{}" {
		var hdrs map[string]string
		if err := json.Unmarshal([]byte(rawHeaders), &hdrs); err != nil {
			p.flash(ev.Context, "alert")
			return fmt.Errorf("invalid HTTP headers JSON: %w", err)
		}
		for k, v := range hdrs {
			req.Header.Set(k, v)
		}
	}

	resp, err := p.httpCli.Do(req)
	if err != nil {
		p.patchHTTPStatus(ev.Context, "ERR", "#EF4444", "alert")
		return fmt.Errorf("HTTP request failed: %w", err)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
	_ = resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		p.patchHTTPStatus(ev.Context, fmt.Sprintf("%d OK", resp.StatusCode), "#22C55E", "ok")
		return nil
	}

	p.patchHTTPStatus(ev.Context, fmt.Sprintf("%d ERR", resp.StatusCode), "#EF4444", "alert")
	return fmt.Errorf("HTTP %s %s returned status %d", method, rawURL, resp.StatusCode)
}

func (p *Provider) patchHTTPStatus(ctxID, subtitle, hexCol, flash string) {
	p.mu.RLock()
	host := p.host
	p.mu.RUnlock()
	if host != nil {
		host.UpdateVisual(ctxID, plugin.VisualPatch{
			Subtitle: new(subtitle),
			Color:    new(hexCol),
			Flash:    flash,
		})
	}
}

func (p *Provider) execWebSocket(ctx context.Context, ev plugin.ActionEvent) error {
	rawURL := stringSetting(ev.Settings, "url", "")
	if rawURL == "" {
		return errors.New("WebSocket message requires a URL")
	}
	msg := stringSettingRaw(ev.Settings, "message")
	if msg == "" {
		return errors.New("WebSocket message payload cannot be empty")
	}

	timeoutSec := max(1, intSetting(ev.Settings, "timeoutSec", 5))
	wsCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()

	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("parse WebSocket URL: %w", err)
	}
	hostAddr := u.Host
	if u.Port() == "" {
		if strings.EqualFold(u.Scheme, "wss") {
			hostAddr = net.JoinHostPort(u.Hostname(), "443")
		} else {
			hostAddr = net.JoinHostPort(u.Hostname(), "80")
		}
	}

	wsCfg, err := websocket.NewConfig(rawURL, "http://localhost")
	if err != nil {
		return err
	}

	var netConn net.Conn
	if strings.EqualFold(u.Scheme, "wss") {
		tlsDialer := &tls.Dialer{NetDialer: &net.Dialer{Timeout: time.Duration(timeoutSec) * time.Second}}
		netConn, err = tlsDialer.DialContext(wsCtx, "tcp", hostAddr)
	} else {
		netConn, err = (&net.Dialer{Timeout: time.Duration(timeoutSec) * time.Second}).DialContext(wsCtx, "tcp", hostAddr)
	}
	if err != nil {
		return fmt.Errorf("dial WebSocket %s: %w", rawURL, err)
	}

	conn, err := websocket.NewClient(wsCfg, netConn)
	if err != nil {
		_ = netConn.Close()
		return fmt.Errorf("WebSocket handshake %s: %w", rawURL, err)
	}
	defer func() { _ = conn.Close() }()

	if dl, ok := wsCtx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	return websocket.Message.Send(conn, msg)
}

func (p *Provider) sendHotkey(ctx context.Context, ev plugin.ActionEvent) error {
	raw := stringSetting(ev.Settings, "chord", "")
	ch, err := ParseChord(raw)
	if err != nil {
		return err
	}
	return p.driver.SendHotkey(ctx, ch)
}

func (p *Provider) typeText(ctx context.Context, ev plugin.ActionEvent) error {
	text := stringSettingRaw(ev.Settings, "text")
	if text == "" {
		return errors.New("type text macro requires non-empty text")
	}
	return p.driver.TypeText(ctx, text)
}

func (p *Provider) execMedia(ctx context.Context, ev plugin.ActionEvent) error {
	if ev.Type == plugin.EventDialRotate {
		if ev.Ticks == 0 {
			return nil
		}
		cmd := MediaVolumeUp
		steps := ev.Ticks
		if steps < 0 {
			cmd = MediaVolumeDown
			steps = -steps
		}
		steps = min(steps, 10)
		for range steps {
			if err := p.driver.SendMedia(ctx, cmd); err != nil {
				return err
			}
		}
		return nil
	}

	if ev.Type == plugin.EventDialPress {
		pushCmd := MediaCommand(stringSetting(ev.Settings, "pushAction", string(MediaVolumeMute)))
		return p.driver.SendMedia(ctx, pushCmd)
	}

	cmd := MediaCommand(stringSetting(ev.Settings, "command", string(MediaPlayPause)))
	return p.driver.SendMedia(ctx, cmd)
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

func stringSettingRaw(settings map[string]any, key string) string {
	if settings == nil {
		return ""
	}
	v, ok := settings[key]
	if !ok || v == nil {
		return ""
	}
	return fmt.Sprint(v)
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
