package webdeck

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	qrcode "github.com/skip2/go-qrcode"
	"golang.org/x/net/websocket"

	"github.com/mrpoundsign/PoundDeck/assets"
	"github.com/mrpoundsign/PoundDeck/internal/app"
	"github.com/mrpoundsign/PoundDeck/pkg/deck"
	"github.com/mrpoundsign/PoundDeck/pkg/tile"
)

//go:embed static/*
var staticFS embed.FS

const (
	sessionCookieName  = "pounddeck_session"
	maxAssetCacheItems = 256
	pairFailLimit      = 5
	pairFailWindow     = 60 * time.Second
)

// Status describes the runtime state, endpoints, and pairing details of the LAN touch deck server.
type Status struct {
	Running          bool               `json:"running"`
	ListenAddr       string             `json:"listenAddr,omitempty"`
	Port             int                `json:"port"`
	BindMode         BindMode           `json:"bindMode"`
	AccessURL        string             `json:"accessUrl,omitempty"`
	PairURL          string             `json:"pairUrl,omitempty"`
	PIN              string             `json:"pin"`
	ConnectedClients int                `json:"connectedClients"`
	PairedDevices    int                `json:"pairedDevices"`
	Addresses        []InterfaceAddress `json:"addresses,omitempty"`
	LastError        string             `json:"lastError,omitempty"`
}

type assetEntry struct {
	filePath    string
	contentType string
}

type wsClient struct {
	conn        *websocket.Conn
	sessionHash string
	send        chan []byte
	done        chan struct{}
	closeOnce   sync.Once
}

func (c *wsClient) enqueue(payload []byte) {
	select {
	case <-c.done:
		return
	default:
	}
	select {
	case c.send <- payload:
	default:
		// Slow client: drop frame cleanly rather than stalling Engine broadcasts.
	}
}

func (c *wsClient) closeConn() {
	c.closeOnce.Do(func() {
		close(c.done)
		if c.conn != nil {
			_ = c.conn.Close()
		}
	})
}

type pairRateLimiter struct {
	mu    sync.Mutex
	fails map[string][]time.Time
}

func newPairRateLimiter() *pairRateLimiter {
	return &pairRateLimiter{fails: make(map[string][]time.Time)}
}

func (l *pairRateLimiter) allow(ip string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := now.Add(-pairFailWindow)
	filtered := pruneTimestamps(l.fails[ip], cutoff)
	if len(filtered) == 0 {
		delete(l.fails, ip)
		return true
	}
	l.fails[ip] = filtered
	return len(filtered) < pairFailLimit
}

func (l *pairRateLimiter) recordFailure(ip string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := now.Add(-pairFailWindow)
	filtered := pruneTimestamps(l.fails[ip], cutoff)
	filtered = append(filtered, now)
	l.fails[ip] = filtered
}

func (l *pairRateLimiter) clear(ip string) {
	l.mu.Lock()
	delete(l.fails, ip)
	l.mu.Unlock()
}

func pruneTimestamps(list []time.Time, cutoff time.Time) []time.Time {
	out := list[:0]
	for _, ts := range list {
		if ts.After(cutoff) {
			out = append(out, ts)
		}
	}
	return out
}

// Server hosts the authenticated LAN HTTP/WebSocket touch deck bound to an Engine.
type Server struct {
	engine *app.Engine

	opMu sync.Mutex
	mu   sync.RWMutex

	cfg        Config
	running    bool
	listenAddr string
	boundPort  int
	lastErr    string

	listener    net.Listener
	httpSrv     *http.Server
	unsubEngine func()

	clients    map[*wsClient]struct{}
	assets     map[string]assetEntry
	assetOrder []string
	lastSnap   *ClientSnapshot
	limiter    *pairRateLimiter

	subMu  sync.Mutex
	nextID uint64
	subs   map[uint64]func(Status)
}

