package webdeck

import (
	"context"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"embed"
	"encoding/base64"
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

	"github.com/mrpoundsign/PoundControl/assets"
	"github.com/mrpoundsign/PoundControl/internal/app"
	"github.com/mrpoundsign/PoundControl/pkg/deck"
	"github.com/mrpoundsign/PoundControl/pkg/secrets"
	"github.com/mrpoundsign/PoundControl/pkg/tile"
)

//go:embed static/*
var staticFS embed.FS

const (
	// DefaultPairingTTL is the default duration an on-demand PIN/QR pairing window stays open.
	DefaultPairingTTL = 2 * time.Minute

	maxAssetCacheItems     = 256
	pairFailLimit          = 5
	pairFailWindow         = 60 * time.Second
	maxPendingPairAttempts = 4
	wsAuthTimeout          = 8 * time.Second
	assetPrefix            = "/api/asset/"
)

// DeviceStatus summarizes one paired remote device along with live connection state.
type DeviceStatus struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Fingerprint string `json:"fingerprint"`
	PairedAt    string `json:"pairedAt"`
	LastSeen    string `json:"lastSeen,omitempty"`
	Connected   bool   `json:"connected"`
}

// PendingPairInfo describes a SPAKE2-verified pairing request awaiting dual-screen confirmation.
type PendingPairInfo struct {
	AttemptID         string    `json:"attemptId"`
	DeviceName        string    `json:"deviceName"`
	RemoteAddr        string    `json:"remoteAddr"`
	ClientFingerprint string    `json:"clientFingerprint"`
	SAS               string    `json:"sas"`
	HostApproved      bool      `json:"hostApproved"`
	ClientConfirmed   bool      `json:"clientConfirmed"`
	ExpiresAt         time.Time `json:"expiresAt"`
}

// Status describes the runtime state, host identity, pairing window, and paired devices of the LAN touch deck.
type Status struct {
	Running          bool               `json:"running"`
	ListenAddr       string             `json:"listenAddr,omitempty"`
	Port             int                `json:"port"`
	BindMode         BindMode           `json:"bindMode"`
	AccessURL        string             `json:"accessUrl,omitempty"`
	PairURL          string             `json:"pairUrl,omitempty"`
	HostID           string             `json:"hostId"`
	HostName         string             `json:"hostName"`
	HostFingerprint  string             `json:"hostFingerprint"`
	PairingOpen      bool               `json:"pairingOpen"`
	PairingExpiresAt time.Time          `json:"pairingExpiresAt,omitzero"`
	PIN              string             `json:"pin,omitempty"`
	ConnectedClients int                `json:"connectedClients"`
	PairedDevices    int                `json:"pairedDevices"`
	Devices          []DeviceStatus     `json:"devices,omitempty"`
	PendingPair      *PendingPairInfo   `json:"pendingPair,omitempty"`
	Addresses        []InterfaceAddress `json:"addresses,omitempty"`
	LastError        string             `json:"lastError,omitempty"`
}

type assetEntry struct {
	filePath    string
	contentType string
}

type activePairingWindow struct {
	id        string
	pin       string
	expiresAt time.Time
	timer     *time.Timer
}

type pendingPairAttempt struct {
	attemptID         string
	windowID          string
	remoteIP          string
	deviceName        string
	clientPub         []byte
	clientFingerprint string
	state             string // "started", "awaiting_confirmation", "approved", "denied", "expired"
	spake             spake2Material
	hostApproved      bool
	clientConfirmed   bool
	enrolledDeviceID  string
	expiresAt         time.Time
}

type wsSendItem struct {
	payload    string
	closeAfter bool
}

type wsClient struct {
	conn         *websocket.Conn
	deviceID     string
	sessionID    string
	clientMacKey []byte
	serverMacKey []byte
	assetMacKey  []byte
	send         chan wsSendItem
	done         chan struct{}
	closeOnce    sync.Once
}

func (c *wsClient) enqueuePayload(payload string, closeAfter bool) {
	select {
	case <-c.done:
		return
	default:
	}
	select {
	case c.send <- wsSendItem{payload: payload, closeAfter: closeAfter}:
	default:
		if closeAfter {
			c.closeConn()
		}
	}
}

