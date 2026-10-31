package jam

import (
	"strings"
	"unicode"
)

// StatusLineMaxRunes caps a session's status line — its mission or its focus
// (one line each, shown on its status card).
const StatusLineMaxRunes = 120

// NormalizeStatusLine trims s and collapses every whitespace run (newlines
// included) to one space. Text over StatusLineMaxRunes is cut to
// StatusLineMaxRunes-1 runes plus "…". Control characters (NUL breaks Postgres
// jsonb) and format characters (bidi overrides can spoof the rail) are
// dropped; whitespace controls count as whitespace. An empty result means "none".
func NormalizeStatusLine(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case unicode.IsSpace(r):
			return r
		case unicode.IsControl(r), unicode.Is(unicode.Cf, r):
			return -1
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= StatusLineMaxRunes {
		return s
	}
	return string(r[:StatusLineMaxRunes-1]) + "…"
}
