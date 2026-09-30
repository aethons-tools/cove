package jam

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/aethons-tools/cove/internal/kit"
	"gopkg.in/yaml.v3"
)

// ManagedKitID is the kit registry name the managed (brokered-cove) kit is
// stored under. Its versions are the registry's monotonic versions (see
// EnsureManagedKit); a KitRef pairs this id with one of them.
const ManagedKitID = "managed"

// anthropicEgressRoots are the Anthropic-owned egress roots a *managed* cove must
// not be able to reach directly: a brokered cove talks to Anthropic only through
// the jam broker, so the managed kit strips these from the interactive base's
// allow-list. This is the COV-208 egress lock — see docs/usage/jam/pool.md. An
// allow-list entry is dropped when its host equals one of these or is a subdomain
// of it (a leading "." wildcard is normalized away first).
var anthropicEgressRoots = []string{"anthropic.com", "claude.com", "claude.ai"}

// ManagedKit derives the managed kit's config from the interactive base kit: the
// same config with every Anthropic egress entry removed (managed coves reach
// Anthropic only via the broker). It is a pure derivation and never mutates base.
//
// Versioning is NOT the config's concern: the kit registry assigns the monotonic
// version (see EnsureManagedKit), matching the abstraction spec's "registry is the
// definition source". The jam host and providers are ordinary infra domains,
// unaffected by the strip: only the three Anthropic roots are removed, and
// everything else keeps its order.
func ManagedKit(base kit.Config) kit.Config {
	cfg := base // Config is copied by value; only the egress slice is replaced below.
	cfg.Image.AllowedDomains = stripAnthropicEgress(base.Image.AllowedDomains)
	return cfg
}

// EnsureManagedKit derives the managed config from base and records it in the kit
// registry under ManagedKitID, returning the reference a Raise carries. The push
// is idempotent: an unchanged config reuses the current version (so restarts don't
// churn versions), and any change bumps a new monotonic version (so a drifted kit
// misses the launcher's prepared-image cache and rebuilds). The KitRef's Digest is
// the config's content hash, carried for integrity.
//
// This is the light half of the spec's kit-reference protocol: the supervisor
// carries only this KitRef on the hot path, and resolves the chunky definition
// from the registry (ResolveKitDefinition) only on an ErrKitNotReady miss.
func EnsureManagedKit(store Store, base kit.Config) (KitRef, error) {
	text, digest, err := marshalKitConfig(ManagedKit(base))
	if err != nil {
		return KitRef{}, fmt.Errorf("managed kit: marshal config: %w", err)
	}
	if k, ok := store.GetKit(ManagedKitID); ok && k.Current != 0 {
		if cur, ok := store.KitConfig(ManagedKitID, k.Current); ok && cur == text {
			return KitRef{ID: ManagedKitID, Version: k.Current, Digest: digest}, nil // unchanged: reuse
		}
	}
	version, err := store.PushKit(ManagedKitID, text)
	if err != nil {
		return KitRef{}, fmt.Errorf("managed kit: push to registry: %w", err)
	}
	return KitRef{ID: ManagedKitID, Version: version, Digest: digest}, nil
}

// ResolveKitDefinition fetches the full kit definition a KitRef names from the
// registry — the chunky transfer the supervisor makes only on an ErrKitNotReady
// miss, before PrepareKit. ok=false when the registry has no such (id, version).
func ResolveKitDefinition(store Store, ref KitRef) (KitDefinition, bool, error) {
	text, ok := store.KitConfig(ref.ID, ref.Version)
	if !ok {
		return KitDefinition{}, false, nil
	}
	var cfg kit.Config
	if err := yaml.Unmarshal([]byte(text), &cfg); err != nil {
		return KitDefinition{}, false, fmt.Errorf("resolve kit %s: unmarshal config: %w", ref, err)
	}
	return KitDefinition{Ref: ref, Config: cfg}, true, nil
}

// marshalKitConfig serializes a kit.Config to the registry's canonical text and
// its content digest (full SHA-256 hex). yaml.Marshal of a struct is
// deterministic, so identical configs yield identical text and digest.
func marshalKitConfig(cfg kit.Config) (text, digest string, err error) {
	b, err := yaml.Marshal(cfg)
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256(b)
	return string(b), hex.EncodeToString(sum[:]), nil
}

// stripAnthropicEgress returns a fresh slice with the Anthropic-owned hosts
// removed, preserving the order of the entries that remain. It always allocates a
// new backing array so the caller's base slice is never mutated.
func stripAnthropicEgress(domains []string) []string {
	out := make([]string, 0, len(domains))
	for _, d := range domains {
		if isAnthropicEgress(d) {
			continue
		}
		out = append(out, d)
	}
	return out
}

// isAnthropicEgress reports whether an allow-list entry names an Anthropic-owned
// host: equal to, or a subdomain of, one of anthropicEgressRoots. A leading "."
// (the squid wildcard form, e.g. ".anthropic.com") is normalized away first.
func isAnthropicEgress(domain string) bool {
	h := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(domain)), ".")
	for _, root := range anthropicEgressRoots {
		if h == root || strings.HasSuffix(h, "."+root) {
			return true
		}
	}
	return false
}
