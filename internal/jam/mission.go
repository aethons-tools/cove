package jam

import "strings"

// MissionMaxRunes caps a session's mission (one line shown beside its name).
const MissionMaxRunes = 120

// NormalizeMission trims s and collapses every whitespace run (newlines
// included) to one space. Text over MissionMaxRunes is cut to
// MissionMaxRunes-1 runes plus "…". An empty result means "no mission".
func NormalizeMission(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= MissionMaxRunes {
		return s
	}
	return string(r[:MissionMaxRunes-1]) + "…"
}
