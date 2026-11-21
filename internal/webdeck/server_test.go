package webdeck

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/websocket"

	"github.com/mrpoundsign/PoundDeck/internal/app"
	"github.com/mrpoundsign/PoundDeck/pkg/builtin/core"
	"github.com/mrpoundsign/PoundDeck/pkg/deck"
	"github.com/mrpoundsign/PoundDeck/pkg/plugin"
)

type fakeDeckProvider struct {
	mu     sync.Mutex
	host   plugin.HostAPI
	events []plugin.ActionEvent
}

func (f *fakeDeckProvider) Kind() plugin.ProviderKind { return plugin.ProviderBuiltin }
func (f *fakeDeckProvider) Name() string              { return "Fake WebDeck Provider" }
func (f *fakeDeckProvider) Manifests() []plugin.Manifest {
	return []plugin.Manifest{{
		UUID:     "com.test.webdeck",
		Name:     "Fake WebDeck",
		Version:  "1.0.0",
		Provider: plugin.ProviderBuiltin,
		Actions:  f.Actions(),
	}}
}
func (f *fakeDeckProvider) Actions() []plugin.ActionDescriptor {
	return []plugin.ActionDescriptor{
		{
			UUID:        "test.webdeck.key",
			PluginUUID:  "com.test.webdeck",
			Name:        "Secret Key",
			Controllers: []deck.ControllerType{deck.ControllerKeypad},
			Provider:    plugin.ProviderBuiltin,
		},
		{
			UUID:        "test.webdeck.dial",
			PluginUUID:  "com.test.webdeck",
			Name:        "Test Dial",
			Controllers: []deck.ControllerType{deck.ControllerEncoder},
			Provider:    plugin.ProviderBuiltin,
		},
	}
}
func (f *fakeDeckProvider) BindHost(host plugin.HostAPI) {
	f.mu.Lock()
	f.host = host
	f.mu.Unlock()
}
func (f *fakeDeckProvider) Dispatch(_ context.Context, ev plugin.ActionEvent) error {
	f.mu.Lock()
	f.events = append(f.events, ev)
	f.mu.Unlock()
	return nil
}
func (f *fakeDeckProvider) Close() error { return nil }

func newTestEngineAndServer(t *testing.T) (*app.Engine, *fakeDeckProvider, *Server) {
	t.Helper()
	reg := plugin.NewRegistry()
	if err := reg.RegisterProvider(core.New()); err != nil {
		t.Fatalf("RegisterProvider(core) error = %v", err)
	}
	prov := &fakeDeckProvider{}
	if err := reg.RegisterProvider(prov); err != nil {
		t.Fatalf("RegisterProvider(fake) error = %v", err)
	}
	eng, err := app.NewEngine(deck.NewMemoryStore(nil), reg)
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })

	srv := NewServer(eng)
	t.Cleanup(func() { _ = srv.Close() })
	if err := srv.SaveConfig(Config{
		Enabled:  true,
		Port:     0,
		BindMode: BindLocalhost,
	}); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}
	return eng, prov, srv
}

func TestServerConfigPersistenceAndQR(t *testing.T) {
	eng, _, srv := newTestEngineAndServer(t)

	cfg := srv.Config()
	if !isValidPIN(cfg.PIN) {
		t.Fatalf("expected valid 6-digit generated PIN, got %q", cfg.PIN)
	}
	if len(cfg.PairToken) < 16 {
		t.Fatalf("expected non-empty PairToken, got %q", cfg.PairToken)
	}
	if got := eng.GlobalSettings(SettingsPluginUUID)["pin"]; got != cfg.PIN {
		t.Fatalf("expected PIN persisted in GlobalSettings, got %#v", got)
	}

	st := srv.Status()
	if !st.Running || st.Port <= 0 {
		t.Fatalf("expected running server with bound port, got %+v", st)
	}
	if !strings.HasPrefix(st.AccessURL, "http://127.0.0.1:") {
		t.Fatalf("expected localhost access URL, got %q", st.AccessURL)
	}
	if !strings.Contains(st.PairURL, "?pair="+cfg.PairToken) {
		t.Fatalf("expected pair URL with token, got %q", st.PairURL)
	}

	qrImg, err := srv.QRImage(200)
	if err != nil {
		t.Fatalf("QRImage() error = %v", err)
	}
	if b := qrImg.Bounds(); b.Dx() != 200 || b.Dy() != 200 {
		t.Fatalf("unexpected QR bounds %+v", b)
	}

	if addrs := DiscoverLANAddresses(); len(addrs) == 0 || addrs[0].IP == "" {
		t.Fatalf("DiscoverLANAddresses() returned empty slice: %+v", addrs)
	}
}

