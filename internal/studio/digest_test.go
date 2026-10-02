package studio

import (
	"testing"

	"github.com/aethons-tools/cove/internal/kit"
)

func TestBuildDigestIgnoresPrompt(t *testing.T) {
	a := StudioKit{Kind: Kind, Base: Base{Image: "r"}, Egress: []string{"b.com", "a.com"}, Prompt: "one"}
	b := a
	b.Prompt = "two (edited)" // raise-time field — must not change the image key
	if BuildDigest(a) != BuildDigest(b) {
		t.Fatal("prompt edit must not change the build-digest")
	}
	c := a
	c.Egress = []string{"a.com", "b.com"} // reordered — same set
	if BuildDigest(a) != BuildDigest(c) {
		t.Fatal("egress order must not change the build-digest")
	}
	d := a
	d.BuildArgs = map[string]string{"X": "1"} // build-affecting change
	if BuildDigest(a) == BuildDigest(d) {
		t.Fatal("a build-arg change must change the build-digest")
	}
}

func TestBuildDigestNilVsEmptyNormalization(t *testing.T) {
	// nil Egress should hash the same as empty non-nil []string{}
	nilEgress := StudioKit{Kind: Kind, Base: Base{Image: "r"}, Egress: nil, BuildArgs: map[string]string{"X": "1"}}
	emptyEgress := StudioKit{Kind: Kind, Base: Base{Image: "r"}, Egress: []string{}, BuildArgs: map[string]string{"X": "1"}}
	if BuildDigest(nilEgress) != BuildDigest(emptyEgress) {
		t.Fatal("nil Egress and empty Egress must produce identical digests")
	}

	// nil BuildArgs should hash the same as empty non-nil map[string]string{}
	nilBuildArgs := StudioKit{Kind: Kind, Base: Base{Image: "r"}, Egress: []string{"a.com"}, BuildArgs: nil}
	emptyBuildArgs := StudioKit{Kind: Kind, Base: Base{Image: "r"}, Egress: []string{"a.com"}, BuildArgs: map[string]string{}}
	if BuildDigest(nilBuildArgs) != BuildDigest(emptyBuildArgs) {
		t.Fatal("nil BuildArgs and empty BuildArgs must produce identical digests")
	}

	// Both nil
	bothNil := StudioKit{Kind: Kind, Base: Base{Image: "r"}, Egress: nil, BuildArgs: nil}
	bothEmpty := StudioKit{Kind: Kind, Base: Base{Image: "r"}, Egress: []string{}, BuildArgs: map[string]string{}}
	if BuildDigest(bothNil) != BuildDigest(bothEmpty) {
		t.Fatal("both nil should equal both empty")
	}
}

func TestBuildDigestBaseChangeAffectsDigest(t *testing.T) {
	a := StudioKit{Kind: Kind, Base: Base{Image: "r1"}, Egress: []string{"a.com"}}
	b := a
	b.Base = Base{Image: "r2"}
	if BuildDigest(a) == BuildDigest(b) {
		t.Fatal("Base change must change the digest")
	}
}

func TestBuildDigestSecretsDoNotAffectDigest(t *testing.T) {
	a := StudioKit{Kind: Kind, Base: Base{Image: "r"}, Egress: []string{"a.com"}, Secrets: map[string]kit.SecretConfig{"KEY": {Description: "first"}}}
	b := a
	b.Secrets = map[string]kit.SecretConfig{"KEY": {Description: "changed"}} // Secrets-only change
	if BuildDigest(a) != BuildDigest(b) {
		t.Fatal("Secrets change must not change the digest")
	}
}
