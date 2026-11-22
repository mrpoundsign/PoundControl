package webdeck

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/mrpoundsign/PoundDeck/internal/app"
	"github.com/mrpoundsign/PoundDeck/pkg/deck"
)

// ClientProfile is the sanitized profile summary exposed to remote touch deck clients.
type ClientProfile struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ClientSlot is the sanitized visual representation of a single keypad button.
// Action configuration settings and secrets are intentionally omitted.
type ClientSlot struct {
	Assigned bool   `json:"assigned"`
	AutoBack bool   `json:"autoBack,omitempty"`
	Title    string `json:"title"`
	Subtitle string `json:"subtitle,omitempty"`
	Color    string `json:"color,omitempty"`
	ImageURL string `json:"imageUrl,omitempty"`
	Flash    string `json:"flash,omitempty"`
}

// ClientDial is the sanitized visual representation of a virtual rotary encoder dial.
type ClientDial struct {
	Index        int     `json:"index"`
	Assigned     bool    `json:"assigned"`
	Title        string  `json:"title"`
	Value        string  `json:"value"`
	Color        string  `json:"color,omitempty"`
	HasIndicator bool    `json:"hasIndicator,omitempty"`
	IndicatorPct float64 `json:"indicatorPct,omitempty"`
}

// ClientSnapshot is the sanitized live deck state broadcast over WebSocket.
type ClientSnapshot struct {
	ActiveProfileID   string                `json:"activeProfileId"`
	ActiveProfileName string                `json:"activeProfileName"`
	Profiles          []ClientProfile       `json:"profiles"`
	Grid              deck.GridSize         `json:"grid"`
	Dials             int                   `json:"dials"`
	PageIndex         int                   `json:"pageIndex"`
	PageCount         int                   `json:"pageCount"`
	PageName          string                `json:"pageName"`
	FolderPath        []string              `json:"folderPath,omitempty"`
	InFolder          bool                  `json:"inFolder"`
	Slots             map[string]ClientSlot `json:"slots"`
	DialSlots         []ClientDial          `json:"dialSlots"`
}

// ServerMessage is an envelope pushed from the server to connected WebSocket clients.
type ServerMessage struct {
	Type     string          `json:"type"`
	Snapshot *ClientSnapshot `json:"snapshot,omitempty"`
	Message  string          `json:"message,omitempty"`
}

// ClientCommand is an inbound interactive command received over WebSocket.
// Supported types include ping, keyDown, keyUp, dialRotate, dialPress, prevPage, nextPage, closeFolder, switchProfile, and unpair.
type ClientCommand struct {
	Type      string `json:"type"`
	Row       int    `json:"row"`
	Col       int    `json:"col"`
	Dial      int    `json:"dial"`
	Ticks     int    `json:"ticks"`
	ProfileID string `json:"profileId,omitempty"`
}

// WSAuthHello is sent by the host immediately after WebSocket upgrade to initiate mutual authentication.
type WSAuthHello struct {
	Type          string `json:"type"`
	HostID        string `json:"hostId"`
	HostName      string `json:"hostName"`
	HostPublicKey string `json:"hostPublicKey"`
	ServerNonce   string `json:"serverNonce"`
}

// WSAuthResponse is sent by the client proving its paired Ed25519 identity and ephemeral X25519 key share.
type WSAuthResponse struct {
	Type         string `json:"type"`
	DeviceID     string `json:"deviceId"`
	ClientNonce  string `json:"clientNonce"`
	ClientEphPub string `json:"clientEphPub"`
	ClientSig    string `json:"clientSig"`
}

// WSAuthOK is returned by the host proving host Ed25519 identity and completing session MAC key agreement.
type WSAuthOK struct {
	Type         string `json:"type"`
	SessionID    string `json:"sessionId"`
	ServerEphPub string `json:"serverEphPub"`
	HostSig      string `json:"hostSig"`
}

// WSSignedFrame wraps an authenticated plaintext JSON payload with monotonic sequence number and HMAC-SHA256 tag.
type WSSignedFrame struct {
	Seq     uint64 `json:"seq"`
	Payload string `json:"payload"`
	MAC     string `json:"mac"`
}