// NewServer creates a LAN touch deck server synchronized with engine.
func NewServer(engine *app.Engine) *Server {
	s := &Server{
		engine:  engine,
		clients: make(map[*wsClient]struct{}),
		assets:  make(map[string]assetEntry),
		limiter: newPairRateLimiter(),
		subs:    make(map[uint64]func(Status)),
	}
	raw := engine.GlobalSettings(SettingsPluginUUID)
	cfg, needsSave := configFromSettings(raw)
	s.cfg = cfg
	if needsSave {
		_ = engine.SaveGlobalSettings(SettingsPluginUUID, configToSettings(cfg))
	}
	return s
}

// Config returns a defensive copy of the current web deck configuration.
func (s *Server) Config() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cp := s.cfg
	cp.Sessions = slices.Clone(s.cfg.Sessions)
	return cp
}

// SaveConfig validates and persists cfg, starting, restarting, or stopping the server as configured.
func (s *Server) SaveConfig(cfg Config) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	s.mu.Lock()
	merged := cfg.normalized()
	if !isValidPIN(merged.PIN) {
		merged.PIN = s.cfg.PIN
	}
	if len(merged.PairToken) < 16 {
		merged.PairToken = s.cfg.PairToken
	}
	if cfg.Sessions == nil {
		merged.Sessions = slices.Clone(s.cfg.Sessions)
	}
	prevRunning := s.running
	prevBind := s.cfg.BindMode
	prevPort := s.cfg.Port
	s.cfg = merged
	s.mu.Unlock()

	if err := s.engine.SaveGlobalSettings(SettingsPluginUUID, configToSettings(merged)); err != nil {
		return err
	}

	var opErr error
	switch {
	case !merged.Enabled:
		opErr = s.stopLocked()
	case !prevRunning || prevBind != merged.BindMode || (merged.Port != 0 && prevPort != merged.Port):
		if prevRunning {
			_ = s.stopLocked()
		}
		opErr = s.startLocked()
	}

	s.notifyStatus()
	return opErr
}

// StartFromConfig starts the server automatically only if Enabled is true in persisted settings.
func (s *Server) StartFromConfig() error {
	if !s.Config().Enabled {
		return nil
	}
	return s.Start()
}

// Start opens the HTTP/WebSocket listener using the current Config.
func (s *Server) Start() error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	err := s.startLocked()
	s.notifyStatus()
	return err
}

