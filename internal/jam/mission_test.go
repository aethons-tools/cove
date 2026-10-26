package jam

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestNormalizeMission(t *testing.T) {
	exact := strings.Repeat("é", MissionMaxRunes)
	tests := []struct {
		name, in, want string
	}{
		{"collapses whitespace", "  fix  the\n\ttests ", "fix the tests"},
		{"empty", "", ""},
		{"only whitespace", " \n ", ""},
		{"drops NUL", "a\x00b", "ab"},
		{"drops bidi override", "a\u202eb", "ab"},
		{"newline still a space", "a\nb", "a b"},
		{"control-only", "\x00\x07\x1b", ""},
		{"truncates with ellipsis", strings.Repeat("é", 130), strings.Repeat("é", 119) + "…"},
		{"exactly the cap is unchanged", exact, exact},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NormalizeMission(tt.in)
			if got != tt.want {
				t.Fatalf("NormalizeMission(%q) = %q, want %q", tt.in, got, tt.want)
			}
			if n := utf8.RuneCountInString(got); n > MissionMaxRunes {
				t.Fatalf("rune count %d exceeds cap", n)
			}
		})
	}
}
