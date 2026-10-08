package jam

import (
	"maps"
	"slices"
	"time"

	"github.com/aethons-tools/cove/internal/jam/sessionctx"
	"github.com/aethons-tools/cove/internal/studio"
)

// studioFacts gathers what a raised session can actually reach, for its
// session context's Studio layer: the destinations in its scopes (unknown names
// skipped, as ConnectorFor does), its effective egress (the role's policy, else
// the kit's ceiling, else unknown), and its message targets (by name — handles
// stay out, as GET /squawks/targets omits them). Only names, upstreams, env KEYS
// and notes leave here — never credentials or env values.
func studioFacts(store Store, a Actor, owner string, roleEgress *EgressPolicy, kitEgress []string, haveKit bool, now time.Time) sessionctx.StudioFacts {
	var f sessionctx.StudioFacts
	byName := map[string]Destination{}
	for _, d := range store.ListDestinations() {
		byName[d.Name] = d
	}
	inScope := map[string]bool{}
	for _, s := range ScopesFor(store, a) {
		for _, n := range s.Destinations {
			inScope[n] = true
		}
	}
	for _, n := range slices.Sorted(maps.Keys(inScope)) {
		d, ok := byName[n]
		if !ok {
			continue
		}
		f.Destinations = append(f.Destinations, sessionctx.StudioDestination{
			Name: d.Name, Upstream: d.Upstream, EnvKeys: slices.Sorted(maps.Keys(d.ClientEnv())), Git: d.GitRouted(), Note: d.Note,
		})
	}
	switch {
	case roleEgress != nil:
		f.Egress, f.EgressKnown = slices.Sorted(slices.Values(roleEgress.Domains)), true
	case haveKit:
		f.Egress, _ = studio.Ceiling(kitEgress)
		f.EgressKnown = true
	}
	for _, t := range ListTargets(store, a, now) {
		who := "project contact"
		switch {
		case t.Kind == "channel":
			who = "channel"
		case t.Kind == "session":
			who = "session"
		case t.Name == owner:
			who = "started you"
		}
		f.Targets = append(f.Targets, sessionctx.StudioTarget{Target: t.Kind + ":" + t.Name, Who: who})
	}
	return f
}
