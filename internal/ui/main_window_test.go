package ui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
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
	"github.com/mrpoundsign/PoundDeck/pkg/builtin/apps"
	"github.com/mrpoundsign/PoundDeck/pkg/builtin/core"
	"github.com/mrpoundsign/PoundDeck/pkg/builtin/obs"
	"github.com/mrpoundsign/PoundDeck/pkg/deck"
	"github.com/mrpoundsign/PoundDeck/pkg/plugin"
	"github.com/mrpoundsign/PoundDeck/pkg/secrets"
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
