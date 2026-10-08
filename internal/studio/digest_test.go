package studio

import (
	"testing"

	"github.com/aethons-tools/cove/internal/harnessinstall"
	"github.com/aethons-tools/cove/internal/kit"
)

func TestBuildDigestIgnoresPrompt(t *testing.T) {
	a := StudioKit{Kind: Kind, Base: Base{Image: "r"}, Egress: []string{"b.com", "a.com"}, Prompt: "one"}
	b := a
	b.Prompt = "two (edited)" // raise-time field — must not change the image key
	if BuildDigest(a, dh) != BuildDigest(b, dh) {
		t.Fatal("prompt edit must not change the build-digest")
	}
	c := a
	c.Egress = []string{"a.com", "b.com"} // reordered — same set
	if BuildDigest(a, dh) != BuildDigest(c, dh) {
		t.Fatal("egress order must not change the build-digest")
	}
	d := a
	d.BuildArgs = map[string]string{"X": "1"} // build-affecting change
	if BuildDigest(a, dh) == BuildDigest(d, dh) {
		t.Fatal("a build-arg change must change the build-digest")
	}
}

func TestBuildDigestNilVsEmptyNormalization(t *testing.T) {
	// nil Egress should hash the same as empty non-nil []string{}
	nilEgress := StudioKit{Kind: Kind, Base: Base{Image: "r"}, Egress: nil, BuildArgs: map[string]string{"X": "1"}}
	emptyEgress := StudioKit{Kind: Kind, Base: Base{Image: "r"}, Egress: []string{}, BuildArgs: map[string]string{"X": "1"}}
	if BuildDigest(nilEgress, dh) != BuildDigest(emptyEgress, dh) {
		t.Fatal("nil Egress and empty Egress must produce identical digests")
	}

	// nil BuildArgs should hash the same as empty non-nil map[string]string{}
	nilBuildArgs := StudioKit{Kind: Kind, Base: Base{Image: "r"}, Egress: []string{"a.com"}, BuildArgs: nil}
	emptyBuildArgs := StudioKit{Kind: Kind, Base: Base{Image: "r"}, Egress: []string{"a.com"}, BuildArgs: map[string]string{}}
	if BuildDigest(nilBuildArgs, dh) != BuildDigest(emptyBuildArgs, dh) {
		t.Fatal("nil BuildArgs and empty BuildArgs must produce identical digests")
	}

	// Both nil
	bothNil := StudioKit{Kind: Kind, Base: Base{Image: "r"}, Egress: nil, BuildArgs: nil}
	bothEmpty := StudioKit{Kind: Kind, Base: Base{Image: "r"}, Egress: []string{}, BuildArgs: map[string]string{}}
	if BuildDigest(bothNil, dh) != BuildDigest(bothEmpty, dh) {
		t.Fatal("both nil should equal both empty")
	}
}

func TestBuildDigestBaseChangeAffectsDigest(t *testing.T) {
	a := StudioKit{Kind: Kind, Base: Base{Image: "r1"}, Egress: []string{"a.com"}}
	b := a
	b.Base = Base{Image: "r2"}
	if BuildDigest(a, dh) == BuildDigest(b, dh) {
		t.Fatal("Base change must change the digest")
	}
}

func TestBuildDigestSecretsDoNotAffectDigest(t *testing.T) {
	a := StudioKit{Kind: Kind, Base: Base{Image: "r"}, Egress: []string{"a.com"}, Secrets: map[string]kit.SecretConfig{"KEY": {Description: "first"}}}
	b := a
	b.Secrets = map[string]kit.SecretConfig{"KEY": {Description: "changed"}} // Secrets-only change
	if BuildDigest(a, dh) != BuildDigest(b, dh) {
		t.Fatal("Secrets change must not change the digest")
	}
}

func TestBuildDigestMCPServers(t *testing.T) {
	a := StudioKit{Kind: Kind, Base: Base{Image: "r"}, Egress: []string{"a.com"}}
	// A kit without mcp-servers hashes no mcpServers key (its pre-COV-240
	// form), so adding the field did not rebuild every existing kit image.
	// (The harness, added by COV-242, did change every digest once.) Fixed
	// install, not Default(), so a DefaultClaudeVersion bump leaves this alone.
	legacy := StudioKit{Kind: Kind, Base: Base{Image: "r"}, Egress: []string{"a.com"}, BuildArgs: map[string]string{"X": "1"}}
	fixed := harnessinstall.Install{Type: "claude", Version: "2.1.287", Plugins: []string{"superpowers@claude-plugins-official"}}
	if got := BuildDigest(legacy, fixed); got != "f871450b4e95054ac43ba11571b0d5f289dddcf1d5f895546c26566553a42d0b" {
		t.Fatalf("digest of a kit without mcp-servers changed: %s", got)
	}
	b := a
	b.MCPServers = map[string]kit.MCPServer{}
	if BuildDigest(a, dh) != BuildDigest(b, dh) {
		t.Fatal("nil and empty mcp-servers must produce identical digests")
	}
	// mcp-servers are baked into the image, so they are build-affecting.
	c := a
	c.MCPServers = map[string]kit.MCPServer{"linear": {Type: "http", URL: "${U}"}}
	if BuildDigest(a, dh) == BuildDigest(c, dh) {
		t.Fatal("an mcp-servers change must change the build-digest")
	}
}

// dh is the default harness install most digest tests key under.
var dh = harnessinstall.Default()

// The harness layer is baked into the image, so its type, exact version and
// plugins are build-affecting (COV-242); plugin order and duplicates are not.
func TestBuildDigestHarness(t *testing.T) {
	sk := StudioKit{Kind: Kind, Base: Base{Image: "r"}, Egress: []string{"a.com"}}
	base := BuildDigest(sk, dh)
	v := dh
	v.Version = "2.1.100"
	if BuildDigest(sk, v) == base {
		t.Fatal("a harness version change must change the build-digest")
	}
	p := dh
	p.Plugins = []string{}
	if BuildDigest(sk, p) == base {
		t.Fatal("a harness plugins change must change the build-digest")
	}
	ty := dh
	ty.Type = "codex"
	if BuildDigest(sk, ty) == base {
		t.Fatal("a harness type change must change the build-digest")
	}
	two := harnessinstall.Install{Type: dh.Type, Version: dh.Version, Plugins: []string{"b@claude-plugins-official", "a@claude-plugins-official"}}
	swapped := harnessinstall.Install{Type: dh.Type, Version: dh.Version, Plugins: []string{"a@claude-plugins-official", "b@claude-plugins-official", "a@claude-plugins-official"}}
	if BuildDigest(sk, two) != BuildDigest(sk, swapped) {
		t.Fatal("plugin order/duplicates must not change the build-digest")
	}
	none := dh
	none.Plugins = nil
	if BuildDigest(sk, none) != BuildDigest(sk, p) {
		t.Fatal("nil and empty plugins must produce identical digests")
	}
}
