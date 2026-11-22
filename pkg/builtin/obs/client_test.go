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

func TestClientManualConnectStopsStaleSupervisorAndMaintains(t *testing.T) {
	srv := newMockOBSServer("secret-pw")
	defer srv.Close()

	errCh := make(chan ClientStatus, 16)
	connectedCh := make(chan struct{}, 8)
	client := NewClient(nil, func(st ClientStatus) {
		switch st.State {
		case StateError:
			select {
			case errCh <- st:
			default:
			}
		case StateConnected:
			select {
			case connectedCh <- struct{}{}:
			default:
			}
		}
	})
	defer client.Disconnect()

	// Start auto-reconnect with a wrong password (simulating stale startup credentials).
	client.StartAutoReconnect(srv.Host, srv.Port, "")
	select {
	case <-errCh:
	case <-time.After(3 * time.Second):
		t.Fatal("expected initial auth error from stale supervisor")
	}

	// Manual Connect with the correct password must stop the stale supervisor and stay connected.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := client.Connect(ctx, srv.Host, srv.Port, "secret-pw"); err != nil {
		t.Fatalf("Connect(secret-pw) error = %v", err)
	}
	// Drain any earlier notifications before starting maintenance watcher.
	for len(errCh) > 0 {
		<-errCh
	}
	for len(connectedCh) > 0 {
		<-connectedCh
	}

	// MaintainCurrentConnection must preserve the live session without dropping it.
	client.MaintainCurrentConnection(srv.Host, srv.Port, "secret-pw")

	// Wait past the initial 1s backoff interval to confirm no stale supervisor drops the session.
	select {
	case st := <-errCh:
		t.Fatalf("unexpected disconnect/error while connected: %+v", st)
	case <-time.After(1500 * time.Millisecond):
	}
	if !client.IsConnected() {
		t.Fatal("expected client to remain connected")
	}

	// If OBS later drops the connection, MaintainCurrentConnection must reconnect with the updated password.
	srv.DropAllConnections()
	select {
	case <-connectedCh:
	case <-time.After(4 * time.Second):
		t.Fatal("expected MaintainCurrentConnection to reconnect after drop")
	}
}
