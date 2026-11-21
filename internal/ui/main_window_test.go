package ui

import (
	"strings"
	"testing"

	"fyne.io/fyne/v2/test"

	"github.com/mrpoundsign/PoundDeck/pkg/deck"
)

func TestMainWindowInitAndSlotSelection(t *testing.T) {
	testApp := test.NewApp()
	defer testApp.Quit()
	testApp.Settings().SetTheme(&StudioTheme{})

	mw := NewMainWindow(testApp)
	if mw.Window() == nil {
		t.Fatal("expected non-nil Fyne window")
	}

	if got := mw.SelectedSlot(); got != (deck.Coordinates{Row: 0, Col: 0}) {
		t.Fatalf("initial SelectedSlot() = %+v, want (0, 0)", got)
	}
	if !strings.Contains(mw.inspectorSlotLbl.Text, "Row 0, Col 0") {
		t.Fatalf("initial inspector text = %q, expected Row 0, Col 0", mw.inspectorSlotLbl.Text)
	}

	target := deck.Coordinates{Row: 2, Col: 3}
	tile, ok := mw.tiles[target.SlotKey()]
	if !ok {
		t.Fatalf("missing tile for slot %s", target.SlotKey())
	}

	test.Tap(tile)

	if got := mw.SelectedSlot(); got != target {
		t.Fatalf("after Tap, SelectedSlot() = %+v, want %+v", got, target)
	}
	if !tile.selected {
		t.Error("expected tapped tile to be marked selected")
	}
	if prev := mw.tiles[(deck.Coordinates{Row: 0, Col: 0}).SlotKey()]; prev.selected {
		t.Error("expected previously selected tile (0, 0) to be deselected")
	}
	if !strings.Contains(mw.inspectorSlotLbl.Text, "Row 2, Col 3") {
		t.Errorf("inspector text after Tap = %q, expected Row 2, Col 3", mw.inspectorSlotLbl.Text)
	}
	if !strings.Contains(mw.statusBarLbl.Text, "(2, 3)") {
		t.Errorf("status bar after Tap = %q, expected (2, 3)", mw.statusBarLbl.Text)
	}

	// Out-of-bounds selection must be safely ignored.
	mw.SelectSlot(deck.Coordinates{Row: 99, Col: 99})
	if got := mw.SelectedSlot(); got != target {
		t.Errorf("out-of-bounds SelectSlot changed selection to %+v, want %+v", got, target)
	}
}
