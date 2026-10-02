package sessionpg

import "testing"

func TestReplaceNUL(t *testing.T) {
	if got, ok := replaceNUL("a\x00b\x00"); !ok || got != "a\uFFFDb\uFFFD" {
		t.Fatalf("got %q %v", got, ok)
	}
	if got, ok := replaceNUL("plain"); ok || got != "plain" {
		t.Fatalf("got %q %v", got, ok)
	}
	// the JSON escape form is plain text, not a NUL byte
	if got, ok := replaceNUL(`{"a":"\u0000"}`); ok || got != `{"a":"\u0000"}` {
		t.Fatalf("got %q %v", got, ok)
	}
}

func TestSanitizeText(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		changed bool
	}{
		{"plain", "plain", false},
		{"ok é\U0001F600", "ok é\U0001F600", false},
		{"a\xffb", "a�b", true},
		{"a\x00b", "a�b", true},
		{"\xff\x00\xfe", "���", true},
		{`{"a":"\ud800"}`, `{"a":"\ud800"}`, false}, // escape text is valid UTF-8
	}
	for _, c := range cases {
		got, ch := sanitizeText(c.in)
		if got != c.want || ch != c.changed {
			t.Errorf("sanitizeText(%q) = %q,%v want %q,%v", c.in, got, ch, c.want, c.changed)
		}
	}
}
