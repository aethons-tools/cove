package jam

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/aethons-tools/cove/internal/jam/sessionctx"
)

// destMu serializes destination create/update so the existence check and the
// write are one step (create-only and update-only stay true under races).
var destMu sync.Mutex

// MaxDestinationNote bounds Destination.Note: it rides in every granted
// session's always-on context.
const MaxDestinationNote = 300

// ValidateDestination checks a destination at write time — the rules shared by
// the JSON admin API, the UI and the config plan: a name, a default
// credential that is configured, env passing ValidateEnv, and endpoints
// passing endpointProblems. subdomains is whether this Jam has a
// destinations domain (serve config destinations:); without one, no endpoint
// may have a subdomain. Rules across destinations are clashProblems'.
// A refusal is a 400 WriteError naming every problem, each also in Fields
// at its admin API field path (destRefusal).
func ValidateDestination(d Destination, credExists func(string) bool, subdomains bool) error {
	return checkDestination(d, credExists, subdomains, nil)
}

// checkDestination is ValidateDestination plus, against all (the other
// destinations; d's own entry is skipped), clashProblems: every problem in
// one refusal.
func checkDestination(d Destination, credExists func(string) bool, subdomains bool, all []Destination) error {
	d.normalize()
	if d.LegacyOAuthBeta {
		return writeErr(http.StatusBadRequest, "oauth_beta was removed: the broker now adds the oauth-2025-04-20 anthropic-beta to every request carrying a subscription-pool credential — see docs/modules/ROOT/pages/usage/jam/pool.adoc (a non-pool subscription token takes a model-spec principal header rule instead)")
	}
	var ps []destProblem
	if d.Name == "" {
		ps = append(ps, destProblem{ep: -1, field: "name", msg: "name is required"})
	}
	if d.CredName != "" && !credExists(d.CredName) {
		ps = append(ps, destProblem{ep: -1, field: "credName", msg: fmt.Sprintf("credential %q does not resolve to a configured credential", d.CredName)})
	}
	if len(d.Note) > MaxDestinationNote {
		ps = append(ps, destProblem{ep: -1, field: "note", msg: fmt.Sprintf("note is %d bytes; at most %d", len(d.Note), MaxDestinationNote)})
	}
	if err := d.ValidateEnv(); err != nil {
		ps = append(ps, destProblem{ep: -1, field: "env", msg: err.Error()})
	}
	ps = append(ps, d.endpointProblems(true, subdomains)...)
	if all != nil {
		ps = append(ps, clashProblems(d, all)...)
	}
	return d.refusal(ps)
}

// destProblem is one reason a destination write is refused, at field — its
// admin API wire name — of the destination (ep < 0) or of its endpoint ep.
type destProblem struct {
	ep         int
	field, msg string
}

// refusal is the 400 WriteError for ps (nil when there are none): its message
// every problem, an endpoint's prefixed with the endpoint's name when d has
// several; its Fields each at its path (name, credName, env, endpoints,
// endpoints[1].route, endpoints[0].applySpec.template, ...).
func (d Destination) refusal(ps []destProblem) error {
	if len(ps) == 0 {
		return nil
	}
	we := &WriteError{Status: http.StatusBadRequest}
	msgs := make([]string, len(ps))
	for i, p := range ps {
		path, msg := p.field, p.msg
		if p.ep >= 0 {
			path = fmt.Sprintf("endpoints[%d].%s", p.ep, p.field)
			if n := d.Endpoints[p.ep].Name; len(d.Endpoints) > 1 && n != "" && p.field != "name" {
				msg = fmt.Sprintf("endpoint %q: %s", n, msg)
			}
		}
		we.Fields = append(we.Fields, sessionctx.FieldError{Field: path, Message: p.msg})
		msgs[i] = msg
	}
	we.Msg = strings.Join(msgs, "; ")
	return we
}

// endpointProblems checks d's endpoints: at least one and at most
// MaxEndpoints, each named (uniquely in d) with an upstream and a subdomain
// or route; a subdomain one valid DNS label (ValidSubdomain), and only when
// subdomains (this Jam has a destinations domain); no route on the Jam host
// claiming a path Jam serves on the cove listener (ReservedRoute: a
// subdomain never reaches those); and each endpoint's header specs,
// allow_paths and header logging. Without addresses, the count-of-one, the
// subdomain's domain, route and upstream rules are skipped: a backup is
// checked so (validateSnapshotContext), as it was before endpoints, so
// whatever a Jam stored imports (the domain is serve config, like
// credentials).
func (d Destination) endpointProblems(addresses, subdomains bool) []destProblem {
	var ps []destProblem
	add := func(ep int, field, format string, a ...any) {
		ps = append(ps, destProblem{ep: ep, field: field, msg: fmt.Sprintf(format, a...)})
	}
	switch {
	case addresses && len(d.Endpoints) == 0:
		add(-1, "endpoints", "an endpoint with a route and an upstream is required")
	case len(d.Endpoints) > MaxEndpoints:
		add(-1, "endpoints", "%d endpoints; at most %d", len(d.Endpoints), MaxEndpoints)
	}
	seen := map[string]bool{}
	for i, e := range d.Endpoints {
		switch {
		case e.Name == "":
			add(i, "name", "every endpoint needs a name")
		case seen[e.Name]:
			add(i, "name", "endpoint name %q is used twice", e.Name)
		}
		seen[e.Name] = true
		switch {
		case e.Subdomain != "" && ValidSubdomain(e.Subdomain) != nil:
			add(i, "subdomain", "%s", ValidSubdomain(e.Subdomain))
		case addresses && e.Subdomain != "" && !subdomains:
			add(i, "subdomain", "subdomain %q: this Jam has no destinations domain", e.Subdomain)
		}
		if addresses {
			switch {
			case e.Route == "" && e.Subdomain == "":
				add(i, "route", "a subdomain or a route is required")
			case e.Subdomain == "" && e.Route != "" && ReservedRoute(e.Route) != "":
				add(i, "route", "route %s claims %s, which Jam serves", e.Route, ReservedRoute(e.Route))
			}
			if e.Upstream == "" {
				add(i, "upstream", "upstream is required")
			}
		}
		for _, p := range e.headerSpecProblems() {
			add(i, p.field, "%s", p.msg)
		}
		if err := e.ValidateAllowPaths(); err != nil {
			add(i, "allowPaths", "%s", err)
		}
		for _, p := range e.headerLoggingProblems() {
			add(i, p.field, "%s", p.msg)
		}
	}
	return ps
}

