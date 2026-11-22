package webdeck

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
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

	srv := NewServer(eng, nil)
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

type testClientIdentity struct {
	deviceID  string
	pub       ed25519.PublicKey
	priv      ed25519.PrivateKey
	hostID    string
	hostPub   ed25519.PublicKey
	hostFp    string
	baseURL   string
	deviceNam string
}

type testWSSession struct {
	conn         *websocket.Conn
	sessionID    string
	clientMacKey []byte
	serverMacKey []byte
	assetMacKey  []byte
	txSeq        uint64
	rxSeq        uint64
}

func (s *testWSSession) sendSigned(t *testing.T, cmd ClientCommand) {
	t.Helper()
	s.txSeq++
	payloadBytes, _ := json.Marshal(cmd)
	payload := string(payloadBytes)
	mac := computeFrameMAC(s.clientMacKey, "c2s", s.txSeq, payload)
	frameBytes, _ := json.Marshal(WSSignedFrame{
		Seq:     s.txSeq,
		Payload: payload,
		MAC:     base64.StdEncoding.EncodeToString(mac),
	})
	if err := websocket.Message.Send(s.conn, string(frameBytes)); err != nil {
		t.Fatalf("send signed frame(%s): %v", cmd.Type, err)
	}
}

func (s *testWSSession) recvSigned(t *testing.T, timeout time.Duration) ServerMessage {
	t.Helper()
	_ = s.conn.SetReadDeadline(time.Now().Add(timeout))
	var raw string
	if err := websocket.Message.Receive(s.conn, &raw); err != nil {
		t.Fatalf("recv signed frame: %v", err)
	}
	var frame WSSignedFrame
	if err := json.Unmarshal([]byte(raw), &frame); err != nil {
		t.Fatalf("unmarshal WSSignedFrame (%s): %v", raw, err)
	}
	if frame.Seq <= s.rxSeq {
		t.Fatalf("non-monotonic server frame seq %d <= %d", frame.Seq, s.rxSeq)
	}
	macBytes, err := base64.StdEncoding.DecodeString(frame.MAC)
	if err != nil || !hmac.Equal(macBytes, computeFrameMAC(s.serverMacKey, "s2c", frame.Seq, frame.Payload)) {
		t.Fatalf("invalid server frame MAC on seq %d", frame.Seq)
	}
	s.rxSeq = frame.Seq
	var msg ServerMessage
	if err := json.Unmarshal([]byte(frame.Payload), &msg); err != nil {
		t.Fatalf("unmarshal ServerMessage: %v", err)
	}
	return msg
}

func startClientSPAKE2(t *testing.T, baseURL, pin, deviceName string) (attemptID string, cPub ed25519.PublicKey, cPriv ed25519.PrivateKey, hostID string, hostPub ed25519.PublicKey, mat spake2Material) {
	t.Helper()
	infoResp, err := http.Get(baseURL + "/api/pair/info")
	if err != nil {
		t.Fatalf("GET /api/pair/info error = %v", err)
	}
	var info struct {
		HostID          string `json:"hostId"`
		HostName        string `json:"hostName"`
		HostPublicKey   string `json:"hostPublicKey"`
		HostFingerprint string `json:"hostFingerprint"`
		PairingOpen     bool   `json:"pairingOpen"`
		WindowID        string `json:"windowId"`
	}
	_ = json.NewDecoder(infoResp.Body).Decode(&info)
	_ = infoResp.Body.Close()
	if !info.PairingOpen || info.WindowID == "" {
		t.Fatalf("expected open pairing window in /api/pair/info, got %+v", info)
	}
	hostPub, err = base64.StdEncoding.DecodeString(info.HostPublicKey)
	if err != nil {
		t.Fatalf("decode hostPublicKey: %v", err)
	}

	cPub, cPriv, err = ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey(client): %v", err)
	}
	st, err := spake2ClientStart(pin, info.WindowID)
	if err != nil {
		t.Fatalf("spake2ClientStart: %v", err)
	}

	startBody, _ := json.Marshal(map[string]string{
		"windowId":        info.WindowID,
		"deviceName":      deviceName,
		"clientPublicKey": base64.StdEncoding.EncodeToString(cPub),
		"pA":              base64.StdEncoding.EncodeToString(st.pA),
	})
	startResp, err := http.Post(baseURL+"/api/pair/start", "application/json", bytes.NewReader(startBody))
	if err != nil {
		t.Fatalf("POST /api/pair/start error = %v", err)
	}
	var startOut struct {
		AttemptID string `json:"attemptId"`
		PB        string `json:"pB"`
	}
	_ = json.NewDecoder(startResp.Body).Decode(&startOut)
	_ = startResp.Body.Close()
	if startResp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/pair/start status = %d", startResp.StatusCode)
	}

	pB, _ := base64.StdEncoding.DecodeString(startOut.PB)
	mat, err = spake2ClientFinish(st, cPub, hostPub, pB)
	if err != nil {
		t.Fatalf("spake2ClientFinish: %v", err)
	}

	return startOut.AttemptID, cPub, cPriv, info.HostID, hostPub, mat
}

