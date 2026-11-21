//go:build wasip1 && wasm

package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"unsafe"
)

//go:wasmimport pounddeck pd_update_visual
func pdUpdateVisual(ctxPtr, ctxLen, patchPtr, patchLen uint32) int32

//go:wasmimport pounddeck pd_set_timer
func pdSetTimer(idPtr, idLen, ctxPtr, ctxLen, intervalMs uint32) int32

type timerState struct {
	running   bool
	remaining int // seconds
	total     int // seconds
	label     string
	color     string
}

var (
	allocMu     sync.Mutex
	allocations = make(map[uint32][]byte)

	stateMu sync.Mutex
	states  = make(map[string]*timerState)
)

//go:wasmexport pd_alloc
func pdAlloc(size uint32) uint32 {
	if size == 0 {
		size = 1
	}
	buf := make([]byte, size)
	ptr := uint32(uintptr(unsafe.Pointer(&buf[0])))
	allocMu.Lock()
	allocations[ptr] = buf
	allocMu.Unlock()
	return ptr
}

//go:wasmexport pd_free
func pdFree(ptr uint32) {
	allocMu.Lock()
	delete(allocations, ptr)
	allocMu.Unlock()
}

func returnHostString(s string) uint64 {
	raw := []byte(s)
	ptr := pdAlloc(uint32(len(raw)))
	allocMu.Lock()
	buf := allocations[ptr]
	copy(buf, raw)
	allocMu.Unlock()
	return (uint64(ptr) << 32) | uint64(len(raw))
}

func bytesPtrLen(b []byte) (uint32, uint32) {
	if len(b) == 0 {
		return 0, 0
	}
	return uint32(uintptr(unsafe.Pointer(&b[0]))), uint32(len(b))
}

func updateVisual(ctxID, title, subtitle, color, flash string, pct *int) {
	patch := map[string]any{
		"title":    title,
		"subtitle": subtitle,
		"color":    color,
	}
	if flash != "" {
		patch["flash"] = flash
	}
	if pct != nil {
		patch["feedback"] = map[string]any{
			"value": subtitle,
			"indicator": map[string]any{
				"value": *pct,
			},
		}
	}
	patchBytes, _ := json.Marshal(patch)
	ctxBytes := []byte(ctxID)
	cp, cl := bytesPtrLen(ctxBytes)
	pp, pl := bytesPtrLen(patchBytes)
	pdUpdateVisual(cp, cl, pp, pl)
}

func setHostTimer(timerID, ctxID string, intervalMs uint32) {
	idBytes := []byte(timerID)
	ctxBytes := []byte(ctxID)
	ip, il := bytesPtrLen(idBytes)
	cp, cl := bytesPtrLen(ctxBytes)
	pdSetTimer(ip, il, cp, cl, intervalMs)
}

//go:wasmexport pd_manifest
func pdManifest() uint64 {
	manifest := map[string]any{
		"uuid":        "com.pounddeck.examples.wasm-pomodoro",
		"name":        "Wasm Break Timer",
		"version":     "1.0.0",
		"author":      "PoundDeck",
		"description": "Sandboxed Go WebAssembly Pomodoro and stream break timer",
		"actions": []map[string]any{
			{
				"uuid":         "com.pounddeck.examples.pomodoro",
				"name":         "Pomodoro Timer",
				"category":     "WebAssembly Plugins",
				"tooltip":      "Start, pause, or reset a countdown timer running inside wazero",
				"defaultTitle": "Focus",
				"defaultColor": "#38BDF8",
				"controllers":  []string{"Keypad", "Encoder"},
				"settingsSchema": []map[string]any{
					{
						"key":      "label",
						"label":    "Timer Label",
						"type":     "text",
						"default":  "Focus",
						"required": false,
					},
					{
						"key":     "minutes",
						"label":   "Duration (minutes)",
						"type":    "number",
						"default": 25,
					},
					{
						"key":     "color",
						"label":   "Accent Color",
						"type":    "text",
						"default": "#38BDF8",
					},
				},
			},
		},
	}
	raw, _ := json.Marshal(manifest)
	return returnHostString(string(raw))
}