func (s *Server) startLocked() error {
	s.mu.RLock()
	alreadyRunning := s.running
	cfg := s.cfg
	s.mu.RUnlock()
	if alreadyRunning {
		return nil
	}

	host := "0.0.0.0"
	if cfg.BindMode == BindLocalhost {
		host = "127.0.0.1"
	}
	addr := net.JoinHostPort(host, strconv.Itoa(cfg.Port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		s.mu.Lock()
		s.lastErr = err.Error()
		s.mu.Unlock()
		return fmt.Errorf("listen web deck on %s: %w", addr, err)
	}

	tcpPort := 0
	if tcpAddr, ok := ln.Addr().(*net.TCPAddr); ok {
		tcpPort = tcpAddr.Port
	}

	srv := &http.Server{
		Handler:           s.routes(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	s.refreshSnapshotFromEngine(s.engine.Snapshot())
	unsub := s.engine.Subscribe(s.onEngineSnapshot)

	s.mu.Lock()
	s.listener = ln
	s.httpSrv = srv
	s.listenAddr = ln.Addr().String()
	s.boundPort = tcpPort
	s.running = true
	s.lastErr = ""
	s.unsubEngine = unsub
	s.mu.Unlock()

	go func() {
		if serveErr := srv.Serve(ln); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			s.mu.Lock()
			if s.httpSrv == srv {
				s.lastErr = serveErr.Error()
				s.running = false
			}
			s.mu.Unlock()
			s.notifyStatus()
		}
	}()
	return nil
}

// Stop shuts down the active HTTP/WebSocket listener and disconnects active clients.
func (s *Server) Stop() error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	err := s.stopLocked()
	s.notifyStatus()
	return err
}

// Close implements clean shutdown on application exit.
func (s *Server) Close() error {
	return s.Stop()
}

func (s *Server) stopLocked() error {
	s.mu.Lock()
	srv := s.httpSrv
	ln := s.listener
	unsub := s.unsubEngine
	clients := make([]*wsClient, 0, len(s.clients))
	for c := range s.clients {
		clients = append(clients, c)
		delete(s.clients, c)
	}
	s.httpSrv = nil
	s.listener = nil
	s.unsubEngine = nil
	s.running = false
	s.listenAddr = ""
	s.boundPort = 0
	s.mu.Unlock()

	if unsub != nil {
		unsub()
	}
	for _, c := range clients {
		c.closeConn()
	}

	var errs []error
	if srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		if err := srv.Shutdown(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs = append(errs, err)
		}
		cancel()
	}
	if ln != nil {
		if err := ln.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// RegeneratePairing issues a new 6-digit PIN and QR pairing token, optionally revoking active sessions.
func (s *Server) RegeneratePairing(revokeSessions bool) (Config, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	pin, err := generatePIN()
	if err != nil {
		return Config{}, err
	}
	tok, err := generateRandomHex(16)
	if err != nil {
		return Config{}, err
	}

	s.mu.Lock()
	next := s.cfg
	next.PIN = pin
	next.PairToken = tok
	if revokeSessions {
		next.Sessions = nil
	}
	s.cfg = next
	var disconnect []*wsClient
	if revokeSessions {
		for c := range s.clients {
			disconnect = append(disconnect, c)
		}
	}
	s.mu.Unlock()

	if err := s.engine.SaveGlobalSettings(SettingsPluginUUID, configToSettings(next)); err != nil {
		return Config{}, err
	}
	if len(disconnect) > 0 {
		s.revokeClients(disconnect)
	}
	s.notifyStatus()

	cp := next
	cp.Sessions = slices.Clone(next.Sessions)
	return cp, nil
}

// RevokeAllSessions invalidates all paired device session tokens and disconnects active WebSockets.
func (s *Server) RevokeAllSessions() error {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	s.mu.Lock()
	next := s.cfg
	next.Sessions = nil
	s.cfg = next
	disconnect := make([]*wsClient, 0, len(s.clients))
	for c := range s.clients {
		disconnect = append(disconnect, c)
	}
	s.mu.Unlock()

	if err := s.engine.SaveGlobalSettings(SettingsPluginUUID, configToSettings(next)); err != nil {
		return err
	}
	if len(disconnect) > 0 {
		s.revokeClients(disconnect)
	}
	s.notifyStatus()
	return nil
}

func (s *Server) revokeClients(clients []*wsClient) {
	msg, _ := json.Marshal(ServerMessage{
		Type:    "auth_revoked",
		Message: "Device access revoked on host",
	})
	for _, c := range clients {
		c.enqueue(msg)
		c.closeConn()
	}
}

// Status returns a snapshot of current server state, URLs, client count, and pairing PIN.
func (s *Server) Status() Status {
	addrs := DiscoverLANAddresses()

	s.mu.RLock()
	running := s.running
	listenAddr := s.listenAddr
	cfg := s.cfg
	port := s.boundPort
	if !running || port == 0 {
		port = cfg.Port
	}
	clientCnt := len(s.clients)
	pairedCnt := len(cfg.Sessions)
	lastErr := s.lastErr
	s.mu.RUnlock()

	host := selectDisplayHost(cfg.BindMode, cfg.PreferredHost, addrs)
	accessURL := fmt.Sprintf("http://%s:%d/", host, port)
	pairURL := accessURL
	if cfg.PairToken != "" {
		pairURL = accessURL + "?pair=" + url.QueryEscape(cfg.PairToken)
	}

	return Status{
		Running:          running,
		ListenAddr:       listenAddr,
		Port:             port,
		BindMode:         cfg.BindMode,
		AccessURL:        accessURL,
		PairURL:          pairURL,
		PIN:              cfg.PIN,
		ConnectedClients: clientCnt,
		PairedDevices:    pairedCnt,
		Addresses:        addrs,
		LastError:        lastErr,
	}
}

// SubscribeStatus registers fn to receive server status changes until the returned unsubscribe func is called.
func (s *Server) SubscribeStatus(fn func(Status)) func() {
	if fn == nil {
		return func() {}
	}
	s.subMu.Lock()
	s.nextID++
	id := s.nextID
	s.subs[id] = fn
	s.subMu.Unlock()

	return func() {
		s.subMu.Lock()
		delete(s.subs, id)
		s.subMu.Unlock()
	}
}

func (s *Server) notifyStatus() {
	st := s.Status()
	s.subMu.Lock()
	listeners := make([]func(Status), 0, len(s.subs))
	for _, fn := range s.subs {
		listeners = append(listeners, fn)
	}
	s.subMu.Unlock()

	for _, fn := range listeners {
		fn(st)
	}
}

// QRImage encodes Status().PairURL as a high-contrast square QR code image of pixel size x size.
func (s *Server) QRImage(size int) (image.Image, error) {
	if size <= 0 {
		size = 200
	}
	st := s.Status()
	qr, err := qrcode.New(st.PairURL, qrcode.Medium)
	if err != nil {
		return nil, fmt.Errorf("encode pairing QR: %w", err)
	}
	qr.ForegroundColor = color.RGBA{R: 10, G: 10, B: 12, A: 255}
	qr.BackgroundColor = color.White
	return qr.Image(size), nil
}

func selectDisplayHost(mode BindMode, preferred string, addrs []InterfaceAddress) string {
	if mode == BindLocalhost {
		return "127.0.0.1"
	}
	if preferred != "" {
		for _, a := range addrs {
			if a.IP == preferred {
				return preferred
			}
		}
	}
	for _, a := range addrs {
		if a.Preferred {
			return a.IP
		}
	}
	if len(addrs) > 0 {
		return addrs[0].IP
	}
	return "127.0.0.1"
}

func (s *Server) onEngineSnapshot(snap app.Snapshot) {
	clientSnap := s.refreshSnapshotFromEngine(snap)
	payload, err := json.Marshal(ServerMessage{
		Type:     "snapshot",
		Snapshot: &clientSnap,
	})
	if err != nil {
		return
	}

	s.mu.RLock()
	clients := make([]*wsClient, 0, len(s.clients))
	for c := range s.clients {
		clients = append(clients, c)
	}
	s.mu.RUnlock()

	for _, c := range clients {
		c.enqueue(payload)
	}
}

func (s *Server) refreshSnapshotFromEngine(snap app.Snapshot) ClientSnapshot {
	clientSnap := buildClientSnapshot(snap, s.registerLocalAsset)
	s.mu.Lock()
	s.lastSnap = &clientSnap
	s.mu.Unlock()
	return clientSnap
}

func (s *Server) registerLocalAsset(uri string) string {
	resolvedPath, mimeType, ok := tile.ResolveImageFile(uri)
	if !ok {
		return ""
	}
	sum := sha256.Sum256([]byte(resolvedPath))
	id := hex.EncodeToString(sum[:12])

	s.mu.Lock()
	if _, exists := s.assets[id]; !exists {
		if len(s.assetOrder) >= maxAssetCacheItems {
			evict := s.assetOrder[0]
			copy(s.assetOrder, s.assetOrder[1:])
			s.assetOrder[len(s.assetOrder)-1] = ""
			s.assetOrder = s.assetOrder[:len(s.assetOrder)-1]
			delete(s.assets, evict)
		}
		s.assets[id] = assetEntry{
			filePath:    resolvedPath,
			contentType: mimeType,
		}
		s.assetOrder = append(s.assetOrder, id)
	}
	s.mu.Unlock()
	return "/api/asset/" + id
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", withSecurityHeaders(true, s.handleIndex))
	mux.HandleFunc("GET /styles.css", withSecurityHeaders(false, s.serveEmbedded("static/styles.css", "text/css; charset=utf-8")))
	mux.HandleFunc("GET /app.js", withSecurityHeaders(false, s.serveEmbedded("static/app.js", "application/javascript; charset=utf-8")))
	mux.HandleFunc("GET /manifest.webmanifest", withSecurityHeaders(false, s.serveEmbedded("static/manifest.webmanifest", "application/manifest+json; charset=utf-8")))
	mux.HandleFunc("GET /icon.png", withSecurityHeaders(false, s.handleAppIcon))
	mux.HandleFunc("GET /api/auth/status", withSecurityHeaders(false, s.handleAuthStatus))
	mux.HandleFunc("POST /api/pair", withSecurityHeaders(false, s.handlePair))
	mux.HandleFunc("POST /api/logout", withSecurityHeaders(false, s.handleLogout))
	mux.HandleFunc("GET /api/asset/{id}", withSecurityHeaders(false, s.handleAsset))
	mux.HandleFunc("GET /ws", withSecurityHeaders(false, s.handleWSUpgrade))
	return mux
}

func withSecurityHeaders(isHTML bool, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), geolocation=(), microphone=()")
		if isHTML {
			h.Set("Content-Security-Policy",
				"default-src 'self'; script-src 'self'; style-src 'self'; "+
					"img-src 'self' data:; connect-src 'self' ws: wss:; manifest-src 'self'; "+
					"object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		}
		next(w, r)
	}
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if pairTok := strings.TrimSpace(r.URL.Query().Get("pair")); pairTok != "" {
		if s.verifyPairToken(pairTok) {
			if rawTok, err := s.mintSession(); err == nil {
				setSessionCookie(w, r, rawTok, 30*24*3600)
				w.Header().Set("Cache-Control", "no-store")
				http.Redirect(w, r, "/", http.StatusSeeOther)
				return
			}
		}
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	raw, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		http.Error(w, "touch deck UI missing", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(raw)
}

func (s *Server) serveEmbedded(path, contentType string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		raw, err := staticFS.ReadFile(path)
		if err != nil {
			http.NotFound(w, nil)
			return
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(raw)
	}
}

func (s *Server) handleAppIcon(w http.ResponseWriter, _ *http.Request) {
	if assets.AppIcon == nil {
		http.NotFound(w, nil)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = w.Write(assets.AppIcon.Content())
}

func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	_, authed := s.authenticatedSessionHash(r)
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": authed})
}

func (s *Server) handlePair(w http.ResponseWriter, r *http.Request) {
	if !isSameOriginRequest(r) {
		http.Error(w, "forbidden origin", http.StatusForbidden)
		return
	}
	ip := clientIPKey(r.RemoteAddr)
	now := time.Now()
	if !s.limiter.allow(ip, now) {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{
			"error": "Too many failed pairing attempts. Please wait 60 seconds and try again.",
		})
		return
	}

	var req struct {
		PIN       string `json:"pin"`
		PairToken string `json:"pairToken"`
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, 4096))
	if err := dec.Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON payload"})
		return
	}

	pin := strings.TrimSpace(req.PIN)
	tok := strings.TrimSpace(req.PairToken)

	s.mu.RLock()
	expectedPIN := s.cfg.PIN
	expectedTok := s.cfg.PairToken
	s.mu.RUnlock()

	valid := (pin != "" && constantTimeEqual(pin, expectedPIN)) ||
		(tok != "" && constantTimeEqual(tok, expectedTok))
	if !valid {
		s.limiter.recordFailure(ip, now)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Invalid pairing PIN"})
		return
	}

	s.limiter.clear(ip)
	rawTok, err := s.mintSession()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to create session"})
		return
	}
	setSessionCookie(w, r, rawTok, 30*24*3600)
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": true})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if !isSameOriginRequest(r) {
		http.Error(w, "forbidden origin", http.StatusForbidden)
		return
	}
	if sessHash, ok := s.authenticatedSessionHash(r); ok {
		s.removeSessionHash(sessHash)
	}
	setSessionCookie(w, r, "", -1)
	w.Header().Set("Clear-Site-Data", `"cookies", "storage"`)
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": false})
}

