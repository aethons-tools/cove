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
