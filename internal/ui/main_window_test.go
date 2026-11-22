package ui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"
	"golang.org/x/net/websocket"

	"github.com/mrpoundsign/PoundDeck/internal/app"
	"github.com/mrpoundsign/PoundDeck/internal/webdeck"
	"github.com/mrpoundsign/PoundDeck/pkg/builtin/apps"
	"github.com/mrpoundsign/PoundDeck/pkg/builtin/core"
	"github.com/mrpoundsign/PoundDeck/pkg/builtin/obs"
	"github.com/mrpoundsign/PoundDeck/pkg/deck"
	"github.com/mrpoundsign/PoundDeck/pkg/plugin"
	"github.com/mrpoundsign/PoundDeck/pkg/sdplugin"
	"github.com/mrpoundsign/PoundDeck/pkg/secrets"
	"github.com/mrpoundsign/PoundDeck/pkg/wasmplugin"
)

type fakeAppsDriver struct {
	mu     sync.Mutex
	medias []apps.MediaCommand
	chords []apps.Chord
}

func (f *fakeAppsDriver) Exec(context.Context, apps.ExecRequest) error { return nil }
func (f *fakeAppsDriver) OpenURL(context.Context, string) error        { return nil }
func (f *fakeAppsDriver) TypeText(context.Context, string) error       { return nil }
func (f *fakeAppsDriver) SendHotkey(_ context.Context, ch apps.Chord) error {
	f.mu.Lock()
	f.chords = append(f.chords, ch)
	f.mu.Unlock()
	return nil
}
func (f *fakeAppsDriver) SendMedia(_ context.Context, cmd apps.MediaCommand) error {
	f.mu.Lock()
	f.medias = append(f.medias, cmd)
	f.mu.Unlock()
	return nil
}

var uiQueue = make(chan func(), 4096)

func TestMain(m *testing.M) {
	dispatchUI = func(f func()) { uiQueue <- f }
	os.Exit(m.Run())
}

func drainUI() {
	for {
		select {
		case fn := <-uiQueue:
			fn()
		default:
			return
		}
	}
}

func newTestStudio(t *testing.T) (*app.Engine, *obs.Provider, *MainWindow) {
	eng, obsProv, _, mw := newTestStudioWithApps(t)
	return eng, obsProv, mw
}

func newTestStudioWithApps(t *testing.T) (*app.Engine, *obs.Provider, *fakeAppsDriver, *MainWindow) {
	t.Helper()
	drainUI()
	testApp := test.NewApp()
	t.Cleanup(func() {
		drainUI()
		testApp.Quit()
	})
	testApp.Settings().SetTheme(&StudioTheme{})

	reg := plugin.NewRegistry()
	if err := reg.RegisterProvider(core.New()); err != nil {
		t.Fatalf("RegisterProvider(core) error = %v", err)
	}
	obsProv := obs.New(secrets.NewMemoryStore())
	if err := reg.RegisterProvider(obsProv); err != nil {
		t.Fatalf("RegisterProvider(obs) error = %v", err)
	}
	appsDrv := &fakeAppsDriver{}
	if err := reg.RegisterProvider(apps.New(appsDrv)); err != nil {
		t.Fatalf("RegisterProvider(apps) error = %v", err)
	}
	eng, err := app.NewEngine(deck.NewMemoryStore(nil), reg)
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	t.Cleanup(func() {
		_ = eng.Close()
		drainUI()
	})

	mw := NewMainWindow(testApp, eng)
	return eng, obsProv, appsDrv, mw
}

func TestMainWindowInitSelectionAndAssignCounter(t *testing.T) {
	_, _, mw := newTestStudio(t)
	if mw.Window() == nil {
		t.Fatal("expected non-nil Fyne window")
	}

	if got := mw.SelectedSlot(); got != (deck.Coordinates{Row: 0, Col: 0}) {
		t.Fatalf("initial SelectedSlot() = %+v, want (0, 0)", got)
	}
	if !strings.Contains(mw.inspector.slotLbl.Text, "Row 0, Col 0") {
		t.Fatalf("initial inspector text = %q, expected Row 0, Col 0", mw.inspector.slotLbl.Text)
	}

	target := deck.Coordinates{Row: 2, Col: 3}
	tile, ok := mw.surface.tiles[target.SlotKey()]
	if !ok {
		t.Fatalf("missing tile for slot %s", target.SlotKey())
	}

	test.Tap(tile)
	drainUI()

	if got := mw.SelectedSlot(); got != target {
		t.Fatalf("after Tap, SelectedSlot() = %+v, want %+v", got, target)
	}
	if !tile.selected {
		t.Error("expected tapped tile to be marked selected")
	}
	if prev := mw.surface.tiles[(deck.Coordinates{Row: 0, Col: 0}).SlotKey()]; prev.selected {
		t.Error("expected previously selected tile (0, 0) to be deselected")
	}

	// Out-of-bounds selection must be ignored.
	mw.SelectSlot(deck.Coordinates{Row: 99, Col: 99})
	drainUI()
	if got := mw.SelectedSlot(); got != target {
		t.Errorf("out-of-bounds SelectSlot changed selection to %+v, want %+v", got, target)
	}

	// Assign Counter from the catalog to (2, 3), edit title in Inspector, save, and trigger in Run Mode.
	counterBtn, ok := mw.catalogButtons[core.ActionCounter]
	if !ok {
		t.Fatal("missing Counter button in action catalog")
	}
	test.Tap(counterBtn)
	drainUI()

	if tile.title != "Count" || tile.subtitle != "0" {
		t.Fatalf("expected assigned Counter tile title=Count subtitle=0, got title=%q subtitle=%q", tile.title, tile.subtitle)
	}

	mw.inspector.titleEntry.SetText("Deaths")
	test.Tap(mw.inspector.saveBtn)
	drainUI()
	if tile.title != "Deaths" {
		t.Fatalf("after Save, tile.title = %q, want Deaths", tile.title)
	}

	// Trigger via Run Mode tap increments Counter subtitle 0 -> 1.
	mw.SetRunMode(true)
	test.Tap(tile)
	drainUI()
	if tile.subtitle != "1" {
		t.Fatalf("after Run Mode Tap, tile.subtitle = %q, want 1", tile.subtitle)
	}
}