func (s *Server) handleAsset(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authenticatedSessionHash(r); !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		http.NotFound(w, r)
		return
	}
	s.mu.RLock()
	entry, ok := s.assets[id]
	s.mu.RUnlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	raw, err := os.ReadFile(entry.filePath)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", entry.contentType)
	w.Header().Set("Cache-Control", "private, max-age=300")
	if entry.contentType == "image/svg+xml" {
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	}
	_, _ = w.Write(raw)
}

func (s *Server) handleWSUpgrade(w http.ResponseWriter, r *http.Request) {
	if !isSameOriginWebSocket(r) {
		http.Error(w, "forbidden websocket origin", http.StatusForbidden)
		return
	}
	sessHash, ok := s.authenticatedSessionHash(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	wsSrv := websocket.Server{
		Handshake: func(_ *websocket.Config, _ *http.Request) error {
			return nil
		},
		Handler: func(conn *websocket.Conn) {
			s.serveWSClient(conn, sessHash)
		},
	}
	wsSrv.ServeHTTP(w, r)
}

func (s *Server) serveWSClient(conn *websocket.Conn, sessHash string) {
	client := &wsClient{
		conn:        conn,
		sessionHash: sessHash,
		send:        make(chan []byte, 32),
		done:        make(chan struct{}),
	}

	s.mu.Lock()
	s.clients[client] = struct{}{}
	snapCopy := s.lastSnap
	s.mu.Unlock()

	if snapCopy == nil {
		cs := s.refreshSnapshotFromEngine(s.engine.Snapshot())
		snapCopy = &cs
	}
	if initPayload, err := json.Marshal(ServerMessage{Type: "snapshot", Snapshot: snapCopy}); err == nil {
		client.enqueue(initPayload)
	}
	s.notifyStatus()

	var wg sync.WaitGroup
	wg.Go(func() {
		for {
			select {
			case <-client.done:
				return
			case msg := <-client.send:
				_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
				err := websocket.Message.Send(conn, string(msg))
				_ = conn.SetWriteDeadline(time.Time{})
				if err != nil {
					client.closeConn()
					return
				}
			}
		}
	})

	for {
		var raw string
		if err := websocket.Message.Receive(conn, &raw); err != nil {
			break
		}
		var cmd ClientCommand
		if err := json.Unmarshal([]byte(raw), &cmd); err != nil {
			continue
		}
		if err := s.executeCommand(client, cmd); err != nil {
			if errPayload, mErr := json.Marshal(ServerMessage{Type: "error", Message: err.Error()}); mErr == nil {
				client.enqueue(errPayload)
			}
		}
	}

	client.closeConn()
	wg.Wait()

	s.mu.Lock()
	delete(s.clients, client)
	s.mu.Unlock()
	s.notifyStatus()
}

func (s *Server) executeCommand(client *wsClient, cmd ClientCommand) error {
	ctx := context.Background()
	snap := s.engine.Snapshot()

	switch cmd.Type {
	case "ping":
		if pong, err := json.Marshal(ServerMessage{Type: "pong"}); err == nil {
			client.enqueue(pong)
		}
		return nil
	case "keyDown":
		coords := deck.Coordinates{Row: cmd.Row, Col: cmd.Col}
		if !snap.Grid.Contains(coords) {
			return fmt.Errorf("invalid keypad coordinates (%d, %d)", cmd.Row, cmd.Col)
		}
		return s.engine.PressKey(ctx, coords)
	case "keyUp":
		coords := deck.Coordinates{Row: cmd.Row, Col: cmd.Col}
		if !snap.Grid.Contains(coords) {
			return nil
		}
		return s.engine.ReleaseKey(ctx, coords)
	case "dialRotate":
		if cmd.Dial < 0 || cmd.Dial >= snap.Dials {
			return fmt.Errorf("invalid dial index %d", cmd.Dial)
		}
		ticks := max(-20, min(20, cmd.Ticks))
		if ticks == 0 {
			return nil
		}
		return s.engine.RotateDial(ctx, cmd.Dial, ticks)
	case "dialPress":
		if cmd.Dial < 0 || cmd.Dial >= snap.Dials {
			return fmt.Errorf("invalid dial index %d", cmd.Dial)
		}
		return s.engine.PressDial(ctx, cmd.Dial)
	case "prevPage":
		return s.engine.PrevPage(true)
	case "nextPage":
		return s.engine.NextPage(true)
	case "closeFolder":
		return s.engine.CloseFolder()
	case "switchProfile":
		if strings.TrimSpace(cmd.ProfileID) == "" {
			return nil
		}
		return s.engine.SwitchProfile(cmd.ProfileID)
	default:
		return fmt.Errorf("unsupported command %q", cmd.Type)
	}
}

func (s *Server) verifyPairToken(tok string) bool {
	s.mu.RLock()
	expected := s.cfg.PairToken
	s.mu.RUnlock()
	return constantTimeEqual(tok, expected)
}

func (s *Server) authenticatedSessionHash(r *http.Request) (string, bool) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || strings.TrimSpace(cookie.Value) == "" {
		return "", false
	}
	digest := hashToken(cookie.Value)
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, valid := range s.cfg.Sessions {
		if constantTimeEqual(digest, valid) {
			return digest, true
		}
	}
	return "", false
}

