package tile

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

func TestDecodeTownCrierSVGWithText(t *testing.T) {
	svg := `<svg xmlns="http://www.w3.org/2000/svg" width="144" height="144" viewBox="0 0 144 144">
		<rect x="4" y="4" width="136" height="136" rx="22" fill="#0F172A" stroke="#38BDF8" stroke-width="4"/>
		<circle cx="72" cy="46" r="17" stroke="#38BDF8" stroke-width="5" fill="none"/>
		<text x="72" y="96" font-family="Segoe UI, Arial, sans-serif" font-size="17" font-weight="700" fill="#F8FAFC" text-anchor="middle">TTS MODE</text>
		<text x="72" y="120" font-family="Segoe UI, Arial, sans-serif" font-size="15" font-weight="700" fill="#38BDF8" text-anchor="middle">ALWAYS ON</text>
	</svg>`

	uri := "data:image/svg+xml;utf8," + url.PathEscape(svg)
	img, err := DecodeImage(uri, 144)
	if err != nil {
		t.Fatalf("DecodeImage() error = %v", err)
	}
	if b := img.Bounds(); b.Dx() != 144 || b.Dy() != 144 {
		t.Fatalf("unexpected decoded bounds: %+v", b)
	}

	// Background shape interior (dark slate #0F172A).
	bgPx := color.RGBAModel.Convert(img.At(20, 20)).(color.RGBA)
	if bgPx.A == 0 || bgPx.B < bgPx.R {
		t.Fatalf("expected non-transparent slate background pixel at (20,20), got %+v", bgPx)
	}

	// Verify bright text pixels were rasterized inside the lower text band (y around 82..100, centered x around 40..104).
	brightTextPixels := 0
	for y := 80; y <= 102; y++ {
		for x := 35; x <= 109; x++ {
			c := color.RGBAModel.Convert(img.At(x, y)).(color.RGBA)
			if c.R > 180 && c.G > 180 && c.B > 180 {
				brightTextPixels++
			}
		}
	}
	if brightTextPixels < 20 {
		t.Fatalf("expected rasterized white <text> pixels in label region, found only %d", brightTextPixels)
	}
}

func TestDecodeBase64AndBundleStemLookup(t *testing.T) {
	svgBytes := []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 72 72"><rect x="4" y="4" width="64" height="64" fill="#22C55E"/></svg>`)
	b64URI := "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString(svgBytes)
	img, err := DecodeImage(b64URI, 72)
	if err != nil {
		t.Fatalf("DecodeImage(base64 svg) error = %v", err)
	}
	c := color.RGBAModel.Convert(img.At(36, 36)).(color.RGBA)
	if c.G < 180 || c.A == 0 {
		t.Fatalf("expected emerald fill at center of base64 svg, got %+v", c)
	}

	// Base64 PNG decoding.
	srcImg := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for y := range 8 {
		for x := range 8 {
			srcImg.SetRGBA(x, y, color.RGBA{R: 255, G: 0, B: 0, A: 255})
		}
	}
	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, srcImg); err != nil {
		t.Fatalf("png.Encode() error = %v", err)
	}
	pngURI := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBuf.Bytes())
	decodedPNG, err := DecodeImage(pngURI, 72)
	if err != nil {
		t.Fatalf("DecodeImage(base64 png) error = %v", err)
	}
	if decodedPNG.Bounds().Dx() != 8 || decodedPNG.Bounds().Dy() != 8 {
		t.Fatalf("unexpected decoded PNG bounds: %+v", decodedPNG.Bounds())
	}

	// Stream Deck stem resolution preferring @2x.png over base extension.
	dir := t.TempDir()
	imgsDir := filepath.Join(dir, "imgs")
	if err := os.MkdirAll(imgsDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	hiImg := image.NewRGBA(image.Rect(0, 0, 16, 16))
	var hiBuf bytes.Buffer
	_ = png.Encode(&hiBuf, hiImg)
	if err := os.WriteFile(filepath.Join(imgsDir, "mode@2x.png"), hiBuf.Bytes(), 0o600); err != nil {
		t.Fatalf("WriteFile(mode@2x.png) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(imgsDir, "mode.png"), pngBuf.Bytes(), 0o600); err != nil {
		t.Fatalf("WriteFile(mode.png) error = %v", err)
	}

	stemImg, err := DecodeImage(filepath.Join(imgsDir, "mode"), 144)
	if err != nil {
		t.Fatalf("DecodeImage(stem) error = %v", err)
	}
	if stemImg.Bounds().Dx() != 16 {
		t.Fatalf("expected @2x.png (width 16) preferred for stem lookup, got width %d", stemImg.Bounds().Dx())
	}
}

func TestCacheHitAndBoundedEviction(t *testing.T) {
	cache := NewCache(2)
	uri1 := "data:image/svg+xml;utf8," + url.PathEscape(`<svg viewBox="0 0 24 24"><rect width="24" height="24" fill="#111111"/></svg>`)
	uri2 := "data:image/svg+xml;utf8," + url.PathEscape(`<svg viewBox="0 0 24 24"><rect width="24" height="24" fill="#222222"/></svg>`)
	uri3 := "data:image/svg+xml;utf8," + url.PathEscape(`<svg viewBox="0 0 24 24"><rect width="24" height="24" fill="#333333"/></svg>`)

	im1a, err := cache.GetOrDecode(uri1, 64)
	if err != nil {
		t.Fatalf("GetOrDecode(uri1) error = %v", err)
	}
	im1b, err := cache.GetOrDecode(uri1, 64)
	if err != nil {
		t.Fatalf("GetOrDecode(uri1 cached) error = %v", err)
	}
	if im1a != im1b {
		t.Fatal("expected exact cached image instance on second GetOrDecode call")
	}

	_, _ = cache.GetOrDecode(uri2, 64)
	_, _ = cache.GetOrDecode(uri3, 64)
	if len(cache.order) != 2 || len(cache.items) != 2 {
		t.Fatalf("expected cache bounded to 2 entries, got order=%d items=%d", len(cache.order), len(cache.items))
	}
}
