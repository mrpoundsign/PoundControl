package hotkey

import (
	"testing"

	"github.com/mrpoundsign/PoundDeck/pkg/deck"
)

type fakeBackend struct {
	bindings   []registeredBinding
	onTrig     func(deck.Coordinates)
	closed     bool
	syncCalls  int
	closeCalls int
}

func (f *fakeBackend) sync(bindings []registeredBinding, onTrigger func(deck.Coordinates)) (int, error) {
	f.syncCalls++
	f.closed = false
	f.bindings = append([]registeredBinding(nil), bindings...)
	f.onTrig = onTrigger
	return len(bindings), nil
}

func (f *fakeBackend) close() {
	f.closeCalls++
	f.closed = true
}

func TestParseHotkey(t *testing.T) {
	cases := []struct {
		raw     string
		want    string
		wantErr bool
	}{
		{raw: "ctrl+shift+m", want: "Ctrl+Shift+M"},
		{raw: "Alt + F9", want: "Alt+F9"},
		{raw: "f13", want: "F13"},
		{raw: "win+ctrl+space", want: "Ctrl+Win+Space"},
		{raw: "ctrl+enter", want: "Ctrl+Enter"},
		{raw: "alt+up", want: "Alt+Up"},
		{raw: "m", wantErr: true},
		{raw: "enter", wantErr: true},
		{raw: "space", wantErr: true},
		{raw: "ctrl+", wantErr: true},
		{raw: "ctrl+a+b", wantErr: true},
		{raw: "ctrl+bogus", wantErr: true},
		{raw: "", wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			spec, err := Parse(tc.raw)
			if (err != nil) != tc.wantErr {
				t.Fatalf("Parse(%q) error = %v, wantErr %v", tc.raw, err, tc.wantErr)
			}
			if !tc.wantErr && spec.String() != tc.want {
				t.Errorf("Parse(%q).String() = %q, want %q", tc.raw, spec.String(), tc.want)
			}
		})
	}
}

func TestManagerSyncTriggerAndClose(t *testing.T) {
	fb := &fakeBackend{}
	var triggered []deck.Coordinates
	mgr := newManagerWithBackend(fb, func(c deck.Coordinates) {
		triggered = append(triggered, c)
	})

	cnt := mgr.SyncBindings([]Binding{
		{Coords: deck.Coordinates{Row: 0, Col: 0}, Hotkey: "ctrl+shift+f1"},
		{Coords: deck.Coordinates{Row: 0, Col: 1}, Hotkey: "F14"},
		{Coords: deck.Coordinates{Row: 0, Col: 2}, Hotkey: "m"},       // invalid bare letter skipped
		{Coords: deck.Coordinates{Row: 0, Col: 3}, Hotkey: "F14"},     // duplicate skipped
		{Coords: deck.Coordinates{Row: 1, Col: 0}, Hotkey: ""},        // empty skipped
	})
	if cnt != 2 || mgr.ActiveCount() != 2 {
		t.Fatalf("expected 2 active bindings, got cnt=%d active=%d", cnt, mgr.ActiveCount())
	}
	if len(fb.bindings) != 2 || fb.bindings[0].spec.String() != "Ctrl+Shift+F1" {
		t.Fatalf("unexpected backend bindings: %+v", fb.bindings)
	}

	fb.onTrig(fb.bindings[1].coords)
	if len(triggered) != 1 || triggered[0] != (deck.Coordinates{Row: 0, Col: 1}) {
		t.Fatalf("unexpected triggered coords: %+v", triggered)
	}

	mgr.Close()
	if !fb.closed || mgr.ActiveCount() != 0 {
		t.Fatalf("expected backend closed and active count 0, closed=%v active=%d", fb.closed, mgr.ActiveCount())
	}
}

func TestManagerSuspendAndResume(t *testing.T) {
	fb := &fakeBackend{}
	mgr := newManagerWithBackend(fb, nil)

	if cnt := mgr.SyncBindings([]Binding{
		{Coords: deck.Coordinates{Row: 0, Col: 0}, Hotkey: "Ctrl+Shift+M"},
	}); cnt != 1 || fb.syncCalls != 1 {
		t.Fatalf("initial sync got cnt=%d syncCalls=%d, want 1/1", cnt, fb.syncCalls)
	}

	mgr.SetSuspended(true)
	if !fb.closed || fb.closeCalls != 1 || mgr.ActiveCount() != 0 {
		t.Fatalf("expected suspended backend closed and active=0, closed=%v closeCalls=%d active=%d",
			fb.closed, fb.closeCalls, mgr.ActiveCount())
	}

	// Updating bindings while suspended defers backend sync until resume.
	if cnt := mgr.SyncBindings([]Binding{
		{Coords: deck.Coordinates{Row: 0, Col: 0}, Hotkey: "Alt+F9"},
		{Coords: deck.Coordinates{Row: 0, Col: 1}, Hotkey: "Ctrl+Enter"},
	}); cnt != 0 || fb.syncCalls != 1 {
		t.Fatalf("sync while suspended should defer backend sync, got cnt=%d syncCalls=%d", cnt, fb.syncCalls)
	}

	mgr.SetSuspended(false)
	if fb.closed || fb.syncCalls != 2 || mgr.ActiveCount() != 2 {
		t.Fatalf("expected resumed backend active=2 syncCalls=2, closed=%v syncCalls=%d active=%d",
			fb.closed, fb.syncCalls, mgr.ActiveCount())
	}
	if len(fb.bindings) != 2 || fb.bindings[0].spec.String() != "Alt+F9" || fb.bindings[1].spec.String() != "Ctrl+Enter" {
		t.Fatalf("unexpected resumed bindings: %+v", fb.bindings)
	}

	// Duplicate resume is a no-op.
	mgr.SetSuspended(false)
	if fb.syncCalls != 2 {
		t.Fatalf("duplicate SetSuspended(false) triggered extra sync calls: %d", fb.syncCalls)
	}

	mgr.Close()
}
