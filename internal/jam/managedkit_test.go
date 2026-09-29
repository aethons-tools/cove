package jam

import (
	"slices"
	"testing"

	"github.com/aethons-tools/cove/internal/kit"
)

func TestManagedKitStripsAnthropicEgress(t *testing.T) {
	base := kit.Config{Name: "k", Image: kit.ImageConfig{AllowedDomains: []string{".anthropic.com", "claude.ai", "proxy.golang.org", ".local.aethons.tools"}}}
	mk := ManagedKit(base)
	for _, banned := range []string{".anthropic.com", ".claude.com", "claude.ai"} {
		if slices.Contains(mk.Config.Image.AllowedDomains, banned) {
			t.Fatalf("managed kit egress must not include %q: %v", banned, mk.Config.Image.AllowedDomains)
		}
	}
	if !slices.Contains(mk.Config.Image.AllowedDomains, "proxy.golang.org") {
		t.Fatal("non-Anthropic domains must be preserved")
	}
	if mk.Ref.ID != "managed" {
		t.Fatalf("ref id = %q", mk.Ref.ID)
	}
}

// TestManagedKitEgressGolden pins the exact resulting allow-list for a
// representative interactive base: every Anthropic-owned host (bare, wildcard,
// subdomain, and claude.ai) is dropped; everything else keeps its order.
func TestManagedKitEgressGolden(t *testing.T) {
	base := kit.Config{
		Name: "k",
		Image: kit.ImageConfig{AllowedDomains: []string{
			"github.com",
			".anthropic.com",
			"api.anthropic.com",
			"proxy.golang.org",
			"anthropic.com",
			"claude.ai",
			".claude.ai",
			"claude.com",
			".claude.com",
			"statsig.anthropic.com",
			"registry.example.com",
			".local.aethons.tools",
		}},
	}
	want := []string{
		"github.com",
		"proxy.golang.org",
		"registry.example.com",
		".local.aethons.tools",
	}
	got := ManagedKit(base).Config.Image.AllowedDomains
	if !slices.Equal(got, want) {
		t.Fatalf("managed egress golden mismatch:\n got:  %v\n want: %v", got, want)
	}
}

// TestManagedKitDoesNotMutateBase — ManagedKit clones; the caller's base egress
// is untouched.
func TestManagedKitDoesNotMutateBase(t *testing.T) {
	base := kit.Config{Name: "k", Image: kit.ImageConfig{AllowedDomains: []string{".anthropic.com", "github.com"}}}
	_ = ManagedKit(base)
	if !slices.Equal(base.Image.AllowedDomains, []string{".anthropic.com", "github.com"}) {
		t.Fatalf("ManagedKit mutated its base: %v", base.Image.AllowedDomains)
	}
}

// TestManagedKitVersionStableAndChanges — the content-hashed version is stable
// for identical content and bumps when the config changes.
func TestManagedKitVersionStableAndChanges(t *testing.T) {
	base := kit.Config{Name: "k", Image: kit.ImageConfig{AllowedDomains: []string{"github.com", ".anthropic.com"}}}

	a := ManagedKit(base)
	b := ManagedKit(base)
	if a.Ref.Version != b.Ref.Version || a.Ref.Digest != b.Ref.Digest {
		t.Fatalf("version not stable for identical content: %s vs %s", a.Ref, b.Ref)
	}
	if a.Ref.Digest == "" {
		t.Fatal("expected a content digest")
	}

	changed := base
	changed.Image.AllowedDomains = []string{"github.com", "example.org", ".anthropic.com"}
	c := ManagedKit(changed)
	if c.Ref.Version == a.Ref.Version {
		t.Fatalf("version must bump when content changes: both %s", a.Ref)
	}
}