func TestServerAuthQRRateLimitAndSecurityHeaders(t *testing.T) {
	_, _, srv := newTestEngineAndServer(t)
	st := srv.Status()
	baseURL := strings.TrimSuffix(st.AccessURL, "/")

	// Verify static UI + strict security headers.
	resp, err := http.Get(baseURL + "/")
	if err != nil {
		t.Fatalf("GET / error = %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / status = %d, want 200", resp.StatusCode)
	}
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") || !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Fatalf("unexpected CSP header: %q", csp)
	}
	if resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("missing nosniff header: %q", resp.Header.Get("X-Content-Type-Options"))
	}

	// Unauthenticated asset and WebSocket routes must fail with 401.
	assetResp, err := http.Get(baseURL + "/api/asset/unknown")
	if err != nil {
		t.Fatalf("GET unauth asset error = %v", err)
	}
	_ = assetResp.Body.Close()
	if assetResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated asset status = %d, want 401", assetResp.StatusCode)
	}

	wsUnauth, err := http.Get(baseURL + "/ws")
	if err != nil {
		t.Fatalf("GET unauth /ws error = %v", err)
	}
	_ = wsUnauth.Body.Close()
	if wsUnauth.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated /ws status = %d, want 401", wsUnauth.StatusCode)
	}

	// Cross-origin WebSocket must be rejected with 403.
	reqCross, _ := http.NewRequest(http.MethodGet, baseURL+"/ws", nil)
	reqCross.Header.Set("Origin", "https://evil.example")
	crossResp, err := http.DefaultClient.Do(reqCross)
	if err != nil {
		t.Fatalf("GET cross-origin /ws error = %v", err)
	}
	_ = crossResp.Body.Close()
	if crossResp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin /ws status = %d, want 403", crossResp.StatusCode)
	}

	// QR ?pair= one-tap flow sets HttpOnly session cookie and redirects cleanly.
	jar, _ := cookiejar.New(nil)
	noRedirectClient := &http.Client{
		Jar: jar,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	qrResp, err := noRedirectClient.Get(st.PairURL)
	if err != nil {
		t.Fatalf("GET PairURL error = %v", err)
	}
	_ = qrResp.Body.Close()
	if qrResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("GET PairURL status = %d, want 303", qrResp.StatusCode)
	}

	client := &http.Client{Jar: jar}
	authCheck, err := client.Get(baseURL + "/api/auth/status")
	if err != nil {
		t.Fatalf("GET /api/auth/status error = %v", err)
	}
	var authBody struct {
		Authenticated bool `json:"authenticated"`
	}
	_ = json.NewDecoder(authCheck.Body).Decode(&authBody)
	_ = authCheck.Body.Close()
	if !authBody.Authenticated {
		t.Fatal("expected authenticated=true after QR pairing")
	}

	// Logout sends Clear-Site-Data and clears authentication.
	logoutResp, err := client.Post(baseURL+"/api/logout", "application/json", http.NoBody)
	if err != nil {
		t.Fatalf("POST /api/logout error = %v", err)
	}
	_ = logoutResp.Body.Close()
	if logoutResp.Header.Get("Clear-Site-Data") == "" {
		t.Fatal("expected Clear-Site-Data header on logout")
	}

	// Valid PIN pairing succeeds.
	pinBody, _ := json.Marshal(map[string]string{"pin": st.PIN})
	pairOK, err := client.Post(baseURL+"/api/pair", "application/json", bytes.NewReader(pinBody))
	if err != nil {
		t.Fatalf("POST /api/pair error = %v", err)
	}
	_ = pairOK.Body.Close()
	if pairOK.StatusCode != http.StatusOK {
		t.Fatalf("valid PIN /api/pair status = %d, want 200", pairOK.StatusCode)
	}

	// Repeated invalid PIN attempts trigger 429 Too Many Requests.
	badBody, _ := json.Marshal(map[string]string{"pin": "999999"})
	if st.PIN == "999999" {
		badBody, _ = json.Marshal(map[string]string{"pin": "000000"})
	}
	for attempt := range pairFailLimit {
		r, reqErr := http.Post(baseURL+"/api/pair", "application/json", bytes.NewReader(badBody))
		if reqErr != nil {
			t.Fatalf("POST bad pair attempt %d error = %v", attempt, reqErr)
		}
		_ = r.Body.Close()
		if r.StatusCode != http.StatusUnauthorized {
			t.Fatalf("bad pair attempt %d status = %d, want 401", attempt, r.StatusCode)
		}
	}
	limitedResp, err := http.Post(baseURL+"/api/pair", "application/json", bytes.NewReader(badBody))
	if err != nil {
		t.Fatalf("POST rate-limited pair error = %v", err)
	}
	_ = limitedResp.Body.Close()
	if limitedResp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("6th failed pair attempt status = %d, want 429", limitedResp.StatusCode)
	}
}

