package jam

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/aethons-tools/cove/internal/kit"
	"gopkg.in/yaml.v3"
)

func TestManagedKitStripsAnthropicEgress(t *testing.T) {
	base := kit.Config{Name: "k", Image: kit.ImageConfig{AllowedDomains: []string{".anthropic.com", "claude.ai", "proxy.golang.org", ".local.aethons.tools"}}}
	got := ManagedKit(base).Image.AllowedDomains
	for _, banned := range []string{".anthropic.com", ".claude.com", "claude.ai"} {
		if slices.Contains(got, banned) {
			t.Fatalf("managed kit egress must not include %q: %v", banned, got)
		}
	}
	if !slices.Contains(got, "proxy.golang.org") {
		t.Fatal("non-Anthropic domains must be preserved")
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
	got := ManagedKit(base).Image.AllowedDomains
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

func newKitTestStore(t *testing.T) Store {
	t.Helper()
	st, err := NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// EnsureManagedKit records the managed config in the registry and returns a ref
// naming that (id, version). Recording the SAME config again reuses the version
// (no churn on restart); a CHANGED config bumps a new monotonic version.
func TestEnsureManagedKitIdempotentThenBumps(t *testing.T) {
	st := newKitTestStore(t)
	base := kit.Config{Name: "k", Image: kit.ImageConfig{AllowedDomains: []string{"github.com", ".anthropic.com"}}}

	a, err := EnsureManagedKit(st, base)
	if err != nil {
		t.Fatalf("EnsureManagedKit: %v", err)
	}
	if a.ID != ManagedKitID || a.Version == 0 || a.Digest == "" {
		t.Fatalf("bad ref: %+v", a)
	}

	b, err := EnsureManagedKit(st, base)
	if err != nil {
		t.Fatalf("EnsureManagedKit (repeat): %v", err)
	}
	if b.Version != a.Version || b.Digest != a.Digest {
		t.Fatalf("unchanged config must reuse the version: %s vs %s", a, b)
	}

	changed := base
	changed.Image.AllowedDomains = []string{"github.com", "example.org", ".anthropic.com"}
	c, err := EnsureManagedKit(st, changed)
	if err != nil {
		t.Fatalf("EnsureManagedKit (changed): %v", err)
	}
	if c.Version == a.Version {
		t.Fatalf("changed config must bump the version: both %d", a.Version)
	}
}

// EnsureManagedKitFor strips + registers the managed variant of a registered kit
// under managed-<name>, idempotently; fails closed on an absent or non-tag-safe
// source kit.
func TestEnsureManagedKitForStripsAndRegistersDerivative(t *testing.T) {
	st := newKitTestStore(t)
	webCfg := kit.Config{Name: "web", Image: kit.ImageConfig{AllowedDomains: []string{"github.com", ".anthropic.com"}}}
	text, err := yaml.Marshal(webCfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PushKit("web", string(text)); err != nil {
		t.Fatal(err)
	}

	ref, err := EnsureManagedKitFor(st, "web")
	if err != nil {
		t.Fatalf("EnsureManagedKitFor: %v", err)
	}
	if ref.ID != "managed-web" || ref.Version == 0 || ref.Digest == "" {
		t.Fatalf("bad ref: %+v", ref)
	}
	def, ok, err := ResolveKitDefinition(st, ref)
	if err != nil || !ok {
		t.Fatalf("resolve: %v %v", ok, err)
	}
	if slices.Contains(def.Config.Image.AllowedDomains, ".anthropic.com") {
		t.Fatalf("derivative must strip Anthropic egress: %v", def.Config.Image.AllowedDomains)
	}
	ref2, err := EnsureManagedKitFor(st, "web")
	if err != nil {
		t.Fatal(err)
	}
	if ref2.Version != ref.Version {
		t.Fatalf("unchanged source must reuse the version: %d vs %d", ref.Version, ref2.Version)
	}
}

// A hand-written config.yml (the on-disk form an operator pushes, comments and
// all) resolves through the canonical parser and strips Anthropic egress — the
// path that a paste-mangled YAML once tripped. Guards against reverting to a
// non-canonical parser that would diverge from push-time validation.
func TestEnsureManagedKitForParsesOnDiskConfig(t *testing.T) {
	st := newKitTestStore(t)
	raw := "# webkit\nname: webkit\nimage:\n  allowed-domains:\n    - example.com\n    - .anthropic.com\n"
	if _, err := st.PushKit("webkit", raw); err != nil {
		t.Fatal(err)
	}
	ref, err := EnsureManagedKitFor(st, "webkit")
	if err != nil {
		t.Fatalf("EnsureManagedKitFor: %v", err)
	}
	def, ok, err := ResolveKitDefinition(st, ref)
	if err != nil || !ok {
		t.Fatalf("resolve: %v %v", ok, err)
	}
	if !slices.Contains(def.Config.Image.AllowedDomains, "example.com") ||
		slices.Contains(def.Config.Image.AllowedDomains, ".anthropic.com") {
		t.Fatalf("want example.com kept, Anthropic stripped: %v", def.Config.Image.AllowedDomains)
	}
}

// A stored config that isn't valid config.yml fails closed with the canonical
// parser's error (surfacing what push should have rejected).
func TestEnsureManagedKitForRejectsInvalidConfig(t *testing.T) {
	st := newKitTestStore(t)
	if _, err := st.PushKit("broken", "name: x\n  bad: indent\n"); err != nil {
		t.Fatal(err) // PushKit stores verbatim; it does not itself validate
	}
	if _, err := EnsureManagedKitFor(st, "broken"); err == nil {
		t.Fatal("invalid stored config must fail closed at resolution")
	}
}

func TestEnsureManagedKitForFailsClosed(t *testing.T) {
	st := newKitTestStore(t)
	if _, err := EnsureManagedKitFor(st, "missing"); err == nil {
		t.Fatal("a kit absent from the registry must fail closed")
	}
	if _, err := EnsureManagedKitFor(st, "bad/name"); err == nil {
		t.Fatal("a non-tag-safe kit name must fail closed")
	}
}

// ResolveKitDefinition round-trips a recorded managed kit back to its config —
// the chunky transfer the supervisor makes on an ErrKitNotReady miss. The
// resolved config carries the Anthropic-stripped egress.
func TestResolveKitDefinitionRoundTrips(t *testing.T) {
	st := newKitTestStore(t)
	base := kit.Config{Name: "k", Image: kit.ImageConfig{AllowedDomains: []string{"github.com", ".anthropic.com", "proxy.golang.org"}}}
	ref, err := EnsureManagedKit(st, base)
	if err != nil {
		t.Fatalf("EnsureManagedKit: %v", err)
	}

	def, ok, err := ResolveKitDefinition(st, ref)
	if err != nil || !ok {
		t.Fatalf("ResolveKitDefinition = %v, %v", ok, err)
	}
	if def.Ref != ref {
		t.Fatalf("ref mismatch: %s vs %s", def.Ref, ref)
	}
	want := []string{"github.com", "proxy.golang.org"}
	if !slices.Equal(def.Config.Image.AllowedDomains, want) {
		t.Fatalf("resolved egress = %v, want %v", def.Config.Image.AllowedDomains, want)
	}

	if _, ok, _ := ResolveKitDefinition(st, KitRef{ID: ManagedKitID, Version: 999}); ok {
		t.Fatal("resolving an absent version must report not found")
	}
}
