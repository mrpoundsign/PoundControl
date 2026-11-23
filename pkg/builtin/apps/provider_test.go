package apps

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/websocket"

	"github.com/mrpoundsign/PoundControl/pkg/deck"
	"github.com/mrpoundsign/PoundControl/pkg/plugin"
)

type mockDriver struct {
	mu      sync.Mutex
	execs   []ExecRequest
	urls    []string
	chords  []Chord
	texts   []string
	medias  []MediaCommand
}

func (m *mockDriver) Exec(_ context.Context, req ExecRequest) error {
	m.mu.Lock()
	m.execs = append(m.execs, req)
	m.mu.Unlock()
	return nil
}

func (m *mockDriver) OpenURL(_ context.Context, target string) error {
	m.mu.Lock()
	m.urls = append(m.urls, target)
	m.mu.Unlock()
	return nil
}

func (m *mockDriver) SendHotkey(_ context.Context, ch Chord) error {
	m.mu.Lock()
	m.chords = append(m.chords, ch)
	m.mu.Unlock()
	return nil
}

func (m *mockDriver) TypeText(_ context.Context, text string) error {
	m.mu.Lock()
	m.texts = append(m.texts, text)
	m.mu.Unlock()
	return nil
}

func (m *mockDriver) SendMedia(_ context.Context, cmd MediaCommand) error {
	m.mu.Lock()
	m.medias = append(m.medias, cmd)
	m.mu.Unlock()
	return nil
}

type mockHost struct {
	mu      sync.Mutex
	patches []plugin.VisualPatch
	global  map[string]map[string]any
}

func (h *mockHost) UpdateVisual(_ string, patch plugin.VisualPatch) {
	h.mu.Lock()
	h.patches = append(h.patches, patch)
	h.mu.Unlock()
}

func (h *mockHost) SaveSettings(string, map[string]any) error { return nil }
func (h *mockHost) GetGlobalSettings(pid string) map[string]any {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.global == nil {
		return nil
	}
	return h.global[pid]
}
func (h *mockHost) SaveGlobalSettings(string, map[string]any) error { return nil }
func (h *mockHost) Log(string, string)                              {}
func (h *mockHost) Deck() plugin.DeckControl                        { return nil }

func TestSplitArgsAndParseChord(t *testing.T) {
	args := SplitArgs(`--flag "C:\Program Files\OBS Studio\obs64.exe" 'two words' plain`)
	want := []string{"--flag", `C:\Program Files\OBS Studio\obs64.exe`, "two words", "plain"}
	if !slices.Equal(args, want) {
		t.Fatalf("SplitArgs() = %#v, want %#v", args, want)
	}

	ch, err := ParseChord("ctrl+alt+f13")
	if err != nil {
		t.Fatalf("ParseChord error = %v", err)
	}
	if ch.String() != "Ctrl+Alt+F13" || ch.WinVK != 0x7C || ch.X11Key != "F13" {
		t.Fatalf("unexpected ParseChord result: %+v", ch)
	}
	if _, err := ParseChord("ctrl+"); err == nil {
		t.Error("expected error for trailing + without key")
	}
	if _, err := ParseChord("ctrl+nope"); err == nil {
		t.Error("expected error for unknown key")
	}
}

