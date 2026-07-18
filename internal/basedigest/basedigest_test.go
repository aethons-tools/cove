package basedigest

import (
	"regexp"
	"testing"
)

func TestParseIgnoresCommentsAndBlanks(t *testing.T) {
	in := "# a comment\n\nsha256:aaa\n  sha256:bbb  \n# trailing\nsha256:ccc\n"
	got := parse(in)
	want := []string{"sha256:aaa", "sha256:bbb", "sha256:ccc"}
	if len(got) != len(want) {
		t.Fatalf("parse returned %d entries, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("entry %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestParseEmpty(t *testing.T) {
	if got := parse("# only comments\n\n"); len(got) != 0 {
		t.Fatalf("expected no entries, got %v", got)
	}
}

// The committed file must yield at least one blessed digest, each a well-formed
// sha256 — a malformed entry would silently break COV-34's provenance gate.
func TestBlessedFileIsWellFormed(t *testing.T) {
	got := Blessed()
	if len(got) == 0 {
		t.Fatal("Blessed() returned no digests; blessed/watermark.txt must list at least one")
	}
	re := regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	for _, d := range got {
		if !re.MatchString(d) {
			t.Fatalf("blessed digest %q is not a well-formed sha256:<64hex>", d)
		}
	}
}

func TestDefaultRefAndBlessedRefs(t *testing.T) {
	blessed := Blessed()
	want := Image + "@" + blessed[0]
	if got := DefaultRef(); got != want {
		t.Fatalf("DefaultRef() = %q, want %q", got, want)
	}
	refs := BlessedRefs()
	if len(refs) != len(blessed) {
		t.Fatalf("BlessedRefs() len = %d, want %d", len(refs), len(blessed))
	}
	for i, d := range blessed {
		if refs[i] != Image+"@"+d {
			t.Fatalf("BlessedRefs()[%d] = %q, want %q", i, refs[i], Image+"@"+d)
		}
	}
}
