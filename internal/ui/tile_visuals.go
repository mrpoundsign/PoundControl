package ui

import (
	"image/color"
	"math"
	"strings"

	"fyne.io/fyne/v2"
)

func isChromaticColor(c color.RGBA) bool {
	hi := max(c.R, c.G, c.B)
	lo := min(c.R, c.G, c.B)
	return hi-lo >= 48
}

func withAlpha(c color.RGBA, a uint8) color.NRGBA {
	return color.NRGBA{R: c.R, G: c.G, B: c.B, A: a}
}

func blendRGBA(base, overlay color.RGBA, t float64) color.RGBA {
	t = min(1.0, max(0.0, t))
	inv := 1.0 - t
	return color.RGBA{
		R: uint8(math.Round(float64(base.R)*inv + float64(overlay.R)*t)),
		G: uint8(math.Round(float64(base.G)*inv + float64(overlay.G)*t)),
		B: uint8(math.Round(float64(base.B)*inv + float64(overlay.B)*t)),
		A: 255,
	}
}

func shimmerIntensity(phase float64) float64 {
	return min(1.0, max(0.0, 0.5-0.5*math.Cos(phase*2*math.Pi)))
}

func measureCanvasTextWidth(text string, textSize float32, style fyne.TextStyle) float32 {
	if text == "" {
		return 0
	}
	return fyne.MeasureText(text, textSize, style).Width
}

// truncateCanvasText trims text with a trailing ellipsis so its measured width does not exceed maxWidth.
func truncateCanvasText(text string, maxWidth, textSize float32, style fyne.TextStyle) string {
	if text == "" || maxWidth <= 0 {
		return text
	}
	if measureCanvasTextWidth(text, textSize, style) <= maxWidth {
		return text
	}
	const ellipsis = "…"
	runes := []rune(text)
	for len(runes) > 0 {
		runes = runes[:len(runes)-1]
		cand := strings.TrimRight(string(runes), " \t") + ellipsis
		if measureCanvasTextWidth(cand, textSize, style) <= maxWidth {
			return cand
		}
	}
	return ellipsis
}

func splitLongWordToLines(word string, maxWidth, textSize float32, style fyne.TextStyle, lines []string, maxLines int) ([]string, string) {
	runes := []rune(word)
	start := 0
	for start < len(runes) {
		if len(lines) == maxLines-1 {
			return lines, string(runes[start:])
		}
		end := start + 1
		for end <= len(runes) {
			cand := string(runes[start:end])
			if measureCanvasTextWidth(cand, textSize, style) > maxWidth {
				if end > start+1 {
					end--
				}
				break
			}
			end++
		}
		if end > len(runes) {
			end = len(runes)
		}
		chunk := string(runes[start:end])
		start = end
		if start < len(runes) {
			lines = append(lines, chunk)
		} else {
			return lines, chunk
		}
	}
	return lines, ""
}

// wrapCanvasText word-wraps text into at most maxLines centered lines bounded by maxWidth,
// truncating any final-line overflow with an ellipsis.
func wrapCanvasText(text string, maxWidth, textSize float32, style fyne.TextStyle, maxLines int) []string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" || maxLines <= 0 {
		return nil
	}
	if maxWidth <= 0 {
		return []string{trimmed}
	}

	rawParas := strings.Split(strings.ReplaceAll(trimmed, "\r\n", "\n"), "\n")
	paras := make([][]string, 0, len(rawParas))
	for _, p := range rawParas {
		words := strings.Fields(p)
		if len(words) > 0 {
			paras = append(paras, words)
		}
	}
	if len(paras) == 0 {
		return nil
	}

	lines := make([]string, 0, min(maxLines, len(paras)*2))
	for pIdx, words := range paras {
		cur := ""
		for wIdx, word := range words {
			if len(lines) == maxLines-1 {
				tailParts := make([]string, 0, len(words)-wIdx+len(paras)-pIdx)
				if cur != "" {
					tailParts = append(tailParts, cur)
				}
				tailParts = append(tailParts, words[wIdx:]...)
				for remIdx := pIdx + 1; remIdx < len(paras); remIdx++ {
					tailParts = append(tailParts, paras[remIdx]...)
				}
				lines = append(lines, truncateCanvasText(strings.Join(tailParts, " "), maxWidth, textSize, style))
				return lines
			}

			if cur == "" {
				if measureCanvasTextWidth(word, textSize, style) <= maxWidth {
					cur = word
					continue
				}
				lines, cur = splitLongWordToLines(word, maxWidth, textSize, style, lines, maxLines)
				continue
			}

			cand := cur + " " + word
			if measureCanvasTextWidth(cand, textSize, style) <= maxWidth {
				cur = cand
				continue
			}

			lines = append(lines, cur)

			if len(lines) == maxLines-1 {
				tailParts := make([]string, 0, len(words)-wIdx+len(paras)-pIdx)
				tailParts = append(tailParts, words[wIdx:]...)
				for remIdx := pIdx + 1; remIdx < len(paras); remIdx++ {
					tailParts = append(tailParts, paras[remIdx]...)
				}
				lines = append(lines, truncateCanvasText(strings.Join(tailParts, " "), maxWidth, textSize, style))
				return lines
			}

			if measureCanvasTextWidth(word, textSize, style) <= maxWidth {
				cur = word
			} else {
				lines, cur = splitLongWordToLines(word, maxWidth, textSize, style, lines, maxLines)
			}
		}
		if cur != "" {
			if len(lines) == maxLines-1 && pIdx < len(paras)-1 {
				tailParts := make([]string, 0, 1+len(paras)-pIdx)
				tailParts = append(tailParts, cur)
				for remIdx := pIdx + 1; remIdx < len(paras); remIdx++ {
					tailParts = append(tailParts, paras[remIdx]...)
				}
				lines = append(lines, truncateCanvasText(strings.Join(tailParts, " "), maxWidth, textSize, style))
				return lines
			}
			lines = append(lines, truncateCanvasText(cur, maxWidth, textSize, style))
		}
		if len(lines) >= maxLines {
			break
		}
	}

	return lines
}
