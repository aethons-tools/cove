package jam

import (
	"strings"
	"unicode"
)

// MissionMaxRunes caps a session's mission (one line shown beside its name).
const MissionMaxRunes = 120

// NormalizeMission trims s and collapses every whitespace run (newlines
// included) to one space. Text over MissionMaxRunes is cut to
// MissionMaxRunes-1 runes plus "…". Control characters (NUL breaks Postgres
// jsonb) and format characters (bidi overrides can spoof the rail) are
// dropped; whitespace controls count as whitespace. An empty result means "no mission".
func NormalizeMission(s string) string {
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
	if len(r) <= MissionMaxRunes {
		return s
	}
	return string(r[:MissionMaxRunes-1]) + "…"
}
