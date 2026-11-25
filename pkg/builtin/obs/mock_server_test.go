package obs

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"

	"golang.org/x/net/websocket"
)

type mockSceneItem struct {
	id      int
	name    string
	enabled bool
}

type mockOBSServer struct {
	srv      *httptest.Server
	Host     string
	Port     int
	Password string

	mu              sync.Mutex
	conns           map[*websocket.Conn]struct{}
	programScene    string
	previewScene    string
	studioMode      bool
	streaming       bool
	recording       bool
	recordingPaused bool
	replayActive    bool
	replaySaved     int
	transitions     int
	scenes          []string
	sceneItems      map[string][]*mockSceneItem
	inputs          []string
	inputMuted      map[string]bool
	inputDb         map[string]float64
	inputMul        map[string]float64
	customCalls     []string
}

func newMockOBSServer(password string) *mockOBSServer {
	micMul := math.Pow(10, -6.0/20.0)
	deskMul := math.Pow(10, -12.0/20.0)
	m := &mockOBSServer{
		Password:     password,
		conns:        make(map[*websocket.Conn]struct{}),
		programScene: "Main",
		previewScene: "Starting Soon",
		scenes:       []string{"Starting Soon", "Main", "BRB"},
		sceneItems: map[string][]*mockSceneItem{
			"Main": {
				{id: 1, name: "Webcam", enabled: true},
				{id: 2, name: "Overlay", enabled: false},
			},
		},
		inputs:     []string{"Mic/Aux", "Desktop Audio"},
		inputMuted: map[string]bool{"Mic/Aux": false, "Desktop Audio": false},
		inputDb:    map[string]float64{"Mic/Aux": -6.0, "Desktop Audio": -12.0},
		inputMul:   map[string]float64{"Mic/Aux": micMul, "Desktop Audio": deskMul},
	}

	wsSrv := websocket.Server{
		Handshake: func(_ *websocket.Config, _ *http.Request) error { return nil },
		Handler:   m.handleConn,
	}
	m.srv = httptest.NewServer(wsSrv)
	u, _ := url.Parse(m.srv.URL)
	m.Host = u.Hostname()
	m.Port, _ = strconv.Atoi(u.Port())
	return m
}

func (m *mockOBSServer) Close() {
	m.mu.Lock()
	for c := range m.conns {
		_ = c.Close()
	}
	m.mu.Unlock()
	m.srv.Close()
}

func (m *mockOBSServer) DropAllConnections() {
	m.mu.Lock()
	conns := make([]*websocket.Conn, 0, len(m.conns))
	for c := range m.conns {
		conns = append(conns, c)
	}
	m.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
}

func (m *mockOBSServer) BroadcastEvent(eventType string, data map[string]any) {
	m.mu.Lock()
	conns := make([]*websocket.Conn, 0, len(m.conns))
	for c := range m.conns {
		conns = append(conns, c)
	}
	m.mu.Unlock()

	for _, c := range conns {
		_ = sendEnvelope(c, opEvent, map[string]any{
			"eventType": eventType,
			"eventData": data,
		})
	}
}

func (m *mockOBSServer) handleConn(conn *websocket.Conn) {
	defer func() { _ = conn.Close() }()

	const salt = "mock-salt-value"
	const challenge = "mock-challenge-value"

	hello := map[string]any{
		"obsWebSocketVersion": "5.5.2",
		"rpcVersion":          1,
	}
	if m.Password != "" {
		hello["authentication"] = map[string]any{
			"challenge": challenge,
			"salt":      salt,
		}
	}
	if err := sendEnvelope(conn, opHello, hello); err != nil {
		return
	}

	identEnv, err := recvEnvelope(conn)
	if err != nil || identEnv.Op != opIdentify {
		return
	}
	var ident identifyPayload
	if err := json.Unmarshal(identEnv.D, &ident); err != nil {
		return
	}
	if m.Password != "" {
		expected := ComputeAuth(m.Password, salt, challenge)
		if ident.Authentication != expected {
			return
		}
	}
	if err := sendEnvelope(conn, opIdentified, map[string]any{"negotiatedRpcVersion": 1}); err != nil {
		return
	}

	m.mu.Lock()
	m.conns[conn] = struct{}{}
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.conns, conn)
		m.mu.Unlock()
	}()

	for {
		env, err := recvEnvelope(conn)
		if err != nil {
			return
		}
		if env.Op != opRequest {
			continue
		}
		var req requestPayload
		if err := json.Unmarshal(env.D, &req); err != nil {
			continue
		}
		respData, code, comment := m.handleRequest(req.RequestType, req.RequestData)
		resp := map[string]any{
			"requestType": req.RequestType,
			"requestId":   req.RequestID,
			"requestStatus": map[string]any{
				"result": code == 100,
				"code":   code,
			},
			"responseData": respData,
		}
		if comment != "" {
			resp["requestStatus"].(map[string]any)["comment"] = comment
		}
		if err := sendEnvelope(conn, opRequestResponse, resp); err != nil {
			return
		}
	}
}

