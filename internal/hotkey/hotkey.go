// Package hotkey parses global desktop keyboard shortcuts and manages OS-level hotkey registrations.
package hotkey

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/mrpoundsign/PoundControl/pkg/deck"
)

// Win32 RegisterHotKey modifier bitmask constants.
const (
	ModAlt      uint32 = 0x0001
	ModControl  uint32 = 0x0002
	ModShift    uint32 = 0x0004
	ModWin      uint32 = 0x0008
	ModNoRepeat uint32 = 0x4000
)

// Spec represents a parsed canonical hotkey shortcut.
type Spec struct {
	Ctrl   bool
	Shift  bool
	Alt    bool
	Win    bool
	Key    string
	WinMod uint32
	WinVK  uint32
}

// String formats the canonical human-readable hotkey combination (e.g. "Ctrl+Shift+F1").
func (s Spec) String() string {
	parts := make([]string, 0, 5)
	if s.Ctrl {
		parts = append(parts, "Ctrl")
	}
	if s.Shift {
		parts = append(parts, "Shift")
	}
	if s.Alt {
		parts = append(parts, "Alt")
	}
	if s.Win {
		parts = append(parts, "Win")
	}
	if s.Key != "" {
		parts = append(parts, s.Key)
	}
	return strings.Join(parts, "+")
}

// Parse validates and normalizes a hotkey string such as "Ctrl+Shift+M" or "F13".
func Parse(raw string) (Spec, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return Spec{}, errors.New("hotkey cannot be empty")
	}

	var spec Spec
	for p := range strings.SplitSeq(trimmed, "+") {
		token := strings.TrimSpace(p)
		if token == "" {
			continue
		}
		switch strings.ToLower(token) {
		case "ctrl", "control":
			spec.Ctrl = true
		case "shift":
			spec.Shift = true
		case "alt":
			spec.Alt = true
		case "win", "super", "meta", "cmd":
			spec.Win = true
		default:
			if spec.Key != "" {
				return Spec{}, fmt.Errorf("multiple non-modifier keys in %q", raw)
			}
			canonical, vk, ok := resolveKeyNameAndVK(token)
			if !ok {
				return Spec{}, fmt.Errorf("unsupported hotkey key %q", token)
			}
			spec.Key = canonical
			spec.WinVK = vk
		}
	}

	if spec.Key == "" {
		return Spec{}, fmt.Errorf("hotkey %q must include a non-modifier key", raw)
	}
	if requiresGlobalModifier(spec.Key) && !spec.Ctrl && !spec.Shift && !spec.Alt && !spec.Win {
		return Spec{}, fmt.Errorf("key %q requires at least one modifier (Ctrl, Shift, Alt, or Win) for global use", spec.Key)
	}

	spec.WinMod = ModNoRepeat
	if spec.Ctrl {
		spec.WinMod |= ModControl
	}
	if spec.Shift {
		spec.WinMod |= ModShift
	}
	if spec.Alt {
		spec.WinMod |= ModAlt
	}
	if spec.Win {
		spec.WinMod |= ModWin
	}

	return spec, nil
}

func requiresGlobalModifier(key string) bool {
	if len(key) == 1 {
		return true
	}
	switch key {
	case "Space", "Tab", "Backspace", "Enter", "Up", "Down", "Left", "Right":
		return true
	default:
		return false
	}
}

func resolveKeyNameAndVK(token string) (string, uint32, bool) {
	upper := strings.ToUpper(strings.TrimSpace(token))

	if len(upper) == 1 && upper[0] >= 'A' && upper[0] <= 'Z' {
		return upper, uint32(upper[0]), true
	}
	if len(upper) == 1 && upper[0] >= '0' && upper[0] <= '9' {
		return upper, uint32(upper[0]), true
	}
	for i := 1; i <= 24; i++ {
		fName := fmt.Sprintf("F%d", i)
		if upper == fName {
			return fName, uint32(0x70 + i - 1), true
		}
	}

	switch upper {
	case "ENTER", "RETURN":
		return "Enter", 0x0D, true
	case "SPACE":
		return "Space", 0x20, true
	case "TAB":
		return "Tab", 0x09, true
	case "BACKSPACE", "BACK":
		return "Backspace", 0x08, true
	case "INSERT", "INS":
		return "Insert", 0x2D, true
	case "DELETE", "DEL":
		return "Delete", 0x2E, true
	case "HOME":
		return "Home", 0x24, true
	case "END":
		return "End", 0x23, true
	case "PAGEUP", "PGUP", "PRIOR":
		return "PageUp", 0x21, true
	case "PAGEDOWN", "PGDN", "NEXT":
		return "PageDown", 0x22, true
	case "UP":
		return "Up", 0x26, true
	case "DOWN":
		return "Down", 0x28, true
	case "LEFT":
		return "Left", 0x25, true
	case "RIGHT":
		return "Right", 0x27, true
	case "PAUSE":
		return "Pause", 0x13, true
	default:
		return "", 0, false
	}
}

