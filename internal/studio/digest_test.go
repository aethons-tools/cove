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

// Golden values computed on main before kit inheritance: unchained kits must
// keep their image tags across the digest refactor.
func TestBuildDigestGolden(t *testing.T) {
	for name, tc := range map[string]struct {
		sk   StudioKit
		want string
	}{
		"image+egress+args": {StudioKit{Kind: Kind, Base: Base{Image: "r"}, Egress: []string{"b.com", "a.com"}, BuildArgs: map[string]string{"X": "1"}}, "6c8fb13a9b4bbdcbe26c568f6d0eee6165561506ffbf707c3d8d6767325e6f0b"},
		"default":           {StudioKit{Kind: Kind}, "aa6e7ecf92dad240d4fb2b80d2a5a97f16b40620b6785415bba8a41199638bee"},
		"mcp server":        {StudioKit{Kind: Kind, MCPServers: map[string]kit.MCPServer{"m": {Command: "x"}}}, "8d19c1e6f8fbb490e6526ce15c0601def69fc7f8455aaebfef86a372155845c2"},
	} {
		if got := BuildDigest(tc.sk, dh); got != tc.want {
			t.Errorf("%s: digest %s, want %s", name, got, tc.want)
		}
	}
}

// TestBuildDigestBuildContext pins the formula: today's payload with
// "buildContext" after "base", omitted when empty — so a kit without a
// Dockerfile keeps its digest exactly.
func TestBuildDigestBuildContext(t *testing.T) {
	h := harnessinstall.Default()
	sk := StudioKit{Kind: Kind, Egress: []string{"b.com", "a.com"}, Prompt: "p"}
	old := hashDigest(struct {
		Base       Base                     `json:"base"`
		Egress     []string                 `json:"egress"`
		BuildArgs  map[string]string        `json:"buildArgs"`
		MCPServers map[string]kit.MCPServer `json:"mcpServers,omitempty"`
		Harness    harnessinstall.Install   `json:"harness"`
	}{Base: sk.Base, Egress: normEgress(sk.Egress), BuildArgs: normBuildArgs(nil), Harness: normHarness(h)})
	if got := BuildDigest(sk, h); got != old {
		t.Fatalf("a kit without a build context changed digest: %s != %s", got, old)
	}
	withCtx := sk
	withCtx.BuildContext = "abc"
	if BuildDigest(withCtx, h) == old {
		t.Fatal("a build context must key the digest")
	}
	edited := withCtx
	edited.Prompt = "other"
	if BuildDigest(edited, h) != BuildDigest(withCtx, h) {
		t.Fatal("a prompt edit must keep the digest")
	}
	parent := StudioKit{Kind: Kind}
	child := StudioKit{Kind: Kind, Base: Base{Kit: "p"}, BuildContext: "c1"}
	child2 := child
	child2.BuildContext = "c2"
	if ChainDigest([]StudioKit{parent, child}, h) == ChainDigest([]StudioKit{parent, child2}, h) {
		t.Fatal("a link's build context must key the chain digest")
	}
}

// TestChainDigestGolden pins a 2-link chain without Dockerfiles to the digest
// it had before build contexts existed (computed at 9e605f4).
func TestChainDigestGolden(t *testing.T) {
	fixed := harnessinstall.Install{Type: "claude", Version: "2.1.287", Plugins: []string{"superpowers@claude-plugins-official"}}
	parent := StudioKit{Kind: Kind, Base: Base{Image: "ghcr.io/x/base@sha256:1"}, Egress: []string{"a.com"}}
	child := StudioKit{Kind: Kind, Base: Base{Kit: "parent@v2"}, Egress: []string{"b.com"}, BuildArgs: map[string]string{"X": "1"}, Prompt: "p"}
	if got := ChainDigest([]StudioKit{parent, child}, fixed); got != "5193a80c02de89a5b182de1ddb7a5651c28058dca78ee1e0181ea5fecc52c919" {
		t.Fatalf("chain digest without build contexts changed: %s", got)
	}
}
