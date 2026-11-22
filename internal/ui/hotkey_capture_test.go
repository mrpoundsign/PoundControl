package ui

import (
	"slices"
	"strings"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"github.com/mrpoundsign/PoundControl/pkg/builtin/apps"
	"github.com/mrpoundsign/PoundControl/pkg/deck"
)

func TestCanonicalKeyFromFyne(t *testing.T) {
	cases := []struct {
		in     fyne.KeyName
		want   string
		wantOK bool
	}{
		{in: fyne.KeyM, want: "M", wantOK: true},
		{in: fyne.KeyName("a"), want: "A", wantOK: true},
		{in: fyne.Key9, want: "9", wantOK: true},
		{in: fyne.KeyF1, want: "F1", wantOK: true},
		{in: fyne.KeyName("F24"), want: "F24", wantOK: true},
		{in: fyne.KeyReturn, want: "Enter", wantOK: true},
		{in: fyne.KeyEnter, want: "Enter", wantOK: true},
		{in: fyne.KeyEscape, want: "Escape", wantOK: true},
		{in: fyne.KeyTab, want: "Tab", wantOK: true},
		{in: fyne.KeySpace, want: "Space", wantOK: true},
		{in: fyne.KeyBackspace, want: "Backspace", wantOK: true},
		{in: fyne.KeyPageUp, want: "PageUp", wantOK: true},
		{in: fyne.KeyPageDown, want: "PageDown", wantOK: true},
		{in: fyne.KeyUp, want: "Up", wantOK: true},
		{in: desktop.KeyControlLeft, want: "", wantOK: false},
		{in: desktop.KeyShiftRight, want: "", wantOK: false},
		{in: desktop.KeyAltLeft, want: "", wantOK: false},
		{in: desktop.KeySuperLeft, want: "", wantOK: false},
		{in: fyne.KeyUnknown, want: "", wantOK: false},
	}

	for _, tc := range cases {
		t.Run(string(tc.in), func(t *testing.T) {
			got, ok := canonicalKeyFromFyne(tc.in)
			if ok != tc.wantOK || got != tc.want {
				t.Fatalf("canonicalKeyFromFyne(%q) = (%q, %v), want (%q, %v)", tc.in, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

func TestHotkeyCaptureGlobalDialogFlow(t *testing.T) {
	eng, _, _, mw := newTestStudioWithApps(t)

	var suspendEvents []bool
	mw.SetHotkeySuspender(func(paused bool) {
		suspendEvents = append(suspendEvents, paused)
	})

	mw.SelectSlot(deck.Coordinates{Row: 0, Col: 0})
	test.Tap(mw.catalogButtons[apps.ActionHotkey])
	drainUI()

	if mw.inspector.hotkeyCaptureBtn == nil {
		t.Fatal("expected Global Hotkey Capture button in Keypad inspector")
	}

	test.Tap(mw.inspector.hotkeyCaptureBtn)
	sess := mw.inspector.lastCapture
	if sess == nil {
		t.Fatal("expected hotkey capture dialog session")
	}
	if !slices.Equal(suspendEvents, []bool{true}) {
		t.Fatalf("expected suspendEvents=[true] on open, got %v", suspendEvents)
	}
	if mw.Window().Canvas().Focused() != sess.pad {
		t.Fatalf("expected capture pad to be focused on open, got %T", mw.Window().Canvas().Focused())
	}
	if !sess.pad.AcceptsTab() {
		t.Fatal("expected capture pad to accept Tab so Ctrl+Tab/Tab can be captured")
	}

	// Bare single letter must be rejected for global hotkeys and leave Apply disabled.
	sess.pad.KeyDown(&fyne.KeyEvent{Name: fyne.KeyM})
	sess.pad.KeyUp(&fyne.KeyEvent{Name: fyne.KeyM})
	if sess.pad.valid {
		t.Fatal("expected bare M to be invalid in global capture mode")
	}
	if !sess.applyBtn.Disabled() {
		t.Fatal("expected Apply button disabled for bare M in global capture mode")
	}
	if !strings.Contains(sess.status.Text, "modifier") {
		t.Errorf("expected status message explaining modifier requirement, got %q", sess.status.Text)
	}

	// Hold LeftControl + LeftShift + F9 -> Ctrl+Shift+F9.
	sess.pad.KeyDown(&fyne.KeyEvent{Name: desktop.KeyControlLeft})
	sess.pad.KeyDown(&fyne.KeyEvent{Name: desktop.KeyShiftLeft})
	sess.pad.KeyDown(&fyne.KeyEvent{Name: fyne.KeyF9})
	// Immediate matching TypedKey delivered by GLFW on the same press must be suppressed.
	sess.pad.TypedKey(&fyne.KeyEvent{Name: fyne.KeyF9})
	sess.pad.KeyUp(&fyne.KeyEvent{Name: fyne.KeyF9})
	sess.pad.KeyUp(&fyne.KeyEvent{Name: desktop.KeyShiftLeft})
	sess.pad.KeyUp(&fyne.KeyEvent{Name: desktop.KeyControlLeft})

	if sess.pad.previewText != "Ctrl+Shift+F9" || !sess.pad.valid {
		t.Fatalf("expected valid Ctrl+Shift+F9 capture, got preview=%q valid=%v", sess.pad.previewText, sess.pad.valid)
	}
	if !sess.ctrlChk.Checked || !sess.shiftChk.Checked || sess.altChk.Checked || sess.winChk.Checked {
		t.Fatalf("unexpected modifier checkbox state: ctrl=%v shift=%v alt=%v win=%v",
			sess.ctrlChk.Checked, sess.shiftChk.Checked, sess.altChk.Checked, sess.winChk.Checked)
	}
	if sess.applyBtn.Disabled() {
		t.Fatal("expected Apply enabled after valid global hotkey capture")
	}

	test.Tap(sess.applyBtn)
	if !slices.Equal(suspendEvents, []bool{true, false}) {
		t.Fatalf("expected suspendEvents=[true, false] after Apply, got %v", suspendEvents)
	}
	if mw.inspector.hotkeyEntry.Text != "Ctrl+Shift+F9" {
		t.Fatalf("hotkeyEntry.Text = %q, want Ctrl+Shift+F9", mw.inspector.hotkeyEntry.Text)
	}

	// Provide required chord field and Save to verify engine persistence.
	chordEntry := mw.inspector.fields["chord"].widget.(*widget.Entry)
	chordEntry.SetText("Ctrl+Alt+Space")
	test.Tap(mw.inspector.saveBtn)
	drainUI()

	slot0 := eng.Snapshot().Slots["0,0"]
	if slot0.Config == nil || slot0.Config.Hotkey != "Ctrl+Shift+F9" {
		t.Fatalf("expected saved Global Hotkey Ctrl+Shift+F9, got %+v", slot0.Config)
	}
}

func TestHotkeyCaptureChordFieldManualModsShortcutAndClear(t *testing.T) {
	_, _, _, mw := newTestStudioWithApps(t)

	var suspendEvents []bool
	mw.SetHotkeySuspender(func(paused bool) {
		suspendEvents = append(suspendEvents, paused)
	})

	mw.SelectSlot(deck.Coordinates{Row: 0, Col: 0})
	test.Tap(mw.catalogButtons[apps.ActionHotkey])
	drainUI()

	capBtn, ok := mw.inspector.hotkeyFieldButtons["chord"]
	if !ok || capBtn == nil {
		t.Fatal("expected Capture button for FieldHotkey 'chord'")
	}
	chordEntry, ok := mw.inspector.fields["chord"].widget.(*widget.Entry)
	if !ok {
		t.Fatalf("expected *widget.Entry for FieldHotkey 'chord', got %T", mw.inspector.fields["chord"].widget)
	}

	test.Tap(capBtn)
	sess := mw.inspector.lastCapture
	if sess == nil {
		t.Fatal("expected chord capture session")
	}
	if !slices.Equal(suspendEvents, []bool{true}) {
		t.Fatalf("expected suspendEvents=[true], got %v", suspendEvents)
	}

	// Manually check Win + Shift (for OS-reserved combos like Win+Shift+S); pad must regain focus.
	test.Tap(sess.winChk)
	test.Tap(sess.shiftChk)
	if mw.Window().Canvas().Focused() != sess.pad {
		t.Fatalf("expected pad to regain focus after modifier toggle, got %T", mw.Window().Canvas().Focused())
	}

	sess.pad.KeyDown(&fyne.KeyEvent{Name: fyne.KeyS})
	sess.pad.KeyUp(&fyne.KeyEvent{Name: fyne.KeyS})
	if sess.pad.previewText != "Shift+Win+S" || !sess.pad.valid {
		t.Fatalf("expected valid Shift+Win+S, got preview=%q valid=%v", sess.pad.previewText, sess.pad.valid)
	}

	// Uncheck manual modifiers and verify bare Escape is accepted in chord mode via TypedKey fallback.
	test.Tap(sess.winChk)
	test.Tap(sess.shiftChk)
	sess.pad.TypedKey(&fyne.KeyEvent{Name: fyne.KeyEscape})
	if sess.pad.previewText != "Escape" || !sess.pad.valid {
		t.Fatalf("expected valid Escape in chord mode, got preview=%q valid=%v", sess.pad.previewText, sess.pad.valid)
	}

	// TypedShortcut fallback (e.g. desktop CustomShortcut Ctrl+Alt+K).
	sess.pad.TypedShortcut(&desktop.CustomShortcut{
		KeyName:  fyne.KeyK,
		Modifier: fyne.KeyModifierControl | fyne.KeyModifierAlt,
	})
	if sess.pad.previewText != "Ctrl+Alt+K" || !sess.pad.valid || sess.applyBtn.Disabled() {
		t.Fatalf("expected valid Ctrl+Alt+K from TypedShortcut, got preview=%q valid=%v disabled=%v",
			sess.pad.previewText, sess.pad.valid, sess.applyBtn.Disabled())
	}

	test.Tap(sess.applyBtn)
	if chordEntry.Text != "Ctrl+Alt+K" {
		t.Fatalf("chordEntry.Text = %q, want Ctrl+Alt+K", chordEntry.Text)
	}
	if !slices.Equal(suspendEvents, []bool{true, false}) {
		t.Fatalf("expected suspendEvents=[true, false], got %v", suspendEvents)
	}

	// Re-open and verify Clear button clears the field and resumes suspender.
	test.Tap(capBtn)
	sess2 := mw.inspector.lastCapture
	if sess2.pad.previewText != "Ctrl+Alt+K" || !sess2.ctrlChk.Checked || !sess2.altChk.Checked {
		t.Fatalf("expected initial chord pre-populated, got preview=%q ctrl=%v alt=%v",
			sess2.pad.previewText, sess2.ctrlChk.Checked, sess2.altChk.Checked)
	}
	test.Tap(sess2.clearBtn)
	if chordEntry.Text != "" {
		t.Fatalf("expected Clear to empty chordEntry, got %q", chordEntry.Text)
	}
	if !slices.Equal(suspendEvents, []bool{true, false, true, false}) {
		t.Fatalf("expected suspendEvents=[true, false, true, false], got %v", suspendEvents)
	}
}
