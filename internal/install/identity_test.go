package install

import (
	"testing"
	"testing/fstest"

	"github.com/aethons-tools/cove/internal/assemble"
)

func TestHashTreeDeterministicAndContentSensitive(t *testing.T) {
	a := fstest.MapFS{
		"Dockerfile":        {Data: []byte("FROM base")},
		"image-files/a.txt": {Data: []byte("one")},
		"image-files/b.txt": {Data: []byte("two")},
	}
	h1, err := HashTree(a)
	if err != nil {
		t.Fatal(err)
	}
	// A byte-identical tree hashes equal regardless of declaration order.
	b := fstest.MapFS{
		"image-files/b.txt": {Data: []byte("two")},
		"image-files/a.txt": {Data: []byte("one")},
		"Dockerfile":        {Data: []byte("FROM base")},
	}
	h2, err := HashTree(b)
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Fatalf("identical trees hashed differently: %s != %s", h1, h2)
	}

	// An edited file, an added file, and a renamed path must each change the hash.
	for name, mut := range map[string]fstest.MapFS{
		"edited":  {"Dockerfile": {Data: []byte("FROM other")}, "image-files/a.txt": {Data: []byte("one")}, "image-files/b.txt": {Data: []byte("two")}},
		"added":   {"Dockerfile": {Data: []byte("FROM base")}, "image-files/a.txt": {Data: []byte("one")}, "image-files/b.txt": {Data: []byte("two")}, "image-files/c.txt": {Data: []byte("three")}},
		"renamed": {"Dockerfile": {Data: []byte("FROM base")}, "image-files/a.txt": {Data: []byte("one")}, "image-files/renamed.txt": {Data: []byte("two")}},
	} {
		got, err := HashTree(mut)
		if err != nil {
			t.Fatal(err)
		}
		if got == h1 {
			t.Errorf("%s tree must not match the original hash", name)
		}
	}
}

// TestAtCoveIdentityIncludesCoveMaster is a smoke check that AtCoveIdentity
// computes cleanly now that it also hashes covemasterbin.BinFS() as a fourth
// field (COV-158).
func TestAtCoveIdentityIncludesCoveMaster(t *testing.T) {
	id, err := AtCoveIdentity()
	if err != nil || id == "" {
		t.Fatalf("AtCoveIdentity: %q err=%v", id, err)
	}
}

func TestAtCoveIdentityDeterministicAndNonEmpty(t *testing.T) {
	id1, err := AtCoveIdentity()
	if err != nil {
		t.Fatal(err)
	}
	id2, err := AtCoveIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if id1 == "" {
		t.Fatal("AtCoveIdentity must be non-empty")
	}
	if id1 != id2 {
		t.Fatalf("AtCoveIdentity must be stable: %s != %s", id1, id2)
	}
}

// The harness layer's rendered baseline preferences (COV-245) are generated
// from code, not embedded payload, so the identity hashes them explicitly: a
// change to the baseline must flip it like a payload edit does.
func TestAtCoveIdentityIncludesHarnessBaseline(t *testing.T) {
	id, err := AtCoveIdentity()
	if err != nil {
		t.Fatal(err)
	}
	orig := harnessBaseline
	t.Cleanup(func() { harnessBaseline = orig })
	harnessBaseline = func() []byte { return []byte(`{"theme":"light"}`) }
	other, err := AtCoveIdentity()
	if err != nil || other == id {
		t.Fatalf("a baseline change must change the identity (err %v)", err)
	}
}

// AssembleContext's own Go output (not an embedded tree) is versioned by
// assemble.Revision; bumping it must change the identity, so images assembled
// by older code are never reused under an unchanged tag.
func TestAtCoveIdentityIncludesAssembleRevision(t *testing.T) {
	before, err := AtCoveIdentity()
	if err != nil {
		t.Fatal(err)
	}
	old := assemble.Revision
	t.Cleanup(func() { assemble.Revision = old })
	assemble.Revision = old + "-test"
	after, err := AtCoveIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if before == after {
		t.Fatal("AtCoveIdentity did not change with assemble.Revision")
	}
}
