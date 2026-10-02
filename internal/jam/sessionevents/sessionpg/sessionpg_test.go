package sessionpg

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/aethons-tools/cove/internal/jam/sessionevents"
)

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

func TestSanitizeEventText(t *testing.T) {
	ev := sessionevents.Event{ActorID: "a\x00", StreamID: "s\xff"}
	ev.Stamp = sessionevents.Stamp{Project: "p\x00", Role: "r\xff", Unit: "u\x00", Owner: "o\xff", SessionKind: "k\x00"}
	ev.Index = sessionevents.Index{Type: "t\x00", Subtype: "s\xff", ToolName: "B\x00\xffash", ClaudeSessionID: "c\x00"}
	got, changed := sanitizeEventText(ev)
	if !changed {
		t.Fatal("changed = false")
	}
	for _, s := range []string{got.ActorID, got.StreamID, got.Stamp.Project, got.Stamp.Role, got.Stamp.Unit,
		got.Stamp.Owner, got.Stamp.SessionKind, got.Index.Type, got.Index.Subtype, got.Index.ToolName, got.Index.ClaudeSessionID} {
		if strings.ContainsRune(s, 0) || !utf8.ValidString(s) {
			t.Fatalf("unsanitized %q", s)
		}
	}
	if got.Index.ToolName != "B��ash" {
		t.Fatalf("ToolName = %q", got.Index.ToolName)
	}
	clean := sessionevents.Event{ActorID: "w1", Index: sessionevents.Index{Type: "assistant", ToolName: "Bash é"}}
	if out, ch := sanitizeEventText(clean); ch || out.Index != clean.Index || out.ActorID != clean.ActorID {
		t.Fatalf("clean event changed: %v %+v", ch, out)
	}
}