func performTestPairing(t *testing.T, srv *Server, deviceName string) testClientIdentity {
	t.Helper()
	st, err := srv.OpenPairingWindow(DefaultPairingTTL)
	if err != nil {
		t.Fatalf("OpenPairingWindow() error = %v", err)
	}
	baseURL := strings.TrimSuffix(st.AccessURL, "/")
	attemptID, cPub, cPriv, hostID, hostPub, mat := startClientSPAKE2(t, baseURL, st.PIN, deviceName)

	cSig := ed25519.Sign(cPriv, pairClientSigPayload(mat.hashTT))
	verBody, _ := json.Marshal(map[string]string{
		"attemptId": attemptID,
		"cA":        base64.StdEncoding.EncodeToString(mat.cA),
		"clientSig": base64.StdEncoding.EncodeToString(cSig),
	})
	verResp, err := http.Post(baseURL+"/api/pair/verify", "application/json", bytes.NewReader(verBody))
	if err != nil {
		t.Fatalf("POST /api/pair/verify error = %v", err)
	}
	var verOut struct {
		CB      string `json:"cB"`
		HostSig string `json:"hostSig"`
		SAS     string `json:"sas"`
	}
	_ = json.NewDecoder(verResp.Body).Decode(&verOut)
	_ = verResp.Body.Close()
	if verResp.StatusCode != http.StatusOK || verOut.SAS != mat.sas {
		t.Fatalf("verify failed: status=%d sas=%q want=%q", verResp.StatusCode, verOut.SAS, mat.sas)
	}
	hSig, _ := base64.StdEncoding.DecodeString(verOut.HostSig)
	if !ed25519.Verify(hostPub, pairHostSigPayload(mat.hashTT), hSig) {
		t.Fatal("host pairing signature verification failed on client")
	}

	confBody, _ := json.Marshal(map[string]any{
		"attemptId": attemptID,
		"confirm":   true,
		"mac":       base64.StdEncoding.EncodeToString(pairConfirmMAC(mat.kcA, attemptID, true)),
	})
	confResp, err := http.Post(baseURL+"/api/pair/confirm", "application/json", bytes.NewReader(confBody))
	if err != nil {
		t.Fatalf("POST /api/pair/confirm error = %v", err)
	}
	_ = confResp.Body.Close()
	if confResp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/pair/confirm status = %d", confResp.StatusCode)
	}

	if err := srv.ResolvePendingPair(attemptID, true); err != nil {
		t.Fatalf("ResolvePendingPair(true) error = %v", err)
	}

	proof := base64.StdEncoding.EncodeToString(pairStatusReqMAC(mat.kcA, attemptID))
	req, _ := http.NewRequest(http.MethodGet, baseURL+"/api/pair/status/"+attemptID, nil)
	req.Header.Set("X-Pair-Proof", proof)
	stResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /api/pair/status error = %v", err)
	}
	var stOut struct {
		Status   string `json:"status"`
		DeviceID string `json:"deviceId"`
		MAC      string `json:"mac"`
	}
	_ = json.NewDecoder(stResp.Body).Decode(&stOut)
	_ = stResp.Body.Close()
	if stOut.Status != "approved" || stOut.DeviceID == "" {
		t.Fatalf("expected approved status + deviceId, got %+v", stOut)
	}
	macBytes, _ := base64.StdEncoding.DecodeString(stOut.MAC)
	if !hmac.Equal(macBytes, pairStatusRespMAC(mat.kcB, attemptID, stOut.Status, stOut.DeviceID)) {
		t.Fatal("invalid status response MAC")
	}

	return testClientIdentity{
		deviceID:  stOut.DeviceID,
		pub:       cPub,
		priv:      cPriv,
		hostID:    hostID,
		hostPub:   hostPub,
		hostFp:    PublicKeyFingerprint(hostPub),
		baseURL:   baseURL,
		deviceNam: deviceName,
	}
}

