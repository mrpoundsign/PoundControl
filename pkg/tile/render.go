// Package tile decodes and rasterizes deck button artwork, including SVG icons with <text> labels.
package tile

import (
	"bytes"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"image"
	"image/color"
	_ "image/jpeg" // register JPEG decoder for data-URI and file-based tile images
	_ "image/png"  // register PNG decoder for data-URI and file-based tile images
	"io"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/srwiley/oksvg"
	"github.com/srwiley/rasterx"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

const defaultTileSize = 144

var (
	fontsOnce   sync.Once
	regularFont *opentype.Font
	boldFont    *opentype.Font
)

func initFonts() {
	fontsOnce.Do(func() {
		regularFont, _ = opentype.Parse(goregular.TTF)
		boldFont, _ = opentype.Parse(gobold.TTF)
	})
}

func fontForWeight(bold bool) *opentype.Font {
	initFonts()
	if bold && boldFont != nil {
		return boldFont
	}
	return regularFont
}

type cacheKey struct {
	uri  string
	size int
}

// Cache caches decoded tile images by URI and target square pixel size.
type Cache struct {
	mu         sync.Mutex
	maxEntries int
	items      map[cacheKey]image.Image
	order      []cacheKey
}

// NewCache creates a bounded thread-safe tile image cache.
func NewCache(maxEntries int) *Cache {
	if maxEntries <= 0 {
		maxEntries = 128
	}
	return &Cache{
		maxEntries: maxEntries,
		items:      make(map[cacheKey]image.Image, maxEntries),
	}
}

// GetOrDecode returns a cached image for (uri, size) or decodes and caches it.
func (c *Cache) GetOrDecode(uri string, size int) (image.Image, error) {
	if size <= 0 {
		size = defaultTileSize
	}
	key := cacheKey{uri: uri, size: size}
	c.mu.Lock()
	if img, ok := c.items[key]; ok {
		c.mu.Unlock()
		return img, nil
	}
	c.mu.Unlock()

	img, err := DecodeImage(uri, size)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	if existing, ok := c.items[key]; ok {
		c.mu.Unlock()
		return existing, nil
	}
	if len(c.order) >= c.maxEntries {
		oldest := c.order[0]
		copy(c.order, c.order[1:])
		c.order[len(c.order)-1] = cacheKey{}
		c.order = c.order[:len(c.order)-1]
		delete(c.items, oldest)
	}
	c.items[key] = img
	c.order = append(c.order, key)
	c.mu.Unlock()
	return img, nil
}

// DecodeImage decodes a data URI or local image path (PNG, JPEG, or SVG with <text> labels)
// into an image suitable for rendering on a virtual deck tile of side length size.
func DecodeImage(uri string, size int) (image.Image, error) {
	trimmed := strings.TrimSpace(uri)
	if trimmed == "" {
		return nil, errors.New("empty image URI")
	}
	if size <= 0 {
		size = defaultTileSize
	}

	if strings.HasPrefix(strings.ToLower(trimmed), "data:") {
		return decodeDataURI(trimmed, size)
	}

	filePath := trimmed
	if strings.HasPrefix(strings.ToLower(trimmed), "file://") {
		if parsed, err := url.Parse(trimmed); err == nil {
			filePath = parsed.Path
			if len(filePath) >= 3 && filePath[0] == '/' && filePath[2] == ':' {
				filePath = filePath[1:]
			}
			filePath = filepath.FromSlash(filePath)
		}
	}

	resolved, ok := resolveImageFile(filePath)
	if !ok {
		return nil, fmt.Errorf("image file not found: %s", filePath)
	}
	raw, err := os.ReadFile(resolved)
	if err != nil {
		return nil, fmt.Errorf("read image file %s: %w", resolved, err)
	}
	if strings.EqualFold(filepath.Ext(resolved), ".svg") || looksLikeSVG(raw) {
		return renderSVG(raw, size)
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("decode raster image %s: %w", resolved, err)
	}
	return img, nil
}

func resolveImageFile(path string) (string, bool) {
	clean := filepath.Clean(path)
	if info, err := os.Stat(clean); err == nil && !info.IsDir() {
		return clean, true
	}

	ext := filepath.Ext(clean)
	stem := strings.TrimSuffix(clean, ext)
	candidates := []string{
		stem + ".svg",
		stem + "@2x.png",
		stem + ".png",
		stem + "@2x.jpg",
		stem + ".jpg",
		stem + ".jpeg",
	}
	for _, cand := range candidates {
		if info, err := os.Stat(cand); err == nil && !info.IsDir() {
			return cand, true
		}
	}
	return "", false
}

func decodeDataURI(raw string, size int) (image.Image, error) {
	comma := strings.IndexByte(raw, ',')
	if comma < 0 {
		return nil, errors.New("invalid data URI: missing comma separator")
	}
	meta := strings.ToLower(raw[5:comma])
	payload := raw[comma+1:]

	isBase64 := strings.Contains(meta, ";base64")
	var data []byte
	if isBase64 {
		clean := strings.Map(func(r rune) rune {
			if r == ' ' || r == '\n' || r == '\r' || r == '\t' {
				return -1
			}
			return r
		}, payload)
		decoded, err := base64.StdEncoding.DecodeString(clean)
		if err != nil {
			decoded, err = base64.RawStdEncoding.DecodeString(clean)
			if err != nil {
				return nil, fmt.Errorf("decode base64 data URI: %w", err)
			}
		}
		data = decoded
	} else {
		if unescaped, err := url.PathUnescape(payload); err == nil {
			data = []byte(unescaped)
		} else {
			data = []byte(payload)
		}
	}

	if strings.Contains(meta, "image/svg+xml") || looksLikeSVG(data) {
		return renderSVG(data, size)
	}

	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode data URI image: %w", err)
	}
	return img, nil
}

