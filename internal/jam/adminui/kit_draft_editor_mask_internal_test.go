package adminui

import "testing"

// A kit.yml the kit page hides whole only because it mentions "context" but
// doesn't parse (a half-edited draft) holds no packed context: the editor
// still opens it. A real packed context never reaches the textarea.
func TestMaskedKitYMLOnlyForPackedContexts(t *testing.T) {
	packed := "H4sIAAAAAAAA/+3OMQ6CQBCF4alHw0FwGRoKsV/4cg+X0qwZgrVhSl/HBUL2pmL+78kD5Rcs="
	for _, c := range []struct {
		text string
		want bool
	}{
		{"kind: studio\nprompt: keep the session context short\n", false},
		{"kind: studio\nprompt: the session context\nbase: [oops\n", false},
		{"kind: studio\n---\nprompt: context\n", false},
		{"kind: studio\nbase:\n  context: " + packed + "\n", true},
		{"kind: studio\nbase: [oops\n  context: " + packed + "\n", true},
		{"a: &x " + packed + "\nbase:\n  context: *x\n", true},
	} {
		if got := maskedKitYML([]byte(c.text)); got != c.want {
			t.Errorf("maskedKitYML(%q) = %v, want %v", c.text, got, c.want)
		}
	}
}
