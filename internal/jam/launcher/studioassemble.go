package launcher

import (
	"github.com/aethons-tools/cove/internal/assemble"
	"github.com/aethons-tools/cove/internal/studio"
)

// studioEgress builds the assemble.Egress for a studio kit: Policy AND ceiling
// are the authored allow-list with Anthropic excluded (COV-208); Infra is the
// jam host so the cove can reach the broker. excluded is returned for surfacing.
func studioEgress(sk studio.StudioKit, jamHost string) (eg assemble.Egress, excluded []string) {
	ceiling, excl := studio.Ceiling(sk.Egress)
	infra := []string(nil)
	if jamHost != "" {
		infra = []string{jamHost}
	}
	return assemble.Egress{Policy: ceiling, Infra: infra}, excl
}