//go:wasmexport pd_handle_event
func pdHandleEvent(ptr, size uint32) int32 {
	raw := unsafe.Slice((*byte)(unsafe.Pointer(uintptr(ptr))), size)
	var ev struct {
		Type     string         `json:"event"`
		Context  string         `json:"context"`
		Ticks    int            `json:"ticks"`
		Settings map[string]any `json:"settings"`
	}
	if err := json.Unmarshal(raw, &ev); err != nil {
		return -1
	}

	stateMu.Lock()
	st := ensureState(ev.Context, ev.Settings)
	var (
		flash       string
		timerAction = -1 // -1 unchanged, 0 stop, >0 start ms
	)

	switch ev.Type {
	case "willAppear", "didReceiveSettings":
		mins, label, col := parseSettings(ev.Settings)
		st.label = label
		st.color = col
		if !st.running {
			st.total = mins * 60
			st.remaining = st.total
		}
	case "willDisappear":
		st.running = false
		timerAction = 0
	case "keyDown":
		if st.remaining <= 0 {
			st.remaining = st.total
		}
		st.running = !st.running
		if st.running {
			timerAction = 1000
		} else {
			timerAction = 0
		}
	case "dialRotate":
		deltaSec := ev.Ticks * 60
		st.remaining = max(60, min(90*60, st.remaining+deltaSec))
		if !st.running || st.remaining > st.total {
			st.total = st.remaining
		}
	case "dialPress":
		st.running = false
		st.remaining = st.total
		timerAction = 0
		flash = "ok"
	}

	title, sub, col, pct := formatVisual(st)
	stateMu.Unlock()

	if timerAction >= 0 {
		setHostTimer(ev.Context, ev.Context, uint32(timerAction))
	}
	updateVisual(ev.Context, title, sub, col, flash, &pct)
	return 0
}

//go:wasmexport pd_on_timer
func pdOnTimer(ptr, size uint32) int32 {
	raw := unsafe.Slice((*byte)(unsafe.Pointer(uintptr(ptr))), size)
	var tev struct {
		TimerID string `json:"timerId"`
		Context string `json:"context"`
	}
	if err := json.Unmarshal(raw, &tev); err != nil {
		return -1
	}

	stateMu.Lock()
	st, ok := states[tev.Context]
	if !ok || !st.running {
		stateMu.Unlock()
		setHostTimer(tev.TimerID, tev.Context, 0)
		return 0
	}

	var flash string
	stopTimer := false
	if st.remaining > 1 {
		st.remaining--
	} else {
		st.remaining = 0
		st.running = false
		flash = "ok"
		stopTimer = true
	}
	title, sub, col, pct := formatVisual(st)
	stateMu.Unlock()

	if stopTimer {
		setHostTimer(tev.TimerID, tev.Context, 0)
	}
	updateVisual(tev.Context, title, sub, col, flash, &pct)
	return 0
}

func ensureState(ctxID string, settings map[string]any) *timerState {
	if st, ok := states[ctxID]; ok {
		return st
	}
	mins, label, col := parseSettings(settings)
	st := &timerState{
		total:     mins * 60,
		remaining: mins * 60,
		label:     label,
		color:     col,
	}
	states[ctxID] = st
	return st
}

func parseSettings(settings map[string]any) (mins int, label, col string) {
	mins = 25
	label = "Focus"
	col = "#38BDF8"
	if settings == nil {
		return mins, label, col
	}
	if s, ok := settings["label"].(string); ok && s != "" {
		label = s
	}
	if c, ok := settings["color"].(string); ok && c != "" {
		col = c
	}
	switch v := settings["minutes"].(type) {
	case float64:
		if int(v) > 0 {
			mins = int(v)
		}
	case int:
		if v > 0 {
			mins = v
		}
	case string:
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			mins = n
		}
	}
	if mins > 90 {
		mins = 90
	}
	return mins, label, col
}

func formatVisual(st *timerState) (title, sub, col string, pct int) {
	mm := st.remaining / 60
	ss := st.remaining % 60
	sub = fmt.Sprintf("%02d:%02d", mm, ss)
	title = st.label
	if !st.running && st.remaining > 0 && st.remaining < st.total {
		title += " (Paused)"
	}
	col = st.color
	if st.total > 0 {
		pct = (st.remaining * 100) / st.total
	}
	return title, sub, col, pct
}

func main() {}