func dialAuthenticatedWS(t *testing.T, id testClientIdentity) *testWSSession {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(id.baseURL, "http") + "/ws"
	wsCfg, err := websocket.NewConfig(wsURL, id.baseURL)
	if err != nil {
		t.Fatalf("websocket.NewConfig() error = %v", err)
	}
	conn, err := websocket.DialConfig(wsCfg)
	if err != nil {
		t.Fatalf("websocket.DialConfig() error = %v", err)
	}

	var rawHello string
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if err := websocket.Message.Receive(conn, &rawHello); err != nil {
		_ = conn.Close()
		t.Fatalf("recv auth_hello error = %v", err)
	}
	var hello WSAuthHello
	if err := json.Unmarshal([]byte(rawHello), &hello); err != nil || hello.Type != "auth_hello" {
		_ = conn.Close()
		t.Fatalf("unexpected auth_hello (%s): %v", rawHello, err)
	}
	if hello.HostID != id.hostID || hello.HostPublicKey != base64.StdEncoding.EncodeToString(id.hostPub) {
		_ = conn.Close()
		t.Fatal("host identity mismatch in auth_hello")
	}

	serverNonce, _ := base64.StdEncoding.DecodeString(hello.ServerNonce)
	clientNonce, _ := generateRandomBytes(32)
	ephPriv, _ := generateX25519Keypair()
	ephPub := ephPriv.PublicKey().Bytes()

	t1 := buildWSTranscript1(id.hostID, id.deviceID, id.hostPub, id.pub, serverNonce, clientNonce, ephPub)
	cSig := ed25519.Sign(id.priv, t1)
	respBytes, _ := json.Marshal(WSAuthResponse{
		Type:         "auth_response",
		DeviceID:     id.deviceID,
		ClientNonce:  base64.StdEncoding.EncodeToString(clientNonce),
		ClientEphPub: base64.StdEncoding.EncodeToString(ephPub),
		ClientSig:    base64.StdEncoding.EncodeToString(cSig),
	})
	if err := websocket.Message.Send(conn, string(respBytes)); err != nil {
		_ = conn.Close()
		t.Fatalf("send auth_response error = %v", err)
	}

	var rawOK string
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if err := websocket.Message.Receive(conn, &rawOK); err != nil {
		_ = conn.Close()
		t.Fatalf("recv auth_ok error = %v", err)
	}
	var authOK WSAuthOK
	if err := json.Unmarshal([]byte(rawOK), &authOK); err != nil || authOK.Type != "auth_ok" {
		_ = conn.Close()
		t.Fatalf("expected auth_ok, got %s", rawOK)
	}

	serverEphPub, _ := base64.StdEncoding.DecodeString(authOK.ServerEphPub)
	hostSig, _ := base64.StdEncoding.DecodeString(authOK.HostSig)
	t2 := buildWSTranscript2(t1, serverEphPub)
	if !ed25519.Verify(id.hostPub, t2, hostSig) {
		_ = conn.Close()
		t.Fatal("host signature verification failed in auth_ok")
	}
	shared, err := computeX25519Shared(ephPriv, serverEphPub)
	if err != nil {
		_ = conn.Close()
		t.Fatalf("computeX25519Shared: %v", err)
	}
	cMac, sMac, aMac, err := deriveWSSessionKeys(shared, t2)
	if err != nil {
		_ = conn.Close()
		t.Fatalf("deriveWSSessionKeys: %v", err)
	}

	return &testWSSession{
		conn:         conn,
		sessionID:    authOK.SessionID,
		clientMacKey: cMac,
		serverMacKey: sMac,
		assetMacKey:  aMac,
	}
}

