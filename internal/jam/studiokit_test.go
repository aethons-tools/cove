package jam

import (
	"slices"
	"testing"

	"github.com/aethons-tools/cove/internal/harnessinstall"
	"github.com/aethons-tools/cove/internal/studio"
)

func newKitTestStore(t *testing.T) Store {
	t.Helper()
	st := NewMemStore()
	return st
}

func TestEnsureStudioKitIdempotentThenBumps(t *testing.T) {
	st := newKitTestStore(t)
	sk := studio.StudioKit{Kind: studio.Kind, Egress: []string{"github.com"}}
	ref, err := EnsureStudioKit(st, "web", sk)
	if err != nil {
		t.Fatal(err)
	}
	if ref.ID != "web" || ref.Version == 0 || ref.Digest != studio.BuildDigest(sk, harnessinstall.Default()) {
		t.Fatalf("bad ref: %+v", ref)
	}
	ref2, _ := EnsureStudioKit(st, "web", sk)
	if ref2.Version != ref.Version {
		t.Fatalf("unchanged kit must reuse version: %d vs %d", ref2.Version, ref.Version)
	}
	// A PROMPT-only edit bumps the registry version but keeps the build-digest.
	sk.Prompt = "edited"
	ref3, _ := EnsureStudioKit(st, "web", sk)
	if ref3.Version == ref.Version {
		t.Fatal("a definition change must bump the registry version")
	}
	if ref3.Digest != ref.Digest {
		t.Fatal("a prompt-only edit must NOT change the build-digest")
	}
}

func TestStudioKitDefinitionFailsClosed(t *testing.T) {
	st := newKitTestStore(t)
	if _, err := StudioKitDefinition(st, "missing", harnessinstall.Default()); err == nil {
		t.Fatal("want fail-closed on an absent kit")
	}
}

func TestResolveKitDefinitionParsesStudio(t *testing.T) {
	st := newKitTestStore(t)
	sk := studio.StudioKit{Kind: studio.Kind, Egress: []string{"github.com", ".anthropic.com"}}
	ref, _ := EnsureStudioKit(st, "web", sk)
	def, ok, err := ResolveKitDefinition(st, ref, harnessinstall.Default())
	if err != nil || !ok {
		t.Fatalf("resolve: ok=%v err=%v", ok, err)
	}
	// The definition is keyed on (and carries) the harness it resolves for.
	h := harnessinstall.Install{Type: "claude", Version: "2.1.100", Plugins: []string{}}
	other, _, _ := ResolveKitDefinition(st, ref, h)
	if other.Ref.Digest != studio.BuildDigest(sk, h) || other.Harness.Version != "2.1.100" || other.Ref.Version != ref.Version {
		t.Fatalf("resolve under a harness = %+v", other)
	}
	cur, err := StudioKitDefinition(st, "web", h)
	if err != nil || cur.Ref != other.Ref || cur.Harness.Version != "2.1.100" {
		t.Fatalf("StudioKitDefinition = %+v, %v", cur, err)
	}
	if !slices.Contains(def.Kit.Egress, ".anthropic.com") {
		t.Fatalf("definition carries the authored kit verbatim (ceiling is applied at assemble): %+v", def.Kit)
	}
}
