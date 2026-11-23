package ui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"
	"github.com/mrpoundsign/appinstaller/pkg/updater"
)

func TestShouldCheckStartupUpdate(t *testing.T) {
	cases := []struct {
		ver  string
		want bool
	}{
		{ver: "", want: false},
		{ver: "dev", want: false},
		{ver: "0.1.0-dev", want: false},
		{ver: "0.1.1-snapshot", want: false},
		{ver: "0.1.0", want: true},
		{ver: "v0.2.3", want: true},
	}
	for _, tc := range cases {
		if got := shouldCheckStartupUpdate(tc.ver); got != tc.want {
			t.Errorf("shouldCheckStartupUpdate(%q) = %v, want %v", tc.ver, got, tc.want)
		}
	}
}

func waitForUIUpdate(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		drainUI()
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for async UI update")
}

func TestCheckForUpdatesManualFlows(t *testing.T) {
	drainUI()
	testApp := test.NewApp()
	t.Cleanup(func() {
		drainUI()
		testApp.Quit()
	})

	var (
		mu     sync.Mutex
		status int
		tag    string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		st := status
		tg := tag
		mu.Unlock()
		if st != http.StatusOK {
			http.Error(w, "upstream error", st)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(updater.ReleaseInfo{
			TagName: tg,
			Name:    "Release " + tg,
			Body:    "- Test release notes",
			HTMLURL: "https://example.invalid/releases/" + tg,
		})
	}))
	defer srv.Close()

	origFactory := newUpdateChecker
	newUpdateChecker = func() *updater.Checker {
		c := updater.NewChecker(updateRepoOwner, updateRepoName)
		c.APIBaseURL = srv.URL
		return c
	}
	t.Cleanup(func() {
		newUpdateChecker = origFactory
	})

	win := testApp.NewWindow("Update Test")
	statusLbl := widget.NewLabel("")
	checkBtn := widget.NewButton("Check", nil)

	// 1. Up-to-date case.
	mu.Lock()
	status = http.StatusOK
	tag = "v0.1.0"
	mu.Unlock()

	var note string
	checkForUpdatesManual(win, "0.1.0", statusLbl, checkBtn, func(s string) { note = s })
	waitForUIUpdate(t, func() bool { return !checkBtn.Disabled() })
	if !strings.Contains(statusLbl.Text, "up to date") || note != statusLbl.Text {
		t.Fatalf("unexpected up-to-date status: label=%q note=%q", statusLbl.Text, note)
	}

	// 2. Newer release available opens update dialog overlay.
	mu.Lock()
	status = http.StatusOK
	tag = "v0.2.0"
	mu.Unlock()

	checkForUpdatesManual(win, "0.1.0", statusLbl, checkBtn, func(s string) { note = s })
	waitForUIUpdate(t, func() bool { return !checkBtn.Disabled() })
	if !strings.Contains(statusLbl.Text, "v0.2.0") || note != statusLbl.Text {
		t.Fatalf("unexpected update available status: label=%q note=%q", statusLbl.Text, note)
	}
	if len(win.Canvas().Overlays().List()) == 0 {
		t.Fatal("expected update dialog overlay when newer release exists")
	}
	for _, ov := range win.Canvas().Overlays().List() {
		ov.Hide()
	}

	// 3. Server error reports failure cleanly.
	mu.Lock()
	status = http.StatusInternalServerError
	mu.Unlock()

	checkForUpdatesManual(win, "0.1.0", statusLbl, checkBtn, func(s string) { note = s })
	waitForUIUpdate(t, func() bool { return !checkBtn.Disabled() })
	if !strings.HasPrefix(statusLbl.Text, "Update check failed:") || note != statusLbl.Text {
		t.Fatalf("unexpected error status: label=%q note=%q", statusLbl.Text, note)
	}
}