func TestMainWindowFolderAutoBackAndPopOutPadSync(t *testing.T) {
	eng, _, mw := newTestStudio(t)

	// Add second page and switch back to Page 1.
	if _, err := eng.AddPage("Page 2"); err != nil {
		t.Fatalf("AddPage() error = %v", err)
	}
	if err := eng.GoToPage(1); err != nil {
		t.Fatalf("GoToPage(1) error = %v", err)
	}
	drainUI()

	// Assign Open Folder at (1, 1) and Counter at Dial 0.
	mw.SelectSlot(deck.Coordinates{Row: 1, Col: 1})
	test.Tap(mw.catalogButtons[core.ActionFolderOpen])
	drainUI()
	mw.SelectDial(0)
	test.Tap(mw.catalogButtons[core.ActionCounter])
	drainUI()

	// Open Pop-Out Pad (always in Run Mode) and verify sync across both windows.
	pad := mw.OpenPopOutPad()
	if pad == nil || pad.Window() == nil {
		t.Fatal("expected non-nil PopOutPad")
	}

	// Rotate Dial 0 from PopOutPad (+ button) and verify Studio dial reflects update.
	test.Tap(pad.surface.dials[0].plusBtn)
	drainUI()
	if got := mw.surface.dials[0].face.value; got != "1" {
		t.Errorf("expected Studio Dial 0 value 1 after PopOutPad +, got %q", got)
	}

	// Tap Folder Open on PopOutPad, verify Studio enters folder with AutoBack at (0, 0).
	folderTile := pad.surface.tiles[(deck.Coordinates{Row: 1, Col: 1}).SlotKey()]
	test.Tap(folderTile)
	drainUI()

	if !strings.Contains(mw.pageLabel.Text, "Folder:") {
		t.Fatalf("expected Studio pageLabel to show Folder breadcrumb, got %q", mw.pageLabel.Text)
	}
	if studioBackTile := mw.surface.tiles[(deck.Coordinates{Row: 0, Col: 0}).SlotKey()]; studioBackTile.title != "← Back" {
		t.Errorf("expected AutoBack tile on Studio (0,0), got %q", studioBackTile.title)
	}

	// Tap AutoBack (0, 0) on PopOutPad to exit folder.
	test.Tap(pad.surface.tiles[(deck.Coordinates{Row: 0, Col: 0}).SlotKey()])
	drainUI()
	if !strings.Contains(mw.pageLabel.Text, "Page 1 / 2") {
		t.Fatalf("expected Studio returned to Page 1 / 2, got %q", mw.pageLabel.Text)
	}

	pad.Close()
	drainUI()
	if mw.popOut != nil {
		t.Error("expected mw.popOut cleared after Close()")
	}
}

