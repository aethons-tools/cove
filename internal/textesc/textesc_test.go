package textesc

import "testing"

func TestEscape(t *testing.T) {
	for _, c := range []struct {
		in         string
		keepLayout bool
		want       string
	}{
		{"plain", false, "plain"},
		{"a\nb\tc\rd", false, `a\nb\tc\rd`},
		{"a\nb\tc\rd", true, "a\nb\tc\\rd"},
		{"\x1b[31m\x7f\u009b", true, `\x1b[31m\x7f\x9b`},
		{"\u202e\u2028\u2029 \U000e0001", true, `\u202e\u2028\u2029 \U000e0001`},
		{"bad\xff", true, `bad\xff`},
		{"héllo ✓", true, "héllo ✓"},
	} {
		if got := Escape(c.in, c.keepLayout); got != c.want {
			t.Errorf("Escape(%q, %v) = %q, want %q", c.in, c.keepLayout, got, c.want)
		}
	}
}
