package jam

import (
	"fmt"
	"maps"
	"slices"

	"github.com/aethons-tools/cove/internal/jam/snippet"
)

// ScopesFor turns an actor's grants into their effective scopes, skipping any
// grant whose role no longer exists (fail-closed: a deleted role stops
// authorizing). An actor with no resolvable grant yields nil.
func ScopesFor(store Store, a Actor) []Scope {
	var scopes []Scope
	for _, g := range a.Grants {
		r, ok := store.GetRole(g.Project, g.Role)
		if !ok {
			continue
		}
		scopes = append(scopes, EffectiveScope(g, r))
	}
	return scopes
}

// ConnectorFor assembles the client connector for an actor: the union of the
// ClientEnv of every destination in its effective scopes, plus git routing when
// one of them routes git. Unknown destination names contribute nothing. Two
// destinations setting one variable to different values, or two different git
// routes, are an error — fail closed rather than pick one.
func ConnectorFor(store Store, a Actor) (snippet.Connector, error) {
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
	var c snippet.Connector
	owner := map[string]string{} // env key → destination that set it
	for _, name := range slices.Sorted(maps.Keys(inScope)) {
		d, ok := byName[name]
		if !ok {
			continue
		}
		for k, v := range d.ClientEnv() {
			if prev, seen := owner[k]; seen && c.Env[k] != v {
				return snippet.Connector{}, fmt.Errorf("destinations %q and %q set %s differently", prev, name, k)
			}
			if c.Env == nil {
				c.Env = map[string]string{}
			}
			c.Env[k], owner[k] = v, name
		}
		if d.GitRouted() {
			if c.GitRoute != "" && c.GitRoute != d.Route {
				return snippet.Connector{}, fmt.Errorf("more than one git destination in scope (%s, %s)", c.GitRoute, d.Route)
			}
			c.GitRoute = d.Route
		}
	}
	return c, nil
}
