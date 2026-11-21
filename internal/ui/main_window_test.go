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

	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"
	"golang.org/x/net/websocket"

	"github.com/mrpoundsign/PoundDeck/internal/app"
	"github.com/mrpoundsign/PoundDeck/pkg/builtin/core"
	"github.com/mrpoundsign/PoundDeck/pkg/builtin/obs"
	"github.com/mrpoundsign/PoundDeck/pkg/deck"
	"github.com/mrpoundsign/PoundDeck/pkg/plugin"
	"github.com/mrpoundsign/PoundDeck/pkg/secrets"
)

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
	eng, err := app.NewEngine(deck.NewMemoryStore(nil), reg)
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	t.Cleanup(func() {
		_ = eng.Close()
		drainUI()
	})

	mw := NewMainWindow(testApp, eng)
	return eng, obsProv, mw
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
