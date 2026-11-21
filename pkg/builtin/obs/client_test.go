package obs

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestClientAuthHandshakeCallEventsAndError(t *testing.T) {
	srv := newMockOBSServer("super-secret")
	defer srv.Close()

	evCh := make(chan Event, 4)
	var stMu sync.Mutex
	var statuses []ConnectionState
	client := NewClient(func(ev Event) {
		evCh <- ev
	}, func(st ClientStatus) {
		stMu.Lock()
		statuses = append(statuses, st.State)
		stMu.Unlock()
	})
	defer client.Disconnect()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// Wrong password must fail authentication cleanly.
	if err := client.Connect(ctx, srv.Host, srv.Port, "wrong-password"); err == nil {
		t.Fatal("expected authentication error with wrong password")
	}

	// Correct password connects and retrieves OBS version.
	if err := client.Connect(ctx, srv.Host, srv.Port, "super-secret"); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	st := client.Status()
	if st.State != StateConnected || st.OBSVersion != "30.2.3" {
		t.Fatalf("unexpected ClientStatus: %+v", st)
	}

	// Successful request/response roundtrip.
	resp, err := client.Call(ctx, "GetSceneList", nil)
	if err != nil {
		t.Fatalf("Call(GetSceneList) error = %v", err)
	}
	if resp["currentProgramSceneName"] != "Main" {
		t.Fatalf("unexpected currentProgramSceneName: %v", resp["currentProgramSceneName"])
	}

	// Unknown request surfaces OBS error code & comment.
	if _, err := client.Call(ctx, "DoesNotExist", nil); err == nil {
		t.Fatal("expected OBS error response for unknown requestType")
	}

	// Server-pushed event reaches callback.
	srv.BroadcastEvent("CurrentProgramSceneChanged", map[string]any{"sceneName": "BRB"})
	select {
	case ev := <-evCh:
		if ev.Type != "CurrentProgramSceneChanged" || ev.Data["sceneName"] != "BRB" {
			t.Fatalf("unexpected event received: %+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for OBS event")
	}
}

func TestClientAutoReconnectAfterDrop(t *testing.T) {
	srv := newMockOBSServer("")
	defer srv.Close()

	connectedCh := make(chan struct{}, 8)
	client := NewClient(nil, func(st ClientStatus) {
		if st.State == StateConnected {
			select {
			case connectedCh <- struct{}{}:
			default:
			}
		}
	})
	defer client.Disconnect()

	client.StartAutoReconnect(srv.Host, srv.Port, "")

	select {
	case <-connectedCh:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for initial auto-reconnect")
	}

	// Drop all connections; supervisor must reconnect automatically.
	srv.DropAllConnections()

	select {
	case <-connectedCh:
	case <-time.After(4 * time.Second):
		t.Fatal("timed out waiting for reconnect after dropped connection")
	}
}