func (c *wsClient) sendSnapshot(snap ClientSnapshot) {
	signed := signSnapshotAssets(snap, c.sessionID, c.assetMacKey)
	raw, err := json.Marshal(ServerMessage{
		Type:     "snapshot",
		Snapshot: &signed,
	})
	if err != nil {
		return
	}
	c.enqueuePayload(string(raw), false)
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

// Server hosts the mutually authenticated LAN HTTP/WebSocket touch deck bound to an Engine.
type Server struct {
	engine      *app.Engine
	secretStore secrets.Store

	opMu sync.Mutex
	mu   sync.RWMutex

	cfg        Config
	hostName   string
	hostPriv   ed25519.PrivateKey
	hostPub    []byte
	hostFp     string
	running    bool
	listenAddr string
	boundPort  int
	lastErr    string

	listener    net.Listener
	httpSrv     *http.Server
	unsubEngine func()

	clients         map[*wsClient]struct{}
	sessions        map[string]*wsClient
	assets          map[string]assetEntry
	assetOrder      []string
	lastSnap        *ClientSnapshot
	limiter         *pairRateLimiter
	pairWindow      *activePairingWindow
	pendingAttempts map[string]*pendingPairAttempt
	attemptOrder    []string

	subMu  sync.Mutex
	nextID uint64
	subs   map[uint64]func(Status)
}

// NewServer creates a LAN touch deck server synchronized with engine and backed by secretStore.
// If secretStore is nil, an ephemeral in-memory secret store is used (recommended for tests).
func NewServer(engine *app.Engine, secretStore secrets.Store) *Server {
	if secretStore == nil {
		secretStore = secrets.NewMemoryStore()
	}
	s := &Server{
		engine:          engine,
		secretStore:     secretStore,
		hostName:        DefaultHostName(),
		clients:         make(map[*wsClient]struct{}),
		sessions:        make(map[string]*wsClient),
		assets:          make(map[string]assetEntry),
		limiter:         newPairRateLimiter(),
		pendingAttempts: make(map[string]*pendingPairAttempt),
		subs:            make(map[uint64]func(Status)),
	}

	raw := engine.GlobalSettings(SettingsPluginUUID)
	cfg, needsSave := configFromSettings(raw)
	cfg, hostPriv, idChanged, err := ensureHostIdentity(cfg, secretStore)
	if err == nil {
		s.hostPriv = hostPriv
		s.hostPub = append([]byte(nil), hostPriv[ed25519.SeedSize:]...)
		s.hostFp = PublicKeyFingerprint(s.hostPub)
	}
	if idChanged {
		needsSave = true
	}
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
	cp.Devices = slices.Clone(s.cfg.Devices)
	return cp
}

// SaveConfig validates and persists cfg, starting, restarting, or stopping the server as configured.
func (s *Server) SaveConfig(cfg Config) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	s.mu.Lock()
	merged := cfg.normalized()
	merged.HostID = s.cfg.HostID
	merged.HostPublicKey = s.cfg.HostPublicKey
	if cfg.Devices == nil {
		merged.Devices = slices.Clone(s.cfg.Devices)
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

// Stop shuts down the active HTTP/WebSocket listener, closes pairing windows, and disconnects clients.
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
	clear(s.sessions)
	s.closePairingWindowLocked()
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

// OpenPairingWindow opens a temporary PIN/QR pairing window (default 2 minutes).
func (s *Server) OpenPairingWindow(ttl time.Duration) (Status, error) {
	if ttl <= 0 {
		ttl = DefaultPairingTTL
	}
	pin, err := generatePIN()
	if err != nil {
		return Status{}, err
	}
	winID, err := generateRandomHex(16)
	if err != nil {
		return Status{}, err
	}
	expiresAt := time.Now().Add(ttl)

	s.mu.Lock()
	s.closePairingWindowLocked()
	win := &activePairingWindow{
		id:        winID,
		pin:       pin,
		expiresAt: expiresAt,
		timer: time.AfterFunc(ttl, func() {
			s.expirePairingWindow(winID)
		}),
	}
	s.pairWindow = win
	s.mu.Unlock()

	s.notifyStatus()
	return s.Status(), nil
}

// ClosePairingWindow closes any active pairing window and expires pending confirmations.
func (s *Server) ClosePairingWindow() {
	s.mu.Lock()
	s.closePairingWindowLocked()
	s.mu.Unlock()
	s.notifyStatus()
}

func (s *Server) expirePairingWindow(winID string) {
	s.mu.Lock()
	if s.pairWindow == nil || s.pairWindow.id != winID {
		s.mu.Unlock()
		return
	}
	s.closePairingWindowLocked()
	s.mu.Unlock()
	s.notifyStatus()
}

func (s *Server) closePairingWindowLocked() {
	if s.pairWindow != nil {
		if s.pairWindow.timer != nil {
			s.pairWindow.timer.Stop()
		}
		winID := s.pairWindow.id
		for _, att := range s.pendingAttempts {
			if att.windowID == winID && (att.state == "started" || att.state == "awaiting_confirmation") {
				att.state = "expired"
			}
		}
		s.pairWindow = nil
	}
}

// ResolvePendingPair records desktop user approval or denial for an active SAS pairing attempt.
// If approved and the client has already confirmed its SAS screen, the device is enrolled immediately.
func (s *Server) ResolvePendingPair(attemptID string, approve bool) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	s.mu.Lock()
	att, ok := s.pendingAttempts[attemptID]
	if !ok {
		s.mu.Unlock()
		return errors.New("pairing attempt not found")
	}
	now := time.Now()
	if now.After(att.expiresAt) || att.state == "expired" || att.state == "denied" {
		if att.state == "started" || att.state == "awaiting_confirmation" {
			att.state = "expired"
		}
		s.mu.Unlock()
		s.notifyStatus()
		return errors.New("pairing attempt is no longer active")
	}
	if att.state == "approved" {
		s.mu.Unlock()
		return nil
	}
	if !approve {
		att.state = "denied"
		s.mu.Unlock()
		s.notifyStatus()
		return nil
	}

	att.hostApproved = true
	if !att.clientConfirmed {
		s.mu.Unlock()
		s.notifyStatus()
		return nil
	}

	nextCfg := s.enrollAttemptLocked(att, now)
	s.mu.Unlock()

	if err := s.engine.SaveGlobalSettings(SettingsPluginUUID, configToSettings(nextCfg)); err != nil {
		return err
	}
	s.notifyStatus()
	return nil
}

func (s *Server) enrollAttemptLocked(att *pendingPairAttempt, now time.Time) Config {
	nowRFC := now.UTC().Format(time.RFC3339)
	pubB64 := base64.StdEncoding.EncodeToString(att.clientPub)

	var dev PairedDevice
	devices := slices.Clone(s.cfg.Devices)
	replaced := false
	for i := range devices {
		if devices[i].PublicKey == pubB64 {
			devices[i].Name = att.deviceName
			devices[i].LastSeen = nowRFC
			dev = devices[i]
			replaced = true
			break
		}
	}
	if !replaced {
		dev = PairedDevice{
			ID:          deck.NewUUID(),
			Name:        att.deviceName,
			PublicKey:   pubB64,
			Fingerprint: att.clientFingerprint,
			PairedAt:    nowRFC,
			LastSeen:    nowRFC,
		}
		devices = append(devices, dev)
		if len(devices) > maxPairedDevices {
			devices = devices[len(devices)-maxPairedDevices:]
		}
	}

	next := s.cfg
	next.Devices = devices
	s.cfg = next

	att.state = "approved"
	att.enrolledDeviceID = dev.ID
	s.closePairingWindowLocked()
	return next
}

// RevokeDevice removes a single paired device and disconnects any active WebSocket sessions belonging to it.
func (s *Server) RevokeDevice(deviceID string) error {
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" {
		return nil
	}
	s.opMu.Lock()
	defer s.opMu.Unlock()

	s.mu.Lock()
	next := s.cfg
	filtered := make([]PairedDevice, 0, len(next.Devices))
	found := false
	for _, dev := range next.Devices {
		if dev.ID == deviceID {
			found = true
			continue
		}
		filtered = append(filtered, dev)
	}
	if !found {
		s.mu.Unlock()
		return fmt.Errorf("device %q not found", deviceID)
	}
	next.Devices = filtered
	s.cfg = next

	var disconnect []*wsClient
	for c := range s.clients {
		if c.deviceID == deviceID {
			disconnect = append(disconnect, c)
		}
	}
	s.mu.Unlock()

	if err := s.engine.SaveGlobalSettings(SettingsPluginUUID, configToSettings(next)); err != nil {
		return err
	}
	if len(disconnect) > 0 {
		s.revokeClients(disconnect, "Device access revoked on host")
	}
	s.notifyStatus()
	return nil
}

