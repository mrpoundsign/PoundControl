package wasmplugin

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mrpoundsign/PoundControl/pkg/deck"
	"github.com/mrpoundsign/PoundControl/pkg/plugin"
)

type recordedPatch struct {
	ctx   string
	patch plugin.VisualPatch
}

type fakeHostAPI struct {
	mu       sync.Mutex
	patches  []recordedPatch
	settings map[string]map[string]any
	globals  map[string]map[string]any
	logs     []string
	navCalls []string
}

func newFakeHostAPI() *fakeHostAPI {
	return &fakeHostAPI{
		settings: make(map[string]map[string]any),
		globals:  make(map[string]map[string]any),
	}
}

func (f *fakeHostAPI) UpdateVisual(ctxID string, patch plugin.VisualPatch) {
	f.mu.Lock()
	f.patches = append(f.patches, recordedPatch{ctx: ctxID, patch: patch})
	f.mu.Unlock()
}

func (f *fakeHostAPI) SaveSettings(ctxID string, settings map[string]any) error {
	f.mu.Lock()
	f.settings[ctxID] = maps.Clone(settings)
	f.mu.Unlock()
	return nil
}

func (f *fakeHostAPI) GetGlobalSettings(pluginUUID string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return maps.Clone(f.globals[pluginUUID])
}

func (f *fakeHostAPI) SaveGlobalSettings(pluginUUID string, settings map[string]any) error {
	f.mu.Lock()
	f.globals[pluginUUID] = maps.Clone(settings)
	f.mu.Unlock()
	return nil
}

func (f *fakeHostAPI) Log(pluginUUID, message string) {
	f.mu.Lock()
	f.logs = append(f.logs, pluginUUID+": "+message)
	f.mu.Unlock()
}

func (f *fakeHostAPI) Deck() plugin.DeckControl {
	return &fakeDeckCtrl{host: f}
}

func (f *fakeHostAPI) latestSubtitle(ctxID string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range slices.Backward(f.patches) {
		if p.ctx == ctxID && p.patch.Subtitle != nil {
			return *p.patch.Subtitle, true
		}
	}
	return "", false
}

func (f *fakeHostAPI) latestPatch(ctxID string) (plugin.VisualPatch, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range slices.Backward(f.patches) {
		if p.ctx == ctxID {
			return p.patch, true
		}
	}
	return plugin.VisualPatch{}, false
}

func (f *fakeHostAPI) patchCount(ctxID string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	cnt := 0
	for _, p := range f.patches {
		if p.ctx == ctxID {
			cnt++
		}
	}
	return cnt
}

type fakeDeckCtrl struct {
	host *fakeHostAPI
}

func (d *fakeDeckCtrl) NextPage(bool) error {
	d.host.mu.Lock()
	d.host.navCalls = append(d.host.navCalls, "nextPage")
	d.host.mu.Unlock()
	return nil
}
func (d *fakeDeckCtrl) PrevPage(bool) error { return nil }
func (d *fakeDeckCtrl) GoToPage(int) error  { return nil }
func (d *fakeDeckCtrl) OpenFolder(string, string) error {
	return nil
}
func (d *fakeDeckCtrl) CloseFolder() error         { return nil }
func (d *fakeDeckCtrl) SwitchProfile(string) error { return nil }
func (d *fakeDeckCtrl) TriggerAction(context.Context, string, map[string]any) error {
	return nil
}

func compileWasmFixture(t *testing.T, moduleDir, outPath string) {
	t.Helper()
	cmd := exec.Command("go", "build", "-buildmode=c-shared", "-o", outPath, ".")
	cmd.Dir = moduleDir
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm", "CGO_ENABLED=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("compile wasip1 fixture (%s) failed: %v\n%s", moduleDir, err, string(out))
	}
}

func TestHostAllowlistMatching(t *testing.T) {
	cases := []struct {
		url     string
		allowed []string
		want    bool
	}{
		{"https://api.example.com/v1", nil, false},
		{"file:///etc/passwd", []string{"*"}, false},
		{"ftp://api.example.com/a", []string{"api.example.com"}, false},
		{"https://user:pass@api.example.com/a", []string{"api.example.com"}, false},
		{"https://api.example.com/v1", []string{"api.example.com"}, true},
		{"https://API.Example.Com/v1", []string{"api.example.com"}, true},
		{"https://hooks.slack.com/services/x", []string{"*.slack.com"}, true},
		{"https://slack.com/services/x", []string{"*.slack.com"}, false},
		{"http://127.0.0.1:8080/ping", []string{"127.0.0.1:8080"}, true},
		{"http://127.0.0.1:9090/ping", []string{"127.0.0.1:8080"}, false},
	}
	for _, tc := range cases {
		if got := isHostAllowed(tc.url, tc.allowed); got != tc.want {
			t.Errorf("isHostAllowed(%q, %v) = %v, want %v", tc.url, tc.allowed, got, tc.want)
		}
	}
}

