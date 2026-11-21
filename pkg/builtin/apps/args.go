package apps

import (
	"strings"
	"unicode"
)

// SplitArgs splits a command-line argument string into tokens while respecting single and double quotes.
// Backslashes are preserved literally for Windows file paths unless escaping a quote inside double quotes.
func SplitArgs(raw string) []string {
	var args []string
	var cur strings.Builder
	var quote rune
	inToken := false

	runes := []rune(raw)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch {
		case quote != 0:
			if r == '\\' && quote == '"' && i+1 < len(runes) && (runes[i+1] == '"' || runes[i+1] == '\\') {
				i++
				cur.WriteRune(runes[i])
				inToken = true
				continue
			}
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
			inToken = true

		case r == '"' || r == '\'':
			quote = r
			inToken = true

		case unicode.IsSpace(r):
			if inToken {
				args = append(args, cur.String())
				cur.Reset()
				inToken = false
			}

		default:
			cur.WriteRune(r)
			inToken = true
		}
	}

	if inToken {
		args = append(args, cur.String())
	}
	return args
}
