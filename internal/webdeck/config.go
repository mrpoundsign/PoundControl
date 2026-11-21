// Package webdeck provides an authenticated local HTTP and WebSocket touch deck server
// and embedded Obsidian Slate web UI synchronized with the PoundDeck engine.
package webdeck

import (
	"cmp"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net"
	"slices"
	"strconv"
	"strings"
)

const (
	// SettingsPluginUUID is the Engine GlobalSettings namespace storing webdeck configuration.
	SettingsPluginUUID = "com.github.mrpoundsign.pounddeck.webdeck"

	// DefaultPort is the default TCP port for the LAN web touch deck server.
	DefaultPort = 9120

	maxPersistedSessions = 32
)

// BindMode controls whether the web deck accepts LAN traffic or restricts to loopback.
type BindMode string

const (
	BindLAN       BindMode = "lan"
	BindLocalhost BindMode = "localhost"
)

// Config holds persisted LAN touch deck server settings and active session digests.
type Config struct {
	Enabled       bool     `json:"enabled"`
	Port          int      `json:"port"`
	BindMode      BindMode `json:"bindMode"`
	PreferredHost string   `json:"preferredHost,omitempty"`
	PIN           string   `json:"pin"`
	PairToken     string   `json:"pairToken"`
	Sessions      []string `json:"sessions,omitempty"`
}

// InterfaceAddress describes a selectable local IPv4 address for pairing URLs and QR codes.
type InterfaceAddress struct {
	Name      string `json:"name"`
	IP        string `json:"ip"`
	Preferred bool   `json:"preferred"`
}

// DefaultConfig returns safe default settings (disabled until enabled in Settings).
func DefaultConfig() Config {
	return Config{
		Enabled:  false,
		Port:     DefaultPort,
		BindMode: BindLAN,
	}
}

func (c Config) normalized() Config {
	out := c
	if out.Port < 0 || out.Port > 65535 {
		out.Port = DefaultPort
	}
	if out.BindMode != BindLAN && out.BindMode != BindLocalhost {
		out.BindMode = BindLAN
	}
	out.PreferredHost = strings.TrimSpace(out.PreferredHost)
	out.PIN = strings.TrimSpace(out.PIN)
	out.PairToken = strings.TrimSpace(out.PairToken)
	if len(out.Sessions) > 0 {
		dedup := make([]string, 0, min(len(out.Sessions), maxPersistedSessions))
		seen := make(map[string]struct{}, len(out.Sessions))
		for _, s := range out.Sessions {
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			if _, ok := seen[s]; ok {
				continue
			}
			seen[s] = struct{}{}
			dedup = append(dedup, s)
		}
		if len(dedup) > maxPersistedSessions {
			dedup = dedup[len(dedup)-maxPersistedSessions:]
		}
		out.Sessions = dedup
	}
	return out
}

func configFromSettings(raw map[string]any) (cfg Config, needsSave bool) {
	cfg = DefaultConfig()
	if raw != nil {
		if b, ok := raw["enabled"].(bool); ok {
			cfg.Enabled = b
		}
		if p, ok := parseIntValue(raw["port"]); ok {
			cfg.Port = p
		}
		if bm, ok := raw["bindMode"].(string); ok {
			cfg.BindMode = BindMode(bm)
		}
		if ph, ok := raw["preferredHost"].(string); ok {
			cfg.PreferredHost = ph
		}
		if pin, ok := raw["pin"].(string); ok {
			cfg.PIN = pin
		}
		if pt, ok := raw["pairToken"].(string); ok {
			cfg.PairToken = pt
		}
		cfg.Sessions = parseStringSlice(raw["sessions"])
	}
	cfg = cfg.normalized()

	if !isValidPIN(cfg.PIN) {
		if pin, err := generatePIN(); err == nil {
			cfg.PIN = pin
			needsSave = true
		}
	}
	if len(cfg.PairToken) < 16 {
		if tok, err := generateRandomHex(16); err == nil {
			cfg.PairToken = tok
			needsSave = true
		}
	}
	return cfg, needsSave
}

func configToSettings(cfg Config) map[string]any {
	norm := cfg.normalized()
	sessions := make([]string, len(norm.Sessions))
	copy(sessions, norm.Sessions)
	return map[string]any{
		"enabled":       norm.Enabled,
		"port":          norm.Port,
		"bindMode":      string(norm.BindMode),
		"preferredHost": norm.PreferredHost,
		"pin":           norm.PIN,
		"pairToken":     norm.PairToken,
		"sessions":      sessions,
	}
}

