package sdplugin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/websocket"

	"github.com/mrpoundsign/PoundDeck/pkg/deck"
	"github.com/mrpoundsign/PoundDeck/pkg/plugin"
)

type recordedHost struct {
	mu             sync.Mutex
	visuals        map[string]plugin.VisualPatch
	savedSettings  map[string]map[string]any
	globalSettings map[string]map[string]any
	logs           []string
	switchedProf   string
}

func newRecordedHost() *recordedHost {
	return &recordedHost{
		visuals:        make(map[string]plugin.VisualPatch),
		savedSettings:  make(map[string]map[string]any),
		globalSettings: make(map[string]map[string]any),
	}
}

func (h *recordedHost) UpdateVisual(ctxID string, patch plugin.VisualPatch) {
	h.mu.Lock()
	cur := h.visuals[ctxID]
	if patch.Title != nil {
		cur.Title = patch.Title
	}
	if patch.Subtitle != nil {
		cur.Subtitle = patch.Subtitle
	}
	if patch.Color != nil {
		cur.Color = patch.Color
	}
	if patch.ImageURI != nil {
		cur.ImageURI = patch.ImageURI
	}
	if patch.State != nil {
		cur.State = patch.State
	}
	if patch.Feedback != nil {
		cur.Feedback = patch.Feedback
	}
	if patch.Flash != "" {
		cur.Flash = patch.Flash
	}
	h.visuals[ctxID] = cur
	h.mu.Unlock()
}

func (h *recordedHost) SaveSettings(ctxID string, settings map[string]any) error {
	h.mu.Lock()
	h.savedSettings[ctxID] = settings
	h.mu.Unlock()
	return nil
}

func (h *recordedHost) GetGlobalSettings(pluginUUID string) map[string]any {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.globalSettings[pluginUUID]
}

func (h *recordedHost) SaveGlobalSettings(pluginUUID string, settings map[string]any) error {
	h.mu.Lock()
	h.globalSettings[pluginUUID] = settings
	h.mu.Unlock()
	return nil
}

func (h *recordedHost) Log(pluginUUID, msg string) {
	h.mu.Lock()
	h.logs = append(h.logs, pluginUUID+":"+msg)
	h.mu.Unlock()
}

func (h *recordedHost) Deck() plugin.DeckControl {
	return h
}

func (h *recordedHost) SwitchProfile(ref string) error {
	h.mu.Lock()
	h.switchedProf = ref
	h.mu.Unlock()
	return nil
}
func (h *recordedHost) NextPage(bool) error                                       { return nil }
func (h *recordedHost) PrevPage(bool) error                                       { return nil }
func (h *recordedHost) GoToPage(int) error                                        { return nil }
func (h *recordedHost) OpenFolder(string, string) error                           { return nil }
func (h *recordedHost) CloseFolder() error                                        { return nil }
func (h *recordedHost) TriggerAction(context.Context, string, map[string]any) error { return nil }

func recvSDKJSON(t *testing.T, conn *websocket.Conn) map[string]any {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	defer func() { _ = conn.SetReadDeadline(time.Time{}) }()
	var raw string
	if err := websocket.Message.Receive(conn, &raw); err != nil {
		t.Fatalf("Receive WS message error = %v", err)
	}
	var msg map[string]any
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatalf("Unmarshal WS message %q error = %v", raw, err)
	}
	return msg
}

func sendSDKJSON(t *testing.T, conn *websocket.Conn, v any) {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("Marshal WS message error = %v", err)
	}
	if err := websocket.Message.Send(conn, string(raw)); err != nil {
		t.Fatalf("Send WS message error = %v", err)
	}
}

