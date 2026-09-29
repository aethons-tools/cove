package jam

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"strings"

	"github.com/aethons-tools/cove/internal/kit"
	"gopkg.in/yaml.v3"
)

// anthropicEgressRoots are the Anthropic-owned egress roots a *managed* cove must
// not be able to reach directly: a brokered cove talks to Anthropic only through
// the jam broker, so the managed kit strips these from the interactive base's
// allow-list. This is the COV-208 egress lock — see docs/usage/jam/pool.md. An
// allow-list entry is dropped when its host equals one of these or is a subdomain
// of it (a leading "." wildcard is normalized away first).
var anthropicEgressRoots = []string{"anthropic.com", "claude.com", "claude.ai"}

// ManagedKit derives the managed kit from the interactive base kit: the same
// config with every Anthropic egress entry removed (managed coves reach Anthropic
// only via the broker), and a content-hashed KitRef{ID:"managed"} whose Version
// bumps whenever the resulting config changes. Deriving the version from a hash
// keeps drift automatic — there is no constant to remember to bump.
//
// The jam host and providers are ordinary infra domains, unaffected by the strip:
// only the three Anthropic roots are removed, and everything else keeps its order.
func ManagedKit(base kit.Config) KitDefinition {
	cfg := base // Config is copied by value; only the egress slice is replaced below.
	cfg.Image.AllowedDomains = stripAnthropicEgress(base.Image.AllowedDomains)

	version, digest := kitVersion(cfg)
	return KitDefinition{
		Ref:    KitRef{ID: "managed", Version: version, Digest: digest},
		Config: cfg,
	}
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

// kitVersion derives a stable (Version, Digest) pair from the resulting config so
// that identical content always yields the same version and any change bumps it.
// The digest is the full SHA-256 hex; Version is the top 31 bits of that hash (a
// positive int, portable across 32/64-bit).
func kitVersion(cfg kit.Config) (int, string) {
	canonical, err := yaml.Marshal(cfg)
	if err != nil {
		// A kit.Config always marshals; fold an impossible failure into the hash
		// input so the result stays deterministic and distinct rather than panicking.
		canonical = []byte("managedkit-marshal-error:" + err.Error())
	}
	sum := sha256.Sum256(canonical)
	digest := hex.EncodeToString(sum[:])
	version := int(binary.BigEndian.Uint32(sum[:4]) & 0x7fffffff)
	return version, digest
}