func TestMainWindowOBSIntegrationAndConnectionBadge(t *testing.T) {
	var sceneMu sync.Mutex
	programScene := "Main"

	wsSrv := websocket.Server{
		Handshake: func(_ *websocket.Config, _ *http.Request) error { return nil },
		Handler: func(conn *websocket.Conn) {
			defer func() { _ = conn.Close() }()
			_ = websocket.JSON.Send(conn, map[string]any{
				"op": 0,
				"d": map[string]any{
					"obsWebSocketVersion": "5.5.2",
					"rpcVersion":          1,
				},
			})
			var ident map[string]any
			if err := websocket.JSON.Receive(conn, &ident); err != nil {
				return
			}
			_ = websocket.JSON.Send(conn, map[string]any{
				"op": 2,
				"d":  map[string]any{"negotiatedRpcVersion": 1},
			})
			for {
				var req struct {
					Op int `json:"op"`
					D  struct {
						RequestType string         `json:"requestType"`
						RequestID   string         `json:"requestId"`
						RequestData map[string]any `json:"requestData"`
					} `json:"d"`
				}
				if err := websocket.JSON.Receive(conn, &req); err != nil {
					return
				}
				var data map[string]any
				sceneMu.Lock()
				switch req.D.RequestType {
				case "GetVersion":
					data = map[string]any{"obsVersion": "30.2.3", "obsWebSocketVersion": "5.5.2"}
				case "GetSceneList":
					data = map[string]any{
						"currentProgramSceneName": programScene,
						"scenes": []map[string]any{
							{"sceneName": "BRB"},
							{"sceneName": "Main"},
						},
					}
				case "SetCurrentProgramScene":
					if s, ok := req.D.RequestData["sceneName"].(string); ok {
						programScene = s
					}
				case "GetInputList":
					data = map[string]any{"inputs": []map[string]any{{"inputName": "Mic/Aux"}}}
				case "GetStreamStatus", "GetRecordStatus", "GetReplayBufferStatus":
					data = map[string]any{"outputActive": false}
				case "GetStudioModeEnabled":
					data = map[string]any{"studioModeEnabled": false}
				}
				sceneMu.Unlock()

				rawData, _ := json.Marshal(data)
				_ = websocket.JSON.Send(conn, map[string]any{
					"op": 7,
					"d": map[string]any{
						"requestType":   req.D.RequestType,
						"requestId":     req.D.RequestID,
						"requestStatus": map[string]any{"result": true, "code": 100},
						"responseData":  json.RawMessage(rawData),
					},
				})
			}
		},
	}
	httpSrv := httptest.NewServer(wsSrv)
	defer httpSrv.Close()

	u, _ := url.Parse(httpSrv.URL)
	port, _ := strconv.Atoi(u.Port())

	_, obsProv, mw := newTestStudio(t)
	mw.SetOBSController(obsProv)
	drainUI()

	if mw.obsBtn.Text != "OBS: Off" {
		t.Fatalf("initial obsBtn.Text = %q, want OBS: Off", mw.obsBtn.Text)
	}
	if _, ok := mw.catalogButtons[obs.ActionSwitchScene]; !ok {
		t.Fatal("expected Switch Scene button in Action Catalog")
	}

	if err := obsProv.SaveConnectionConfig(obs.ConnectionConfig{Host: u.Hostname(), Port: port}, "", false); err != nil {
		t.Fatalf("SaveConnectionConfig error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := obsProv.Connect(ctx); err != nil {
		t.Fatalf("obsProv.Connect error = %v", err)
	}
	drainUI()

	if mw.obsBtn.Text != "OBS: Live (30.2.3)" {
		t.Fatalf("connected obsBtn.Text = %q, want OBS: Live (30.2.3)", mw.obsBtn.Text)
	}

	// Assign Switch Scene at (0, 0); Inspector renders SelectEntry populated with live OBS scenes.
	mw.SelectSlot(deck.Coordinates{Row: 0, Col: 0})
	test.Tap(mw.catalogButtons[obs.ActionSwitchScene])
	drainUI()

	sceneBinding, ok := mw.inspector.fields["sceneName"]
	if !ok {
		t.Fatal("expected sceneName binding in inspector")
	}
	sceneEntry, ok := sceneBinding.widget.(*widget.SelectEntry)
	if !ok {
		t.Fatalf("expected *widget.SelectEntry for sceneName, got %T", sceneBinding.widget)
	}

	// Choose BRB, save, and press tile in Run Mode to switch OBS scene to BRB.
	sceneEntry.SetText("BRB")
	test.Tap(mw.inspector.saveBtn)
	drainUI()

	mw.SetRunMode(true)
	tile0 := mw.surface.tiles[(deck.Coordinates{Row: 0, Col: 0}).SlotKey()]
	test.Tap(tile0)
	drainUI()

	if tile0.subtitle != "ACTIVE" {
		t.Errorf("expected switched scene tile subtitle ACTIVE, got %q", tile0.subtitle)
	}
	sceneMu.Lock()
	gotScene := programScene
	sceneMu.Unlock()
	if gotScene != "BRB" {
		t.Errorf("mock OBS programScene = %q, want BRB", gotScene)
	}
}

func TestMainWindowAppsActionsHotkeyAndSettingsDialog(t *testing.T) {
	eng, obsProv, appsDrv, mw := newTestStudioWithApps(t)
	mw.SetConfigPath("/tmp/test-config.json")
	mw.SetHotkeyCountProvider(func() int { return 1 })
	mw.SetOBSController(obsProv)
	drainUI()

	hotkeyBtn, ok := mw.catalogButtons[apps.ActionHotkey]
	if !ok {
		t.Fatal("expected Send Hotkey button in Action Catalog")
	}
	mw.SelectSlot(deck.Coordinates{Row: 0, Col: 0})
	test.Tap(hotkeyBtn)
	drainUI()

	if mw.inspector.hotkeyEntry == nil {
		t.Fatal("expected Global Hotkey entry in Keypad inspector")
	}
	chordBinding, ok := mw.inspector.fields["chord"]
	if !ok {
		t.Fatal("expected chord field in inspector")
	}
	chordBinding.widget.(*widget.Entry).SetText("Ctrl+Shift+M")
	mw.inspector.hotkeyEntry.SetText("ctrl+shift+f1")
	test.Tap(mw.inspector.saveBtn)
	drainUI()

	slot0 := eng.Snapshot().Slots["0,0"]
	if slot0.Config == nil || slot0.Config.Hotkey != "Ctrl+Shift+F1" {
		t.Fatalf("expected canonical Global Hotkey Ctrl+Shift+F1 saved on slot, got %+v", slot0.Config)
	}
	tile0 := mw.surface.tiles[(deck.Coordinates{Row: 0, Col: 0}).SlotKey()]
	if tile0.hotkey != "Ctrl+Shift+F1" {
		t.Errorf("expected tile0.hotkey = Ctrl+Shift+F1, got %q", tile0.hotkey)
	}

	// Assign Media & System Volume to Dial 0 and rotate (+1).
	mw.SelectDial(0)
	test.Tap(mw.catalogButtons[apps.ActionMedia])
	drainUI()
	test.Tap(mw.surface.dials[0].plusBtn)
	drainUI()

	appsDrv.mu.Lock()
	medias := append([]apps.MediaCommand(nil), appsDrv.medias...)
	appsDrv.mu.Unlock()
	if len(medias) != 1 || medias[0] != apps.MediaVolumeUp {
		t.Fatalf("expected MediaVolumeUp from Dial 0 +, got %v", medias)
	}

	// Open and close unified tabbed Settings dialog cleanly.
	dlg := mw.ShowSettingsDialog()
	if dlg == nil {
		t.Fatal("expected non-nil Settings dialog")
	}
	dlg.Hide()
	drainUI()
}

func TestMainWindowOverwriteConfirmAndUndoRedo(t *testing.T) {
	eng, _, _, mw := newTestStudioWithApps(t)

	if !mw.undoBtn.Disabled() || !mw.redoBtn.Disabled() {
		t.Fatal("expected Undo and Redo header buttons initially disabled")
	}

	// 1) Assigning onto an empty keypad slot happens in 1 click with no confirmation dialog.
	mw.SelectSlot(deck.Coordinates{Row: 0, Col: 0})
	test.Tap(mw.catalogButtons[obs.ActionSwitchScene])
	drainUI()
	if mw.confirmDlg != nil {
		t.Fatal("expected no overwrite confirmation dialog for empty slot")
	}
	if mw.undoBtn.Disabled() {
		t.Fatal("expected Undo button enabled after assignment")
	}

	// Edit title, scene, and global hotkey and save atomically in 1 step.
	mw.inspector.titleEntry.SetText("Intro Scene")
	mw.inspector.hotkeyEntry.SetText("Ctrl+Shift+1")
	mw.inspector.fields["sceneName"].widget.(*widget.SelectEntry).SetText("Intro")
	test.Tap(mw.inspector.saveBtn)
	drainUI()

	slot0 := eng.Snapshot().Slots["0,0"]
	if slot0.Config == nil || slot0.Config.Title != "Intro Scene" || slot0.Config.Hotkey != "Ctrl+Shift+1" || slot0.Config.Settings["sceneName"] != "Intro" {
		t.Fatalf("unexpected saved slot state: %+v", slot0.Config)
	}

	// Single-step Undo reverts title, hotkey, and scene together and rebuilds inspector fields.
	test.Tap(mw.undoBtn)
	drainUI()
	slot0 = eng.Snapshot().Slots["0,0"]
	if slot0.Config == nil || slot0.Config.Title != "Scene" || slot0.Config.Hotkey != "" {
		t.Fatalf("expected single-step undo of Edit slot, got %+v", slot0.Config)
	}
	if mw.inspector.titleEntry.Text != "Scene" || mw.inspector.hotkeyEntry.Text != "" {
		t.Fatalf("expected inspector rebuilt after undo, got title=%q hotkey=%q",
			mw.inspector.titleEntry.Text, mw.inspector.hotkeyEntry.Text)
	}

	// Redo restores customized slot.
	triggerCanvasShortcut := func(s fyne.Shortcut) {
		t.Helper()
		mw.shortcuts.TypedShortcut(s)
	}
	triggerCanvasShortcut(&desktop.CustomShortcut{
		KeyName:  fyne.KeyZ,
		Modifier: fyne.KeyModifierShortcutDefault | fyne.KeyModifierShift,
	})
	drainUI()
	if slot0 = eng.Snapshot().Slots["0,0"]; slot0.Config == nil || slot0.Config.Title != "Intro Scene" || slot0.Config.Hotkey != "Ctrl+Shift+1" {
		t.Fatalf("expected Intro Scene restored via Ctrl+Shift+Z, got %+v", slot0.Config)
	}

	// 2) Clicking a different action on an occupied slot prompts confirmation; Dismiss keeps existing.
	test.Tap(mw.catalogButtons[apps.ActionOpenURL])
	if mw.confirmDlg == nil {
		t.Fatal("expected overwrite confirmation dialog for occupied slot")
	}
	mw.confirmDlg.Dismiss()
	drainUI()
	if mw.confirmDlg != nil {
		t.Fatal("expected confirmDlg cleared on Dismiss")
	}
	if slot0 = eng.Snapshot().Slots["0,0"]; slot0.Config == nil || slot0.Config.ActionUUID != obs.ActionSwitchScene || slot0.Config.Title != "Intro Scene" {
		t.Fatalf("expected dismissed overwrite to preserve SwitchScene, got %+v", slot0.Config)
	}

	// Confirming replacement overwrites the slot, and Ctrl+Z + Redo work cleanly.
	test.Tap(mw.catalogButtons[apps.ActionOpenURL])
	if mw.confirmDlg == nil {
		t.Fatal("expected overwrite confirmation dialog on second attempt")
	}
	mw.confirmDlg.Confirm()
	drainUI()
	if mw.confirmDlg != nil {
		t.Fatal("expected confirmDlg cleared on Confirm")
	}
	if slot0 = eng.Snapshot().Slots["0,0"]; slot0.Config == nil || slot0.Config.ActionUUID != apps.ActionOpenURL {
		t.Fatalf("expected OpenURL after Confirm, got %+v", slot0.Config)
	}

	triggerCanvasShortcut(&fyne.ShortcutUndo{})
	drainUI()
	slot0 = eng.Snapshot().Slots["0,0"]
	if slot0.Config == nil || slot0.Config.ActionUUID != obs.ActionSwitchScene || slot0.Config.Title != "Intro Scene" || slot0.Config.Hotkey != "Ctrl+Shift+1" {
		t.Fatalf("expected Ctrl+Z to restore Intro Scene, got %+v", slot0.Config)
	}
	if mw.inspector.titleEntry.Text != "Intro Scene" || mw.inspector.hotkeyEntry.Text != "Ctrl+Shift+1" {
		t.Fatalf("expected inspector to display restored Intro Scene, got title=%q hotkey=%q",
			mw.inspector.titleEntry.Text, mw.inspector.hotkeyEntry.Text)
	}

	test.Tap(mw.redoBtn)
	drainUI()
	if slot0 = eng.Snapshot().Slots["0,0"]; slot0.Config == nil || slot0.Config.ActionUUID != apps.ActionOpenURL {
		t.Fatalf("expected Redo button to reapply OpenURL, got %+v", slot0.Config)
	}

	// 3) Clear button clears slot, and Undo restores it.
	test.Tap(mw.inspector.clearBtn)
	drainUI()
	if _, exists := eng.Snapshot().Slots["0,0"]; exists {
		t.Fatal("expected slot (0,0) cleared")
	}
	test.Tap(mw.undoBtn)
	drainUI()
	if slot0 = eng.Snapshot().Slots["0,0"]; slot0.Config == nil || slot0.Config.ActionUUID != apps.ActionOpenURL {
		t.Fatalf("expected Undo of Clear to restore OpenURL, got %+v", slot0.Config)
	}

	// 4) Canvas Ctrl+Z must be ignored while a modal overlay (Settings dialog) is open.
	settingsDlg := mw.ShowSettingsDialog()
	triggerCanvasShortcut(&fyne.ShortcutUndo{})
	drainUI()
	if slot0 = eng.Snapshot().Slots["0,0"]; slot0.Config == nil || slot0.Config.ActionUUID != apps.ActionOpenURL {
		t.Fatalf("expected Ctrl+Z ignored while modal overlay is open, got %+v", slot0.Config)
	}
	settingsDlg.Hide()
	drainUI()

	// 5) Occupied encoder dial prompts confirmation and preserves dial action on Dismiss.
	mw.SelectDial(0)
	test.Tap(mw.catalogButtons[apps.ActionMedia])
	drainUI()
	if mw.confirmDlg != nil {
		t.Fatal("expected no prompt assigning to empty Dial 0")
	}
	test.Tap(mw.catalogButtons[core.ActionCounter])
	if mw.confirmDlg == nil {
		t.Fatal("expected overwrite confirmation prompt for occupied Dial 0")
	}
	mw.confirmDlg.Dismiss()
	drainUI()
	if dial0 := eng.Snapshot().DialSlots[0]; dial0.Config == nil || dial0.Config.ActionUUID != apps.ActionMedia {
		t.Fatalf("expected Dismiss on occupied Dial 0 to preserve ActionMedia, got %+v", dial0.Config)
	}
}

type openedPIRecord struct {
	title string
	url   string
}

type fakePIViewer struct {
	mu          sync.Mutex
	opened      []openedPIRecord
	closedCount int
}

func (f *fakePIViewer) Open(title, targetURL string) error {
	f.mu.Lock()
	f.opened = append(f.opened, openedPIRecord{title: title, url: targetURL})
	f.mu.Unlock()
	return nil
}

func (f *fakePIViewer) CloseAll() {
	f.mu.Lock()
	f.closedCount++
	f.mu.Unlock()
}

type fakeVisualProvider struct {
	mu   sync.Mutex
	host plugin.HostAPI
}

func (f *fakeVisualProvider) Kind() plugin.ProviderKind { return plugin.ProviderBuiltin }
func (f *fakeVisualProvider) Name() string              { return "Test Visual Provider" }
func (f *fakeVisualProvider) Manifests() []plugin.Manifest {
	return []plugin.Manifest{{
		UUID:     "com.test.visual",
		Name:     "Test Visual",
		Version:  "1.0.0",
		Provider: plugin.ProviderBuiltin,
		Actions:  f.Actions(),
	}}
}
func (f *fakeVisualProvider) Actions() []plugin.ActionDescriptor {
	return []plugin.ActionDescriptor{
		{
			UUID:        "test.svg.tile",
			PluginUUID:  "com.test.visual",
			Name:        "SVG Tile",
			Category:    "Visual Test",
			Controllers: []deck.ControllerType{deck.ControllerKeypad},
			Provider:    plugin.ProviderBuiltin,
		},
		{
			UUID:        "test.dial.meter",
			PluginUUID:  "com.test.visual",
			Name:        "Dial Meter",
			Category:    "Visual Test",
			Controllers: []deck.ControllerType{deck.ControllerEncoder},
			Provider:    plugin.ProviderBuiltin,
		},
	}
}
func (f *fakeVisualProvider) BindHost(host plugin.HostAPI) {
	f.mu.Lock()
	f.host = host
	f.mu.Unlock()
}
func (f *fakeVisualProvider) Dispatch(context.Context, plugin.ActionEvent) error { return nil }
func (f *fakeVisualProvider) Close() error                                       { return nil }

func TestMainWindowSDPluginArtworkDialFeedbackAndPI(t *testing.T) {
	drainUI()
	testApp := test.NewApp()
	t.Cleanup(func() {
		drainUI()
		testApp.Quit()
	})
	testApp.Settings().SetTheme(&StudioTheme{})

	rootTmp := t.TempDir()
	defaultPluginsDir := filepath.Join(rootTmp, "default_plugins")
	extraPluginsDir := filepath.Join(rootTmp, "extra_plugins")
	managedNodeDir := filepath.Join(rootTmp, "managed_node")
	bundleDir := filepath.Join(extraPluginsDir, "com.example.demo.sdPlugin")
	if err := os.MkdirAll(filepath.Join(bundleDir, "pi"), 0o755); err != nil {
		t.Fatalf("MkdirAll pi: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(bundleDir, "bin"), 0o755); err != nil {
		t.Fatalf("MkdirAll bin: %v", err)
	}

	manifest := `{
		"UUID": "com.example.demo",
		"Name": "Demo Plugin",
		"Version": "1.2.3",
		"Author": "Tester",
		"SDKVersion": 2,
		"CodePath": "bin/plugin.js",
		"Nodejs": { "MinimumVersion": "999.0.0" },
		"PropertyInspectorPath": "pi/index.html",
		"Actions": [
			{
				"UUID": "com.example.demo.status",
				"Name": "Demo Status",
				"Controllers": ["Keypad", "Encoder"]
			}
		]
	}`
	if err := os.WriteFile(filepath.Join(bundleDir, "manifest.json"), []byte(manifest), 0o600); err != nil {
		t.Fatalf("WriteFile manifest: %v", err)
	}
	piHTML := `<!DOCTYPE html><html><body>
		<input id="endpoint" data-setting="endpoint" type="text" placeholder="http://127.0.0.1:9000">
		<input id="autoPoll" data-setting="autoPoll" type="checkbox">
	</body></html>`
	if err := os.WriteFile(filepath.Join(bundleDir, "pi", "index.html"), []byte(piHTML), 0o600); err != nil {
		t.Fatalf("WriteFile pi/index.html: %v", err)
	}
	if err := os.WriteFile(filepath.Join(bundleDir, "bin", "plugin.js"), []byte("// test stub\n"), 0o600); err != nil {
		t.Fatalf("WriteFile bin/plugin.js: %v", err)
	}

	reg := plugin.NewRegistry()
	if err := reg.RegisterProvider(core.New()); err != nil {
		t.Fatalf("RegisterProvider(core) error = %v", err)
	}
	visProv := &fakeVisualProvider{}
	if err := reg.RegisterProvider(visProv); err != nil {
		t.Fatalf("RegisterProvider(visProv) error = %v", err)
	}
	sdHost := sdplugin.NewProvider(sdplugin.Options{
		DefaultPluginsDir: defaultPluginsDir,
		ManagedNodeDir:    managedNodeDir,
	})
	if err := reg.RegisterProvider(sdHost); err != nil {
		t.Fatalf("RegisterProvider(sdHost) error = %v", err)
	}

	eng, err := app.NewEngine(deck.NewMemoryStore(nil), reg)
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	t.Cleanup(func() {
		_ = sdHost.Close()
		_ = eng.Close()
		drainUI()
	})
	if err := sdHost.Start(); err != nil {
		t.Fatalf("sdHost.Start() error = %v", err)
	}

	piViewer := &fakePIViewer{}
	mw := NewMainWindow(testApp, eng)
	mw.SetSDPluginController(sdHost)
	mw.SetPIViewer(piViewer)
	drainUI()

	if mw.catalogButtons["com.example.demo.status"] != nil {
		t.Fatal("expected external action absent before adding extra search path")
	}

	if err := sdHost.SaveHostConfig(sdplugin.HostConfig{
		ExtraPaths: []string{extraPluginsDir},
	}); err != nil {
		t.Fatalf("SaveHostConfig() error = %v", err)
	}
	if err := mw.ReloadExternalPlugins(); err != nil {
		t.Fatalf("ReloadExternalPlugins() error = %v", err)
	}
	drainUI()

	demoBtn := mw.catalogButtons["com.example.demo.status"]
	if demoBtn == nil {
		t.Fatal("expected reloaded Action Catalog to include com.example.demo.status")
	}

	// Assign external action to (0, 0): verify extracted HTML fields + full PI launch.
	test.Tap(demoBtn)
	drainUI()
	if _, ok := mw.inspector.fields["endpoint"]; !ok {
		t.Fatalf("expected extracted native HTML field \"endpoint\" in inspector, got %+v", mw.inspector.fields)
	}
	if _, ok := mw.inspector.fields["autoPoll"]; !ok {
		t.Fatalf("expected extracted native HTML field \"autoPoll\" in inspector, got %+v", mw.inspector.fields)
	}
	if mw.inspector.openPIBtn == nil {
		t.Fatal("expected Open Full Property Inspector button for .sdPlugin action")
	}

	test.Tap(mw.inspector.openPIBtn)
	piViewer.mu.Lock()
	openedCount := len(piViewer.opened)
	var openedURL string
	if openedCount > 0 {
		openedURL = piViewer.opened[0].url
	}
	piViewer.mu.Unlock()
	if openedCount != 1 {
		t.Fatalf("expected 1 PI Open call, got %d", openedCount)
	}
	if !strings.HasPrefix(openedURL, "http://127.0.0.1:") || !strings.Contains(openedURL, "/pi/com.example.demo/pi/index.html?token=") {
		t.Fatalf("unexpected Property Inspector URL: %q", openedURL)
	}

	// Settings dialog should open cleanly on the Plugins tab.
	pluginsDlg := mw.ShowSettingsDialogTab(settingsTabPlugins)
	drainUI()
	pluginsDlg.Hide()
	drainUI()

	// Test SVG + <text> artwork + setTitle("") full-art rendering on slot (0, 1).
	svgBtn := mw.catalogButtons["test.svg.tile"]
	if svgBtn == nil {
		t.Fatal("expected test.svg.tile in catalog")
	}
	mw.SelectSlot(deck.Coordinates{Row: 0, Col: 1})
	test.Tap(svgBtn)
	drainUI()
	slot01 := eng.Snapshot().Slots["0,1"]
	if slot01.Config == nil {
		t.Fatal("expected slot (0,1) assigned")
	}
	emptyTitle := ""
	svgURI := "data:image/svg+xml;utf8,<svg xmlns='http://www.w3.org/2000/svg' width='144' height='144'><rect width='144' height='144' fill='%230a0a0c'/><text x='72' y='72' fill='%23ffffff'>REC</text></svg>"
	visProv.host.UpdateVisual(slot01.Config.Context, plugin.VisualPatch{
		Title:    &emptyTitle,
		ImageURI: &svgURI,
	})
	drainUI()

	tile01 := mw.surface.tiles["0,1"]
	if tile01 == nil {
		t.Fatal("expected tile widget for (0,1)")
	}
	if tile01.title != "" {
		t.Fatalf("expected explicit empty title preserved, got %q", tile01.title)
	}
	if tile01.decodedImg == nil || tile01.decodedImg.Bounds().Dx() != 144 {
		t.Fatalf("expected 144px decoded SVG image on tile (0,1), got %#v", tile01.decodedImg)
	}

	// Test Encoder dial setFeedback indicator bar on Dial 0.
	dialBtn := mw.catalogButtons["test.dial.meter"]
	if dialBtn == nil {
		t.Fatal("expected test.dial.meter in catalog")
	}
	mw.SelectDial(0)
	test.Tap(dialBtn)
	drainUI()
	dial0 := eng.Snapshot().DialSlots[0]
	if dial0.Config == nil {
		t.Fatal("expected dial 0 assigned")
	}
	dialVal := "75%"
	visProv.host.UpdateVisual(dial0.Config.Context, plugin.VisualPatch{
		Subtitle: &dialVal,
		Feedback: map[string]any{
			"value":     "75%",
			"indicator": map[string]any{"value": 75},
		},
	})
	drainUI()

	face := mw.surface.dials[0].face
	if face.value != "75%" {
		t.Fatalf("dial value = %q, want %q", face.value, "75%")
	}
	if !face.hasIndicator || face.indicatorPct != 75 {
		t.Fatalf("dial indicator = (%v, %v), want (true, 75)", face.hasIndicator, face.indicatorPct)
	}

	// Closing MainWindow must close any open Property Inspector windows.
	mw.Window().Close()
	piViewer.mu.Lock()
	closedCount := piViewer.closedCount
	piViewer.mu.Unlock()
	if closedCount < 1 {
		t.Fatalf("expected piViewer.CloseAll() invoked on window close, got closedCount=%d", closedCount)
	}
}

func TestMainWindowWebDeckSettingsAndHeaderBadge(t *testing.T) {
	eng, _, mw := newTestStudio(t)
	webSrv := webdeck.NewServer(eng, nil)
	t.Cleanup(func() {
		_ = webSrv.Close()
		drainUI()
	})

	mw.SetWebDeckController(webSrv)
	drainUI()

	if !mw.webBtn.Visible() || mw.webBtn.Text != "Web Deck: Off" {
		t.Fatalf("expected visible \"Web Deck: Off\" header badge, got visible=%v text=%q", mw.webBtn.Visible(), mw.webBtn.Text)
	}

	if err := webSrv.SaveConfig(webdeck.Config{
		Enabled:  true,
		Port:     0,
		BindMode: webdeck.BindLocalhost,
	}); err != nil {
		t.Fatalf("webSrv.SaveConfig() error = %v", err)
	}
	drainUI()

	st := webSrv.Status()
	wantBadge := "Web Deck: :" + strconv.Itoa(st.Port)
	if mw.webBtn.Text != wantBadge {
		t.Fatalf("header badge = %q, want %q", mw.webBtn.Text, wantBadge)
	}

	dlg := mw.ShowSettingsDialogTab(settingsTabWeb)
	drainUI()

	openSt, err := webSrv.OpenPairingWindow(webdeck.DefaultPairingTTL)
	if err != nil || !openSt.PairingOpen || len(openSt.PIN) != 6 || openSt.HostFingerprint == "" {
		t.Fatalf("OpenPairingWindow() = (%+v, %v), want open window with 6-digit PIN & host fingerprint", openSt, err)
	}
	drainUI()

	qrImg, err := webSrv.QRImage(180)
	if err != nil || qrImg.Bounds().Dx() != 180 {
		t.Fatalf("QRImage(180) = (%#v, %v), want 180x180", qrImg, err)
	}

	dlg.Hide()
	drainUI()

	// Verify incoming SPAKE2-verified pairing request opens and clears Studio's mutual confirmation modal.
	pendingSt := webSrv.Status()
	pendingSt.PendingPair = &webdeck.PendingPairInfo{
		AttemptID:         "att-ui-1",
		DeviceName:        "Pixel Companion",
		RemoteAddr:        "127.0.0.1",
		ClientFingerprint: "AAAA-BBBB-CCCC",
		SAS:               "482-915",
		ExpiresAt:         time.Now().Add(time.Minute),
	}
	mw.applyWebDeckStatus(pendingSt)
	drainUI()
	if mw.pairDialog == nil || mw.activePairAttemptID != "att-ui-1" || mw.pairApproveBtn == nil {
		t.Fatalf("expected active Studio pairing approval dialog, got dlg=%v id=%q btn=%v",
			mw.pairDialog, mw.activePairAttemptID, mw.pairApproveBtn)
	}
	pendingSt.PendingPair = nil
	mw.applyWebDeckStatus(pendingSt)
	drainUI()
	if mw.pairDialog != nil || mw.activePairAttemptID != "" {
		t.Fatalf("expected Studio pairing approval dialog cleared, got dlg=%v id=%q", mw.pairDialog, mw.activePairAttemptID)
	}

	webSrv.ClosePairingWindow()
	if err := webSrv.RevokeAllSessions(); err != nil {
		t.Fatalf("webSrv.RevokeAllSessions() error = %v", err)
	}

	if err := webSrv.Stop(); err != nil {
		t.Fatalf("webSrv.Stop() error = %v", err)
	}
	drainUI()
	if mw.webBtn.Text != "Web Deck: Off" {
		t.Fatalf("expected badge back to \"Web Deck: Off\" after Stop, got %q", mw.webBtn.Text)
	}
	drainUI()

	// Verify buildGridDimensionControls supports 5x2 presets, custom dimensions, and bounds validation.
	presetSel, colsEnt, rowsEnt, _, resolveGrid := buildGridDimensionControls(deck.DefaultGrid(), 2)
	presetSel.SetSelected("5×2 (Phone)")
	if colsEnt.Text != "5" || rowsEnt.Text != "2" {
		t.Fatalf("expected 5×2 preset to set entries 5x2, got %sx%s", colsEnt.Text, rowsEnt.Text)
	}
	colsEnt.SetText("7")
	rowsEnt.SetText("2")
	if presetSel.Selected != customGridPresetLabel {
		t.Fatalf("expected custom dimensions to select %q, got %q", customGridPresetLabel, presetSel.Selected)
	}
	if grid, dials, err := resolveGrid(); err != nil || grid != (deck.GridSize{Cols: 7, Rows: 2}) || dials != 2 {
		t.Fatalf("resolveGrid(7x2) = (%+v, %d, %v), want 7x2 / 2 / nil", grid, dials, err)
	}
	colsEnt.SetText("0")
	if _, _, err := resolveGrid(); err == nil {
		t.Fatal("expected resolveGrid(0x2) validation error")
	}
	colsEnt.SetText("20")
	if _, _, err := resolveGrid(); err == nil {
		t.Fatal("expected resolveGrid(20x2) validation error")
	}
}

func TestMainWindowWasmPluginReloadAndSettings(t *testing.T) {
	testApp := test.NewApp()
	t.Cleanup(func() {
		drainUI()
		testApp.Quit()
	})
	testApp.Settings().SetTheme(&StudioTheme{})

	tmp := t.TempDir()
	defaultDir := filepath.Join(tmp, "wasm_default")
	extraDir := filepath.Join(tmp, "wasm_extra")
	if err := os.MkdirAll(extraDir, 0o755); err != nil {
		t.Fatalf("MkdirAll extraDir: %v", err)
	}
	wasmPath := filepath.Join(extraDir, "pomodoro.wasm")

	cmd := exec.Command("go", "build", "-buildmode=c-shared", "-o", wasmPath, ".")
	cmd.Dir = filepath.Join("..", "..", "examples", "wasm-pomodoro")
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm", "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build wasm-pomodoro failed: %v\n%s", err, out)
	}

	reg := plugin.NewRegistry()
	if err := reg.RegisterProvider(core.New()); err != nil {
		t.Fatalf("RegisterProvider(core) error = %v", err)
	}
	wasmHost := wasmplugin.NewProvider(wasmplugin.Options{
		DefaultPluginsDir: defaultDir,
	})
	if err := reg.RegisterProvider(wasmHost); err != nil {
		t.Fatalf("RegisterProvider(wasmHost) error = %v", err)
	}

	eng, err := app.NewEngine(deck.NewMemoryStore(nil), reg)
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	t.Cleanup(func() {
		_ = wasmHost.Close()
		_ = eng.Close()
		drainUI()
	})
	if err := wasmHost.Start(); err != nil {
		t.Fatalf("wasmHost.Start() error = %v", err)
	}

	mw := NewMainWindow(testApp, eng)
	mw.SetWasmPluginController(wasmHost)
	drainUI()

	if mw.catalogButtons["com.pounddeck.examples.pomodoro"] != nil {
		t.Fatal("expected pomodoro wasm action absent before adding extra search path")
	}

	if err := wasmHost.SaveHostConfig(wasmplugin.HostConfig{
		ExtraPaths: []string{extraDir},
	}); err != nil {
		t.Fatalf("SaveHostConfig() error = %v", err)
	}
	if err := mw.ReloadExternalPlugins(); err != nil {
		t.Fatalf("ReloadExternalPlugins() error = %v", err)
	}
	drainUI()

	pomoBtn := mw.catalogButtons["com.pounddeck.examples.pomodoro"]
	if pomoBtn == nil {
		t.Fatal("expected reloaded Action Catalog to include com.pounddeck.examples.pomodoro")
	}

	test.Tap(pomoBtn)
	drainUI()

	for _, key := range []string{"label", "minutes", "color"} {
		if _, ok := mw.inspector.fields[key]; !ok {
			t.Fatalf("expected inspector field %q for pomodoro action, got %+v", key, mw.inspector.fields)
		}
	}

	if mw.inspector.triggerBtn == nil {
		t.Fatal("expected trigger button in inspector")
	}
	test.Tap(mw.inspector.triggerBtn)
	drainUI()

	tile00 := mw.surface.tiles["0,0"]
	if tile00 == nil {
		t.Fatal("expected tile widget for (0,0)")
	}
	if tile00.title != "Focus" {
		t.Fatalf("expected tile (0,0) title \"Focus\", got %q", tile00.title)
	}
	if tile00.subtitle == "" {
		t.Fatal("expected non-empty countdown subtitle on tile (0,0)")
	}

	pluginsDlg := mw.ShowSettingsDialogTab(settingsTabPlugins)
	drainUI()
	pluginsDlg.Hide()
	drainUI()
}

func TestMainWindowMoveSwapCopyAcrossPagesAndProfiles(t *testing.T) {
	eng, _, mw := newTestStudio(t)
	mw.win.Resize(fyne.NewSize(1240, 760))
	drainUI()

	c00 := deck.Coordinates{Row: 0, Col: 0}
	c01 := deck.Coordinates{Row: 0, Col: 1}
	c02 := deck.Coordinates{Row: 0, Col: 2}
	c10 := deck.Coordinates{Row: 1, Col: 0}
	c11 := deck.Coordinates{Row: 1, Col: 1}

	// 1. Clickable Page Strip: starts with Page 1, "+" creates & switches to Page 2.
	if len(mw.pageButtons) != 1 || mw.pageButtons[0].Text != "Page 1" {
		t.Fatalf("unexpected initial page strip buttons: %d", len(mw.pageButtons))
	}
	if mw.pageAddBtn == nil {
		t.Fatal("expected non-nil pageAddBtn in page strip")
	}
	test.Tap(mw.pageAddBtn)
	drainUI()
	if snap := eng.Snapshot(); snap.PageCount != 2 || snap.PageIndex != 1 || len(mw.pageButtons) != 2 {
		t.Fatalf("expected Page 2 added & active, got count=%d idx=%d btns=%d", snap.PageCount, snap.PageIndex, len(mw.pageButtons))
	}
	test.Tap(mw.pageButtons[0])
	drainUI()
	if eng.Snapshot().PageIndex != 0 {
		t.Fatalf("expected Page 1 active after clicking page pill, got %d", eng.Snapshot().PageIndex)
	}

	// Assign customized actions on Page 1 (0,0) and (0,1).
	if _, err := eng.AssignKeypadAction(c00, core.ActionCounter); err != nil {
		t.Fatalf("AssignKeypadAction(c00) error = %v", err)
	}
	if err := eng.UpdateKeypadSlot(c00, "Alpha", "#38BDF8", nil); err != nil {
		t.Fatalf("UpdateKeypadSlot(c00) error = %v", err)
	}
	if _, err := eng.AssignKeypadAction(c01, core.ActionDelay); err != nil {
		t.Fatalf("AssignKeypadAction(c01) error = %v", err)
	}
	if err := eng.UpdateKeypadSlot(c01, "Beta", "#22C55E", nil); err != nil {
		t.Fatalf("UpdateKeypadSlot(c01) error = %v", err)
	}
	drainUI()

	// 2. Inspector Move + Cross-Page Pick-Target + Esc cancel + Auto-Swap + Undo.
	mw.SelectSlot(c00)
	drainUI()
	if mw.inspector.moveBtn.Disabled() || mw.inspector.copyBtn.Disabled() {
		t.Fatal("expected Move and Copy buttons enabled for assigned slot")
	}
	test.Tap(mw.inspector.moveBtn)
	drainUI()
	if mw.clipMode != clipMove || !mw.clipBanner.Visible() {
		t.Fatalf("expected clipMove active with visible banner, got mode=%v visible=%v", mw.clipMode, mw.clipBanner.Visible())
	}

	test.Tap(mw.pageButtons[1])
	drainUI()
	mw.handleKeyTap(c10)
	drainUI()
	if mw.clipMode != clipNone || mw.clipBanner.Visible() {
		t.Fatalf("expected Move mode cleared after placing on Page 2, got mode=%v", mw.clipMode)
	}
	if got := eng.Snapshot().Slots[c10.SlotKey()].Config; got == nil || got.Title != "Alpha" {
		t.Fatalf("expected Page 2 (1,0) to hold Alpha, got %+v", got)
	}

	// Start Move on Page 1 (0,1), verify bare Esc key cancels Move mode cleanly.
	test.Tap(mw.pageButtons[0])
	drainUI()
	if eng.Snapshot().Slots[c00.SlotKey()].Config != nil {
		t.Fatal("expected Page 1 (0,0) cleared after moving Alpha to Page 2")
	}
	mw.SelectSlot(c01)
	test.Tap(mw.inspector.moveBtn)
	drainUI()
	if mw.clipMode != clipMove {
		t.Fatal("expected clipMove before Esc")
	}
	mw.win.Canvas().OnTypedKey()(&fyne.KeyEvent{Name: fyne.KeyEscape})
	drainUI()
	if mw.clipMode != clipNone || mw.clipBanner.Visible() {
		t.Fatalf("expected bare Esc to cancel Move mode, got mode=%v", mw.clipMode)
	}

	// Move Page 1 (0,1) ("Beta") onto occupied Page 2 (1,0) ("Alpha") -> cross-page swap, then Undo.
	mw.StartMoveSelected()
	test.Tap(mw.pageButtons[1])
	drainUI()
	mw.handleKeyTap(c10)
	drainUI()
	if got := eng.Snapshot().Slots[c10.SlotKey()].Config; got == nil || got.Title != "Beta" {
		t.Fatalf("expected Page 2 (1,0) to hold Beta after swap, got %+v", got)
	}
	test.Tap(mw.pageButtons[0])
	drainUI()
	if got := eng.Snapshot().Slots[c01.SlotKey()].Config; got == nil || got.Title != "Alpha" {
		t.Fatalf("expected Page 1 (0,1) to hold Alpha after swap, got %+v", got)
	}
	mw.handleUndo()
	drainUI()
	if got := eng.Snapshot().Slots[c10.SlotKey()].Config; got == nil || got.Title != "Alpha" {
		t.Fatalf("expected Undo to restore Alpha at Page 2 (1,0), got %+v", got)
	}
	test.Tap(mw.pageButtons[0])
	drainUI()
	if got := eng.Snapshot().Slots[c01.SlotKey()].Config; got == nil || got.Title != "Beta" {
		t.Fatalf("expected Undo to restore Beta at Page 1 (0,1), got %+v", got)
	}
	test.Tap(mw.pageButtons[1])
	drainUI()

	// 3. Drag threshold guard (<10px ignored), tile->tile drag, and tile->pageAddBtn drop.
	// Force a fresh layout pass in headless test driver (NewMainWindow initialized at 1240x760).
	mw.win.Resize(fyne.NewSize(1280, 800))
	drainUI()
	tile10 := mw.surface.tiles[c10.SlotKey()]
	tile11 := mw.surface.tiles[c11.SlotKey()]
	if tile10 == nil || tile11 == nil {
		t.Fatal("expected tiles for (1,0) and (1,1)")
	}
	if tile11.Size().Width <= 0 || mw.pageAddBtn.Size().Width <= 0 {
		t.Fatalf("expected positive laid-out bounds, got tile11=%+v pageAddBtn=%+v", tile11.Size(), mw.pageAddBtn.Size())
	}
	tile10.Dragged(&fyne.DragEvent{
		Position: fyne.NewPos(10, 10),
		Dragged:  fyne.Delta{DX: 3, DY: 3},
	})
	if mw.dragActive {
		t.Fatal("expected sub-10px drag delta not to activate drag mode")
	}
	tile10.DragEnd()

	drv := fyne.CurrentApp().Driver()
	dstPos := drv.AbsolutePositionForObject(tile11).Add(fyne.NewPos(tile11.Size().Width/2, tile11.Size().Height/2))
	tile10.Dragged(&fyne.DragEvent{
		Position:         fyne.NewPos(12, 12),
		AbsolutePosition: dstPos,
		Dragged:          fyne.Delta{DX: 20, DY: 0},
	})
	if !mw.dragActive || !mw.hasHoverDropKey || mw.hoverDropKey != c11 {
		t.Fatalf("expected dragActive hovering (1,1), got active=%v hover=%v key=%+v", mw.dragActive, mw.hasHoverDropKey, mw.hoverDropKey)
	}
	tile10.DragEnd()
	drainUI()
	if mw.dragActive {
		t.Fatal("expected dragActive cleared after DragEnd")
	}
	if got := eng.Snapshot().Slots[c11.SlotKey()].Config; got == nil || got.Title != "Alpha" {
		t.Fatalf("expected drag drop to move Alpha to (1,1), got %+v", got)
	}

	addPos := drv.AbsolutePositionForObject(mw.pageAddBtn).Add(fyne.NewPos(mw.pageAddBtn.Size().Width/2, mw.pageAddBtn.Size().Height/2))
	mw.handleTileDragMove(c11, addPos)
	if !mw.dragActive || !mw.hoverAddPage {
		t.Fatalf("expected drag hover on '+' add page button, got active=%v hoverAdd=%v", mw.dragActive, mw.hoverAddPage)
	}
	mw.handleTileDragEnd(c11, addPos)
	drainUI()
	if snap := eng.Snapshot(); snap.PageCount != 3 || len(mw.pageButtons) != 3 {
		t.Fatalf("expected Page 3 created via drag onto '+', got PageCount=%d btns=%d", snap.PageCount, len(mw.pageButtons))
	}
	test.Tap(mw.pageButtons[2])
	drainUI()
	if got := eng.Snapshot().Slots[c00.SlotKey()].Config; got == nil || got.Title != "Alpha" {
		t.Fatalf("expected Page 3 (0,0) to hold Alpha after drop onto '+', got %+v", got)
	}

	// 4. Entry shortcut safety: Ctrl+C while Inspector Entry focused must NOT trigger slot Copy.
	mw.SelectSlot(c00)
	mw.inspector.titleEntry.SetText("Editable")
	mw.win.Canvas().Focus(mw.inspector.titleEntry)
	mw.shortcuts.TypedShortcut(&fyne.ShortcutCopy{})
	drainUI()
	if mw.clipMode != clipNone {
		t.Fatalf("expected focused entry Ctrl+C not to hijack Studio slot copy, got mode=%v", mw.clipMode)
	}
	mw.win.Canvas().Focus(nil)

	// 5. Cross-profile Copy/Paste (including Folder subtree cloning) + Dial Move/Swap.
	test.Tap(mw.pageButtons[0])
	drainUI()
	if _, err := eng.AssignKeypadAction(c02, core.ActionFolderOpen); err != nil {
		t.Fatalf("AssignKeypadAction(FolderOpen) error = %v", err)
	}
	if err := eng.PressKey(context.Background(), c02); err != nil {
		t.Fatalf("open folder error = %v", err)
	}
	origFolderID := eng.Snapshot().ActiveFolderID
	if _, err := eng.AssignKeypadAction(c01, core.ActionDelay); err != nil {
		t.Fatalf("AssignKeypadAction(folder child) error = %v", err)
	}
	if err := eng.UpdateKeypadSlot(c01, "Inner Delay", "", nil); err != nil {
		t.Fatalf("UpdateKeypadSlot(folder child) error = %v", err)
	}
	_ = eng.CloseFolder()
	drainUI()

	mw.SelectSlot(c02)
	mw.shortcuts.TypedShortcut(&fyne.ShortcutCopy{})
	drainUI()
	if mw.clipMode != clipCopy || mw.clipSlot == nil {
		t.Fatalf("expected clipCopy after Ctrl+C on folder slot, got mode=%v", mw.clipMode)
	}

	if _, err := eng.CreateProfile("Second", deck.GridSize{Cols: 3, Rows: 2}, 2); err != nil {
		t.Fatalf("CreateProfile(Second) error = %v", err)
	}
	drainUI()
	mw.SelectSlot(c00)
	mw.shortcuts.TypedShortcut(&fyne.ShortcutPaste{})
	drainUI()
	if mw.confirmDlg != nil {
		t.Fatal("expected no confirmation dialog when pasting onto empty slot")
	}

	pastedFolder := eng.Snapshot().Slots[c00.SlotKey()].Config
	if pastedFolder == nil || pastedFolder.ActionUUID != core.ActionFolderOpen {
		t.Fatalf("expected pasted FolderOpen at Profile 2 (0,0), got %+v", pastedFolder)
	}
	clonedFID, _ := pastedFolder.Settings["folderId"].(string)
	if clonedFID == "" || clonedFID == origFolderID {
		t.Fatalf("expected independent cloned folderId != %q, got %q", origFolderID, clonedFID)
	}
	if err := eng.PressKey(context.Background(), c00); err != nil {
		t.Fatalf("open pasted folder error = %v", err)
	}
	drainUI()
	if inner := eng.Snapshot().Slots[c01.SlotKey()].Config; inner == nil || inner.Title != "Inner Delay" {
		t.Fatalf("expected Inner Delay inside cloned folder at (0,1), got %+v", inner)
	}
	_ = eng.CloseFolder()
	drainUI()

	// Pasting onto an occupied slot prompts confirmation: Dismiss keeps existing, Confirm replaces (and Undo restores).
	if _, err := eng.AssignKeypadAction(c01, core.ActionCounter); err != nil {
		t.Fatalf("AssignKeypadAction(c01) error = %v", err)
	}
	if err := eng.UpdateKeypadSlot(c01, "KeepMe", "", nil); err != nil {
		t.Fatalf("UpdateKeypadSlot(c01) error = %v", err)
	}
	drainUI()
	mw.SelectSlot(c01)
	mw.shortcuts.TypedShortcut(&fyne.ShortcutPaste{})
	drainUI()
	if mw.confirmDlg == nil {
		t.Fatal("expected overwrite confirmation dialog when pasting onto occupied slot")
	}
	if got := eng.Snapshot().Slots[c01.SlotKey()].Config; got == nil || got.Title != "KeepMe" {
		t.Fatalf("expected KeepMe preserved before confirming paste, got %+v", got)
	}
	mw.confirmDlg.Dismiss()
	drainUI()
	if mw.confirmDlg != nil {
		t.Fatal("expected confirmDlg cleared after Dismiss")
	}
	if got := eng.Snapshot().Slots[c01.SlotKey()].Config; got == nil || got.Title != "KeepMe" {
		t.Fatalf("expected Dismiss to preserve KeepMe, got %+v", got)
	}

	mw.shortcuts.TypedShortcut(&fyne.ShortcutPaste{})
	drainUI()
	if mw.confirmDlg == nil {
		t.Fatal("expected overwrite confirmation dialog on second paste attempt")
	}
	mw.confirmDlg.Confirm()
	drainUI()
	if mw.confirmDlg != nil {
		t.Fatal("expected confirmDlg cleared after Confirm")
	}
	if got := eng.Snapshot().Slots[c01.SlotKey()].Config; got == nil || got.ActionUUID != core.ActionFolderOpen {
		t.Fatalf("expected Confirm to replace (0,1) with FolderOpen, got %+v", got)
	}
	mw.handleUndo()
	drainUI()
	if got := eng.Snapshot().Slots[c01.SlotKey()].Config; got == nil || got.Title != "KeepMe" {
		t.Fatalf("expected Undo after confirmed paste to restore KeepMe, got %+v", got)
	}

	mw.ClearClipboard()

	// Dial Move / Swap in Profile 2 (2 dials).
	if _, err := eng.AssignDialAction(0, core.ActionCounter); err != nil {
		t.Fatalf("AssignDialAction(0) error = %v", err)
	}
	if err := eng.UpdateDialSlot(0, "Dial Zero", "", nil); err != nil {
		t.Fatalf("UpdateDialSlot(0) error = %v", err)
	}
	if _, err := eng.AssignDialAction(1, core.ActionCounter); err != nil {
		t.Fatalf("AssignDialAction(1) error = %v", err)
	}
	if err := eng.UpdateDialSlot(1, "Dial One", "", nil); err != nil {
		t.Fatalf("UpdateDialSlot(1) error = %v", err)
	}
	drainUI()

	mw.SelectDial(0)
	mw.StartMoveSelected()
	mw.handleDialTap(1)
	drainUI()
	dials := eng.Snapshot().DialSlots
	if dials[0].Config == nil || dials[0].Config.Title != "Dial One" || dials[1].Config == nil || dials[1].Config.Title != "Dial Zero" {
		t.Fatalf("expected swapped dials, got d0=%+v d1=%+v", dials[0].Config, dials[1].Config)
	}
}