// RevokeAllSessions removes all paired devices and disconnects active WebSockets.
func (s *Server) RevokeAllSessions() error {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	s.mu.Lock()
	next := s.cfg
	next.Devices = nil
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
		s.revokeClients(disconnect, "All device access revoked on host")
	}
	s.notifyStatus()
	return nil
}

func (s *Server) revokeClients(clients []*wsClient, reason string) {
	msg, _ := json.Marshal(ServerMessage{
		Type:    "auth_revoked",
		Message: reason,
	})
	payload := string(msg)
	for _, c := range clients {
		c.enqueuePayload(payload, true)
	}
}

// Status returns a snapshot of current server state, host fingerprint, pairing window, and paired devices.
func (s *Server) Status() Status {
	addrs := DiscoverLANAddresses()
	now := time.Now()

	s.mu.RLock()
	running := s.running
	listenAddr := s.listenAddr
	cfg := s.cfg
	hostName := s.hostName
	hostFp := s.hostFp
	port := s.boundPort
	if !running || port == 0 {
		port = cfg.Port
	}
	clientCnt := len(s.clients)
	lastErr := s.lastErr

	connectedByDev := make(map[string]bool, len(s.clients))
	for c := range s.clients {
		connectedByDev[c.deviceID] = true
	}

	devices := make([]DeviceStatus, len(cfg.Devices))
	for i, d := range cfg.Devices {
		devices[i] = DeviceStatus{
			ID:          d.ID,
			Name:        d.Name,
			Fingerprint: d.Fingerprint,
			PairedAt:    d.PairedAt,
			LastSeen:    d.LastSeen,
			Connected:   connectedByDev[d.ID],
		}
	}

	var (
		pairingOpen      bool
		pairingExpiresAt time.Time
		pin              string
	)
	if s.pairWindow != nil && now.Before(s.pairWindow.expiresAt) {
		pairingOpen = true
		pairingExpiresAt = s.pairWindow.expiresAt
		pin = s.pairWindow.pin
	}

	var pendingInfo *PendingPairInfo
	for _, attemptID := range slices.Backward(s.attemptOrder) {
		att := s.pendingAttempts[attemptID]
		if att == nil || att.state != "awaiting_confirmation" || !now.Before(att.expiresAt) {
			continue
		}
		pendingInfo = &PendingPairInfo{
			AttemptID:         att.attemptID,
			DeviceName:        att.deviceName,
			RemoteAddr:        att.remoteIP,
			ClientFingerprint: att.clientFingerprint,
			SAS:               att.spake.sas,
			HostApproved:      att.hostApproved,
			ClientConfirmed:   att.clientConfirmed,
			ExpiresAt:         att.expiresAt,
		}
		break
	}
	s.mu.RUnlock()

	host := selectDisplayHost(cfg.BindMode, cfg.PreferredHost, addrs)
	accessURL := fmt.Sprintf("http://%s:%d/", host, port)
	pairURL := accessURL
	if pairingOpen && pin != "" {
		pairURL = accessURL + "#pair=" + url.QueryEscape(pin) + "&fp=" + url.QueryEscape(hostFp)
	}

	return Status{
		Running:          running,
		ListenAddr:       listenAddr,
		Port:             port,
		BindMode:         cfg.BindMode,
		AccessURL:        accessURL,
		PairURL:          pairURL,
		HostID:           cfg.HostID,
		HostName:         hostName,
		HostFingerprint:  hostFp,
		PairingOpen:      pairingOpen,
		PairingExpiresAt: pairingExpiresAt,
		PIN:              pin,
		ConnectedClients: clientCnt,
		PairedDevices:    len(devices),
		Devices:          devices,
		PendingPair:      pendingInfo,
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

	s.mu.RLock()
	clients := make([]*wsClient, 0, len(s.clients))
	for c := range s.clients {
		clients = append(clients, c)
	}
	s.mu.RUnlock()

	for _, c := range clients {
		c.sendSnapshot(clientSnap)
	}
}

func (s *Server) refreshSnapshotFromEngine(snap app.Snapshot) ClientSnapshot {
	clientSnap := buildClientSnapshot(snap, s.registerLocalAsset)
	s.mu.Lock()
	s.lastSnap = &clientSnap
	s.mu.Unlock()
	return clientSnap
}

func signAssetURL(raw, sessionID string, assetMacKey []byte) (string, bool) {
	assetID, ok := strings.CutPrefix(raw, assetPrefix)
	if !ok {
		return raw, false
	}
	if baseID, _, hasQ := strings.Cut(assetID, "?"); hasQ {
		assetID = baseID
	}
	mac := computeAssetMAC(assetMacKey, sessionID, assetID)
	return assetPrefix + assetID + "?sid=" + url.QueryEscape(sessionID) + "&mac=" + mac, true
}

func signSnapshotAssets(snap ClientSnapshot, sessionID string, assetMacKey []byte) ClientSnapshot {
	if len(snap.Slots) == 0 || sessionID == "" || len(assetMacKey) == 0 {
		return snap
	}
	hasAsset := false
	for _, sl := range snap.Slots {
		if strings.HasPrefix(sl.ImageURL, assetPrefix) || strings.HasPrefix(sl.BackgroundImageURL, assetPrefix) {
			hasAsset = true
			break
		}
	}
	if !hasAsset {
		return snap
	}
	slotsCopy := make(map[string]ClientSlot, len(snap.Slots))
	for k, sl := range snap.Slots {
		if signed, ok := signAssetURL(sl.ImageURL, sessionID, assetMacKey); ok {
			sl.ImageURL = signed
		}
		if signed, ok := signAssetURL(sl.BackgroundImageURL, sessionID, assetMacKey); ok {
			sl.BackgroundImageURL = signed
		}
		slotsCopy[k] = sl
	}
	out := snap
	out.Slots = slotsCopy
	return out
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
	return assetPrefix + id
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", withSecurityHeaders(true, s.handleIndex))
	mux.HandleFunc("GET /styles.css", withSecurityHeaders(false, s.serveEmbedded("static/styles.css", "text/css; charset=utf-8")))
	mux.HandleFunc("GET /crypto.js", withSecurityHeaders(false, s.serveEmbedded("static/crypto.js", "application/javascript; charset=utf-8")))
	mux.HandleFunc("GET /app.js", withSecurityHeaders(false, s.serveEmbedded("static/app.js", "application/javascript; charset=utf-8")))
	mux.HandleFunc("GET /manifest.webmanifest", withSecurityHeaders(false, s.serveEmbedded("static/manifest.webmanifest", "application/manifest+json; charset=utf-8")))
	mux.HandleFunc("GET /icon.png", withSecurityHeaders(false, s.handleAppIcon))
	mux.HandleFunc("GET /api/pair/info", withSecurityHeaders(false, s.handlePairInfo))
	mux.HandleFunc("POST /api/pair/start", withSecurityHeaders(false, s.handlePairStart))
	mux.HandleFunc("POST /api/pair/verify", withSecurityHeaders(false, s.handlePairVerify))
	mux.HandleFunc("POST /api/pair/confirm", withSecurityHeaders(false, s.handlePairConfirm))
	mux.HandleFunc("GET /api/pair/status/{attemptId}", withSecurityHeaders(false, s.handlePairStatus))
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

func (s *Server) handleIndex(w http.ResponseWriter, _ *http.Request) {
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

func (s *Server) handlePairInfo(w http.ResponseWriter, _ *http.Request) {
	now := time.Now()
	s.mu.RLock()
	resp := map[string]any{
		"hostId":          s.cfg.HostID,
		"hostName":        s.hostName,
		"hostPublicKey":   s.cfg.HostPublicKey,
		"hostFingerprint": s.hostFp,
		"pairingOpen":     false,
	}
	if s.pairWindow != nil && now.Before(s.pairWindow.expiresAt) {
		resp["pairingOpen"] = true
		resp["windowId"] = s.pairWindow.id
		resp["expiresAt"] = s.pairWindow.expiresAt.UTC().Format(time.RFC3339)
	}
	s.mu.RUnlock()
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handlePairStart(w http.ResponseWriter, r *http.Request) {
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
		WindowID        string `json:"windowId"`
		DeviceName      string `json:"deviceName"`
		ClientPublicKey string `json:"clientPublicKey"`
		PA              string `json:"pA"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON payload"})
		return
	}

	clientPub, err := base64.StdEncoding.DecodeString(strings.TrimSpace(req.ClientPublicKey))
	if err != nil || len(clientPub) != ed25519.PublicKeySize {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid client public key"})
		return
	}
	pABytes, err := base64.StdEncoding.DecodeString(strings.TrimSpace(req.PA))
	if err != nil || len(pABytes) != 32 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid SPAKE2 client share"})
		return
	}

	s.mu.Lock()
	win := s.pairWindow
	if win == nil || !now.Before(win.expiresAt) {
		s.closePairingWindowLocked()
		s.mu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "Pairing is closed on PoundControl desktop. Click Pair New Device first.",
		})
		return
	}
	if !constantTimeEqual(strings.TrimSpace(req.WindowID), win.id) {
		s.mu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "Pairing window expired or rotated. Refresh and try again.",
		})
		return
	}

	pin := win.pin
	winID := win.id
	expiresAt := win.expiresAt
	hostPub := append([]byte(nil), s.hostPub...)
	s.mu.Unlock()

	mat, err := spake2ServerCompute(pin, winID, clientPub, hostPub, pABytes)
	if err != nil {
		s.limiter.recordFailure(ip, now)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid SPAKE2 share"})
		return
	}
	attemptID, err := generateRandomHex(16)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to create pairing attempt"})
		return
	}
	att := &pendingPairAttempt{
		attemptID:         attemptID,
		windowID:          winID,
		remoteIP:          ip,
		deviceName:        sanitizeDeviceName(req.DeviceName),
		clientPub:         clientPub,
		clientFingerprint: PublicKeyFingerprint(clientPub),
		state:             "started",
		spake:             mat,
		expiresAt:         expiresAt,
	}

	s.mu.Lock()
	s.storePendingAttemptLocked(att)
	s.mu.Unlock()

	writeJSON(w, http.StatusOK, map[string]string{
		"attemptId": attemptID,
		"pB":        base64.StdEncoding.EncodeToString(mat.pB),
		"expiresAt": expiresAt.UTC().Format(time.RFC3339),
	})
}

func (s *Server) storePendingAttemptLocked(att *pendingPairAttempt) {
	s.pendingAttempts[att.attemptID] = att
	s.attemptOrder = append(s.attemptOrder, att.attemptID)
	for len(s.attemptOrder) > maxPendingPairAttempts {
		evictIdx := 0
		for i, id := range s.attemptOrder {
			if prev := s.pendingAttempts[id]; prev == nil || prev.state != "awaiting_confirmation" {
				evictIdx = i
				break
			}
		}
		evictID := s.attemptOrder[evictIdx]
		delete(s.pendingAttempts, evictID)
		copy(s.attemptOrder[evictIdx:], s.attemptOrder[evictIdx+1:])
		s.attemptOrder[len(s.attemptOrder)-1] = ""
		s.attemptOrder = s.attemptOrder[:len(s.attemptOrder)-1]
	}
}

func (s *Server) handlePairVerify(w http.ResponseWriter, r *http.Request) {
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
		AttemptID string `json:"attemptId"`
		CA        string `json:"cA"`
		ClientSig string `json:"clientSig"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON payload"})
		return
	}
	cABytes, err := base64.StdEncoding.DecodeString(strings.TrimSpace(req.CA))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid confirmation tag"})
		return
	}
	sigBytes, err := base64.StdEncoding.DecodeString(strings.TrimSpace(req.ClientSig))
	if err != nil || len(sigBytes) != ed25519.SignatureSize {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid client signature"})
		return
	}

	attemptID := strings.TrimSpace(req.AttemptID)
	s.mu.Lock()
	att, ok := s.pendingAttempts[attemptID]
	if !ok || att.state != "started" || !now.Before(att.expiresAt) || s.pairWindow == nil || s.pairWindow.id != att.windowID {
		s.mu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]string{"error": "Pairing attempt expired. Please try again."})
		return
	}

	cAValid := hmac.Equal(cABytes, att.spake.cA)
	sigValid := ed25519.Verify(att.clientPub, pairClientSigPayload(att.spake.hashTT), sigBytes)
	if !cAValid || !sigValid {
		delete(s.pendingAttempts, attemptID)
		s.mu.Unlock()
		s.limiter.recordFailure(ip, now)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Invalid pairing PIN"})
		return
	}

	att.state = "awaiting_confirmation"
	cBB64 := base64.StdEncoding.EncodeToString(att.spake.cB)
	hostSigB64 := base64.StdEncoding.EncodeToString(ed25519.Sign(s.hostPriv, pairHostSigPayload(att.spake.hashTT)))
	sas := att.spake.sas
	s.mu.Unlock()

	s.limiter.clear(ip)
	s.notifyStatus()

	writeJSON(w, http.StatusOK, map[string]string{
		"cB":      cBB64,
		"hostSig": hostSigB64,
		"sas":     sas,
		"status":  "awaiting_confirmation",
	})
}