func TestProviderPomodoroLifecycleTimersAndHotReload(t *testing.T) {
	tmp := t.TempDir()
	defaultDir := filepath.Join(tmp, "wasm")
	if err := os.MkdirAll(defaultDir, 0o755); err != nil {
		t.Fatalf("MkdirAll error = %v", err)
	}

	pomodoroSrc := filepath.Join("..", "..", "examples", "wasm-pomodoro")
	pomodoroWasm := filepath.Join(defaultDir, "pomodoro.wasm")
	compileWasmFixture(t, pomodoroSrc, pomodoroWasm)

	host := newFakeHostAPI()
	prov := NewProvider(Options{
		DefaultPluginsDir: defaultDir,
	})
	prov.BindHost(host)
	t.Cleanup(func() { _ = prov.Close() })

	if err := prov.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	statuses := prov.Plugins()
	if len(statuses) != 1 || statuses[0].State != StatusRunning {
		t.Fatalf("expected 1 running wasm plugin, got %+v", statuses)
	}

	ctxID := "slot-pomodoro-0"
	actionUUID := "com.poundcontrol.examples.pomodoro"
	if err := prov.Dispatch(context.Background(), plugin.ActionEvent{
		Type:       plugin.EventWillAppear,
		Context:    ctxID,
		ActionUUID: actionUUID,
		Controller: deck.ControllerKeypad,
		Settings:   map[string]any{"minutes": 1, "label": "Break"},
	}); err != nil {
		t.Fatalf("Dispatch(willAppear) error = %v", err)
	}
	if sub, ok := host.latestSubtitle(ctxID); !ok || sub != "01:00" {
		t.Fatalf("expected initial subtitle 01:00, got (%q, %v)", sub, ok)
	}

	// KeyDown starts the 1s timer; wait up to 2.5s for 00:59 tick.
	if err := prov.Dispatch(context.Background(), plugin.ActionEvent{
		Type:       plugin.EventKeyDown,
		Context:    ctxID,
		ActionUUID: actionUUID,
		Controller: deck.ControllerKeypad,
	}); err != nil {
		t.Fatalf("Dispatch(keyDown) error = %v", err)
	}
	deadline := time.Now().Add(2500 * time.Millisecond)
	var ticked bool
	for time.Now().Before(deadline) {
		if sub, ok := host.latestSubtitle(ctxID); ok && sub != "01:00" {
			ticked = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !ticked {
		sub, _ := host.latestSubtitle(ctxID)
		t.Fatalf("expected host timer to tick pomodoro countdown below 01:00, stayed at %q (patches=%d)", sub, host.patchCount(ctxID))
	}

	// Dial press resets timer to configured 01:00.
	if err := prov.Dispatch(context.Background(), plugin.ActionEvent{
		Type:       plugin.EventDialPress,
		Context:    ctxID,
		ActionUUID: actionUUID,
		Controller: deck.ControllerEncoder,
	}); err != nil {
		t.Fatalf("Dispatch(dialPress) error = %v", err)
	}
	if sub, _ := host.latestSubtitle(ctxID); sub != "01:00" {
		t.Fatalf("expected dialPress reset to 01:00, got %q", sub)
	}

	// Corrupt .wasm file added alongside healthy module reports StatusError on Reload without breaking healthy actions.
	if err := os.WriteFile(filepath.Join(defaultDir, "broken.wasm"), []byte("not-valid-wasm"), 0o600); err != nil {
		t.Fatalf("WriteFile(broken.wasm) error = %v", err)
	}
	if err := prov.Reload(); err != nil {
		t.Fatalf("Reload() error = %v", err)
	}
	reloaded := prov.Plugins()
	if len(reloaded) != 2 {
		t.Fatalf("expected 2 discovered wasm entries, got %+v", reloaded)
	}
	var sawRunning, sawError bool
	for _, st := range reloaded {
		switch st.State {
		case StatusRunning:
			sawRunning = true
		case StatusError:
			sawError = true
		}
	}
	if !sawRunning || !sawError {
		t.Fatalf("expected 1 StatusRunning and 1 StatusError, got %+v", reloaded)
	}
}

func TestProviderSandboxHTTPAndExecutionTimeout(t *testing.T) {
	allowedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("hello-wasm"))
	}))
	defer allowedSrv.Close()

	blockedHits := 0
	blockedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		blockedHits++
		_, _ = w.Write([]byte("should-not-reach"))
	}))
	defer blockedSrv.Close()

	allowedURL, _ := url.Parse(allowedSrv.URL)
	allowedHostPort := allowedURL.Host

	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "sandboxsrc")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatalf("MkdirAll error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "go.mod"), []byte("module example/sandboxwasm\n\ngo 1.24\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(go.mod) error = %v", err)
	}

	guestCode := fmt.Sprintf(`//go:build wasip1 && wasm

package main

import (
	"encoding/json"
	"fmt"
	"sync"
	"unsafe"
)

//go:wasmimport poundcontrol pc_update_visual
func pcUpdateVisual(ctxPtr, ctxLen, patchPtr, patchLen uint32) int32

//go:wasmimport poundcontrol pc_http_request
func pcHTTPRequest(reqPtr, reqLen, respPtr, respCap uint32) int32

//go:wasmimport poundcontrol pc_deck_nav
func pcDeckNav(cmdPtr, cmdLen uint32) int32

var (
	allocMu     sync.Mutex
	allocations = make(map[uint32][]byte)
)

//go:wasmexport pc_alloc
func pcAlloc(size uint32) uint32 {
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

//go:wasmexport pc_free
func pcFree(ptr uint32) {
	allocMu.Lock()
	delete(allocations, ptr)
	allocMu.Unlock()
}

func returnHostString(s string) uint64 {
	raw := []byte(s)
	ptr := pcAlloc(uint32(len(raw)))
	allocMu.Lock()
	copy(allocations[ptr], raw)
	allocMu.Unlock()
	return (uint64(ptr) << 32) | uint64(len(raw))
}

func ptrLen(b []byte) (uint32, uint32) {
	if len(b) == 0 {
		return 0, 0
	}
	return uint32(uintptr(unsafe.Pointer(&b[0]))), uint32(len(b))
}

//go:wasmexport pc_manifest
func pcManifest() uint64 {
	manifest := map[string]any{
		"uuid":    "com.test.wasm.sandbox",
		"name":    "Sandbox Test",
		"version": "1.0.0",
		"permissions": map[string]any{
			"allowedHosts": []string{%q},
		},
		"actions": []map[string]any{
			{"uuid": "com.test.wasm.http_ok", "name": "HTTP Allowed"},
			{"uuid": "com.test.wasm.http_blocked", "name": "HTTP Blocked"},
			{"uuid": "com.test.wasm.hang", "name": "Hang"},
		},
	}
	raw, _ := json.Marshal(manifest)
	return returnHostString(string(raw))
}

//go:wasmexport pc_handle_event
func pcHandleEvent(ptr, size uint32) int32 {
	raw := unsafe.Slice((*byte)(unsafe.Pointer(uintptr(ptr))), size)
	var ev struct {
		Type       string `+"`"+`json:"event"`+"`"+`
		Context    string `+"`"+`json:"context"`+"`"+`
		ActionUUID string `+"`"+`json:"action"`+"`"+`
	}
	if err := json.Unmarshal(raw, &ev); err != nil {
		return -1
	}
	if ev.ActionUUID == "com.test.wasm.hang" {
		for {
		}
	}
	target := %q
	if ev.ActionUUID == "com.test.wasm.http_blocked" {
		target = %q
	} else {
		navBytes := []byte(`+"`"+`{"action":"nextPage"}`+"`"+`)
		np, nl := ptrLen(navBytes)
		pcDeckNav(np, nl)
	}

	reqBytes, _ := json.Marshal(map[string]any{"method": "GET", "url": target})
	rp, rl := ptrLen(reqBytes)
	respBuf := make([]byte, 4096)
	op, _ := ptrLen(respBuf)
	n := pcHTTPRequest(rp, rl, op, uint32(len(respBuf)))
	sub := "err"
	if n > 0 {
		var resp struct {
			Status int    `+"`"+`json:"status"`+"`"+`
			Body   string `+"`"+`json:"body"`+"`"+`
		}
		if json.Unmarshal(respBuf[:n], &resp) == nil {
			if resp.Status == 403 {
				sub = "403:blocked"
			} else {
				sub = fmt.Sprintf("%%d:%%s", resp.Status, resp.Body)
			}
		}
	}
	patchBytes, _ := json.Marshal(map[string]any{"subtitle": sub})
	cp, cl := ptrLen([]byte(ev.Context))
	pp, pl := ptrLen(patchBytes)
	pcUpdateVisual(cp, cl, pp, pl)
	return 0
}

func main() {}
`, allowedHostPort, allowedSrv.URL, blockedSrv.URL)

	if err := os.WriteFile(filepath.Join(srcDir, "main.go"), []byte(guestCode), 0o600); err != nil {
		t.Fatalf("WriteFile(main.go) error = %v", err)
	}

	extraDir := filepath.Join(tmp, "extra")
	if err := os.MkdirAll(extraDir, 0o755); err != nil {
		t.Fatalf("MkdirAll error = %v", err)
	}
	wasmOut := filepath.Join(extraDir, "sandbox.wasm")
	compileWasmFixture(t, srcDir, wasmOut)

	host := newFakeHostAPI()
	prov := NewProvider(Options{
		DefaultPluginsDir: filepath.Join(tmp, "empty_default"),
		CallTimeout:       200 * time.Millisecond,
	})
	prov.BindHost(host)
	t.Cleanup(func() { _ = prov.Close() })

	if err := prov.SaveHostConfig(HostConfig{
		ExtraPaths: []string{extraDir},
	}); err != nil {
		t.Fatalf("SaveHostConfig() error = %v", err)
	}
	if err := prov.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	// Allowed HTTP call succeeds and triggers deck navigation.
	if err := prov.Dispatch(context.Background(), plugin.ActionEvent{
		Type:       plugin.EventKeyDown,
		Context:    "ctx-http-ok",
		ActionUUID: "com.test.wasm.http_ok",
	}); err != nil {
		t.Fatalf("Dispatch(http_ok) error = %v", err)
	}
	if sub, _ := host.latestSubtitle("ctx-http-ok"); sub != "200:hello-wasm" {
		t.Fatalf("allowed HTTP visual = %q, want 200:hello-wasm", sub)
	}
	host.mu.Lock()
	navCnt := len(host.navCalls)
	host.mu.Unlock()
	if navCnt != 1 {
		t.Fatalf("expected pc_deck_nav nextPage call recorded, got %d", navCnt)
	}

	// Disallowed host must be blocked by sandbox (403, zero requests reaching blockedSrv).
	if err := prov.Dispatch(context.Background(), plugin.ActionEvent{
		Type:       plugin.EventKeyDown,
		Context:    "ctx-http-blocked",
		ActionUUID: "com.test.wasm.http_blocked",
	}); err != nil {
		t.Fatalf("Dispatch(http_blocked) error = %v", err)
	}
	if sub, _ := host.latestSubtitle("ctx-http-blocked"); sub != "403:blocked" {
		t.Fatalf("blocked HTTP visual = %q, want 403:blocked", sub)
	}
	if blockedHits != 0 {
		t.Fatalf("sandbox allowed request to unpermitted host (%d hits)", blockedHits)
	}

	// Infinite loop inside guest must terminate cleanly within CallTimeout and mark module StatusError.
	start := time.Now()
	hangErr := prov.Dispatch(context.Background(), plugin.ActionEvent{
		Type:       plugin.EventKeyDown,
		Context:    "ctx-hang",
		ActionUUID: "com.test.wasm.hang",
	})
	elapsed := time.Since(start)
	if hangErr == nil {
		t.Fatal("expected timeout error from infinite loop guest")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("guest timeout took too long: %v", elapsed)
	}
	statuses := prov.Plugins()
	if len(statuses) != 1 || statuses[0].State != StatusError || !strings.Contains(strings.ToLower(statuses[0].Error), "deadline") {
		t.Fatalf("expected timed-out module in StatusError with deadline reason, got %+v", statuses)
	}
}

