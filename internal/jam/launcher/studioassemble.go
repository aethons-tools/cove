package launcher

import (
	"strings"

	"github.com/aethons-tools/cove/internal/assemble"
	"github.com/aethons-tools/cove/internal/studio"
)

// studioEgress builds the assemble.Egress for a studio kit: Policy AND ceiling
// are the authored allow-list with Anthropic excluded (COV-208); Infra is the
// jam host so the cove can reach the broker, plus ".<destDomain>" when the
// Jam has a destinations domain, so the cove can reach its subdomain endpoints
// (COV-312) — never anything wider. A Jam host the dot entry already covers
// (the domain itself, or a name under it) is left out, so the one squid ACL
// holds no redundant pair. excluded is returned for surfacing.
func studioEgress(sk studio.StudioKit, jamHost, destDomain string) (eg assemble.Egress, excluded []string) {
	ceiling, excl := studio.Ceiling(sk.Egress)
	var infra []string
	if jamHost != "" && (destDomain == "" || (jamHost != destDomain && !strings.HasSuffix(jamHost, "."+destDomain))) {
		infra = append(infra, jamHost)
	}
	if destDomain != "" {
		infra = append(infra, "."+destDomain)
	}
	return assemble.Egress{Policy: ceiling, Infra: infra}, excl
}