// validateEndpoints is endpointProblems as an error (nil when none).
func (d Destination) validateEndpoints(addresses, subdomains bool) error {
	return d.refusal(d.endpointProblems(addresses, subdomains))
}

// clashProblems is each endpoint of d with the subdomain and route of an
// earlier one of d's or of another destination's in all (d's own entry there
// skipped): each address belongs to one endpoint in the whole Jam. An
// endpoint with no address is endpointProblems'.
func clashProblems(d Destination, all []Destination) []destProblem {
	type addr struct{ sub, route string }
	owner := map[addr]string{}
	for _, o := range all {
		if o.Name == d.Name {
			continue
		}
		for _, e := range o.Endpoints {
			owner[addr{e.Subdomain, e.Route}] = fmt.Sprintf("destination %q's endpoint %q", o.Name, e.Name)
		}
	}
	var ps []destProblem
	for i, e := range d.Endpoints {
		a := addr{e.Subdomain, e.Route}
		if a == (addr{}) {
			continue
		}
		if prev, ok := owner[a]; ok {
			field := "route"
			if e.Route == "" {
				field = "subdomain"
			}
			ps = append(ps, destProblem{ep: i, field: field, msg: fmt.Sprintf("%s is already %s", endpointAddr(e), prev)})
			continue
		}
		owner[a] = fmt.Sprintf("endpoint %q", e.Name)
	}
	return ps
}

// endpointAddr is e's address for a message: its subdomain and/or route.
func endpointAddr(e Endpoint) string {
	switch {
	case e.Subdomain == "":
		return "route " + e.Route
	case e.Route == "":
		return "subdomain " + e.Subdomain
	}
	return "subdomain " + e.Subdomain + " route " + e.Route
}

// findDestination returns the stored destination named name.
func findDestination(store Store, name string) (Destination, bool) {
	ds := store.ListDestinations()
	if i := slices.IndexFunc(ds, func(d Destination) bool { return d.Name == name }); i >= 0 {
		return ds[i], true
	}
	return Destination{}, false
}

func destinationExists(store Store, name string) bool {
	_, ok := findDestination(store, name)
	return ok
}

// CreateDestination validates (subdomains as ValidateDestination) and stores a
// new destination (a lone unnamed endpoint named after it); an existing name
// is a 409 WriteError.
func CreateDestination(store Store, d Destination, credExists func(string) bool, subdomains bool) error {
	d.Endpoints = slices.Clone(d.Endpoints)
	d.normalize()
	destMu.Lock()
	defer destMu.Unlock()
	if err := checkDestination(d, credExists, subdomains, store.ListDestinations()); err != nil {
		return err
	}
	if destinationExists(store, d.Name) {
		return writeErr(http.StatusConflict, "destination %q already exists", d.Name)
	}
	return store.AddDestination(d)
}

// UpdateDestination validates and replaces an existing destination (a lone
// unnamed endpoint named after it) — every
// field but its operator notes, which have their own write
// (SetOperatorNotes) and are kept; the name is the key. A missing one is a
// 404 WriteError; subdomains is as ValidateDestination's. The lookup and the
// write are one step under destMu, so a concurrent notes change can't be
// reverted.
func UpdateDestination(store Store, d Destination, credExists func(string) bool, subdomains bool) error {
	d.Endpoints = slices.Clone(d.Endpoints)
	d.normalize()
	destMu.Lock()
	defer destMu.Unlock()
	cur, ok := findDestination(store, d.Name)
	if !ok {
		if err := ValidateDestination(d, credExists, subdomains); err != nil {
			return err
		}
		return writeErr(http.StatusNotFound, "destination %q does not exist", d.Name)
	}
	d.OperatorNotes = cur.OperatorNotes
	if err := checkDestination(d, credExists, subdomains, store.ListDestinations()); err != nil {
		return err
	}
	return store.AddDestination(d)
}