func TestProviderPomodoroSecondsPauseDoneColor(t *testing.T) {
	tmp := t.TempDir()
	defaultDir := filepath.Join(tmp, "wasm")
	if err := os.MkdirAll(defaultDir, 0o755); err != nil {
		t.Fatalf("MkdirAll error = %v", err)
	}

	pomodoroSrc := filepath.Join("..", "..", "examples", "wasm-pomodoro")
	pomodoroWasm := filepath.Join(defaultDir, "pomodoro.wasm")
	compileWasmFixture(t, pomodoroSrc, pomodoroWasm)

	host := newFakeHostAPI()
	prov := NewProvider(Options{
		DefaultPluginsDir: defaultDir,
	})
	prov.BindHost(host)
	t.Cleanup(func() { _ = prov.Close() })

	if err := prov.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	wantSections := map[string]string{
		"minutes":   "Timer",
		"seconds":   "Timer",
		"doneColor": plugin.SectionAppearance,
		"label":     plugin.SectionAdvanced,
	}
	gotSections := make(map[string]string, len(wantSections))
	var foundSeconds bool
	for _, act := range prov.Actions() {
		if act.UUID != "com.poundcontrol.examples.pomodoro" {
			continue
		}
		for _, field := range act.SettingsSchema {
			gotSections[field.Key] = field.Section
			if field.Key == "seconds" && field.Type == plugin.FieldNumber {
				foundSeconds = true
			}
		}
	}
	if !foundSeconds {
		t.Fatalf("expected pomodoro settingsSchema to expose %q number field", "seconds")
	}
	for k, wantSec := range wantSections {
		if gotSections[k] != wantSec {
			t.Fatalf("pomodoro field %q Section = %q, want %q", k, gotSections[k], wantSec)
		}
	}

	ctxID := "slot-pomodoro-sec"
	actionUUID := "com.poundcontrol.examples.pomodoro"
	if err := prov.Dispatch(context.Background(), plugin.ActionEvent{
		Type:       plugin.EventWillAppear,
		Context:    ctxID,
		ActionUUID: actionUUID,
		Controller: deck.ControllerKeypad,
		Settings: map[string]any{
			"minutes":   0,
			"seconds":   2,
			"doneColor": "#EF4444",
		},
	}); err != nil {
		t.Fatalf("Dispatch(willAppear) error = %v", err)
	}

	initPatch, ok := host.latestPatch(ctxID)
	if !ok || initPatch.Subtitle == nil || *initPatch.Subtitle != "00:02" {
		t.Fatalf("expected initial subtitle 00:02, got (%+v, %v)", initPatch, ok)
	}
	if initPatch.Color == nil || *initPatch.Color != "" {
		t.Fatalf("expected initial color patch empty to preserve slot Accent Color, got %v", initPatch.Color)
	}
	if initPatch.Title != nil {
		t.Fatalf("expected Title omitted when override label is blank, got %q", *initPatch.Title)
	}

	// Start countdown and wait for 00:01 tick.
	if err := prov.Dispatch(context.Background(), plugin.ActionEvent{
		Type:       plugin.EventKeyDown,
		Context:    ctxID,
		ActionUUID: actionUUID,
		Controller: deck.ControllerKeypad,
	}); err != nil {
		t.Fatalf("Dispatch(start keyDown) error = %v", err)
	}
	deadline := time.Now().Add(2500 * time.Millisecond)
	var tickedOne bool
	for time.Now().Before(deadline) {
		if sub, ok := host.latestSubtitle(ctxID); ok && sub == "00:01" {
			tickedOne = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !tickedOne {
		sub, _ := host.latestSubtitle(ctxID)
		t.Fatalf("expected timer to tick to 00:01, got %q", sub)
	}

	// Pause via KeyDown and verify subtitle shows paused state even with blank label.
	if err := prov.Dispatch(context.Background(), plugin.ActionEvent{
		Type:       plugin.EventKeyDown,
		Context:    ctxID,
		ActionUUID: actionUUID,
		Controller: deck.ControllerKeypad,
	}); err != nil {
		t.Fatalf("Dispatch(pause keyDown) error = %v", err)
	}
	if sub, ok := host.latestSubtitle(ctxID); !ok || sub != "00:01 (Paused)" {
		t.Fatalf("expected paused subtitle %q, got (%q, %v)", "00:01 (Paused)", sub, ok)
	}

	// Resume via KeyDown and wait for completion at 00:00 with doneColor + flash.
	if err := prov.Dispatch(context.Background(), plugin.ActionEvent{
		Type:       plugin.EventKeyDown,
		Context:    ctxID,
		ActionUUID: actionUUID,
		Controller: deck.ControllerKeypad,
	}); err != nil {
		t.Fatalf("Dispatch(resume keyDown) error = %v", err)
	}
	deadline = time.Now().Add(2500 * time.Millisecond)
	var finishedPatch plugin.VisualPatch
	var finished bool
	for time.Now().Before(deadline) {
		if p, ok := host.latestPatch(ctxID); ok && p.Subtitle != nil && *p.Subtitle == "00:00" {
			finishedPatch = p
			finished = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !finished {
		sub, _ := host.latestSubtitle(ctxID)
		t.Fatalf("expected timer to reach 00:00, stayed at %q", sub)
	}
	if finishedPatch.Color == nil || *finishedPatch.Color != "#EF4444" {
		t.Fatalf("expected finished color #EF4444, got %v", finishedPatch.Color)
	}
	if finishedPatch.Flash != "ok" {
		t.Fatalf("expected finished flash %q, got %q", "ok", finishedPatch.Flash)
	}

	// Reset via DialPress restores 00:02 and clears temporary doneColor override.
	if err := prov.Dispatch(context.Background(), plugin.ActionEvent{
		Type:       plugin.EventDialPress,
		Context:    ctxID,
		ActionUUID: actionUUID,
		Controller: deck.ControllerEncoder,
	}); err != nil {
		t.Fatalf("Dispatch(dialPress) error = %v", err)
	}
	resetPatch, ok := host.latestPatch(ctxID)
	if !ok || resetPatch.Subtitle == nil || *resetPatch.Subtitle != "00:02" {
		t.Fatalf("expected reset subtitle 00:02, got (%+v, %v)", resetPatch, ok)
	}
	if resetPatch.Color == nil || *resetPatch.Color != "" {
		t.Fatalf("expected reset color patch empty, got %v", resetPatch.Color)
	}
}

func TestProviderRejectsCommandWasmWithoutInitialize(t *testing.T) {
	tmp := t.TempDir()
	defaultDir := filepath.Join(tmp, "wasm")
	if err := os.MkdirAll(defaultDir, 0o755); err != nil {
		t.Fatalf("MkdirAll error = %v", err)
	}

	pomodoroSrc := filepath.Join("..", "..", "examples", "wasm-pomodoro")
	commandWasm := filepath.Join(defaultDir, "command.wasm")
	cmd := exec.Command("go", "build", "-o", commandWasm, ".")
	cmd.Dir = pomodoroSrc
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm", "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("compile command wasm fixture failed: %v\n%s", err, string(out))
	}

	prov := NewProvider(Options{
		DefaultPluginsDir: defaultDir,
	})
	prov.BindHost(newFakeHostAPI())
	t.Cleanup(func() { _ = prov.Close() })

	if err := prov.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	statuses := prov.Plugins()
	if len(statuses) != 1 || statuses[0].State != StatusError {
		t.Fatalf("expected 1 StatusError entry for WASI command module, got %+v", statuses)
	}
	if !strings.Contains(statuses[0].Error, "_initialize") || !strings.Contains(statuses[0].Error, "-buildmode=c-shared") {
		t.Fatalf("expected error mentioning _initialize and -buildmode=c-shared, got %q", statuses[0].Error)
	}
}

func TestProviderGlobalSettingsSchemaAndCallback(t *testing.T) {
	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "guest_global")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatalf("MkdirAll error = %v", err)
	}

	guestCode := `//go:build wasip1 && wasm

package main

import (
	"encoding/json"
	"unsafe"
)

//go:wasmimport poundcontrol pc_log
func pcLog(msgPtr, msgLen uint32)

//go:wasmimport poundcontrol pc_update_visual
func pcUpdateVisual(ctxPtr, ctxLen, patchPtr, patchLen uint32) int32

var allocations = map[uint32][]byte{}
var lastChannel = ""

//go:wasmexport pc_alloc
func pcAlloc(size uint32) uint32 {
	if size == 0 {
		size = 1
	}
	b := make([]byte, size)
	ptr := uint32(uintptr(unsafe.Pointer(&b[0])))
	allocations[ptr] = b
	return ptr
}

//go:wasmexport pc_free
func pcFree(ptr uint32) {
	delete(allocations, ptr)
}

func returnHostString(s string) uint64 {
	raw := []byte(s)
	ptr := pcAlloc(uint32(len(raw)))
	copy(allocations[ptr], raw)
	return (uint64(ptr) << 32) | uint64(len(raw))
}

func ptrLen(b []byte) (uint32, uint32) {
	if len(b) == 0 {
		return 0, 0
	}
	return uint32(uintptr(unsafe.Pointer(&b[0]))), uint32(len(b))
}

//go:wasmexport pc_manifest
func pcManifest() uint64 {
	manifest := map[string]any{
		"uuid":    "com.test.wasm.globalcfg",
		"name":    "Global Config Test",
		"version": "1.0.0",
		"globalSettingsSchema": []map[string]any{
			{"key": "defaultChannel", "label": "Default Channel", "type": "text", "default": "lobby"},
			{"key": "token", "label": "OAuth Token", "type": "password"},
		},
		"actions": []map[string]any{
			{"uuid": "com.test.wasm.globalcfg.say", "name": "Say"},
		},
	}
	raw, _ := json.Marshal(manifest)
	return returnHostString(string(raw))
}

//go:wasmexport pc_on_global_settings
func pcOnGlobalSettings(ptr, size uint32) int32 {
	raw := unsafe.Slice((*byte)(unsafe.Pointer(uintptr(ptr))), size)
	var gs map[string]any
	if err := json.Unmarshal(raw, &gs); err != nil {
		return -1
	}
	if ch, ok := gs["defaultChannel"].(string); ok {
		lastChannel = ch
	}
	msg := []byte("global:" + lastChannel)
	mp, ml := ptrLen(msg)
	pcLog(mp, ml)
	return 0
}

//go:wasmexport pc_handle_event
func pcHandleEvent(ptr, size uint32) int32 {
	raw := unsafe.Slice((*byte)(unsafe.Pointer(uintptr(ptr))), size)
	var ev struct {
		Context string ` + "`" + `json:"context"` + "`" + `
	}
	if err := json.Unmarshal(raw, &ev); err != nil {
		return -1
	}
	patchBytes, _ := json.Marshal(map[string]any{"subtitle": lastChannel})
	cp, cl := ptrLen([]byte(ev.Context))
	pp, pl := ptrLen(patchBytes)
	pcUpdateVisual(cp, cl, pp, pl)
	return 0
}

func main() {}
`
	if err := os.WriteFile(filepath.Join(srcDir, "go.mod"), []byte("module guestglobal\n\ngo 1.24\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(go.mod) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "main.go"), []byte(guestCode), 0o600); err != nil {
		t.Fatalf("WriteFile(main.go) error = %v", err)
	}

	defaultDir := filepath.Join(tmp, "wasm")
	if err := os.MkdirAll(defaultDir, 0o755); err != nil {
		t.Fatalf("MkdirAll error = %v", err)
	}
	compileWasmFixture(t, srcDir, filepath.Join(defaultDir, "globalcfg.wasm"))

	host := newFakeHostAPI()
	prov := NewProvider(Options{DefaultPluginsDir: defaultDir})
	prov.BindHost(host)
	t.Cleanup(func() { _ = prov.Close() })

	if err := prov.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	manifests := prov.Manifests()
	if len(manifests) != 1 || len(manifests[0].GlobalSettingsSchema) != 2 {
		t.Fatalf("expected 1 manifest with 2 GlobalSettingsSchema fields, got %+v", manifests)
	}
	statuses := prov.Plugins()
	if len(statuses) != 1 || !statuses[0].HasGlobalSettings {
		t.Fatalf("expected PluginStatus.HasGlobalSettings = true, got %+v", statuses)
	}

	if err := prov.OnGlobalSettingsChanged(context.Background(), "com.test.wasm.globalcfg", map[string]any{
		"defaultChannel": "mrpoundsign",
		"token":          "tok123",
	}); err != nil {
		t.Fatalf("OnGlobalSettingsChanged() error = %v", err)
	}

	if err := prov.Dispatch(context.Background(), plugin.ActionEvent{
		Type:       plugin.EventKeyDown,
		Context:    "ctx-say",
		ActionUUID: "com.test.wasm.globalcfg.say",
	}); err != nil {
		t.Fatalf("Dispatch() error = %v", err)
	}
	patch, ok := host.latestPatch("ctx-say")
	if !ok || patch.Subtitle == nil || *patch.Subtitle != "mrpoundsign" {
		t.Fatalf("expected subtitle updated from pc_on_global_settings to %q, got (%+v, %v)", "mrpoundsign", patch, ok)
	}
}

func TestProviderConfigActionAndManifestIcons(t *testing.T) {
	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "guest_cfg_action")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatalf("MkdirAll error = %v", err)
	}

	guestCode := `//go:build wasip1 && wasm

package main

import (
	"encoding/json"
	"unsafe"
)

//go:wasmimport poundcontrol pc_save_global_settings
func pcSaveGlobalSettings(jsonPtr, jsonLen uint32) int32

//go:wasmimport poundcontrol pc_update_visual
func pcUpdateVisual(ctxPtr, ctxLen, patchPtr, patchLen uint32) int32

var allocations = map[uint32][]byte{}

//go:wasmexport pc_alloc
func pcAlloc(size uint32) uint32 {
	if size == 0 {
		size = 1
	}
	b := make([]byte, size)
	ptr := uint32(uintptr(unsafe.Pointer(&b[0])))
	allocations[ptr] = b
	return ptr
}

//go:wasmexport pc_free
func pcFree(ptr uint32) {
	delete(allocations, ptr)
}

func returnHostString(s string) uint64 {
	raw := []byte(s)
	ptr := pcAlloc(uint32(len(raw)))
	copy(allocations[ptr], raw)
	return (uint64(ptr) << 32) | uint64(len(raw))
}

func ptrLen(b []byte) (uint32, uint32) {
	if len(b) == 0 {
		return 0, 0
	}
	return uint32(uintptr(unsafe.Pointer(&b[0]))), uint32(len(b))
}

//go:wasmexport pc_manifest
func pcManifest() uint64 {
	manifest := map[string]any{
		"uuid":         "com.test.wasm.cfgaction",
		"name":         "Config Action Test",
		"version":      "1.0.0",
		"icon":         "data:image/svg+xml;base64,plug",
		"categoryIcon": "data:image/svg+xml;base64,cat",
		"globalSettingsSchema": []map[string]any{
			{"key": "clientId", "label": "Client ID", "type": "text", "default": "default-cid"},
			{"key": "deviceCode", "label": "Code", "type": "info"},
			{"key": "authConnect", "label": "Connect", "type": "button"},
		},
		"actions": []map[string]any{
			{"uuid": "com.test.wasm.cfgaction.act", "name": "Act", "icon": "data:image/svg+xml;base64,act"},
		},
	}
	raw, _ := json.Marshal(manifest)
	return returnHostString(string(raw))
}

//go:wasmexport pc_on_config_action
func pcOnConfigAction(ptr, size uint32) int32 {
	raw := unsafe.Slice((*byte)(unsafe.Pointer(uintptr(ptr))), size)
	var ev struct {
		Action   string         ` + "`" + `json:"action"` + "`" + `
		Settings map[string]any ` + "`" + `json:"settings"` + "`" + `
	}
	if err := json.Unmarshal(raw, &ev); err != nil {
		return -1
	}
	if ev.Action == "positiveErr" {
		return 1
	}
	if ev.Action != "authConnect" {
		return -3
	}
	cid, _ := ev.Settings["clientId"].(string)
	updated, _ := json.Marshal(map[string]any{
		"clientId": cid,
		"deviceCode": map[string]any{
			"text":     "ABCD-1234",
			"copyable": true,
		},
		"authConnect": map[string]any{
			"label":    "Cancel Sign-In",
			"disabled": false,
		},
	})
	gp, gl := ptrLen(updated)
	if rc := pcSaveGlobalSettings(gp, gl); rc != 0 {
		return -4
	}
	return 0
}

//go:wasmexport pc_handle_event
func pcHandleEvent(ptr, size uint32) int32 {
	raw := unsafe.Slice((*byte)(unsafe.Pointer(uintptr(ptr))), size)
	var ev struct {
		Context string ` + "`" + `json:"context"` + "`" + `
	}
	if err := json.Unmarshal(raw, &ev); err != nil {
		return -1
	}
	img := "data:image/svg+xml;base64,safe"
	if ev.Context == "unsafeImg" {
		img = "file:///etc/passwd"
	}
	patchBytes, _ := json.Marshal(map[string]any{"image": img, "subtitle": ev.Context})
	cp, cl := ptrLen([]byte(ev.Context))
	pp, pl := ptrLen(patchBytes)
	pcUpdateVisual(cp, cl, pp, pl)
	return 0
}

func main() {}
`
	if err := os.WriteFile(filepath.Join(srcDir, "go.mod"), []byte("module guestcfgaction\n\ngo 1.24\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(go.mod) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "main.go"), []byte(guestCode), 0o600); err != nil {
		t.Fatalf("WriteFile(main.go) error = %v", err)
	}

	defaultDir := filepath.Join(tmp, "wasm")
	if err := os.MkdirAll(defaultDir, 0o755); err != nil {
		t.Fatalf("MkdirAll error = %v", err)
	}
	compileWasmFixture(t, srcDir, filepath.Join(defaultDir, "cfgaction.wasm"))

	host := newFakeHostAPI()
	prov := NewProvider(Options{DefaultPluginsDir: defaultDir})
	prov.BindHost(host)
	t.Cleanup(func() { _ = prov.Close() })

	if err := prov.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	manifests := prov.Manifests()
	if len(manifests) != 1 {
		t.Fatalf("expected 1 manifest, got %+v", manifests)
	}
	if manifests[0].Icon != "data:image/svg+xml;base64,plug" ||
		manifests[0].CategoryIcon != "data:image/svg+xml;base64,cat" ||
		manifests[0].Actions[0].Icon != "data:image/svg+xml;base64,act" {
		t.Fatalf("unexpected manifest/action icons: %+v", manifests[0])
	}

	if err := prov.OnConfigAction(context.Background(), "com.test.wasm.cfgaction", "authConnect", map[string]any{
		"clientId": "custom-cid",
	}); err != nil {
		t.Fatalf("OnConfigAction(authConnect) error = %v", err)
	}

	gs := host.GetGlobalSettings("com.test.wasm.cfgaction")
	if gs["clientId"] != "custom-cid" {
		t.Fatalf("expected guest-saved clientId=custom-cid, got %+v", gs)
	}
	codeMap, ok := gs["deviceCode"].(map[string]any)
	if !ok || codeMap["text"] != "ABCD-1234" || codeMap["copyable"] != true {
		t.Fatalf("expected guest-saved copyable deviceCode map, got %#v", gs["deviceCode"])
	}

	// Non-zero guest return codes (both negative and positive) must fail the call without marking StatusError.
	if err := prov.OnConfigAction(context.Background(), "com.test.wasm.cfgaction", "unknownBtn", nil); err == nil {
		t.Fatal("expected error for negative guest return code")
	}
	if err := prov.OnConfigAction(context.Background(), "com.test.wasm.cfgaction", "positiveErr", nil); err == nil {
		t.Fatal("expected error for positive non-zero guest return code")
	}
	if statuses := prov.Plugins(); len(statuses) != 1 || statuses[0].State != StatusRunning {
		t.Fatalf("expected module to stay StatusRunning after non-zero return codes, got %+v", statuses)
	}

	// pc_update_visual accepts data:image/* URIs and strips unsafe file:// image URIs.
	if err := prov.Dispatch(context.Background(), plugin.ActionEvent{
		Type:       plugin.EventKeyDown,
		Context:    "safeImg",
		ActionUUID: "com.test.wasm.cfgaction.act",
	}); err != nil {
		t.Fatalf("Dispatch(safeImg) error = %v", err)
	}
	if pSafe, ok := host.latestPatch("safeImg"); !ok || pSafe.ImageURI == nil || *pSafe.ImageURI != "data:image/svg+xml;base64,safe" {
		t.Fatalf("expected safe data:image URI preserved, got (%+v, %v)", pSafe, ok)
	}

	if err := prov.Dispatch(context.Background(), plugin.ActionEvent{
		Type:       plugin.EventKeyDown,
		Context:    "unsafeImg",
		ActionUUID: "com.test.wasm.cfgaction.act",
	}); err != nil {
		t.Fatalf("Dispatch(unsafeImg) error = %v", err)
	}
	if pUnsafe, ok := host.latestPatch("unsafeImg"); !ok || pUnsafe.ImageURI != nil {
		t.Fatalf("expected unsafe file:// image URI stripped, got (%+v, %v)", pUnsafe, ok)
	}
}

func TestSanitizeWasmImageURI(t *testing.T) {
	pluginDir := t.TempDir()
	wantIcon := filepath.Join(pluginDir, "assets", "icon.svg")

	cases := []struct {
		name string
		raw  string
		want string
	}{
		{name: "empty", raw: "   ", want: ""},
		{name: "data image svg", raw: " data:image/svg+xml;base64,abc ", want: "data:image/svg+xml;base64,abc"},
		{name: "data image png", raw: "DATA:IMAGE/PNG;base64,xyz", want: "DATA:IMAGE/PNG;base64,xyz"},
		{name: "relative subpath", raw: "assets/icon.svg", want: wantIcon},
		{name: "reject data text html", raw: "data:text/html,<script>", want: ""},
		{name: "reject file uri", raw: "file:///etc/passwd", want: ""},
		{name: "reject http uri", raw: "http://example.com/a.png", want: ""},
		{name: "reject https uri", raw: "https://example.com/a.png", want: ""},
		{name: "reject parent traversal", raw: "../outside.png", want: ""},
		{name: "reject nested traversal", raw: "assets/../../outside.png", want: ""},
		{name: "reject dot self", raw: ".", want: ""},
		{name: "reject posix abs", raw: "/etc/passwd", want: ""},
		{name: "reject windows drive", raw: `C:\Windows\win.ini`, want: ""},
		{name: "reject unc path", raw: `\\server\share\icon.png`, want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizeWasmImageURI(tc.raw, pluginDir); got != tc.want {
				t.Fatalf("sanitizeWasmImageURI(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestProviderDynamicActionsOnStartAndRegister(t *testing.T) {
	tmp := t.TempDir()
	modDir := filepath.Join(tmp, "dynplugin")
	defaultDir := filepath.Join(tmp, "wasm")
	if err := os.MkdirAll(modDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(modDir): %v", err)
	}
	if err := os.MkdirAll(defaultDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(defaultDir): %v", err)
	}

	if err := os.WriteFile(filepath.Join(modDir, "go.mod"), []byte("module example.com/dynplugin\n\ngo 1.24\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(go.mod): %v", err)
	}
	guestSrc := `package main

import (
	"sync"
	"unsafe"
)

//go:wasmimport poundcontrol pc_register_actions
func hostRegisterActions(ptr, length uint32) int32

var (
	allocMu sync.Mutex
	allocs  = map[uint32][]byte{}
)

//go:wasmexport pc_alloc
func pc_alloc(size uint32) uint32 {
	if size == 0 {
		size = 1
	}
	buf := make([]byte, size)
	ptr := uint32(uintptr(unsafe.Pointer(&buf[0])))
	allocMu.Lock()
	allocs[ptr] = buf
	allocMu.Unlock()
	return ptr
}

//go:wasmexport pc_free
func pc_free(ptr uint32) {
	allocMu.Lock()
	delete(allocs, ptr)
	allocMu.Unlock()
}

func allocAndWrite(data []byte) (uint32, uint32) {
	ptr := pc_alloc(uint32(len(data)))
	allocMu.Lock()
	buf := allocs[ptr]
	allocMu.Unlock()
	copy(buf, data)
	return ptr, uint32(len(data))
}

//go:wasmexport pc_manifest
func pc_manifest() uint64 {
	raw := []byte(` + "`" + `{"uuid":"com.test.dyn","name":"DynTest","version":"1.0.0","actions":[]}` + "`" + `)
	ptr, n := allocAndWrite(raw)
	return (uint64(ptr) << 32) | uint64(n)
}

//go:wasmexport pc_on_start
func pc_on_start() int32 {
	raw := []byte(` + "`" + `[
		{"uuid":"com.test.dyn.one","name":"Dynamic One","hideTitle":true,"controllers":["Keypad","Encoder"]},
		{"uuid":"com.test.dyn.one","name":"Duplicate Ignored"},
		{"uuid":"","name":"Empty Ignored"}
	]` + "`" + `)
	ptr, n := allocAndWrite(raw)
	defer pc_free(ptr)
	return hostRegisterActions(ptr, n)
}

//go:wasmexport pc_handle_event
func pc_handle_event(ptr, length uint32) int32 {
	return 0
}

func main() {}
`
	if err := os.WriteFile(filepath.Join(modDir, "main.go"), []byte(guestSrc), 0o600); err != nil {
		t.Fatalf("WriteFile(main.go): %v", err)
	}
	compileWasmFixture(t, modDir, filepath.Join(defaultDir, "dyn.wasm"))

	changedCh := make(chan struct{}, 4)
	host := newFakeHostAPI()
	prov := NewProvider(Options{
		DefaultPluginsDir: defaultDir,
	})
	prov.SetOnActionsChanged(func() {
		select {
		case changedCh <- struct{}{}:
		default:
		}
	})
	prov.BindHost(host)
	t.Cleanup(func() { _ = prov.Close() })

	if err := prov.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	select {
	case <-changedCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for OnActionsChanged from pc_on_start -> pc_register_actions")
	}

	acts := prov.Actions()
	if len(acts) != 1 || acts[0].UUID != "com.test.dyn.one" || acts[0].Name != "Dynamic One" || !acts[0].HideTitle {
		t.Fatalf("unexpected dynamically registered actions: %+v", acts)
	}
	if len(acts[0].Controllers) != 2 {
		t.Fatalf("expected Keypad+Encoder controllers preserved, got %+v", acts[0].Controllers)
	}

	manifests := prov.Manifests()
	if len(manifests) != 1 || len(manifests[0].Actions) != 1 || manifests[0].Actions[0].UUID != "com.test.dyn.one" {
		t.Fatalf("unexpected manifest after dynamic registration: %+v", manifests)
	}

	if err := prov.Dispatch(context.Background(), plugin.ActionEvent{
		Type:       plugin.EventKeyDown,
		Context:    "slot-dyn",
		ActionUUID: "com.test.dyn.one",
	}); err != nil {
		t.Fatalf("Dispatch on dynamically registered action failed: %v", err)
	}
}
