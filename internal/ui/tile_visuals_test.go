package ui

import (
	"image/color"
	"math"
	"strings"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/test"

	"github.com/mrpoundsign/PoundControl/internal/app"
	"github.com/mrpoundsign/PoundControl/pkg/deck"
)

func TestWrapCanvasTextAndTruncate(t *testing.T) {
	testApp := test.NewApp()
	t.Cleanup(testApp.Quit)

	bold := fyne.TextStyle{Bold: true}
	const maxW = float32(82) // matches 100px tile min width minus glow & text margins

	lines := wrapCanvasText("Desktop and Printers", maxW, 12, bold, maxTileTitleLines)
	if len(lines) < 2 || len(lines) > maxTileTitleLines {
		t.Fatalf("expected 2..%d wrapped lines, got %d (%q)", maxTileTitleLines, len(lines), lines)
	}
	for i, line := range lines {
		if w := measureCanvasTextWidth(line, 12, bold); w > maxW {
			t.Errorf("line %d %q width %.1f exceeds maxWidth %.1f", i, line, w, maxW)
		}
	}
	if joined := strings.Join(lines, " "); joined != "Desktop and Printers" {
		t.Errorf("joined wrapped lines = %q, want %q", joined, "Desktop and Printers")
	}

	unspaced := wrapCanvasText("SupercalifragilisticexpialidociousAndMore", maxW, 12, bold, maxTileTitleLines)
	if len(unspaced) < 2 || len(unspaced) > maxTileTitleLines {
		t.Fatalf("expected unspaced word wrapped into 2..%d lines, got %d (%q)", maxTileTitleLines, len(unspaced), unspaced)
	}
	for i, line := range unspaced {
		if w := measureCanvasTextWidth(line, 12, bold); w > maxW {
			t.Errorf("unspaced line %d %q width %.1f exceeds maxWidth %.1f", i, line, w, maxW)
		}
	}
	if !strings.HasSuffix(unspaced[len(unspaced)-1], "…") {
		t.Errorf("expected overflowing unspaced word to end with ellipsis, got %q", unspaced[len(unspaced)-1])
	}

	capped := wrapCanvasText("One Two Three Four Five Six Seven Eight Nine Ten", maxW, 12, bold, 2)
	if len(capped) != 2 {
		t.Fatalf("expected exactly 2 lines when capped at 2, got %d (%q)", len(capped), capped)
	}
	if !strings.HasSuffix(capped[1], "…") {
		t.Errorf("expected capped last line to end with ellipsis, got %q", capped[1])
	}
	if w := measureCanvasTextWidth(capped[1], 12, bold); w > maxW {
		t.Errorf("capped line 2 width %.1f exceeds maxWidth %.1f", w, maxW)
	}

	explicit := wrapCanvasText("First Line\nSecond Line", 140, 12, bold, maxTileTitleLines)
	if len(explicit) != 2 || explicit[0] != "First Line" || explicit[1] != "Second Line" {
		t.Errorf("explicit newline wrap = %q, want [First Line Second Line]", explicit)
	}

	if got := truncateCanvasText("Short", maxW, 12, bold); got != "Short" {
		t.Errorf("truncateCanvasText short = %q, want Short", got)
	}
	trunc := truncateCanvasText("Extremely Long Subtitle Value", maxW, 12, bold)
	if !strings.HasSuffix(trunc, "…") {
		t.Errorf("expected truncated text to end with ellipsis, got %q", trunc)
	}
	if w := measureCanvasTextWidth(trunc, 12, bold); w > maxW {
		t.Errorf("truncated text %q width %.1f exceeds maxWidth %.1f", trunc, w, maxW)
	}
}

func TestColorGlowAndShimmerMath(t *testing.T) {
	chromatic := []string{"#38BDF8", "#22C55E", "#EF4444", "#FBBF24", "#9146FF"}
	for _, hex := range chromatic {
		c := parseHexColor(hex, color.RGBA{})
		if !isChromaticColor(c) {
			t.Errorf("expected %s (%+v) to be chromatic", hex, c)
		}
	}

	neutral := []string{"#64748B", "#475569", "#27272A", "#94A3B8", "#000000", "#FFFFFF"}
	for _, hex := range neutral {
		c := parseHexColor(hex, color.RGBA{})
		if isChromaticColor(c) {
			t.Errorf("expected %s (%+v) not to be chromatic", hex, c)
		}
	}

	green := parseHexColor("#22C55E", color.RGBA{})
	alphaCol := withAlpha(green, 140)
	if alphaCol != (color.NRGBA{R: green.R, G: green.G, B: green.B, A: 140}) {
		t.Errorf("withAlpha = %+v, want non-premultiplied NRGBA", alphaCol)
	}

	if got := shimmerIntensity(0.0); math.Abs(got-0.0) > 1e-6 {
		t.Errorf("shimmerIntensity(0.0) = %v, want 0", got)
	}
	if got := shimmerIntensity(0.25); math.Abs(got-0.5) > 1e-6 {
		t.Errorf("shimmerIntensity(0.25) = %v, want 0.5", got)
	}
	if got := shimmerIntensity(0.5); math.Abs(got-1.0) > 1e-6 {
		t.Errorf("shimmerIntensity(0.5) = %v, want 1", got)
	}
	for i := range 20 {
		s := shimmerIntensity(float64(i) / 20.0)
		if s < 0 || s > 1 {
			t.Fatalf("shimmerIntensity out of [0,1]: %v", s)
		}
	}
}

