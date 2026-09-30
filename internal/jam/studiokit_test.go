package jam

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/aethons-tools/cove/internal/studio"
)

func newKitTestStore(t *testing.T) Store {
	t.Helper()
	st, err := NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestEnsureStudioKitIdempotentThenBumps(t *testing.T) {
	st := newKitTestStore(t)
	sk := studio.StudioKit{Kind: studio.Kind, Name: "web", Egress: []string{"github.com"}}
	ref, err := EnsureStudioKit(st, sk)
	if err != nil {
		t.Fatal(err)
	}
	if ref.ID != "web" || ref.Version == 0 || ref.Digest != studio.BuildDigest(sk) {
		t.Fatalf("bad ref: %+v", ref)
	}
	ref2, _ := EnsureStudioKit(st, sk)
	if ref2.Version != ref.Version {
		t.Fatalf("unchanged kit must reuse version: %d vs %d", ref2.Version, ref.Version)
	}
	// A PROMPT-only edit bumps the registry version but keeps the build-digest.
	sk.Prompt = "edited"
	ref3, _ := EnsureStudioKit(st, sk)
	if ref3.Version == ref.Version {
		t.Fatal("a definition change must bump the registry version")
	}
	if ref3.Digest != ref.Digest {
		t.Fatal("a prompt-only edit must NOT change the build-digest")
	}
}

func TestStudioKitRefFailsClosed(t *testing.T) {
	st := newKitTestStore(t)
	if _, err := StudioKitRef(st, "missing"); err == nil {
		t.Fatal("want fail-closed on an absent kit")
	}
}

func TestResolveKitDefinitionParsesStudio(t *testing.T) {
	st := newKitTestStore(t)
	sk := studio.StudioKit{Kind: studio.Kind, Name: "web", Egress: []string{"github.com", ".anthropic.com"}}
	ref, _ := EnsureStudioKit(st, sk)
	def, ok, err := ResolveKitDefinition(st, ref)
	if err != nil || !ok {
		t.Fatalf("resolve: ok=%v err=%v", ok, err)
	}
	if def.Kit.Name != "web" || !slices.Contains(def.Kit.Egress, ".anthropic.com") {
		t.Fatalf("definition carries the authored kit verbatim (ceiling is applied at assemble): %+v", def.Kit)
	}
}