func looksLikeSVG(data []byte) bool {
	trimmed := strings.TrimSpace(string(data))
	if strings.HasPrefix(trimmed, "<svg") || strings.Contains(trimmed[:min(len(trimmed), 256)], "<svg") {
		return true
	}
	return false
}

func renderSVG(svgData []byte, size int) (image.Image, error) {
	stripped := stripTextElements(svgData)
	icon, err := oksvg.ReadIconStream(bytes.NewReader(stripped), oksvg.WarnErrorMode)
	if err != nil {
		return nil, fmt.Errorf("parse SVG shapes: %w", err)
	}

	vbX, vbY, vbW, vbH := parseSVGViewport(svgData, icon)
	if vbW <= 0 {
		vbW = float64(size)
	}
	if vbH <= 0 {
		vbH = float64(size)
	}

	scale := min(float64(size)/vbW, float64(size)/vbH)
	targetW := vbW * scale
	targetH := vbH * scale
	offX := (float64(size) - targetW) / 2
	offY := (float64(size) - targetH) / 2

	dst := image.NewRGBA(image.Rect(0, 0, size, size))
	icon.SetTarget(offX, offY, targetW, targetH)
	scanner := rasterx.NewScannerGV(size, size, dst, dst.Bounds())
	dasher := rasterx.NewDasher(size, size, scanner)
	icon.Draw(dasher, 1.0)

	drawSVGTextOverlays(dst, svgData, vbX, vbY, scale, offX, offY)
	return dst, nil
}

// stripTextElements removes <text>...</text> (and self-closing <text .../>) elements
// before oksvg parsing so vector primitives still render without oksvg rejecting <text>.
func stripTextElements(src []byte) []byte {
	lower := strings.ToLower(string(src))
	var out bytes.Buffer
	out.Grow(len(src))

	pos := 0
	for pos < len(src) {
		rel := strings.Index(lower[pos:], "<text")
		if rel < 0 {
			out.Write(src[pos:])
			break
		}
		start := pos + rel
		nextIdx := start + 5
		if nextIdx < len(src) {
			ch := src[nextIdx]
			if ch != ' ' && ch != '\t' && ch != '\n' && ch != '\r' && ch != '>' && ch != '/' {
				out.Write(src[pos:nextIdx])
				pos = nextIdx
				continue
			}
		}

		out.Write(src[pos:start])

		gtRel := strings.IndexByte(lower[start:], '>')
		if gtRel < 0 {
			break
		}
		openEnd := start + gtRel
		if openEnd > start && src[openEnd-1] == '/' {
			pos = openEnd + 1
			continue
		}

		closeRel := strings.Index(lower[openEnd+1:], "</text>")
		if closeRel < 0 {
			break
		}
		pos = openEnd + 1 + closeRel + len("</text>")
	}
	return out.Bytes()
}

type svgStyle struct {
	fill       string
	fontSize   float64
	fontWeight string
	textAnchor string
	x          float64
	y          float64
}

func (s svgStyle) clone() svgStyle {
	return s
}