func TestServerConfigPersistencePairingWindowAndQR(t *testing.T) {
	eng, _, srv := newTestEngineAndServer(t)

	cfg := srv.Config()
	if cfg.HostID == "" || cfg.HostPublicKey == "" {
		t.Fatalf("expected generated HostID and HostPublicKey, got %+v", cfg)
	}
	if got := eng.GlobalSettings(SettingsPluginUUID)["hostPublicKey"]; got != cfg.HostPublicKey {
		t.Fatalf("expected HostPublicKey persisted in GlobalSettings, got %#v", got)
	}

	st := srv.Status()
	if !st.Running || st.Port <= 0 {
		t.Fatalf("expected running server with bound port, got %+v", st)
	}
	if !strings.HasPrefix(st.AccessURL, "http://127.0.0.1:") {
		t.Fatalf("expected localhost access URL, got %q", st.AccessURL)
	}
	if st.PairingOpen || st.PIN != "" {
		t.Fatalf("expected pairing window closed initially, got %+v", st)
	}

	openSt, err := srv.OpenPairingWindow(DefaultPairingTTL)
	if err != nil {
		t.Fatalf("OpenPairingWindow() error = %v", err)
	}
	if !openSt.PairingOpen || !isValidPIN(openSt.PIN) {
		t.Fatalf("expected valid 6-digit PIN when open, got %+v", openSt)
	}
	wantFrag := "#pair=" + url.QueryEscape(openSt.PIN) + "&fp=" + url.QueryEscape(openSt.HostFingerprint)
	if !strings.Contains(openSt.PairURL, wantFrag) {
		t.Fatalf("expected PairURL fragment %q, got %q", wantFrag, openSt.PairURL)
	}

	qrImg, err := srv.QRImage(200)
	if err != nil || qrImg.Bounds().Dx() != 200 || qrImg.Bounds().Dy() != 200 {
		t.Fatalf("unexpected QRImage result (%#v, %v)", qrImg, err)
	}

	srv.ClosePairingWindow()
	closedSt := srv.Status()
	if closedSt.PairingOpen || closedSt.PIN != "" {
		t.Fatalf("expected pairing closed after ClosePairingWindow, got %+v", closedSt)
	}

	// Verify legacy config migration preserves network settings cleanly.
	migCfg, needsSave := configFromSettings(map[string]any{
		"enabled":   true,
		"port":      9125,
		"bindMode":  "localhost",
		"pin":       "123456",
		"pairToken": "old-static-token",
		"sessions":  []string{"old-cookie-hash"},
	})
	if !needsSave || !migCfg.Enabled || migCfg.Port != 9125 || migCfg.BindMode != BindLocalhost {
		t.Fatalf("unexpected migrated config: needsSave=%v cfg=%+v", needsSave, migCfg)
	}
}