func (m *mockOBSServer) handleRequest(reqType string, data map[string]any) (map[string]any, int, string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	switch reqType {
	case "GetVersion":
		return map[string]any{
			"obsVersion":          "30.2.3",
			"obsWebSocketVersion": "5.5.2",
			"rpcVersion":          1,
		}, 100, ""

	case "GetSceneList":
		arr := make([]map[string]any, len(m.scenes))
		for i, name := range m.scenes {
			arr[i] = map[string]any{"sceneName": name, "sceneIndex": i}
		}
		return map[string]any{
			"currentProgramSceneName": m.programScene,
			"currentPreviewSceneName": m.previewScene,
			"scenes":                  arr,
		}, 100, ""

	case "SetCurrentProgramScene":
		scene := stringMapVal(data, "sceneName")
		m.programScene = scene
		return nil, 100, ""

	case "SetCurrentPreviewScene":
		scene := stringMapVal(data, "sceneName")
		m.previewScene = scene
		return nil, 100, ""

	case "GetInputList":
		arr := make([]map[string]any, len(m.inputs))
		for i, name := range m.inputs {
			arr[i] = map[string]any{"inputName": name}
		}
		return map[string]any{"inputs": arr}, 100, ""

	case "GetSceneItemList":
		scene := stringMapVal(data, "sceneName")
		items := m.sceneItems[scene]
		arr := make([]map[string]any, len(items))
		for i, it := range items {
			arr[i] = map[string]any{
				"sceneItemId":      it.id,
				"sourceName":       it.name,
				"sceneItemEnabled": it.enabled,
			}
		}
		return map[string]any{"sceneItems": arr}, 100, ""

	case "GetSceneItemId":
		scene := stringMapVal(data, "sceneName")
		source := stringMapVal(data, "sourceName")
		for _, it := range m.sceneItems[scene] {
			if it.name == source {
				return map[string]any{"sceneItemId": it.id}, 100, ""
			}
		}
		return nil, 600, "scene item not found"

	case "GetSceneItemEnabled":
		scene := stringMapVal(data, "sceneName")
		id := intMapVal(data, "sceneItemId", 0)
		for _, it := range m.sceneItems[scene] {
			if it.id == id {
				return map[string]any{"sceneItemEnabled": it.enabled}, 100, ""
			}
		}
		return nil, 600, "scene item not found"

	case "SetSceneItemEnabled":
		scene := stringMapVal(data, "sceneName")
		id := intMapVal(data, "sceneItemId", 0)
		enabled := boolMapVal(data, "sceneItemEnabled", false)
		for _, it := range m.sceneItems[scene] {
			if it.id == id {
				it.enabled = enabled
				return nil, 100, ""
			}
		}
		return nil, 600, "scene item not found"

	case "GetInputMute":
		input := stringMapVal(data, "inputName")
		return map[string]any{"inputMuted": m.inputMuted[input]}, 100, ""

	case "ToggleInputMute":
		input := stringMapVal(data, "inputName")
		m.inputMuted[input] = !m.inputMuted[input]
		return map[string]any{"inputMuted": m.inputMuted[input]}, 100, ""

	case "SetInputMute":
		input := stringMapVal(data, "inputName")
		m.inputMuted[input] = boolMapVal(data, "inputMuted", false)
		return nil, 100, ""

	case "GetInputVolume":
		input := stringMapVal(data, "inputName")
		mul := m.inputMul[input]
		var dbVal any
		if mul > 0 {
			dbVal = m.inputDb[input]
		}
		return map[string]any{
			"inputVolumeMul": mul,
			"inputVolumeDb":  dbVal,
		}, 100, ""

	case "SetInputVolume":
		input := stringMapVal(data, "inputName")
		if _, hasMul := data["inputVolumeMul"]; hasMul {
			mul := max(floatMapVal(data, "inputVolumeMul", 0), 0)
			m.inputMul[input] = mul
			if mul > 0 {
				m.inputDb[input] = 20.0 * math.Log10(mul)
			} else {
				m.inputDb[input] = -100.0
			}
		} else {
			db := floatMapVal(data, "inputVolumeDb", 0)
			m.inputDb[input] = db
			m.inputMul[input] = math.Pow(10, db/20.0)
		}
		return nil, 100, ""

	case "GetStreamStatus":
		return map[string]any{"outputActive": m.streaming}, 100, ""
	case "StartStream":
		m.streaming = true
		return nil, 100, ""
	case "StopStream":
		m.streaming = false
		return nil, 100, ""
	case "ToggleStream":
		m.streaming = !m.streaming
		return map[string]any{"outputActive": m.streaming}, 100, ""

	case "GetRecordStatus":
		return map[string]any{"outputActive": m.recording, "outputPaused": m.recordingPaused}, 100, ""
	case "StartRecord":
		m.recording = true
		m.recordingPaused = false
		return nil, 100, ""
	case "StopRecord":
		m.recording = false
		m.recordingPaused = false
		return nil, 100, ""
	case "ToggleRecord":
		m.recording = !m.recording
		m.recordingPaused = false
		return map[string]any{"outputActive": m.recording}, 100, ""
	case "ToggleRecordPause":
		m.recordingPaused = !m.recordingPaused
		return map[string]any{"outputPaused": m.recordingPaused}, 100, ""

	case "GetReplayBufferStatus":
		return map[string]any{"outputActive": m.replayActive}, 100, ""
	case "StartReplayBuffer":
		m.replayActive = true
		return nil, 100, ""
	case "StopReplayBuffer":
		m.replayActive = false
		return nil, 100, ""
	case "ToggleReplayBuffer":
		m.replayActive = !m.replayActive
		return map[string]any{"outputActive": m.replayActive}, 100, ""
	case "SaveReplayBuffer":
		m.replaySaved++
		return nil, 100, ""

	case "GetStudioModeEnabled":
		return map[string]any{"studioModeEnabled": m.studioMode}, 100, ""
	case "SetStudioModeEnabled":
		m.studioMode = boolMapVal(data, "studioModeEnabled", false)
		return nil, 100, ""
	case "TriggerStudioModeTransition":
		m.transitions++
		m.programScene = m.previewScene
		return nil, 100, ""

	case "TriggerHotkeyByName":
		m.customCalls = append(m.customCalls, stringMapVal(data, "hotkeyName"))
		return nil, 100, ""

	default:
		return nil, 204, "unknown requestType"
	}
}
