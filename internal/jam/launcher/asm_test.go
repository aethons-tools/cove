package launcher

import (
	"strings"
	"testing"
)

func TestTagForFormat(t *testing.T) {
	kit := strings.Repeat("a", 64)
	asm := strings.Repeat("b", 64)
	got := tagFor(kit, asm)
	want := "cove-kit:" + strings.Repeat("a", 32) + "-" + strings.Repeat("b", 32)
	if got != want {
		t.Fatalf("tagFor = %q, want %q", got, want)
	}
	if len(got) > 128 {
		t.Fatalf("tag %d chars > docker's 128", len(got))
	}
}

func TestImageTagShortDigest(t *testing.T) {
	if got := tagFor("cafef00d", "beef"); got != "cove-kit:cafef00d-beef" {
		t.Fatalf("short digests must be used whole, got %q", got)
	}
}

func TestAsmDigestCoversEveryInput(t *testing.T) {
	base := asmDigest("id", "ref", "jam.a", []byte("key"))
	if base != asmDigest("id", "ref", "jam.a", []byte("key")) {
		t.Fatal("asmDigest not deterministic")
	}
	for name, d := range map[string]string{
		"identity": asmDigest("id2", "ref", "jam.a", []byte("key")),
		"base":     asmDigest("id", "ref2", "jam.a", []byte("key")),
		"jamhost":  asmDigest("id", "ref", "jam.b", []byte("key")),
		"key":      asmDigest("id", "ref", "jam.a", []byte("key2")),
	} {
		if d == base {
			t.Fatalf("changing %s did not change the digest", name)
		}
	}
	// Length-prefixed: shifting a boundary must not collide.
	if asmDigest("ab", "c", "h", nil) == asmDigest("a", "bc", "h", nil) {
		t.Fatal("field boundary shift collided")
	}
}

func TestLauncherTagDependsOnJamHostAndKey(t *testing.T) {
	ref := KitRef{ID: "web", Version: 1, Digest: strings.Repeat("c", 64)}
	a := New(Config{JamHost: "jam.a", PublicKey: []byte("k1")})
	b := New(Config{JamHost: "jam.b", PublicKey: []byte("k1")})
	c := New(Config{JamHost: "jam.a", PublicKey: []byte("k2")})
	if a.imageTag(ref) == b.imageTag(ref) || a.imageTag(ref) == c.imageTag(ref) {
		t.Fatalf("tag ignores JamHost/key: %s %s %s", a.imageTag(ref), b.imageTag(ref), c.imageTag(ref))
	}
	if !strings.HasPrefix(a.imageTag(ref), "cove-kit:"+strings.Repeat("c", 32)+"-") {
		t.Fatalf("tag %q lacks the kit digest prefix", a.imageTag(ref))
	}
}