func TestProviderMissingNodeStatus(t *testing.T) {
	root := t.TempDir()
	manifest := `{
		"SDKVersion": 2,
		"Name": "Node Plugin",
		"Version": "1.0.0",
		"CodePath": "bin/plugin.js",
		"Nodejs": {"Version": "20"},
		"Actions": [{"UUID": "com.example.node.act", "Name": "Node Act"}]
	}`
	createSyntheticBundle(t, root, "com.example.node.sdPlugin", manifest, "")

	nm := NewNodeManager(filepath.Join(root, "managed-node"))
	nm.elgatoPaths = nil
	nm.lookPath = func(string) (string, error) { return "", os.ErrNotExist }

	host := newRecordedHost()
	prov := NewProvider(Options{
		DefaultPluginsDir: root,
		nodeManager:       nm,
	})
	prov.BindHost(host)
	defer func() { _ = prov.Close() }()

	if err := prov.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	statuses := prov.Plugins()
	if len(statuses) != 1 || statuses[0].State != StatusNeedsNode {
		t.Fatalf("expected StatusNeedsNode when Node missing, got %+v", statuses)
	}
	if minMaj, ok := prov.NeedsNodeForAction("com.example.node.act"); !ok || minMaj != 20 {
		t.Fatalf("NeedsNodeForAction = (%d, %v), want (20, true)", minMaj, ok)
	}
	// Dispatching a key press cleanly surfaces an actionable Node runtime error without panicking.
	err := prov.Dispatch(context.Background(), plugin.ActionEvent{
		Type:       plugin.EventKeyDown,
		Context:    "ctx-1",
		ActionUUID: "com.example.node.act",
	})
	if err == nil || !strings.Contains(err.Error(), "Node.js") {
		t.Fatalf("expected actionable Node.js error, got %v", err)
	}
}