func TestServerMutualPairingSPAKE2RateLimitAndDualConfirmation(t *testing.T) {
	_, _, srv := newTestEngineAndServer(t)
	openSt, err := srv.OpenPairingWindow(DefaultPairingTTL)
	if err != nil {
		t.Fatalf("OpenPairingWindow() error = %v", err)
	}
	baseURL := strings.TrimSuffix(openSt.AccessURL, "/")

	// Verify static routes and strict security headers.
	for _, path := range []string{"/", "/styles.css", "/crypto.js", "/app.js"} {
		resp, reqErr := http.Get(baseURL + path)
		if reqErr != nil {
			t.Fatalf("GET %s error = %v", path, reqErr)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK || resp.Header.Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("unexpected response on %s: %d", path, resp.StatusCode)
		}
		if path == "/" {
			csp := resp.Header.Get("Content-Security-Policy")
			if !strings.Contains(csp, "default-src 'self'") || !strings.Contains(csp, "frame-ancestors 'none'") {
				t.Fatalf("unexpected CSP header: %q", csp)
			}
		}
	}

	// Wrong PINs must fail /api/pair/verify with 401 without popping PendingPair, and lock after 5 attempts.
	wrongPIN := "999999"
	if openSt.PIN == "999999" {
		wrongPIN = "000000"
	}
	for attempt := range pairFailLimit {
		attID, _, cPriv, _, _, mat := startClientSPAKE2(t, baseURL, wrongPIN, "Rogue Phone")
		cSig := ed25519.Sign(cPriv, pairClientSigPayload(mat.hashTT))
		verBody, _ := json.Marshal(map[string]string{
			"attemptId": attID,
			"cA":        base64.StdEncoding.EncodeToString(mat.cA),
			"clientSig": base64.StdEncoding.EncodeToString(cSig),
		})
		r, reqErr := http.Post(baseURL+"/api/pair/verify", "application/json", bytes.NewReader(verBody))
		if reqErr != nil {
			t.Fatalf("POST verify attempt %d error = %v", attempt, reqErr)
		}
		_ = r.Body.Close()
		if r.StatusCode != http.StatusUnauthorized {
			t.Fatalf("wrong PIN verify status = %d, want 401", r.StatusCode)
		}
		if srv.Status().PendingPair != nil {
			t.Fatal("wrong PIN must NEVER expose a PendingPair prompt on Studio")
		}
	}

	// 6th attempt from same IP must return 429 Too Many Requests.
	limBody, _ := json.Marshal(map[string]string{
		"windowId":        "any",
		"deviceName":      "Blocked",
		"clientPublicKey": base64.StdEncoding.EncodeToString(make([]byte, 32)),
		"pA":              base64.StdEncoding.EncodeToString(make([]byte, 32)),
	})
	limResp, err := http.Post(baseURL+"/api/pair/start", "application/json", bytes.NewReader(limBody))
	if err != nil {
		t.Fatalf("rate limit POST error = %v", err)
	}
	_ = limResp.Body.Close()
	if limResp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("6th attempt status = %d, want 429", limResp.StatusCode)
	}

	// Reset rate limiter for valid dual-screen test using paired helper + host-deny + dual confirm.
	srv.limiter.clear("127.0.0.1")
	att1, _, cPriv1, _, _, mat1 := startClientSPAKE2(t, baseURL, openSt.PIN, "Deny Test Phone")
	cSig1 := ed25519.Sign(cPriv1, pairClientSigPayload(mat1.hashTT))
	verBody1, _ := json.Marshal(map[string]string{
		"attemptId": att1,
		"cA":        base64.StdEncoding.EncodeToString(mat1.cA),
		"clientSig": base64.StdEncoding.EncodeToString(cSig1),
	})
	vr1, _ := http.Post(baseURL+"/api/pair/verify", "application/json", bytes.NewReader(verBody1))
	_ = vr1.Body.Close()
	if cur := srv.Status().PendingPair; cur == nil || cur.SAS != mat1.sas || cur.DeviceName != "Deny Test Phone" {
		t.Fatalf("expected PendingPair with matching SAS %q, got %+v", mat1.sas, cur)
	}
	// Host denies -> must NOT enroll device.
	if err := srv.ResolvePendingPair(att1, false); err != nil {
		t.Fatalf("ResolvePendingPair(false) error = %v", err)
	}
	if srv.Status().PairedDevices != 0 || srv.Status().PendingPair != nil {
		t.Fatal("denied pairing must leave PairedDevices=0 and clear PendingPair")
	}

	// Host approves BEFORE client confirms -> must wait until client confirms!
	att2, _, cPriv2, _, _, mat2 := startClientSPAKE2(t, baseURL, openSt.PIN, "My Pixel")
	cSig2 := ed25519.Sign(cPriv2, pairClientSigPayload(mat2.hashTT))
	verBody2, _ := json.Marshal(map[string]string{
		"attemptId": att2,
		"cA":        base64.StdEncoding.EncodeToString(mat2.cA),
		"clientSig": base64.StdEncoding.EncodeToString(cSig2),
	})
	vr2, _ := http.Post(baseURL+"/api/pair/verify", "application/json", bytes.NewReader(verBody2))
	_ = vr2.Body.Close()

	if err := srv.ResolvePendingPair(att2, true); err != nil {
		t.Fatalf("ResolvePendingPair(true) before client confirm error = %v", err)
	}
	if stMid := srv.Status(); stMid.PairedDevices != 0 || stMid.PendingPair == nil || !stMid.PendingPair.HostApproved {
		t.Fatalf("expected device NOT enrolled until client confirms too, got %+v", stMid)
	}

	// Client confirms now -> device enrolls and pairing window auto-closes!
	confBody2, _ := json.Marshal(map[string]any{
		"attemptId": att2,
		"confirm":   true,
		"mac":       base64.StdEncoding.EncodeToString(pairConfirmMAC(mat2.kcA, att2, true)),
	})
	cr2, err := http.Post(baseURL+"/api/pair/confirm", "application/json", bytes.NewReader(confBody2))
	if err != nil {
		t.Fatalf("POST confirm error = %v", err)
	}
	_ = cr2.Body.Close()
	if stDone := srv.Status(); stDone.PairedDevices != 1 || stDone.PairingOpen || stDone.PendingPair != nil {
		t.Fatalf("expected 1 paired device and closed window after mutual confirm, got %+v", stDone)
	}
}