func (s *Server) mintSession() (string, error) {
	raw, err := generateRandomHex(32)
	if err != nil {
		return "", err
	}
	digest := hashToken(raw)

	s.opMu.Lock()
	defer s.opMu.Unlock()

	s.mu.Lock()
	next := s.cfg
	sessions := slices.Clone(next.Sessions)
	sessions = append(sessions, digest)
	if len(sessions) > maxPersistedSessions {
		sessions = sessions[len(sessions)-maxPersistedSessions:]
	}
	next.Sessions = sessions
	s.cfg = next
	s.mu.Unlock()

	if err := s.engine.SaveGlobalSettings(SettingsPluginUUID, configToSettings(next)); err != nil {
		return "", err
	}
	s.notifyStatus()
	return raw, nil
}

func (s *Server) removeSessionHash(digest string) {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	s.mu.Lock()
	next := s.cfg
	filtered := make([]string, 0, len(next.Sessions))
	for _, existing := range next.Sessions {
		if existing != digest {
			filtered = append(filtered, existing)
		}
	}
	next.Sessions = filtered
	s.cfg = next
	var disconnect []*wsClient
	for c := range s.clients {
		if c.sessionHash == digest {
			disconnect = append(disconnect, c)
		}
	}
	s.mu.Unlock()

	_ = s.engine.SaveGlobalSettings(SettingsPluginUUID, configToSettings(next))
	for _, c := range disconnect {
		c.closeConn()
	}
	s.notifyStatus()
}

func setSessionCookie(w http.ResponseWriter, r *http.Request, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   r.TLS != nil,
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func isSameOriginRequest(r *http.Request) bool {
	if site := strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")); site != "" {
		if site != "same-origin" && site != "none" {
			return false
		}
	}
	if origin := strings.TrimSpace(r.Header.Get("Origin")); origin != "" {
		parsed, err := url.Parse(origin)
		if err != nil || !strings.EqualFold(parsed.Host, r.Host) {
			return false
		}
	}
	return true
}

func isSameOriginWebSocket(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return isSameOriginRequest(r)
	}
	parsed, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return strings.EqualFold(parsed.Host, r.Host)
}

func clientIPKey(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err == nil && host != "" {
		return host
	}
	return remoteAddr
}
