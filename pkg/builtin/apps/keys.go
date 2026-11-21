package apps

import (
	"errors"
	"fmt"
	"strings"
)

// Chord describes a parsed modifier + key combination normalized for Windows, Linux, and macOS drivers.
type Chord struct {
	Ctrl       bool
	Shift      bool
	Alt        bool
	Win        bool
	Key        string
	WinVK      uint16
	WinExt     bool
	X11Key     string
	MacKeyCode int
	MacKeyText string
}

// String returns the canonical human-readable representation of the chord.
func (c Chord) String() string {
	parts := make([]string, 0, 5)
	if c.Ctrl {
		parts = append(parts, "Ctrl")
	}
	if c.Shift {
		parts = append(parts, "Shift")
	}
	if c.Alt {
		parts = append(parts, "Alt")
	}
	if c.Win {
		parts = append(parts, "Win")
	}
	if c.Key != "" {
		parts = append(parts, c.Key)
	}
	return strings.Join(parts, "+")
}

// ParseChord parses a keyboard chord string such as "Ctrl+Shift+M" or "F13".
func ParseChord(raw string) (Chord, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return Chord{}, errors.New("hotkey chord cannot be empty")
	}

	var ch Chord
	for p := range strings.SplitSeq(trimmed, "+") {
		token := strings.TrimSpace(p)
		if token == "" {
			continue
		}
		switch strings.ToLower(token) {
		case "ctrl", "control":
			ch.Ctrl = true
		case "shift":
			ch.Shift = true
		case "alt", "option":
			ch.Alt = true
		case "win", "super", "meta", "cmd", "command":
			ch.Win = true
		default:
			if ch.Key != "" {
				return Chord{}, fmt.Errorf("multiple non-modifier keys in %q", raw)
			}
			spec, ok := lookupKey(token)
			if !ok {
				return Chord{}, fmt.Errorf("unsupported key %q", token)
			}
			ch.Key = spec.name
			ch.WinVK = spec.vk
			ch.WinExt = spec.ext
			ch.X11Key = spec.x11
			ch.MacKeyCode = spec.macCode
			ch.MacKeyText = spec.macText
		}
	}

	if ch.Key == "" {
		return Chord{}, fmt.Errorf("chord %q must include a key", raw)
	}
	return ch, nil
}

type keyEntry struct {
	name    string
	vk      uint16
	ext     bool
	x11     string
	macCode int
	macText string
}

var macAlphaCodes = map[byte]int{
	'A': 0, 'B': 11, 'C': 8, 'D': 2, 'E': 14, 'F': 3, 'G': 5, 'H': 4, 'I': 34,
	'J': 38, 'K': 40, 'L': 37, 'M': 46, 'N': 45, 'O': 31, 'P': 35, 'Q': 12,
	'R': 15, 'S': 1, 'T': 17, 'U': 32, 'V': 9, 'W': 13, 'X': 7, 'Y': 16, 'Z': 6,
}

var macDigitCodes = map[byte]int{
	'0': 29, '1': 18, '2': 19, '3': 20, '4': 21, '5': 23, '6': 22, '7': 26, '8': 28, '9': 25,
}

var macFuncCodes = []int{
	122, 120, 99, 118, 96, 97, 98, 100, 101, 109, 103, 111,
	105, 107, 113, 106, 64, 79, 80, 90, 0, 0, 0, 0,
}

func lookupKey(token string) (keyEntry, bool) {
	upper := strings.ToUpper(strings.TrimSpace(token))

	if len(upper) == 1 && upper[0] >= 'A' && upper[0] <= 'Z' {
		ch := upper[0]
		return keyEntry{
			name:    upper,
			vk:      uint16(ch),
			x11:     strings.ToLower(upper),
			macCode: macAlphaCodes[ch],
			macText: strings.ToLower(upper),
		}, true
	}
	if len(upper) == 1 && upper[0] >= '0' && upper[0] <= '9' {
		ch := upper[0]
		return keyEntry{
			name:    upper,
			vk:      uint16(ch),
			x11:     upper,
			macCode: macDigitCodes[ch],
			macText: upper,
		}, true
	}

	for i := 1; i <= 24; i++ {
		fName := fmt.Sprintf("F%d", i)
		if upper == fName {
			return keyEntry{
				name:    fName,
				vk:      uint16(0x70 + i - 1),
				x11:     fName,
				macCode: macFuncCodes[i-1],
			}, true
		}
	}

	switch upper {
	case "ENTER", "RETURN":
		return keyEntry{name: "Enter", vk: 0x0D, x11: "Return", macCode: 36}, true
	case "ESCAPE", "ESC":
		return keyEntry{name: "Escape", vk: 0x1B, x11: "Escape", macCode: 53}, true
	case "TAB":
		return keyEntry{name: "Tab", vk: 0x09, x11: "Tab", macCode: 48}, true
	case "SPACE":
		return keyEntry{name: "Space", vk: 0x20, x11: "space", macCode: 49, macText: " "}, true
	case "BACKSPACE", "BACK":
		return keyEntry{name: "Backspace", vk: 0x08, x11: "BackSpace", macCode: 51}, true
	case "DELETE", "DEL":
		return keyEntry{name: "Delete", vk: 0x2E, ext: true, x11: "Delete", macCode: 117}, true
	case "INSERT", "INS":
		return keyEntry{name: "Insert", vk: 0x2D, ext: true, x11: "Insert", macCode: 114}, true
	case "HOME":
		return keyEntry{name: "Home", vk: 0x24, ext: true, x11: "Home", macCode: 115}, true
	case "END":
		return keyEntry{name: "End", vk: 0x23, ext: true, x11: "End", macCode: 119}, true
	case "PAGEUP", "PGUP", "PRIOR":
		return keyEntry{name: "PageUp", vk: 0x21, ext: true, x11: "Page_Up", macCode: 116}, true
	case "PAGEDOWN", "PGDN", "NEXT":
		return keyEntry{name: "PageDown", vk: 0x22, ext: true, x11: "Page_Down", macCode: 121}, true
	case "UP":
		return keyEntry{name: "Up", vk: 0x26, ext: true, x11: "Up", macCode: 126}, true
	case "DOWN":
		return keyEntry{name: "Down", vk: 0x28, ext: true, x11: "Down", macCode: 125}, true
	case "LEFT":
		return keyEntry{name: "Left", vk: 0x25, ext: true, x11: "Left", macCode: 123}, true
	case "RIGHT":
		return keyEntry{name: "Right", vk: 0x27, ext: true, x11: "Right", macCode: 124}, true
	case "PAUSE":
		return keyEntry{name: "Pause", vk: 0x13, x11: "Pause", macCode: 113}, true
	default:
		return keyEntry{}, false
	}
}