func TestServerWSMutualHandshakeSignedFramesAssetsAndRevoke(t *testing.T) {
	eng, prov, srv := newTestEngineAndServer(t)

	assetDir := t.TempDir()
	svgPath := filepath.Join(assetDir, "custom.svg")
	if err := os.WriteFile(svgPath, []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><rect width="24" height="24" fill="#38BDF8"/></svg>`), 0o600); err != nil {
		t.Fatalf("WriteFile(svg) error = %v", err)
	}

	c0 := deck.Coordinates{Row: 0, Col: 0}
	if _, err := eng.AssignKeypadAction(c0, "test.webdeck.key"); err != nil {
		t.Fatalf("AssignKeypadAction(0,0) error = %v", err)
	}
	if err := eng.UpdateKeypadSlotAppearance(c0, app.SlotAppearance{
		Title:           "Webhook Key",
		Color:           "#38BDF8",
		BackgroundColor: "#FBBF24",
		BackgroundImage: svgPath,
	}, map[string]any{
		"webhook": "https://example.com/hook?token=SUPERSECRET_TOKEN",
	}); err != nil {
		t.Fatalf("UpdateKeypadSlotAppearance(0,0) error = %v", err)
	}

	c1 := deck.Coordinates{Row: 0, Col: 1}
	slot1, _ := eng.AssignKeypadAction(c1, "test.webdeck.key")
	emptyTitle := ""
	svgDataURI := `data:image/svg+xml;utf8,<svg xmlns="http://www.w3.org/2000/svg" width="72" height="72"><rect width="72" height="72" fill="#0a0a0c"/><text x="36" y="36" fill="#ffffff">REC</text></svg>`
	prov.host.UpdateVisual(slot1.Context, plugin.VisualPatch{
		Title:    &emptyTitle,
		ImageURI: &svgDataURI,
	})

	c2 := deck.Coordinates{Row: 0, Col: 2}
	slot2, _ := eng.AssignKeypadAction(c2, "test.webdeck.key")
	prov.host.UpdateVisual(slot2.Context, plugin.VisualPatch{
		ImageURI: &svgPath,
	})

	dialSlot, _ := eng.AssignDialAction(0, "test.webdeck.dial")
	if err := eng.UpdateDialSlotAppearance(0, app.SlotAppearance{
		Title:           "Vol",
		BackgroundColor: "#1E293B",
	}, nil); err != nil {
		t.Fatalf("UpdateDialSlotAppearance(0) error = %v", err)
	}
	prov.host.UpdateVisual(dialSlot.Context, plugin.VisualPatch{
		Feedback: map[string]any{
			"value":     "68%",
			"indicator": map[string]any{"value": 68},
		},
	})

	baseURL := strings.TrimSuffix(srv.Status().AccessURL, "/")

	// Unauthenticated HTTP asset & non-upgrade /ws requests must fail with 401; cross-origin WS fails with 403.
	assetUnauth, _ := http.Get(baseURL + "/api/asset/unknown")
	_ = assetUnauth.Body.Close()
	if assetUnauth.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauth asset status = %d, want 401", assetUnauth.StatusCode)
	}
	wsUnauth, _ := http.Get(baseURL + "/ws")
	_ = wsUnauth.Body.Close()
	if wsUnauth.StatusCode != http.StatusUnauthorized {
		t.Fatalf("non-upgrade /ws status = %d, want 401", wsUnauth.StatusCode)
	}
	reqCross, _ := http.NewRequest(http.MethodGet, baseURL+"/ws", nil)
	reqCross.Header.Set("Origin", "https://evil.example")
	crossResp, _ := http.DefaultClient.Do(reqCross)
	_ = crossResp.Body.Close()
	if crossResp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin /ws status = %d, want 403", crossResp.StatusCode)
	}

	// Mutually pair Device A & Device B.
	devA := performTestPairing(t, srv, "Phone A")
	devB := performTestPairing(t, srv, "Tablet B")

	wsA := dialAuthenticatedWS(t, devA)
	defer func() { _ = wsA.conn.Close() }()

	// Initial signed snapshot hides secrets, normalizes SVG, and signs asset URL.
	initMsg := wsA.recvSigned(t, 3*time.Second)
	if initMsg.Type != "snapshot" || initMsg.Snapshot == nil {
		t.Fatalf("expected signed initial snapshot, got %+v", initMsg)
	}
	rawInit, _ := json.Marshal(initMsg)
	if strings.Contains(string(rawInit), "SUPERSECRET_TOKEN") {
		t.Fatal("WS snapshot leaked secret SlotConfig.Settings")
	}
	if initMsg.Snapshot.Slots["0,0"].BackgroundColor != "#FBBF24" {
		t.Fatalf("expected custom BackgroundColor #FBBF24 on (0,0), got %+v", initMsg.Snapshot.Slots["0,0"])
	}
	if initMsg.Snapshot.DialSlots[0].BackgroundColor != "#1E293B" {
		t.Fatalf("expected custom BackgroundColor #1E293B on dial 0, got %+v", initMsg.Snapshot.DialSlots[0])
	}
	bgAssetURL := initMsg.Snapshot.Slots["0,0"].BackgroundImageURL
	if !strings.HasPrefix(bgAssetURL, "/api/asset/") || !strings.Contains(bgAssetURL, "?sid="+wsA.sessionID+"&mac=") {
		t.Fatalf("expected signed /api/asset BackgroundImageURL, got %q", bgAssetURL)
	}
	if resp, err := http.Get(baseURL + bgAssetURL); err != nil {
		t.Fatalf("GET signed bg asset error = %v", err)
	} else {
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("signed bg asset status = %d, want 200", resp.StatusCode)
		}
	}
	if plainBG, _, _ := strings.Cut(bgAssetURL, "?"); true {
		if resp, err := http.Get(baseURL + plainBG); err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("unsigned bg asset status = %d, want 401", resp.StatusCode)
			}
		}
	}

	if !strings.HasPrefix(initMsg.Snapshot.Slots["0,1"].ImageURL, "data:image/svg+xml;base64,") {
		t.Fatalf("expected base64 normalized SVG, got %q", initMsg.Snapshot.Slots["0,1"].ImageURL)
	}
	signedAssetURL := initMsg.Snapshot.Slots["0,2"].ImageURL
	if !strings.HasPrefix(signedAssetURL, "/api/asset/") || !strings.Contains(signedAssetURL, "?sid="+wsA.sessionID+"&mac=") {
		t.Fatalf("expected signed /api/asset URL, got %q", signedAssetURL)
	}

	assetResp, err := http.Get(baseURL + signedAssetURL)
	if err != nil {
		t.Fatalf("GET signed asset error = %v", err)
	}
	assetBytes, _ := io.ReadAll(assetResp.Body)
	_ = assetResp.Body.Close()
	if assetResp.StatusCode != http.StatusOK || !strings.Contains(string(assetBytes), "#38BDF8") {
		t.Fatalf("unexpected signed asset response %d: %q", assetResp.StatusCode, string(assetBytes))
	}
	// Stripping query params fails with 401.
	plainAssetURL, _, _ := strings.Cut(signedAssetURL, "?")
	badAssetResp, _ := http.Get(baseURL + plainAssetURL)
	_ = badAssetResp.Body.Close()
	if badAssetResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unsigned asset request status = %d, want 401", badAssetResp.StatusCode)
	}

	// Send valid signed commands.
	for _, cmd := range []ClientCommand{
		{Type: "ping"},
		{Type: "keyDown", Row: 0, Col: 0},
		{Type: "keyUp", Row: 0, Col: 0},
		{Type: "dialRotate", Dial: 0, Ticks: 3},
		{Type: "dialPress", Dial: 0},
	} {
		wsA.sendSigned(t, cmd)
	}

	var gotPong bool
	for range 4 {
		m := wsA.recvSigned(t, 2*time.Second)
		if m.Type == "pong" {
			gotPong = true
			break
		}
	}
	if !gotPong {
		t.Fatal("expected signed pong response")
	}

	wantTypes := []plugin.EventType{plugin.EventKeyDown, plugin.EventKeyUp, plugin.EventDialRotate, plugin.EventDialPress}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		prov.mu.Lock()
		got := make([]plugin.EventType, len(prov.events))
		for i, ev := range prov.events {
			got[i] = ev.Type
		}
		prov.mu.Unlock()
		ok := true
		for _, w := range wantTypes {
			if !slices.Contains(got, w) {
				ok = false
				break
			}
		}
		if ok {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}

	// Live Engine page switch broadcasts signed snapshot.
	if _, err := eng.AddPage("Page 2"); err != nil {
		t.Fatalf("AddPage error = %v", err)
	}
	if err := eng.NextPage(true); err != nil {
		t.Fatalf("NextPage error = %v", err)
	}
	var sawPage2 bool
	for range 6 {
		m := wsA.recvSigned(t, 2*time.Second)
		if m.Type == "snapshot" && m.Snapshot != nil && m.Snapshot.PageIndex == 1 {
			sawPage2 = true
			break
		}
	}
	if !sawPage2 {
		t.Fatal("expected signed Page 2 snapshot broadcast")
	}

	// Tampered frame MAC must be rejected with auth_revoked and NOT dispatched.
	wsB := dialAuthenticatedWS(t, devB)
	_ = wsB.recvSigned(t, 2*time.Second) // initial snapshot
	tampered, _ := json.Marshal(WSSignedFrame{
		Seq:     1,
		Payload: `{"type":"nextPage"}`,
		MAC:     base64.StdEncoding.EncodeToString(make([]byte, 32)),
	})
	_ = websocket.Message.Send(wsB.conn, string(tampered))
	revMsg := wsB.recvSigned(t, 2*time.Second)
	if revMsg.Type != "auth_revoked" {
		t.Fatalf("expected auth_revoked on bad frame MAC, got %+v", revMsg)
	}
	_ = wsB.conn.Close()

	// Replayed client sequence number (even with a valid MAC for seq=1) must also be rejected.
	wsBReplay := dialAuthenticatedWS(t, devB)
	_ = wsBReplay.recvSigned(t, 2*time.Second)
	wsBReplay.sendSigned(t, ClientCommand{Type: "ping"})
	pongReplay := wsBReplay.recvSigned(t, 2*time.Second)
	if pongReplay.Type != "pong" {
		t.Fatalf("expected pong before replay test, got %+v", pongReplay)
	}
	wsBReplay.txSeq = 0 // Force seq=1 reuse
	wsBReplay.sendSigned(t, ClientCommand{Type: "ping"})
	var gotReplayRevoke bool
	for range 6 {
		m := wsBReplay.recvSigned(t, 2*time.Second)
		if m.Type == "auth_revoked" {
			gotReplayRevoke = true
			break
		}
	}
	if !gotReplayRevoke {
		t.Fatal("expected auth_revoked on replayed client seq=1 frame")
	}
	_ = wsBReplay.conn.Close()

	// Revoke Device A individually -> Device A gets auth_revoked & fails reconnect with auth_error.
	if err := srv.RevokeDevice(devA.deviceID); err != nil {
		t.Fatalf("RevokeDevice(devA) error = %v", err)
	}
	var gotRevokeA bool
	for range 8 {
		m := wsA.recvSigned(t, 2*time.Second)
		if m.Type == "auth_revoked" {
			gotRevokeA = true
			break
		}
	}
	if !gotRevokeA {
		t.Fatal("expected auth_revoked for revoked devA")
	}

	wsURLA := "ws" + strings.TrimPrefix(devA.baseURL, "http") + "/ws"
	wsCfgA, _ := websocket.NewConfig(wsURLA, devA.baseURL)
	connRevA, err := websocket.DialConfig(wsCfgA)
	if err != nil {
		t.Fatalf("dial revoked devA WS error = %v", err)
	}
	var rawHelloA string
	_ = connRevA.SetReadDeadline(time.Now().Add(2 * time.Second))
	_ = websocket.Message.Receive(connRevA, &rawHelloA)
	var helloA WSAuthHello
	_ = json.Unmarshal([]byte(rawHelloA), &helloA)
	sNonceA, _ := base64.StdEncoding.DecodeString(helloA.ServerNonce)
	cNonceA, _ := generateRandomBytes(32)
	ephPrivA, _ := generateX25519Keypair()
	t1A := buildWSTranscript1(devA.hostID, devA.deviceID, devA.hostPub, devA.pub, sNonceA, cNonceA, ephPrivA.PublicKey().Bytes())
	respRevA, _ := json.Marshal(WSAuthResponse{
		Type:         "auth_response",
		DeviceID:     devA.deviceID,
		ClientNonce:  base64.StdEncoding.EncodeToString(cNonceA),
		ClientEphPub: base64.StdEncoding.EncodeToString(ephPrivA.PublicKey().Bytes()),
		ClientSig:    base64.StdEncoding.EncodeToString(ed25519.Sign(devA.priv, t1A)),
	})
	_ = websocket.Message.Send(connRevA, string(respRevA))
	var rawErrA string
	_ = connRevA.SetReadDeadline(time.Now().Add(2 * time.Second))
	if err := websocket.Message.Receive(connRevA, &rawErrA); err != nil {
		t.Fatalf("recv auth_error on revoked reconnect: %v", err)
	}
	var errMsgA ServerMessage
	_ = json.Unmarshal([]byte(rawErrA), &errMsgA)
	if errMsgA.Type != "auth_error" {
		t.Fatalf("expected auth_error when reconnecting revoked devA, got %+v", errMsgA)
	}
	_ = connRevA.Close()

	// Reconnecting as devB succeeds; unpair command removes devB cleanly.
	wsB2 := dialAuthenticatedWS(t, devB)
	_ = wsB2.recvSigned(t, 2*time.Second)
	wsB2.sendSigned(t, ClientCommand{Type: "unpair"})
	var gotUnpairB bool
	for range 6 {
		m := wsB2.recvSigned(t, 2*time.Second)
		if m.Type == "auth_revoked" {
			gotUnpairB = true
			break
		}
	}
	if !gotUnpairB {
		t.Fatal("expected auth_revoked on self unpair")
	}
	_ = wsB2.conn.Close()

	if stEnd := srv.Status(); stEnd.PairedDevices != 0 {
		t.Fatalf("expected 0 paired devices after revoke + unpair, got %d", stEnd.PairedDevices)
	}
}