func (s *svgStyle) applyStyleKV(k, v string) {
	switch k {
	case "fill":
		if v != "" {
			s.fill = v
		}
	case "font-size":
		if fs, ok := parseSVGLength(v); ok && fs > 0 {
			s.fontSize = fs
		}
	case "font-weight":
		if v != "" {
			s.fontWeight = v
		}
	case "text-anchor":
		if v != "" {
			s.textAnchor = strings.ToLower(v)
		}
	}
}

func (s *svgStyle) applyAttrs(attrs []xml.Attr, isText bool) {
	for _, a := range attrs {
		name := strings.ToLower(a.Name.Local)
		val := strings.TrimSpace(a.Value)
		switch name {
		case "style":
			s.applyInlineStyle(val)
		case "x":
			if isText {
				if x, ok := parseSVGLength(val); ok {
					s.x = x
				}
			}
		case "y":
			if isText {
				if y, ok := parseSVGLength(val); ok {
					s.y = y
				}
			}
		default:
			s.applyStyleKV(name, val)
		}
	}
}

func (s *svgStyle) applyInlineStyle(raw string) {
	for decl := range strings.SplitSeq(raw, ";") {
		parts := strings.SplitN(decl, ":", 2)
		if len(parts) != 2 {
			continue
		}
		s.applyStyleKV(strings.ToLower(strings.TrimSpace(parts[0])), strings.TrimSpace(parts[1]))
	}
}

type textRun struct {
	text       string
	x          float64
	y          float64
	fontSize   float64
	bold       bool
	fill       color.NRGBA
	textAnchor string
}

func parseSVGViewport(svgData []byte, icon *oksvg.SvgIcon) (vbX, vbY, vbW, vbH float64) {
	dec := xml.NewDecoder(bytes.NewReader(svgData))
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		se, ok := tok.(xml.StartElement)
		if !ok || strings.ToLower(se.Name.Local) != "svg" {
			continue
		}
		var w, h float64
		for _, a := range se.Attr {
			switch strings.ToLower(a.Name.Local) {
			case "viewbox":
				fields := strings.Fields(strings.ReplaceAll(a.Value, ",", " "))
				if len(fields) == 4 {
					if x, err := strconv.ParseFloat(fields[0], 64); err == nil {
						vbX = x
					}
					if y, err := strconv.ParseFloat(fields[1], 64); err == nil {
						vbY = y
					}
					if vw, err := strconv.ParseFloat(fields[2], 64); err == nil {
						vbW = vw
					}
					if vh, err := strconv.ParseFloat(fields[3], 64); err == nil {
						vbH = vh
					}
				}
			case "width":
				if pw, ok := parseSVGLength(a.Value); ok {
					w = pw
				}
			case "height":
				if ph, ok := parseSVGLength(a.Value); ok {
					h = ph
				}
			}
		}
		if vbW <= 0 && w > 0 {
			vbW = w
		}
		if vbH <= 0 && h > 0 {
			vbH = h
		}
		break
	}
	if icon != nil {
		if vbW <= 0 && icon.ViewBox.W > 0 {
			vbX = icon.ViewBox.X
			vbY = icon.ViewBox.Y
			vbW = icon.ViewBox.W
			vbH = icon.ViewBox.H
		}
	}
	return vbX, vbY, vbW, vbH
}

func drawSVGTextOverlays(dst *image.RGBA, svgData []byte, vbX, vbY, scale, offX, offY float64) {
	runs := extractTextRuns(svgData)
	if len(runs) == 0 {
		return
	}

	for _, run := range runs {
		if strings.TrimSpace(run.text) == "" || run.fill.A == 0 {
			continue
		}
		pxSize := max(6.0, run.fontSize*scale)
		fnt := fontForWeight(run.bold)
		if fnt == nil {
			continue
		}
		face, err := opentype.NewFace(fnt, &opentype.FaceOptions{
			Size:    pxSize,
			DPI:     72,
			Hinting: font.HintingFull,
		})
		if err != nil {
			continue
		}

		px := offX + (run.x-vbX)*scale
		py := offY + (run.y-vbY)*scale
		adv := font.MeasureString(face, run.text)
		advPx := float64(adv) / 64.0

		drawX := px
		switch run.textAnchor {
		case "middle":
			drawX = px - advPx/2
		case "end":
			drawX = px - advPx
		}

		drawer := font.Drawer{
			Dst:  dst,
			Src:  image.NewUniform(run.fill),
			Face: face,
			Dot: fixed.Point26_6{
				X: fixed.Int26_6(math.Round(drawX * 64)),
				Y: fixed.Int26_6(math.Round(py * 64)),
			},
		}
		drawer.DrawString(run.text)
		_ = face.Close()
	}
}

