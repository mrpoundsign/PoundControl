// Package webdeck provides a mutually authenticated local HTTP and WebSocket touch deck server
// and embedded Obsidian Slate web UI synchronized with the PoundDeck engine.
package webdeck

import (
	"cmp"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
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

	maxPairedDevices = 32
)

// BindMode controls whether the web deck accepts LAN traffic or restricts to loopback.
type BindMode string

const (
	BindLAN       BindMode = "lan"
	BindLocalhost BindMode = "localhost"
)

// PairedDevice holds a paired remote device's verified Ed25519 identity and audit metadata.
type PairedDevice struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	PublicKey   string `json:"publicKey"`
	Fingerprint string `json:"fingerprint"`
	PairedAt    string `json:"pairedAt"`
	LastSeen    string `json:"lastSeen,omitempty"`
}

// Config holds persisted LAN touch deck server settings, host identity, and paired device public keys.
type Config struct {
	Enabled       bool           `json:"enabled"`
	Port          int            `json:"port"`
	BindMode      BindMode       `json:"bindMode"`
	PreferredHost string         `json:"preferredHost,omitempty"`
	HostID        string         `json:"hostId,omitempty"`
	HostPublicKey string         `json:"hostPublicKey,omitempty"`
	Devices       []PairedDevice `json:"devices,omitempty"`
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
	out.HostID = strings.TrimSpace(out.HostID)
	out.HostPublicKey = strings.TrimSpace(out.HostPublicKey)

	if len(out.Devices) > 0 {
		dedup := make([]PairedDevice, 0, min(len(out.Devices), maxPairedDevices))
		seenID := make(map[string]struct{}, len(out.Devices))
		seenPub := make(map[string]struct{}, len(out.Devices))
		for _, dev := range out.Devices {
			dev.ID = strings.TrimSpace(dev.ID)
			dev.Name = sanitizeDeviceName(dev.Name)
			dev.PublicKey = strings.TrimSpace(dev.PublicKey)
			dev.PairedAt = strings.TrimSpace(dev.PairedAt)
			dev.LastSeen = strings.TrimSpace(dev.LastSeen)
			if dev.ID == "" || dev.PublicKey == "" {
				continue
			}
			pubBytes, err := base64.StdEncoding.DecodeString(dev.PublicKey)
			if err != nil || len(pubBytes) != ed25519.PublicKeySize {
				continue
			}
			dev.PublicKey = base64.StdEncoding.EncodeToString(pubBytes)
			dev.Fingerprint = PublicKeyFingerprint(pubBytes)
			if _, ok := seenID[dev.ID]; ok {
				continue
			}
			if _, ok := seenPub[dev.PublicKey]; ok {
				continue
			}
			seenID[dev.ID] = struct{}{}
			seenPub[dev.PublicKey] = struct{}{}
			dedup = append(dedup, dev)
		}
		if len(dedup) > maxPairedDevices {
			dedup = dedup[len(dedup)-maxPairedDevices:]
		}
		out.Devices = dedup
	}
	return out
}

func sanitizeDeviceName(raw string) string {
	clean := strings.TrimSpace(raw)
	clean = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return ' '
		}
		return r
	}, clean)
	clean = strings.Join(strings.Fields(clean), " ")
	if len(clean) > 48 {
		clean = strings.TrimSpace(clean[:48])
	}
	if clean == "" {
		return "Touch Deck"
	}
	return clean
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
		if hid, ok := raw["hostId"].(string); ok {
			cfg.HostID = hid
		}
		if hpk, ok := raw["hostPublicKey"].(string); ok {
			cfg.HostPublicKey = hpk
		}
		cfg.Devices = parsePairedDevices(raw["devices"])
		if _, hasLegacy := raw["sessions"]; hasLegacy {
			needsSave = true
		}
		if _, hasLegacyPIN := raw["pin"]; hasLegacyPIN {
			needsSave = true
		}
	}
	return cfg.normalized(), needsSave
}

func configToSettings(cfg Config) map[string]any {
	norm := cfg.normalized()
	devices := slices.Clone(norm.Devices)
	return map[string]any{
		"enabled":       norm.Enabled,
		"port":          norm.Port,
		"bindMode":      string(norm.BindMode),
		"preferredHost": norm.PreferredHost,
		"hostId":        norm.HostID,
		"hostPublicKey": norm.HostPublicKey,
		"devices":       devices,
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

func parsePairedDevices(raw any) []PairedDevice {
	switch v := raw.(type) {
	case []PairedDevice:
		return slices.Clone(v)
	case []any:
		rawBytes, err := json.Marshal(v)
		if err != nil {
			return nil
		}
		var out []PairedDevice
		if err := json.Unmarshal(rawBytes, &out); err != nil {
			return nil
		}
		return out
	case []map[string]any:
		rawBytes, err := json.Marshal(v)
		if err != nil {
			return nil
		}
		var out []PairedDevice
		if err := json.Unmarshal(rawBytes, &out); err != nil {
			return nil
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

func generateRandomBytes(numBytes int) ([]byte, error) {
	buf := make([]byte, numBytes)
	if _, err := rand.Read(buf); err != nil {
		return nil, fmt.Errorf("generate random bytes: %w", err)
	}
	return buf, nil
}

func generateRandomHex(numBytes int) (string, error) {
	buf, err := generateRandomBytes(numBytes)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
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