func parseIntValue(raw any) (int, bool) {
	switch v := raw.(type) {
	case int:
		return v, true
	case int64:
		return int(v), true
	case float64:
		return int(v), true
	case json.Number:
		n, err := v.Int64()
		if err != nil {
			return 0, false
		}
		return int(n), true
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return 0, false
		}
		return n, true
	default:
		return 0, false
	}
}

func parseStringSlice(raw any) []string {
	switch v := raw.(type) {
	case []string:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if t := strings.TrimSpace(item); t != "" {
				out = append(out, t)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				if t := strings.TrimSpace(s); t != "" {
					out = append(out, t)
				}
			}
		}
		return out
	default:
		return nil
	}
}

func isValidPIN(pin string) bool {
	if len(pin) != 6 {
		return false
	}
	for i := range len(pin) {
		if pin[i] < '0' || pin[i] > '9' {
			return false
		}
	}
	return true
}

func generatePIN() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return "", fmt.Errorf("generate PIN: %w", err)
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

func generateRandomHex(numBytes int) (string, error) {
	buf := make([]byte, numBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func constantTimeEqual(a, b string) bool {
	if len(a) != len(b) || len(a) == 0 {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// DiscoverLANAddresses returns active non-loopback IPv4 interface addresses sorted with the
// primary outbound non-virtual LAN interface first.
func DiscoverLANAddresses() []InterfaceAddress {
	preferredIP := detectOutboundIPv4()

	ifaces, err := net.Interfaces()
	if err != nil {
		if preferredIP != "" {
			return []InterfaceAddress{{Name: "Default", IP: preferredIP, Preferred: true}}
		}
		return []InterfaceAddress{{Name: "Loopback", IP: "127.0.0.1", Preferred: true}}
	}

	type candidate struct {
		addr    InterfaceAddress
		virtual bool
		primary bool
	}

	var list []candidate
	seen := make(map[string]struct{})
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		virt := isVirtualInterfaceName(iface.Name)
		for _, a := range addrs {
			ip := extractIPv4(a)
			if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
				continue
			}
			ipStr := ip.String()
			if _, dup := seen[ipStr]; dup {
				continue
			}
			seen[ipStr] = struct{}{}
			list = append(list, candidate{
				addr: InterfaceAddress{
					Name:      iface.Name,
					IP:        ipStr,
					Preferred: ipStr == preferredIP,
				},
				virtual: virt,
				primary: ipStr == preferredIP,
			})
		}
	}

	slices.SortFunc(list, func(a, b candidate) int {
		if a.primary != b.primary {
			if a.primary {
				return -1
			}
			return 1
		}
		if a.virtual != b.virtual {
			if !a.virtual {
				return -1
			}
			return 1
		}
		if c := cmp.Compare(a.addr.Name, b.addr.Name); c != 0 {
			return c
		}
		return cmp.Compare(a.addr.IP, b.addr.IP)
	})

	if len(list) == 0 {
		return []InterfaceAddress{{Name: "Loopback", IP: "127.0.0.1", Preferred: true}}
	}

	out := make([]InterfaceAddress, len(list))
	hasPreferred := false
	for i, c := range list {
		out[i] = c.addr
		if c.addr.Preferred {
			hasPreferred = true
		}
	}
	if !hasPreferred {
		out[0].Preferred = true
	}
	return out
}

func detectOutboundIPv4() string {
	conn, err := net.Dial("udp4", "1.1.1.1:80")
	if err != nil {
		return ""
	}
	defer func() { _ = conn.Close() }()
	udpAddr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok || udpAddr.IP == nil {
		return ""
	}
	ip4 := udpAddr.IP.To4()
	if ip4 == nil || ip4.IsLoopback() {
		return ""
	}
	return ip4.String()
}

func extractIPv4(a net.Addr) net.IP {
	switch v := a.(type) {
	case *net.IPNet:
		return v.IP.To4()
	case *net.IPAddr:
		return v.IP.To4()
	default:
		return nil
	}
}

func isVirtualInterfaceName(name string) bool {
	lower := strings.ToLower(name)
	for _, token := range []string{
		"wsl",
		"hyper-v",
		"vethernet",
		"docker",
		"virtualbox",
		"vmware",
		"vboxnet",
	} {
		if strings.Contains(lower, token) {
			return true
		}
	}
	return false
}