// Binding associates a deck keypad coordinate with a global hotkey expression.
type Binding struct {
	Coords deck.Coordinates
	Hotkey string
}

type registeredBinding struct {
	id     uintptr
	spec   Spec
	coords deck.Coordinates
}

type backend interface {
	sync(bindings []registeredBinding, onTrigger func(deck.Coordinates)) (int, error)
	close()
}

// Manager synchronizes a set of deck slot hotkey bindings with the host operating system.
type Manager struct {
	opMu       sync.Mutex
	mu         sync.Mutex
	backend    backend
	onTrigger  func(deck.Coordinates)
	active     int
	suspended  bool
	lastKey    string
	lastParsed []registeredBinding
	closed     bool
}

// NewManager creates a Manager backed by the native OS hotkey registrar.
func NewManager(onTrigger func(deck.Coordinates)) *Manager {
	return newManagerWithBackend(newOSBackend(), onTrigger)
}

func newManagerWithBackend(b backend, onTrigger func(deck.Coordinates)) *Manager {
	return &Manager{
		backend:   b,
		onTrigger: onTrigger,
	}
}

// SyncBindings validates bindings and updates active global hotkey registrations if the set changed.
// It returns the number of successfully registered bindings.
func (m *Manager) SyncBindings(bindings []Binding) int {
	parsed := make([]registeredBinding, 0, len(bindings))
	seen := make(map[string]struct{}, len(bindings))
	var sigBuilder strings.Builder

	for _, b := range bindings {
		if strings.TrimSpace(b.Hotkey) == "" {
			continue
		}
		spec, err := Parse(b.Hotkey)
		if err != nil {
			continue
		}
		canon := spec.String()
		if _, dup := seen[canon]; dup {
			continue
		}
		seen[canon] = struct{}{}
		id := uintptr(len(parsed) + 1)
		parsed = append(parsed, registeredBinding{
			id:     id,
			spec:   spec,
			coords: b.Coords,
		})
		fmt.Fprintf(&sigBuilder, "%s@%d,%d;", canon, b.Coords.Row, b.Coords.Col)
	}
	sig := sigBuilder.String()

	m.opMu.Lock()
	defer m.opMu.Unlock()

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return 0
	}
	if sig == m.lastKey {
		if m.suspended {
			m.mu.Unlock()
			return 0
		}
		cnt := m.active
		m.mu.Unlock()
		return cnt
	}
	m.lastKey = sig
	m.lastParsed = slices.Clone(parsed)
	if m.suspended {
		m.active = 0
		m.mu.Unlock()
		return 0
	}
	b := m.backend
	cb := m.onTrigger
	m.mu.Unlock()

	cnt, _ := b.sync(parsed, cb)

	m.mu.Lock()
	if !m.closed && !m.suspended {
		m.active = cnt
	}
	m.mu.Unlock()
	return cnt
}

// SetSuspended temporarily unregisters all active global hotkeys when paused is true
// (for example, during interactive hotkey capture) and restores the latest synced bindings on resume.
func (m *Manager) SetSuspended(paused bool) {
	m.opMu.Lock()
	defer m.opMu.Unlock()

	m.mu.Lock()
	if m.closed || m.suspended == paused {
		m.mu.Unlock()
		return
	}
	m.suspended = paused
	b := m.backend
	if paused {
		m.active = 0
		m.mu.Unlock()
		if b != nil {
			b.close()
		}
		return
	}
	bindings := slices.Clone(m.lastParsed)
	cb := m.onTrigger
	m.mu.Unlock()

	var cnt int
	if b != nil && len(bindings) > 0 {
		cnt, _ = b.sync(bindings, cb)
	}

	m.mu.Lock()
	if !m.closed && !m.suspended {
		m.active = cnt
	}
	m.mu.Unlock()
}

// ActiveCount returns the number of currently active global hotkey registrations.
func (m *Manager) ActiveCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.active
}

// Close unregisters all active hotkeys and stops the background listener.
func (m *Manager) Close() {
	m.opMu.Lock()
	defer m.opMu.Unlock()

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	m.suspended = false
	m.active = 0
	m.lastKey = ""
	m.lastParsed = nil
	b := m.backend
	m.mu.Unlock()

	if b != nil {
		b.close()
	}
}