func TestProviderWebSocketAndPropertyInspectorBridge(t *testing.T) {
	root := t.TempDir()
	manifest := `{
		"SDKVersion": 2,
		"UUID": "com.example.live",
		"Name": "Live Test",
		"Version": "1.0.0",
		"CodePath": "bin/plugin.js",
		"PropertyInspectorPath": "property-inspector/index.html",
		"Actions": [
			{"UUID": "com.example.live.toggle", "Name": "Live Toggle", "Controllers": ["Keypad"]},
			{"UUID": "com.example.live.dial", "Name": "Live Dial", "Controllers": ["Encoder"]}
		]
	}`
	piHTML := `<!DOCTYPE html><html><body><h3>PI</h3><script src="pi.js"></script></body></html>`
	bundleDir := createSyntheticBundle(t, root, "com.example.live.sdPlugin", manifest, piHTML)
	if err := os.WriteFile(filepath.Join(bundleDir, "property-inspector", "pi.js"), []byte("window.piLoaded=true;"), 0o600); err != nil {
		t.Fatalf("WriteFile(pi.js) error = %v", err)
	}

	host := newRecordedHost()
	_ = host.SaveGlobalSettings("com.example.live", map[string]any{"host": "127.0.0.1"})

	var openedURL string
	spawnCh := make(chan string, 1)

	nm := NewNodeManager(filepath.Join(root, "no-node"))
	nm.elgatoPaths = nil
	nm.lookPath = func(string) (string, error) { return "", os.ErrNotExist }

	prov := NewProvider(Options{
		DefaultPluginsDir: root,
		nodeManager:       nm,
		OpenURL: func(_ context.Context, rawURL string) error {
			openedURL = rawURL
			return nil
		},
		spawnOverride: func(inst *pluginInstance, _ int) error {
			spawnCh <- inst.launchToken
			return nil
		},
	})
	prov.BindHost(host)
	defer func() { _ = prov.Close() }()

	if err := prov.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	launchToken := <-spawnCh
	port := prov.Port()
	if port <= 0 {
		t.Fatalf("expected non-zero loopback port, got %d", port)
	}

	// Queue an initial willAppear before plugin WS connects (tests replay on registerPlugin!).
	if err := prov.Dispatch(context.Background(), plugin.ActionEvent{
		Type:        plugin.EventWillAppear,
		Context:     "slot-ctx-1",
		ActionUUID:  "com.example.live.toggle",
		Controller:  deck.ControllerKeypad,
		Coordinates: deck.Coordinates{Row: 1, Col: 2},
		Settings:    map[string]any{"mode": "auto"},
	}); err != nil {
		t.Fatalf("Dispatch(willAppear before connect) error = %v", err)
	}

	// Connect simulated external plugin WebSocket.
	wsURL := fmt.Sprintf("ws://127.0.0.1:%d/", port)
	plugConn, err := websocket.Dial(wsURL, "", "http://127.0.0.1/")
	if err != nil {
		t.Fatalf("websocket.Dial(plugin) error = %v", err)
	}
	defer func() { _ = plugConn.Close() }()

	sendSDKJSON(t, plugConn, map[string]any{
		"event": "registerPlugin",
		"uuid":  launchToken,
	})

	// Expect global settings first, then replayed willAppear.
	msg1 := recvSDKJSON(t, plugConn)
	if msg1["event"] != "didReceiveGlobalSettings" {
		t.Fatalf("expected didReceiveGlobalSettings, got %+v", msg1)
	}
	msg2 := recvSDKJSON(t, plugConn)
	if msg2["event"] != "willAppear" || msg2["context"] != "slot-ctx-1" {
		t.Fatalf("expected replayed willAppear for slot-ctx-1, got %+v", msg2)
	}

	// Plugin pushes setTitle(""), setImage(svg), setState(1), setFeedback, showOk, openUrl.
	sendSDKJSON(t, plugConn, map[string]any{
		"event":   "setTitle",
		"context": "slot-ctx-1",
		"payload": map[string]any{"title": ""},
	})
	sendSDKJSON(t, plugConn, map[string]any{
		"event":   "setImage",
		"context": "slot-ctx-1",
		"payload": map[string]any{"image": "data:image/svg+xml;utf8,%3Csvg%3E%3C/svg%3E"},
	})
	sendSDKJSON(t, plugConn, map[string]any{
		"event":   "setState",
		"context": "slot-ctx-1",
		"payload": map[string]any{"state": 1},
	})
	sendSDKJSON(t, plugConn, map[string]any{
		"event":   "setFeedback",
		"context": "slot-ctx-1",
		"payload": map[string]any{"value": "80%", "indicator": map[string]any{"value": 80}},
	})
	sendSDKJSON(t, plugConn, map[string]any{
		"event":   "openUrl",
		"payload": map[string]any{"url": "https://example.com/docs"},
	})

	// Verify openUrl eventually runs cleanly and visual updates arrive on host.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		host.mu.Lock()
		vis := host.visuals["slot-ctx-1"]
		gotURL := openedURL
		host.mu.Unlock()
		if vis.Title != nil && *vis.Title == "" &&
			vis.ImageURI != nil && strings.HasPrefix(*vis.ImageURI, "data:image/svg+xml") &&
			vis.State != nil && *vis.State == 1 &&
			vis.Subtitle != nil && *vis.Subtitle == "80%" &&
			gotURL == "https://example.com/docs" {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	host.mu.Lock()
	vis := host.visuals["slot-ctx-1"]
	host.mu.Unlock()
	if vis.Title == nil || *vis.Title != "" || vis.Subtitle == nil || *vis.Subtitle != "80%" || vis.State == nil || *vis.State != 1 {
		t.Fatalf("unexpected visual state recorded on host: %+v", vis)
	}

	// Open HTML Property Inspector session -> fetch HTTP HTML + bootstrap + relative asset.
	sess, ok := prov.PropertyInspectorURL(
		"slot-ctx-1",
		"com.example.live.toggle",
		deck.ControllerKeypad,
		deck.Coordinates{Row: 1, Col: 2},
		0,
		map[string]any{"mode": "auto"},
	)
	if !ok || !strings.Contains(sess.URL, "/pi/com.example.live/property-inspector/index.html?token=") {
		t.Fatalf("unexpected PropertyInspectorURL: ok=%v sess=%+v", ok, sess)
	}

	resp, err := http.Get(sess.URL)
	if err != nil {
		t.Fatalf("HTTP GET PI HTML error = %v", err)
	}
	htmlBody, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if !strings.Contains(string(htmlBody), "connectElgatoStreamDeckSocket") || !strings.Contains(string(htmlBody), "registerPropertyInspector") {
		t.Fatalf("expected injected Property Inspector bootstrap script in HTML, got:\n%s", string(htmlBody))
	}

	jsResp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/pi/com.example.live/property-inspector/pi.js", port))
	if err != nil {
		t.Fatalf("HTTP GET pi.js error = %v", err)
	}
	jsBody, _ := io.ReadAll(jsResp.Body)
	_ = jsResp.Body.Close()
	if !strings.Contains(string(jsBody), "window.piLoaded=true") {
		t.Fatalf("unexpected pi.js body: %q", string(jsBody))
	}

	// Traversal request must be forbidden/not-found.
	travResp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/pi/com.example.live/../../etc/passwd", port))
	if err == nil {
		_ = travResp.Body.Close()
		if travResp.StatusCode == http.StatusOK {
			t.Fatal("expected path traversal under /pi/ to be rejected")
		}
	}

	// Connect PI WebSocket -> verify plugin receives propertyInspectorDidAppear & setSettings sync.
	parts := strings.Split(sess.URL, "token=")
	piToken := parts[1]
	piConn, err := websocket.Dial(wsURL, "", "http://127.0.0.1/")
	if err != nil {
		t.Fatalf("websocket.Dial(pi) error = %v", err)
	}
	defer func() { _ = piConn.Close() }()

	sendSDKJSON(t, piConn, map[string]any{
		"event": "registerPropertyInspector",
		"uuid":  piToken,
	})
	appearPI := recvSDKJSON(t, plugConn)
	if appearPI["event"] != "propertyInspectorDidAppear" || appearPI["context"] != "slot-ctx-1" {
		t.Fatalf("expected propertyInspectorDidAppear on plugin WS, got %+v", appearPI)
	}

	sendSDKJSON(t, piConn, map[string]any{
		"event":   "setSettings",
		"context": "slot-ctx-1",
		"payload": map[string]any{"mode": "manual", "speed": 1.5},
	})
	recvSettings := recvSDKJSON(t, plugConn)
	if recvSettings["event"] != "didReceiveSettings" || recvSettings["context"] != "slot-ctx-1" {
		t.Fatalf("expected didReceiveSettings forwarded to plugin, got %+v", recvSettings)
	}
	host.mu.Lock()
	savedMode := host.savedSettings["slot-ctx-1"]["mode"]
	host.mu.Unlock()
	if savedMode != "manual" {
		t.Fatalf("expected savedSettings[mode]=manual, got %v", savedMode)
	}

	// Bidirectional sendToPlugin <-> sendToPropertyInspector forwarding.
	sendSDKJSON(t, piConn, map[string]any{
		"event":   "sendToPlugin",
		"action":  "com.example.live.toggle",
		"context": "slot-ctx-1",
		"payload": map[string]any{"req": "refresh"},
	})
	forwardedToPlug := recvSDKJSON(t, plugConn)
	if forwardedToPlug["event"] != "sendToPlugin" {
		t.Fatalf("expected sendToPlugin, got %+v", forwardedToPlug)
	}

	sendSDKJSON(t, plugConn, map[string]any{
		"event":   "sendToPropertyInspector",
		"action":  "com.example.live.toggle",
		"context": "slot-ctx-1",
		"payload": map[string]any{"items": []string{"one", "two"}},
	})
	forwardedToPI := recvSDKJSON(t, piConn)
	if forwardedToPI["event"] != "sendToPropertyInspector" {
		t.Fatalf("expected sendToPropertyInspector, got %+v", forwardedToPI)
	}

	// Encoder dialRotate & dialPress routing (SDK v2 -> dialPress).
	if err := prov.Dispatch(context.Background(), plugin.ActionEvent{
		Type:       plugin.EventDialRotate,
		Context:    "dial-ctx-0",
		ActionUUID: "com.example.live.dial",
		Controller: deck.ControllerEncoder,
		DialIndex:  0,
		Ticks:      -2,
	}); err != nil {
		t.Fatalf("Dispatch(dialRotate) error = %v", err)
	}
	rotMsg := recvSDKJSON(t, plugConn)
	if rotMsg["event"] != "dialRotate" || rotMsg["context"] != "dial-ctx-0" {
		t.Fatalf("expected dialRotate for dial-ctx-0, got %+v", rotMsg)
	}

	// SaveHostConfig + HostConfig round-trip cleanly.
	if err := prov.SaveHostConfig(HostConfig{
		ExtraPaths:     []string{filepath.Join(root, "extra"), filepath.Join(root, "extra")},
		ScanElgato:     true,
		CustomNodePath: "/usr/bin/node",
	}); err != nil {
		t.Fatalf("SaveHostConfig() error = %v", err)
	}
	gotCfg := prov.HostConfig()
	if len(gotCfg.ExtraPaths) != 1 || !gotCfg.ScanElgato || gotCfg.CustomNodePath != "/usr/bin/node" {
		t.Fatalf("unexpected HostConfig round-trip: %+v", gotCfg)
	}
}
