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

// DestinationHostsFor is the sorted, unique hosts (<sub>.<domain>) of the
// subdomain endpoints of the destinations a's grants name, on this Jam's
// destinations domain; nil without one. Only granted destinations count: a
// studio resolves the subdomains its role may use, not every one.
func DestinationHostsFor(store Store, a Actor) []string {
	domain := DestinationsDomain()
	if domain == "" {
		return nil
	}
	inScope := map[string]bool{}
	for _, s := range ScopesFor(store, a) {
		for _, n := range s.Destinations {
			inScope[n] = true
		}
	}
	var hosts []string
	for _, d := range store.ListDestinations() {
		if !inScope[d.Name] {
			continue
		}
		for _, e := range d.Endpoints {
			if host, err := e.hostOn(domain); err == nil && host != "" {
				hosts = append(hosts, host)
			}
		}
	}
	slices.Sort(hosts)
	return slices.Compact(hosts)
}

// ConnectorFor assembles the client connector for an actor: the union of the
// ClientEnv of every destination in its effective scopes, plus git routing when
// one of their endpoints routes git, plus the model-spec its roles resolve to
// (ModelSpecFor) — so a cove re-reads its spec with every connector refresh.
// Endpoint placeholders and a git endpoint's host resolve on the destinations
// domain (DestinationsDomain). Unknown destination names contribute nothing.
// Two destinations setting one variable to different values, two different
// git routes, or an endpoint on a subdomain this Jam cannot name, are an
// error — fail closed rather than pick one.
func ConnectorFor(store Store, a Actor) (snippet.Connector, error) {
	domain := DestinationsDomain()
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
		// Re-checked here, not only at the admin API: env can reach the store by
		// config import or a direct write, and must never clobber the identity
		// token or smuggle a malformed key into a rendered snippet. A key under
		// the reserved JAM_ prefix (one stored before the jam rename reserved
		// it) is dropped, as modelspec.ProviderEnv drops one, not fatal to every
		// connector in scope: destination.env:<name> asks for its rename.
		d = withoutReservedEnv(d)
		if err := d.ValidateEnv(); err != nil {
			return snippet.Connector{}, fmt.Errorf("destination %q: %w", name, err)
		}
		env, err := d.clientEnv(domain)
		if err != nil {
			return snippet.Connector{}, fmt.Errorf("destination %q: %w", name, err)
		}
		for k, v := range env {
			if prev, seen := owner[k]; seen && c.Env[k] != v {
				return snippet.Connector{}, fmt.Errorf("destinations %q and %q set %s differently", prev, name, k)
			}
			if c.Env == nil {
				c.Env = map[string]string{}
			}
			c.Env[k], owner[k] = v, name
		}
		for _, e := range d.GitEndpoints() {
			host, err := e.hostOn(domain)
			if err != nil {
				return snippet.Connector{}, fmt.Errorf("destination %q: %w", name, err)
			}
			route := e.Route
			if route == "" { // an endpoint taking every path on its subdomain
				route = "/"
			}
			if c.GitRoute != "" && (c.GitRoute != route || c.GitHost != host) {
				return snippet.Connector{}, fmt.Errorf("more than one git destination in scope (%s, %s)", c.GitHost+c.GitRoute, host+route)
			}
			c.GitRoute, c.GitHost, c.GitTarget = route, host, snippet.GitTargetFor(e.Upstream)
		}
	}
	spec, err := ModelSpecFor(store, a)
	if err != nil {
		return snippet.Connector{}, err
	}
	c.ModelSpec = spec
	return c, nil
}

// ModelSpecFor resolves the model-spec an actor's agent runs under: the one
// every granted role resolves to (Role.ModelSpecName — unbound means
// DefaultModelSpec). nil, nil when the actor has no resolvable grant, or when
// its roles are unbound and DefaultModelSpec is not stored (never seeded: the
// harness keeps its built-in defaults). Roles resolving to different specs, or
// an explicit binding to a spec that no longer exists, are an error — fail
// closed rather than pick one. The spec carries names only, never a secret,
// and never its operator notes: it is delivered to the cove.
func ModelSpecFor(store Store, a Actor) (*ModelSpec, error) {
	n, explicit, by, err := modelSpecNameFor(store, a)
	if err != nil || n == "" {
		return nil, err
	}
	m, ok := store.GetModelSpec(n)
	if !ok {
		if !explicit {
			return nil, nil
		}
		return nil, missingModelSpecErr(n, by)
	}
	m.OperatorNotes = ""
	return &m, nil
}

// missingModelSpecErr is ModelSpecFor's error for an explicit binding (by
// project/role) to a model-spec n that does not exist.
func missingModelSpecErr(n, by string) error {
	return fmt.Errorf("model-spec %q bound by role %s does not exist", n, by)
}

// modelSpecNameFor is ModelSpecFor's name resolution: the one spec name every
// granted role resolves to ("" when none), whether a role binds it explicitly,
// and the first project/role resolving to it. Roles resolving to different
// specs are an error.
func modelSpecNameFor(store Store, a Actor) (name string, explicit bool, by string, err error) {
	byName := map[string]string{} // spec name → the first project/role resolving to it
	explicitly := map[string]bool{}
	for _, g := range a.Grants {
		r, ok := store.GetRole(g.Project, g.Role)
		if !ok {
			continue
		}
		n := r.ModelSpecName()
		if _, seen := byName[n]; !seen {
			byName[n] = ProjectName(store, g.Project) + "/" + r.Name
		}
		if r.ModelSpec != "" {
			explicitly[n] = true
		}
	}
	names := slices.Sorted(maps.Keys(byName))
	switch len(names) {
	case 0:
		return "", false, "", nil
	case 1:
	default:
		return "", false, "", fmt.Errorf("roles %s and %s resolve to different model-specs (%s, %s)", byName[names[0]], byName[names[1]], names[0], names[1])
	}
	n := names[0]
	return n, explicitly[n], byName[n], nil
}