func (s *Server) handlePairConfirm(w http.ResponseWriter, r *http.Request) {
	if !isSameOriginRequest(r) {
		http.Error(w, "forbidden origin", http.StatusForbidden)
		return
	}
	var req struct {
		AttemptID string `json:"attemptId"`
		Confirm   bool   `json:"confirm"`
		MAC       string `json:"mac"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON payload"})
		return
	}
	macBytes, err := base64.StdEncoding.DecodeString(strings.TrimSpace(req.MAC))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid MAC encoding"})
		return
	}

	attemptID := strings.TrimSpace(req.AttemptID)
	now := time.Now()

	s.opMu.Lock()
	defer s.opMu.Unlock()

	s.mu.Lock()
	att, ok := s.pendingAttempts[attemptID]
	if !ok {
		s.mu.Unlock()
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Pairing attempt not found"})
		return
	}
	if !hmac.Equal(macBytes, pairConfirmMAC(att.spake.kcA, attemptID, req.Confirm)) {
		s.mu.Unlock()
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Invalid pairing proof"})
		return
	}

	var cfgToSave *Config
	if !now.After(att.expiresAt) && (att.state == "awaiting_confirmation" || att.state == "approved") {
		if !req.Confirm {
			att.state = "denied"
		} else {
			att.clientConfirmed = true
			if att.hostApproved && att.state != "approved" {
				cfg := s.enrollAttemptLocked(att, now)
				cfgToSave = &cfg
			}
		}
	} else if now.After(att.expiresAt) && att.state == "awaiting_confirmation" {
		att.state = "expired"
	}

	resp := buildPairStatusPayload(att)
	s.mu.Unlock()

	if cfgToSave != nil {
		if err := s.engine.SaveGlobalSettings(SettingsPluginUUID, configToSettings(*cfgToSave)); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to persist paired device"})
			return
		}
	}
	s.notifyStatus()
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handlePairStatus(w http.ResponseWriter, r *http.Request) {
	attemptID := strings.TrimSpace(r.PathValue("attemptId"))
	proofB64 := strings.TrimSpace(r.Header.Get("X-Pair-Proof"))
	proofBytes, err := base64.StdEncoding.DecodeString(proofB64)
	if attemptID == "" || err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Missing or invalid pairing proof"})
		return
	}

	now := time.Now()
	s.mu.Lock()
	att, ok := s.pendingAttempts[attemptID]
	if !ok || !hmac.Equal(proofBytes, pairStatusReqMAC(att.spake.kcA, attemptID)) {
		s.mu.Unlock()
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Unauthorized pairing status request"})
		return
	}
	expiredNow := false
	if now.After(att.expiresAt) && (att.state == "started" || att.state == "awaiting_confirmation") {
		att.state = "expired"
		expiredNow = true
	}
	resp := buildPairStatusPayload(att)
	s.mu.Unlock()

	if expiredNow {
		s.notifyStatus()
	}
	writeJSON(w, http.StatusOK, resp)
}

func buildPairStatusPayload(att *pendingPairAttempt) map[string]any {
	devID := ""
	if att.state == "approved" {
		devID = att.enrolledDeviceID
	}
	mac := pairStatusRespMAC(att.spake.kcB, att.attemptID, att.state, devID)
	return map[string]any{
		"attemptId":       att.attemptID,
		"status":          att.state,
		"hostApproved":    att.hostApproved,
		"clientConfirmed": att.clientConfirmed,
		"deviceId":        devID,
		"mac":             base64.StdEncoding.EncodeToString(mac),
	}
}

func (s *Server) handleAsset(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	sid := strings.TrimSpace(r.URL.Query().Get("sid"))
	macParam := strings.TrimSpace(r.URL.Query().Get("mac"))
	if id == "" {
		http.NotFound(w, r)
		return
	}
	if sid == "" || macParam == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	s.mu.RLock()
	sess, ok := s.sessions[sid]
	var expectedMAC string
	if ok {
		expectedMAC = computeAssetMAC(sess.assetMacKey, sid, id)
	}
	entry, hasAsset := s.assets[id]
	s.mu.RUnlock()

	if !ok || !constantTimeEqual(macParam, expectedMAC) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !hasAsset {
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
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		http.Error(w, "websocket upgrade required", http.StatusUnauthorized)
		return
	}

	wsSrv := websocket.Server{
		Handshake: func(_ *websocket.Config, _ *http.Request) error {
			return nil
		},
		Handler: s.serveWSClient,
	}
	wsSrv.ServeHTTP(w, r)
}

func (s *Server) serveWSClient(conn *websocket.Conn) {
	client, err := s.authenticateWSConn(conn)
	if err != nil {
		if msg, mErr := json.Marshal(ServerMessage{Type: "auth_error", Message: err.Error()}); mErr == nil {
			_ = conn.SetWriteDeadline(time.Now().Add(time.Second))
			_ = websocket.Message.Send(conn, string(msg))
		}
		_ = conn.Close()
		return
	}

	s.touchDeviceLastSeen(client.deviceID)

	s.mu.Lock()
	s.clients[client] = struct{}{}
	s.sessions[client.sessionID] = client
	snapCopy := s.lastSnap
	s.mu.Unlock()

	if snapCopy == nil {
		cs := s.refreshSnapshotFromEngine(s.engine.Snapshot())
		snapCopy = &cs
	}
	client.sendSnapshot(*snapCopy)
	s.notifyStatus()

	var wg sync.WaitGroup
	wg.Go(func() {
		var serverSeq uint64
		for {
			select {
			case <-client.done:
				return
			case item := <-client.send:
				serverSeq++
				mac := computeFrameMAC(client.serverMacKey, "s2c", serverSeq, item.payload)
				frameBytes, fErr := json.Marshal(WSSignedFrame{
					Seq:     serverSeq,
					Payload: item.payload,
					MAC:     base64.StdEncoding.EncodeToString(mac),
				})
				if fErr != nil {
					client.closeConn()
					return
				}
				_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
				err := websocket.Message.Send(conn, string(frameBytes))
				_ = conn.SetWriteDeadline(time.Time{})
				if err != nil || item.closeAfter {
					client.closeConn()
					return
				}
			}
		}
	})

	var (
		lastClientSeq uint64
		closePending  bool
	)
	for {
		var raw string
		if err := websocket.Message.Receive(conn, &raw); err != nil {
			break
		}
		var frame WSSignedFrame
		if err := json.Unmarshal([]byte(raw), &frame); err != nil {
			break
		}
		macBytes, decErr := base64.StdEncoding.DecodeString(frame.MAC)
		if decErr != nil || frame.Seq <= lastClientSeq ||
			!hmac.Equal(macBytes, computeFrameMAC(client.clientMacKey, "c2s", frame.Seq, frame.Payload)) {
			if errPayload, mErr := json.Marshal(ServerMessage{
				Type:    "auth_revoked",
				Message: "Invalid frame signature or replay detected.",
			}); mErr == nil {
				client.enqueuePayload(string(errPayload), true)
				closePending = true
			}
			break
		}
		lastClientSeq = frame.Seq

		var cmd ClientCommand
		if err := json.Unmarshal([]byte(frame.Payload), &cmd); err != nil {
			continue
		}
		if cmd.Type == "unpair" {
			_ = s.RevokeDevice(client.deviceID)
			closePending = true
			break
		}
		if err := s.executeCommand(client, cmd); err != nil {
			if errPayload, mErr := json.Marshal(ServerMessage{Type: "error", Message: err.Error()}); mErr == nil {
				client.enqueuePayload(string(errPayload), false)
			}
		}
	}

	if closePending {
		select {
		case <-client.done:
		case <-time.After(250 * time.Millisecond):
		}
	}
	client.closeConn()
	wg.Wait()

	s.mu.Lock()
	delete(s.clients, client)
	delete(s.sessions, client.sessionID)
	s.mu.Unlock()
	s.notifyStatus()
}

func (s *Server) authenticateWSConn(conn *websocket.Conn) (*wsClient, error) {
	serverNonce, err := generateRandomBytes(32)
	if err != nil {
		return nil, err
	}

	s.mu.RLock()
	hostID := s.cfg.HostID
	hostName := s.hostName
	hostPub := append([]byte(nil), s.hostPub...)
	hostPubB64 := s.cfg.HostPublicKey
	hostPriv := s.hostPriv
	s.mu.RUnlock()

	if len(hostPub) != ed25519.PublicKeySize || len(hostPriv) != ed25519.PrivateKeySize {
		return nil, errors.New("host identity is not initialized")
	}

	_ = conn.SetDeadline(time.Now().Add(wsAuthTimeout))
	helloBytes, err := json.Marshal(WSAuthHello{
		Type:          "auth_hello",
		HostID:        hostID,
		HostName:      hostName,
		HostPublicKey: hostPubB64,
		ServerNonce:   base64.StdEncoding.EncodeToString(serverNonce),
	})
	if err != nil {
		return nil, err
	}
	if err := websocket.Message.Send(conn, string(helloBytes)); err != nil {
		return nil, fmt.Errorf("send auth_hello: %w", err)
	}

	var rawResp string
	if err := websocket.Message.Receive(conn, &rawResp); err != nil {
		return nil, fmt.Errorf("receive auth_response: %w", err)
	}
	var resp WSAuthResponse
	if err := json.Unmarshal([]byte(rawResp), &resp); err != nil || resp.Type != "auth_response" {
		return nil, errors.New("invalid auth_response payload")
	}

	dev, ok := s.lookupPairedDevice(strings.TrimSpace(resp.DeviceID))
	if !ok {
		return nil, errors.New("device is not paired or access was revoked")
	}
	clientPub, err := base64.StdEncoding.DecodeString(dev.PublicKey)
	if err != nil || len(clientPub) != ed25519.PublicKeySize {
		return nil, errors.New("stored device public key invalid")
	}
	clientNonce, err := base64.StdEncoding.DecodeString(strings.TrimSpace(resp.ClientNonce))
	if err != nil || len(clientNonce) != 32 {
		return nil, errors.New("invalid client nonce")
	}
	clientEphPub, err := base64.StdEncoding.DecodeString(strings.TrimSpace(resp.ClientEphPub))
	if err != nil || len(clientEphPub) != 32 {
		return nil, errors.New("invalid client ephemeral key")
	}
	clientSig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(resp.ClientSig))
	if err != nil || len(clientSig) != ed25519.SignatureSize {
		return nil, errors.New("invalid client signature")
	}

	t1 := buildWSTranscript1(hostID, dev.ID, hostPub, clientPub, serverNonce, clientNonce, clientEphPub)
	if !ed25519.Verify(clientPub, t1, clientSig) {
		return nil, errors.New("client mutual signature verification failed")
	}

	serverEphPriv, err := generateX25519Keypair()
	if err != nil {
		return nil, err
	}
	sharedSecret, err := computeX25519Shared(serverEphPriv, clientEphPub)
	if err != nil {
		return nil, err
	}
	serverEphPub := serverEphPriv.PublicKey().Bytes()
	t2 := buildWSTranscript2(t1, serverEphPub)
	hostSig := ed25519.Sign(hostPriv, t2)

	sessionID, err := generateRandomHex(16)
	if err != nil {
		return nil, err
	}
	clientMacKey, serverMacKey, assetMacKey, err := deriveWSSessionKeys(sharedSecret, t2)
	if err != nil {
		return nil, err
	}

	okBytes, err := json.Marshal(WSAuthOK{
		Type:         "auth_ok",
		SessionID:    sessionID,
		ServerEphPub: base64.StdEncoding.EncodeToString(serverEphPub),
		HostSig:      base64.StdEncoding.EncodeToString(hostSig),
	})
	if err != nil {
		return nil, err
	}
	if err := websocket.Message.Send(conn, string(okBytes)); err != nil {
		return nil, fmt.Errorf("send auth_ok: %w", err)
	}
	_ = conn.SetDeadline(time.Time{})

	return &wsClient{
		conn:         conn,
		deviceID:     dev.ID,
		sessionID:    sessionID,
		clientMacKey: clientMacKey,
		serverMacKey: serverMacKey,
		assetMacKey:  assetMacKey,
		send:         make(chan wsSendItem, 32),
		done:         make(chan struct{}),
	}, nil
}

func (s *Server) lookupPairedDevice(deviceID string) (PairedDevice, bool) {
	if deviceID == "" {
		return PairedDevice{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, dev := range s.cfg.Devices {
		if dev.ID == deviceID {
			return dev, true
		}
	}
	return PairedDevice{}, false
}

func (s *Server) touchDeviceLastSeen(deviceID string) {
	if deviceID == "" {
		return
	}
	s.opMu.Lock()
	defer s.opMu.Unlock()

	nowRFC := time.Now().UTC().Format(time.RFC3339)
	s.mu.Lock()
	var updatedCfg *Config
	for i := range s.cfg.Devices {
		if s.cfg.Devices[i].ID == deviceID {
			s.cfg.Devices[i].LastSeen = nowRFC
			cp := s.cfg
			cp.Devices = slices.Clone(s.cfg.Devices)
			updatedCfg = &cp
			break
		}
	}
	s.mu.Unlock()
	if updatedCfg != nil {
		_ = s.engine.SaveGlobalSettings(SettingsPluginUUID, configToSettings(*updatedCfg))
	}
}

func (s *Server) executeCommand(client *wsClient, cmd ClientCommand) error {
	ctx := context.Background()
	snap := s.engine.Snapshot()

	switch cmd.Type {
	case "ping":
		if pong, err := json.Marshal(ServerMessage{Type: "pong"}); err == nil {
			client.enqueuePayload(string(pong), false)
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
