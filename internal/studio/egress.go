package studio

import (
	"slices"
	"strings"
)

// anthropicEgressRoots are the Anthropic-owned hosts a studio (brokered) cove
// must not reach directly — it talks to Anthropic only through the jam broker
// (COV-208). The studio egress CEILING structurally excludes these; the
// exclusion is surfaced (prepare log + `show`), never a silent policy rewrite.
var anthropicEgressRoots = []string{"anthropic.com", "claude.com", "claude.ai"}

// Ceiling returns the studio egress ceiling: the authored allow-list, sorted and
// deduped, with every Anthropic-owned entry removed; excluded is the sorted set
// that was removed (for surfacing). A leading "." (squid wildcard) is normalized
// away before matching.
func Ceiling(authored []string) (ceiling, excluded []string) {
	seen := map[string]bool{}
	for _, d := range authored {
		if seen[d] {
			continue
		}
		seen[d] = true
		if isAnthropicEgress(d) {
			excluded = append(excluded, d)
		} else {
			ceiling = append(ceiling, d)
		}
	}
	slices.Sort(ceiling)
	slices.Sort(excluded)
	return ceiling, excluded
}

func isAnthropicEgress(domain string) bool {
	h := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(domain)), ".")
	h = strings.TrimSuffix(h, ".")
	for _, root := range anthropicEgressRoots {
		if h == root || strings.HasSuffix(h, "."+root) {
			return true
		}
	}
	return false
}