func TestServerWebSocketSanitizationCommandsAssetsAndRevoke(t *testing.T) {
	eng, prov, srv := newTestEngineAndServer(t)

	// Prepare local SVG asset file in t.TempDir().
	assetDir := t.TempDir()
	svgPath := filepath.Join(assetDir, "custom.svg")
	if err := os.WriteFile(svgPath, []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><rect width="24" height="24" fill="#38BDF8"/></svg>`), 0o600); err != nil {
		t.Fatalf("WriteFile(svg) error = %v", err)
	}

	// Bind key (0,0) with secret settings, key (0,1) with UTF-8 SVG data URI + empty title,
	// key (0,2) with local SVG file, and dial 0 with indicator feedback.
	c0 := deck.Coordinates{Row: 0, Col: 0}
	if _, err := eng.AssignKeypadAction(c0, "test.webdeck.key"); err != nil {
		t.Fatalf("AssignKeypadAction(0,0) error = %v", err)
	}
	if err := eng.UpdateKeypadSlot(c0, "Webhook Key", "#38BDF8", map[string]any{
		"webhook": "https://example.com/hook?token=SUPERSECRET_TOKEN",
	}); err != nil {
		t.Fatalf("UpdateKeypadSlot(0,0) error = %v", err)
	}

	c1 := deck.Coordinates{Row: 0, Col: 1}
	slot1, err := eng.AssignKeypadAction(c1, "test.webdeck.key")
	if err != nil {
		t.Fatalf("AssignKeypadAction(0,1) error = %v", err)
	}
	emptyTitle := ""
	svgDataURI := `data:image/svg+xml;utf8,<svg xmlns="http://www.w3.org/2000/svg" width="72" height="72"><rect width="72" height="72" fill="#0a0a0c"/><text x="36" y="36" fill="#ffffff">REC</text></svg>`
	prov.host.UpdateVisual(slot1.Context, plugin.VisualPatch{
		Title:    &emptyTitle,
		ImageURI: &svgDataURI,
	})

	c2 := deck.Coordinates{Row: 0, Col: 2}
	slot2, err := eng.AssignKeypadAction(c2, "test.webdeck.key")
	if err != nil {
		t.Fatalf("AssignKeypadAction(0,2) error = %v", err)
	}
	prov.host.UpdateVisual(slot2.Context, plugin.VisualPatch{
		ImageURI: &svgPath,
	})

	dialSlot, err := eng.AssignDialAction(0, "test.webdeck.dial")
	if err != nil {
		t.Fatalf("AssignDialAction(0) error = %v", err)
	}
	prov.host.UpdateVisual(dialSlot.Context, plugin.VisualPatch{
		Feedback: map[string]any{
			"value":     "68%",
			"indicator": map[string]any{"value": 68},
		},
	})

	st := srv.Status()
	baseURL := strings.TrimSuffix(st.AccessURL, "/")

	// Authenticate via PIN to obtain HttpOnly session cookie.
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	pinBody, _ := json.Marshal(map[string]string{"pin": st.PIN})
	pairResp, err := client.Post(baseURL+"/api/pair", "application/json", bytes.NewReader(pinBody))
	if err != nil {
		t.Fatalf("POST /api/pair error = %v", err)
	}
	_ = pairResp.Body.Close()
	if pairResp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/pair status = %d", pairResp.StatusCode)
	}

	wsURL := "ws" + strings.TrimPrefix(baseURL, "http") + "/ws"
	wsCfg, err := websocket.NewConfig(wsURL, baseURL)
	if err != nil {
		t.Fatalf("websocket.NewConfig() error = %v", err)
	}
	cookies := jar.Cookies(pairResp.Request.URL)
	cookieHeader := make([]string, 0, len(cookies))
	for _, ck := range cookies {
		cookieHeader = append(cookieHeader, ck.Name+"="+ck.Value)
	}
	wsCfg.Header = http.Header{"Cookie": {strings.Join(cookieHeader, "; ")}}

	conn, err := websocket.DialConfig(wsCfg)
	if err != nil {
		t.Fatalf("websocket.DialConfig() error = %v", err)
	}
	defer func() { _ = conn.Close() }()

	// Read initial snapshot and verify sanitization & data transformation.
	var rawMsg string
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if err := websocket.Message.Receive(conn, &rawMsg); err != nil {
		t.Fatalf("receive initial snapshot error = %v", err)
	}
	if strings.Contains(rawMsg, "SUPERSECRET_TOKEN") {
		t.Fatal("WS snapshot leaked secret SlotConfig.Settings!")
	}
	var initMsg ServerMessage
	if err := json.Unmarshal([]byte(rawMsg), &initMsg); err != nil || initMsg.Snapshot == nil {
		t.Fatalf("unmarshal initial snapshot failed (%v): %s", err, rawMsg)
	}
	s01 := initMsg.Snapshot.Slots["0,1"]
	if !strings.HasPrefix(s01.ImageURL, "data:image/svg+xml;base64,") {
		t.Fatalf("expected UTF-8 SVG normalized to base64 data URI, got %q", s01.ImageURL)
	}
	b64Part := strings.TrimPrefix(s01.ImageURL, "data:image/svg+xml;base64,")
	if decoded, err := base64.StdEncoding.DecodeString(b64Part); err != nil || !strings.Contains(string(decoded), "#0a0a0c") {
		t.Fatalf("decoded SVG data mismatch (%v): %q", err, string(decoded))
	}

	s02 := initMsg.Snapshot.Slots["0,2"]
	if !strings.HasPrefix(s02.ImageURL, "/api/asset/") {
		t.Fatalf("expected local file mapped to /api/asset/, got %q", s02.ImageURL)
	}
	assetResp, err := client.Get(baseURL + s02.ImageURL)
	if err != nil {
		t.Fatalf("GET mapped asset error = %v", err)
	}
	assetBytes, _ := io.ReadAll(assetResp.Body)
	_ = assetResp.Body.Close()
	if assetResp.StatusCode != http.StatusOK || !strings.Contains(assetResp.Header.Get("Content-Type"), "image/svg+xml") {
		t.Fatalf("unexpected asset response %d / %q", assetResp.StatusCode, assetResp.Header.Get("Content-Type"))
	}
	if !strings.Contains(assetResp.Header.Get("Content-Security-Policy"), "sandbox") {
		t.Fatalf("expected SVG asset sandbox CSP, got %q", assetResp.Header.Get("Content-Security-Policy"))
	}
	if !strings.Contains(string(assetBytes), "#38BDF8") {
		t.Fatalf("unexpected SVG asset body: %q", string(assetBytes))
	}

	if d0 := initMsg.Snapshot.DialSlots[0]; !d0.HasIndicator || d0.IndicatorPct != 68 {
		t.Fatalf("expected Dial 0 indicator 68%%, got %+v", d0)
	}

	// Send interactive commands over WebSocket.
	for _, cmd := range []ClientCommand{
		{Type: "ping"},
		{Type: "keyDown", Row: 0, Col: 0},
		{Type: "keyUp", Row: 0, Col: 0},
		{Type: "dialRotate", Dial: 0, Ticks: 3},
		{Type: "dialPress", Dial: 0},
	} {
		rawCmd, _ := json.Marshal(cmd)
		if err := websocket.Message.Send(conn, string(rawCmd)); err != nil {
			t.Fatalf("send %s error = %v", cmd.Type, err)
		}
	}

	// Expect pong response.
	var gotPong bool
	for range 4 {
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		if err := websocket.Message.Receive(conn, &rawMsg); err != nil {
			break
		}
		var m ServerMessage
		if json.Unmarshal([]byte(rawMsg), &m) == nil && m.Type == "pong" {
			gotPong = true
			break
		}
	}
	if !gotPong {
		t.Fatal("expected pong reply from WS server")
	}

	// Wait until all 4 interactive WS commands have been dispatched to the provider.
	wantTypes := []plugin.EventType{plugin.EventKeyDown, plugin.EventKeyUp, plugin.EventDialRotate, plugin.EventDialPress}
	var types []plugin.EventType
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		prov.mu.Lock()
		types = make([]plugin.EventType, len(prov.events))
		for i, e := range prov.events {
			types[i] = e.Type
		}
		prov.mu.Unlock()

		allSeen := true
		for _, want := range wantTypes {
			if !slices.Contains(types, want) {
				allSeen = false
				break
			}
		}
		if allSeen {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	for _, want := range wantTypes {
		if !slices.Contains(types, want) {
			t.Fatalf("expected dispatched event %q, got %v", want, types)
		}
	}

	// Engine state update pushes a new snapshot to the connected WS client.
	if _, err := eng.AddPage("Page 2"); err != nil {
		t.Fatalf("AddPage error = %v", err)
	}
	if err := eng.NextPage(true); err != nil {
		t.Fatalf("NextPage error = %v", err)
	}
	var sawPage2 bool
	for range 6 {
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		if err := websocket.Message.Receive(conn, &rawMsg); err != nil {
			break
		}
		var m ServerMessage
		if json.Unmarshal([]byte(rawMsg), &m) == nil && m.Type == "snapshot" && m.Snapshot != nil && m.Snapshot.PageIndex == 1 {
			sawPage2 = true
			break
		}
	}
	if !sawPage2 {
		t.Fatal("expected WebSocket client to receive broadcast for Page 2")
	}

	// Revoking sessions sends auth_revoked / disconnects and invalidates cookie.
	if err := srv.RevokeAllSessions(); err != nil {
		t.Fatalf("RevokeAllSessions() error = %v", err)
	}
	authAfter, err := client.Get(baseURL + "/api/auth/status")
	if err != nil {
		t.Fatalf("GET auth status after revoke error = %v", err)
	}
	var revBody struct {
		Authenticated bool `json:"authenticated"`
	}
	_ = json.NewDecoder(authAfter.Body).Decode(&revBody)
	_ = authAfter.Body.Close()
	if revBody.Authenticated {
		t.Fatal("expected authenticated=false after RevokeAllSessions")
	}
}
