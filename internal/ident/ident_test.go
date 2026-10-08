package ident

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestNewHasKindPrefixAndParses(t *testing.T) {
	for _, k := range []Kind{Project, User, Connection, Account, Session, Channel} {
		id := New(k)
		if !strings.HasPrefix(string(id), string(k)+"_") {
			t.Fatalf("New(%s) = %q, want prefix %s_", k, id, k)
		}
		if len(id) != len(k)+1+26 {
			t.Fatalf("New(%s) = %q, want length %d", k, id, len(k)+27)
		}
		got, err := Parse(string(id))
		if err != nil || got != id {
			t.Fatalf("Parse(%q) = %q, %v", id, got, err)
		}
		if id.Kind() != k {
			t.Fatalf("%q.Kind() = %q, want %q", id, id.Kind(), k)
		}
	}
}

func TestNewIsUnique(t *testing.T) {
	seen := map[ID]bool{}
	for i := 0; i < 10000; i++ {
		id := New(User)
		if seen[id] {
			t.Fatalf("duplicate id %q after %d", id, i)
		}
		seen[id] = true
	}
}

func TestTextOrderFollowsTime(t *testing.T) {
	t0 := time.UnixMilli(1_700_000_000_000)
	// Maximal entropy for the earlier id, minimal for the later one: time must dominate.
	a := newAt(User, t0, bytes.NewReader(bytes.Repeat([]byte{0xff}, 10)))
	b := newAt(User, t0.Add(time.Millisecond), bytes.NewReader(make([]byte, 10)))
	if !(a < b) {
		t.Fatalf("want %q < %q", a, b)
	}
}

func TestParseRejects(t *testing.T) {
	valid := string(New(User))
	body := valid[len("usr_"):]
	for _, s := range []string{
		"",
		"usr",
		"usr_",
		"xyz_" + body,                  // unknown kind
		"USR_" + body,                  // kind is lowercase
		"usr_" + body[:25],             // short
		"usr_" + body + "0",            // long
		"usr_" + strings.ToUpper(body), // body is lowercase
		"usr_8" + body[1:],             // first char > 7 overflows 128 bits
		"usr_" + body[:25] + "u",       // 'u' is not in the alphabet
		"usr_" + body[:25] + "i",
	} {
		if _, err := Parse(s); err == nil {
			t.Errorf("Parse(%q) = nil error, want rejection", s)
		}
	}
}
