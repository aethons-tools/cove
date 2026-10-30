package basedigest

import (
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
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
	if l := LocalRef(); l != "" {
		t.Skipf("a local base is embedded (%s; scripts/local.sh): these check the published default", l)
	}
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

func TestDefaultRefIsTheRecutRepo(t *testing.T) {
	if l := LocalRef(); l != "" {
		t.Skipf("a local base is embedded (%s; scripts/local.sh): these check the published default", l)
	}
	if Image != "ghcr.io/aethons-tools/jam-studio-base" {
		t.Fatalf("Image = %q", Image)
	}
	if !strings.HasPrefix(DefaultRef(), "ghcr.io/aethons-tools/jam-studio-base@sha256:") {
		t.Fatalf("DefaultRef = %q", DefaultRef())
	}
	for _, r := range BlessedRefs() {
		if !strings.HasPrefix(r, Image+"@") {
			t.Fatalf("blessed ref %q is not under %s", r, Image)
		}
	}
}

// withFiles makes the package read fsys instead of the embedded blessed/.
func withFiles(t *testing.T, fsys fstest.MapFS) {
	t.Helper()
	old := files
	files = fsys
	t.Cleanup(func() { files = old })
}

const (
	testWatermark = "sha256:" + "a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1"
	testGenerated = "sha256:" + "b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2"
)

func TestLocalRefAbsent(t *testing.T) {
	withFiles(t, fstest.MapFS{"blessed/watermark.txt": {Data: []byte(testWatermark + "\n")}})
	if got := LocalRef(); got != "" {
		t.Fatalf("LocalRef() = %q, want none", got)
	}
	if got := DefaultRef(); got != Image+"@"+testWatermark {
		t.Fatalf("DefaultRef() = %q", got)
	}
	if got := BlessedRefs(); len(got) != 1 || got[0] != Image+"@"+testWatermark {
		t.Fatalf("BlessedRefs() = %q", got)
	}
}

// A `just local` build: the local base is the default and the first blessed
// ref, and the published bases stay trusted after it.
func TestLocalRefPresent(t *testing.T) {
	withFiles(t, fstest.MapFS{
		"blessed/watermark.txt": {Data: []byte(testWatermark + "\n")},
		"blessed/generated.txt": {Data: []byte(testGenerated + "\n")},
		"blessed/local.txt":     {Data: []byte("# written by scripts/local.sh\njam-studio-base:dev-0123abcd4567\n")},
	})
	const local = "jam-studio-base:dev-0123abcd4567"
	if got := LocalRef(); got != local {
		t.Fatalf("LocalRef() = %q, want %q", got, local)
	}
	if got := DefaultRef(); got != local {
		t.Fatalf("DefaultRef() = %q, want the local base", got)
	}
	want := []string{local, Image + "@" + testGenerated}
	if got := BlessedRefs(); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("BlessedRefs() = %q, want %q", got, want)
	}
	// Blessed (the published digests) is unchanged by a local base.
	if got := Blessed(); len(got) != 1 || got[0] != testGenerated {
		t.Fatalf("Blessed() = %q", got)
	}
}

func TestLocalRefEmptyFileIsNone(t *testing.T) {
	withFiles(t, fstest.MapFS{
		"blessed/watermark.txt": {Data: []byte(testWatermark + "\n")},
		"blessed/local.txt":     {Data: []byte("# nothing\n\n")},
	})
	if got := LocalRef(); got != "" {
		t.Fatalf("LocalRef() = %q, want none", got)
	}
}

func TestVersionNote(t *testing.T) {
	withFiles(t, fstest.MapFS{"blessed/watermark.txt": {Data: []byte(testWatermark)}})
	if got := VersionNote(); got != "" {
		t.Fatalf("release VersionNote() = %q", got)
	}
	withFiles(t, fstest.MapFS{"blessed/watermark.txt": {Data: []byte(testWatermark)}, "blessed/local.txt": {Data: []byte("jam-studio-base:dev-1")}})
	if got := VersionNote(); got != " (local studio base jam-studio-base:dev-1)" {
		t.Fatalf("local VersionNote() = %q", got)
	}
}