func TestDeckTileWidgetWrapGlowAndShimmer(t *testing.T) {
	testApp := test.NewApp()
	t.Cleanup(testApp.Quit)

	tile := newDeckTileWidget(deck.Coordinates{Row: 0, Col: 5}, nil)
	r := test.TempWidgetRenderer(t, tile).(*deckTileRenderer)
	tile.Resize(fyne.NewSize(100, 96))

	// Active tile with long title wraps across lines and renders GPU outer + inner + LED bar glow.
	tile.ApplySlot(&app.RenderedSlot{
		Config: &deck.SlotConfig{ActionUUID: "obs.scene"},
		Visual: app.SlotVisual{
			Title:    "Desktop and Printers",
			Subtitle: "ACTIVE",
			Color:    "#22C55E",
			State:    1,
		},
	}, false)

	if !tile.isActive() {
		t.Fatal("expected tile to report isActive() == true when State == 1")
	}
	if !r.titleLines[0].Visible() || !r.titleLines[1].Visible() {
		t.Fatalf("expected long title wrapped across >=2 visible lines; line0=%v (%q) line1=%v (%q)",
			r.titleLines[0].Visible(), r.titleLines[0].Text,
			r.titleLines[1].Visible(), r.titleLines[1].Text)
	}
	if got := strings.TrimSpace(r.titleLines[0].Text + " " + r.titleLines[1].Text); got != "Desktop and Printers" {
		t.Errorf("wrapped tile lines = %q, want %q", got, "Desktop and Printers")
	}
	if r.bg.Shadow.BlurRadius <= 0 || r.bg.Shadow.Variant != canvas.DropShadow {
		t.Fatalf("expected active outer DropShadow glow, got %+v", r.bg.Shadow)
	}
	if r.accentBar.Shadow.BlurRadius <= 0 || r.accentBar.Shadow.Variant != canvas.BoxShadow {
		t.Fatalf("expected active LED bar BoxShadow glow, got %+v", r.accentBar.Shadow)
	}
	if !r.innerRim.Visible() {
		t.Fatal("expected innerRim visible on active tile")
	}

	tile.setShimmerPhase(0.0)
	lowBlur := r.bg.Shadow.BlurRadius
	lowSpread := r.bg.Shadow.Spread
	lowBarBlur := r.accentBar.Shadow.BlurRadius
	tile.setShimmerPhase(0.5)
	highBlur := r.bg.Shadow.BlurRadius
	highSpread := r.bg.Shadow.Spread
	highBarBlur := r.accentBar.Shadow.BlurRadius
	if highBlur <= lowBlur || highSpread <= lowSpread || highBarBlur <= lowBarBlur {
		t.Errorf("expected shimmer phase 0.5 > 0.0: blur(%v vs %v) spread(%v vs %v) barBlur(%v vs %v)",
			lowBlur, highBlur, lowSpread, highSpread, lowBarBlur, highBarBlur)
	}

	// Inactive chromatic tile keeps subtle ambient glow (smaller than active peak).
	tile.ApplySlot(&app.RenderedSlot{
		Config: &deck.SlotConfig{ActionUUID: "obs.scene"},
		Visual: app.SlotVisual{
			Title:    "BRB",
			Subtitle: "Ready",
			Color:    "#38BDF8",
			State:    0,
		},
	}, false)
	if tile.isActive() {
		t.Fatal("expected inactive tile when State == 0")
	}
	if r.bg.Shadow.BlurRadius <= 0 || r.bg.Shadow.BlurRadius >= highBlur {
		t.Fatalf("expected ambient outer shadow blur in (0, %v), got %v", highBlur, r.bg.Shadow.BlurRadius)
	}
	if r.accentBar.Shadow.BlurRadius <= 0 || !r.innerRim.Visible() {
		t.Fatalf("expected ambient bar shadow and innerRim on chromatic idle tile, got barBlur=%v innerRim=%v",
			r.accentBar.Shadow.BlurRadius, r.innerRim.Visible())
	}

	// Muted neutral inactive tile and unassigned tile clear all glow layers.
	tile.ApplySlot(&app.RenderedSlot{
		Config: &deck.SlotConfig{ActionUUID: "obs.scene"},
		Visual: app.SlotVisual{
			Title:    "Stream",
			Subtitle: "OFFLINE",
			Color:    "#64748B",
			State:    0,
		},
	}, false)
	if r.bg.Shadow.BlurRadius != 0 || r.accentBar.Shadow.BlurRadius != 0 || r.innerRim.Visible() {
		t.Fatalf("expected no glow on muted slate inactive tile: bgBlur=%v barBlur=%v innerRim=%v",
			r.bg.Shadow.BlurRadius, r.accentBar.Shadow.BlurRadius, r.innerRim.Visible())
	}

	tile.ApplySlot(nil, false)
	if r.bg.Shadow.BlurRadius != 0 || r.accentBar.Shadow.BlurRadius != 0 || r.innerRim.Visible() {
		t.Fatalf("expected no glow on unassigned tile: bgBlur=%v barBlur=%v innerRim=%v",
			r.bg.Shadow.BlurRadius, r.accentBar.Shadow.BlurRadius, r.innerRim.Visible())
	}
}