func extractTextRuns(svgData []byte) []textRun {
	dec := xml.NewDecoder(bytes.NewReader(svgData))
	stack := []svgStyle{{
		fill:       "#FFFFFF",
		fontSize:   14,
		fontWeight: "normal",
		textAnchor: "start",
	}}

	var runs []textRun
	var textBuf strings.Builder
	inText := false

	for {
		tok, err := dec.Token()
		if err == io.EOF || err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			name := strings.ToLower(t.Name.Local)
			st := stack[len(stack)-1].clone()
			st.applyAttrs(t.Attr, name == "text" || name == "tspan")
			stack = append(stack, st)
			if name == "text" {
				inText = true
				textBuf.Reset()
			} else if name == "tspan" && inText && textBuf.Len() > 0 {
				// Emit preceding chunk inside <text> before tspan style changes take effect.
				parentSt := stack[len(stack)-2]
				appendTextRun(&runs, textBuf.String(), parentSt)
				textBuf.Reset()
			}

		case xml.CharData:
			if inText {
				textBuf.Write(t)
			}

		case xml.EndElement:
			name := strings.ToLower(t.Name.Local)
			if len(stack) > 1 {
				topSt := stack[len(stack)-1]
				stack = stack[:len(stack)-1]

				if name == "text" {
					appendTextRun(&runs, textBuf.String(), topSt)
					textBuf.Reset()
					inText = false
				} else if name == "tspan" && inText {
					appendTextRun(&runs, textBuf.String(), topSt)
					textBuf.Reset()
				}
			}
		}
	}
	return runs
}

func appendTextRun(runs *[]textRun, text string, st svgStyle) {
	cleaned := strings.Join(strings.Fields(text), " ")
	if cleaned == "" {
		return
	}
	col, ok := parseSVGColor(st.fill)
	if !ok {
		return
	}
	bold := strings.EqualFold(st.fontWeight, "bold")
	if !bold {
		if w, err := strconv.Atoi(strings.TrimSpace(st.fontWeight)); err == nil && w >= 600 {
			bold = true
		}
	}
	*runs = append(*runs, textRun{
		text:       cleaned,
		x:          st.x,
		y:          st.y,
		fontSize:   st.fontSize,
		bold:       bold,
		fill:       col,
		textAnchor: st.textAnchor,
	})
}

func parseSVGLength(raw string) (float64, bool) {
	s := strings.TrimSpace(strings.ToLower(raw))
	s = strings.TrimSuffix(s, "px")
	s = strings.TrimSpace(s)
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

func parseSVGColor(raw string) (color.NRGBA, bool) {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" || s == "none" || s == "transparent" {
		return color.NRGBA{}, false
	}
	if strings.HasPrefix(s, "#") {
		hex := s[1:]
		switch len(hex) {
		case 3:
			r, err1 := strconv.ParseUint(strings.Repeat(hex[0:1], 2), 16, 8)
			g, err2 := strconv.ParseUint(strings.Repeat(hex[1:2], 2), 16, 8)
			b, err3 := strconv.ParseUint(strings.Repeat(hex[2:3], 2), 16, 8)
			if err1 == nil && err2 == nil && err3 == nil {
				return color.NRGBA{R: uint8(r), G: uint8(g), B: uint8(b), A: 255}, true
			}
		case 6:
			v, err := strconv.ParseUint(hex, 16, 32)
			if err == nil {
				return color.NRGBA{R: uint8(v >> 16), G: uint8(v >> 8), B: uint8(v), A: 255}, true
			}
		case 8:
			v, err := strconv.ParseUint(hex, 16, 32)
			if err == nil {
				return color.NRGBA{R: uint8(v >> 24), G: uint8(v >> 16), B: uint8(v >> 8), A: uint8(v)}, true
			}
		}
		return color.NRGBA{}, false
	}
	switch s {
	case "white":
		return color.NRGBA{R: 255, G: 255, B: 255, A: 255}, true
	case "black":
		return color.NRGBA{R: 0, G: 0, B: 0, A: 255}, true
	case "red":
		return color.NRGBA{R: 255, G: 0, B: 0, A: 255}, true
	case "green":
		return color.NRGBA{R: 0, G: 128, B: 0, A: 255}, true
	case "blue":
		return color.NRGBA{R: 0, G: 0, B: 255, A: 255}, true
	case "yellow":
		return color.NRGBA{R: 255, G: 255, B: 0, A: 255}, true
	}
	return color.NRGBA{}, false
}