func TestProviderOSActionsAndNetwork(t *testing.T) {
	drv := &mockDriver{}
	host := &mockHost{}
	prov := New(drv)
	prov.BindHost(host)
	defer func() { _ = prov.Close() }()

	for _, m := range prov.Manifests() {
		if err := m.Validate(); err != nil {
			t.Fatalf("manifest invalid: %v", err)
		}
	}

	ctx := context.Background()

	// Exec, OpenURL, Hotkey, TypeText, Media Keypad + Dial.
	if err := prov.Dispatch(ctx, plugin.ActionEvent{
		Type: plugin.EventKeyDown, Context: "c1", ActionUUID: ActionExec,
		Settings: map[string]any{"command": "notepad.exe", "args": `"my file.txt" --ro`, "detached": true},
	}); err != nil {
		t.Fatalf("ActionExec error = %v", err)
	}
	if len(drv.execs) != 1 || !slices.Equal(drv.execs[0].Args, []string{"my file.txt", "--ro"}) {
		t.Fatalf("unexpected exec request: %+v", drv.execs)
	}

	if err := prov.Dispatch(ctx, plugin.ActionEvent{
		Type: plugin.EventKeyDown, Context: "c2", ActionUUID: ActionOpenURL,
		Settings: map[string]any{"url": "https://example.com"},
	}); err != nil {
		t.Fatalf("ActionOpenURL error = %v", err)
	}
	if len(drv.urls) != 1 || drv.urls[0] != "https://example.com" {
		t.Fatalf("unexpected openURL calls: %+v", drv.urls)
	}

	if err := prov.Dispatch(ctx, plugin.ActionEvent{
		Type: plugin.EventKeyDown, Context: "c3", ActionUUID: ActionHotkey,
		Settings: map[string]any{"chord": "Ctrl+Shift+M"},
	}); err != nil {
		t.Fatalf("ActionHotkey error = %v", err)
	}
	if len(drv.chords) != 1 || drv.chords[0].String() != "Ctrl+Shift+M" {
		t.Fatalf("unexpected hotkey calls: %+v", drv.chords)
	}

	if err := prov.Dispatch(ctx, plugin.ActionEvent{
		Type: plugin.EventKeyDown, Context: "c4", ActionUUID: ActionTypeText,
		Settings: map[string]any{"text": "Hello stream!"},
	}); err != nil {
		t.Fatalf("ActionTypeText error = %v", err)
	}
	if len(drv.texts) != 1 || drv.texts[0] != "Hello stream!" {
		t.Fatalf("unexpected typeText calls: %+v", drv.texts)
	}

	if err := prov.Dispatch(ctx, plugin.ActionEvent{
		Type: plugin.EventDialRotate, Context: "c5", ActionUUID: ActionMedia,
		Controller: deck.ControllerEncoder, Ticks: 2,
	}); err != nil {
		t.Fatalf("ActionMedia DialRotate error = %v", err)
	}
	if err := prov.Dispatch(ctx, plugin.ActionEvent{
		Type: plugin.EventDialPress, Context: "c5", ActionUUID: ActionMedia,
		Controller: deck.ControllerEncoder,
	}); err != nil {
		t.Fatalf("ActionMedia DialPress error = %v", err)
	}
	wantMedia := []MediaCommand{MediaVolumeUp, MediaVolumeUp, MediaVolumeMute}
	if !slices.Equal(drv.medias, wantMedia) {
		t.Fatalf("media calls = %v, want %v", drv.medias, wantMedia)
	}

	// HTTP Webhook test against local httptest server.
	var gotMethod, gotAuth, gotBody string
	httpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/fail" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		gotMethod = r.Method
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer httpSrv.Close()

	if err := prov.Dispatch(ctx, plugin.ActionEvent{
		Type: plugin.EventKeyDown, Context: "http1", ActionUUID: ActionHTTP,
		Settings: map[string]any{
			"method":  "POST",
			"url":     httpSrv.URL + "/ok",
			"headers": `{"Authorization":"Bearer abc"}`,
			"body":    `{"action":"speak"}`,
		},
	}); err != nil {
		t.Fatalf("ActionHTTP success error = %v", err)
	}
	if gotMethod != http.MethodPost || gotAuth != "Bearer abc" || gotBody != `{"action":"speak"}` {
		t.Fatalf("unexpected HTTP request: method=%q auth=%q body=%q", gotMethod, gotAuth, gotBody)
	}
	lastPatch := host.patches[len(host.patches)-1]
	if lastPatch.Subtitle == nil || *lastPatch.Subtitle != "200 OK" || lastPatch.Flash != "ok" {
		t.Fatalf("unexpected HTTP ok patch: %+v", lastPatch)
	}

	if err := prov.Dispatch(ctx, plugin.ActionEvent{
		Type: plugin.EventKeyDown, Context: "http1", ActionUUID: ActionHTTP,
		Settings: map[string]any{"method": "GET", "url": httpSrv.URL + "/fail"},
	}); err == nil {
		t.Fatal("expected HTTP 500 error")
	}

	// WebSocket message test against local httptest WS server.
	wsCh := make(chan string, 1)
	wsSrv := httptest.NewServer(websocket.Handler(func(conn *websocket.Conn) {
		var msg string
		if err := websocket.Message.Receive(conn, &msg); err == nil {
			wsCh <- msg
		}
		_ = conn.Close()
	}))
	defer wsSrv.Close()

	wsURL := "ws" + strings.TrimPrefix(wsSrv.URL, "http")
	if err := prov.Dispatch(ctx, plugin.ActionEvent{
		Type: plugin.EventKeyDown, Context: "ws1", ActionUUID: ActionWebSocket,
		Settings: map[string]any{"url": wsURL, "message": `{"action":"panic"}`},
	}); err != nil {
		t.Fatalf("ActionWebSocket error = %v", err)
	}
	select {
	case gotWS := <-wsCh:
		if gotWS != `{"action":"panic"}` {
			t.Fatalf("received WS payload = %q, want {\"action\":\"panic\"}", gotWS)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for WebSocket message")
	}
}

func TestAppsGlobalSettingsFallbacks(t *testing.T) {
	drv := &mockDriver{}
	prov := New(drv)
	host := &mockHost{global: map[string]map[string]any{
		PluginUUID: {
			"defaultWorkingDir":     "/srv/scripts",
			"defaultHttpTimeoutSec": 2,
		},
	}}
	prov.BindHost(host)

	if len(prov.Manifests()) != 1 || len(prov.Manifests()[0].GlobalSettingsSchema) < 2 {
		t.Fatalf("expected apps manifest to declare GlobalSettingsSchema, got %+v", prov.Manifests())
	}

	ctx := context.Background()
	// Blank workingDir falls back to defaultWorkingDir.
	if err := prov.Dispatch(ctx, plugin.ActionEvent{
		Type:       plugin.EventKeyDown,
		Context:    "exec-default",
		ActionUUID: ActionExec,
		Settings:   map[string]any{"command": "tool"},
	}); err != nil {
		t.Fatalf("ActionExec fallback error = %v", err)
	}
	if len(drv.execs) != 1 || drv.execs[0].WorkingDir != "/srv/scripts" {
		t.Fatalf("expected fallback WorkingDir /srv/scripts, got %+v", drv.execs)
	}

	// Explicit workingDir overrides defaultWorkingDir.
	if err := prov.Dispatch(ctx, plugin.ActionEvent{
		Type:       plugin.EventKeyDown,
		Context:    "exec-override",
		ActionUUID: ActionExec,
		Settings:   map[string]any{"command": "tool", "workingDir": "/custom"},
	}); err != nil {
		t.Fatalf("ActionExec override error = %v", err)
	}
	if len(drv.execs) != 2 || drv.execs[1].WorkingDir != "/custom" {
		t.Fatalf("expected explicit WorkingDir /custom, got %+v", drv.execs)
	}

	// Blank timeoutSec uses defaultHttpTimeoutSec.
	slowSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer slowSrv.Close()

	host.global[PluginUUID]["defaultHttpTimeoutSec"] = 1
	start := time.Now()
	err := prov.Dispatch(ctx, plugin.ActionEvent{
		Type:       plugin.EventKeyDown,
		Context:    "http-timeout",
		ActionUUID: ActionHTTP,
		Settings:   map[string]any{"method": "GET", "url": slowSrv.URL},
	})
	if err == nil {
		t.Fatal("expected HTTP request to time out using defaultHttpTimeoutSec=1")
	}
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Fatalf("expected ~1s timeout from defaultHttpTimeoutSec, took %v", elapsed)
	}
}