func buildClientSnapshot(snap app.Snapshot, mapImage func(string) string) ClientSnapshot {
	profiles := make([]ClientProfile, len(snap.Profiles))
	for i, p := range snap.Profiles {
		profiles[i] = ClientProfile{
			ID:   p.ID,
			Name: p.Name,
		}
	}

	folderPath := make([]string, len(snap.FolderPath))
	copy(folderPath, snap.FolderPath)

	slots := make(map[string]ClientSlot, len(snap.Slots))
	for key, r := range snap.Slots {
		var imgURL string
		if rawURI := strings.TrimSpace(r.Visual.ImageURI); rawURI != "" {
			if strings.HasPrefix(strings.ToLower(rawURI), "data:") {
				imgURL = NormalizeDataImageURI(rawURI)
			} else if mapImage != nil {
				imgURL = mapImage(rawURI)
			}
		}
		slots[key] = ClientSlot{
			Assigned: r.Config != nil,
			AutoBack: r.AutoBack,
			Title:    r.Visual.Title,
			Subtitle: r.Visual.Subtitle,
			Color:    r.Visual.Color,
			ImageURL: imgURL,
			Flash:    r.Visual.Flash,
		}
	}

	dials := make([]ClientDial, snap.Dials)
	for i := range snap.Dials {
		d := ClientDial{
			Index: i,
			Title: "Dial " + strconv.Itoa(i+1),
			Value: "Empty",
		}
		if r, ok := snap.DialSlots[i]; ok {
			d.Assigned = r.Config != nil
			d.Title = r.Visual.Title
			if d.Title == "" {
				d.Title = "Dial " + strconv.Itoa(i+1)
			}
			d.Value = r.Visual.Subtitle
			if d.Value == "" {
				if rawVal, exists := r.Visual.Feedback["value"]; exists {
					d.Value = fmt.Sprint(rawVal)
				}
			}
			if d.Value == "" {
				if d.Assigned {
					d.Value = "Ready"
				} else {
					d.Value = "Empty"
				}
			}
			d.Color = r.Visual.Color
			if pct, hasInd := parseDialIndicator(r.Visual.Feedback["indicator"]); hasInd {
				d.HasIndicator = true
				d.IndicatorPct = pct
			}
		}
		dials[i] = d
	}

	return ClientSnapshot{
		ActiveProfileID:   snap.ActiveProfileID,
		ActiveProfileName: snap.ActiveProfileName,
		Profiles:          profiles,
		Grid:              snap.Grid,
		Dials:             snap.Dials,
		PageIndex:         snap.PageIndex,
		PageCount:         snap.PageCount,
		PageName:          snap.PageName,
		FolderPath:        folderPath,
		InFolder:          snap.InFolder,
		Slots:             slots,
		DialSlots:         dials,
	}
}

// NormalizeDataImageURI converts plain UTF-8 or URL-encoded SVG data URIs (which may contain
// unescaped '#' characters in hex colors) into safe base64 SVG data URIs for browser <img> elements.
func NormalizeDataImageURI(uri string) string {
	trimmed := strings.TrimSpace(uri)
	header, payload, ok := strings.Cut(trimmed, ",")
	if !ok {
		return ""
	}
	hLower := strings.ToLower(header)
	switch {
	case strings.HasPrefix(hLower, "data:image/svg+xml"):
		if strings.Contains(hLower, ";base64") {
			return "data:image/svg+xml;base64," + strings.TrimSpace(payload)
		}
		decoded := payload
		if strings.Contains(payload, "%") {
			if unescaped, err := url.PathUnescape(payload); err == nil {
				decoded = unescaped
			}
		}
		return "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString([]byte(decoded))
	case strings.HasPrefix(hLower, "data:image/png"),
		strings.HasPrefix(hLower, "data:image/jpeg"),
		strings.HasPrefix(hLower, "data:image/jpg"),
		strings.HasPrefix(hLower, "data:image/webp"),
		strings.HasPrefix(hLower, "data:image/gif"):
		return trimmed
	default:
		return ""
	}
}

func parseDialIndicator(raw any) (float64, bool) {
	if m, ok := raw.(map[string]any); ok {
		if enabled, exists := m["enabled"].(bool); exists && !enabled {
			return 0, false
		}
		raw = m["value"]
	}
	var val float64
	switch v := raw.(type) {
	case float64:
		val = v
	case float32:
		val = float64(v)
	case int:
		val = float64(v)
	case int64:
		val = float64(v)
	case json.Number:
		parsed, err := v.Float64()
		if err != nil {
			return 0, false
		}
		val = parsed
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(v, "%")), 64)
		if err != nil {
			return 0, false
		}
		val = parsed
	default:
		return 0, false
	}
	if val < 0 {
		val = 0
	}
	if val > 100 {
		val = 100
	}
	return val, true
}
