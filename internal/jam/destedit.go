package jam

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sync"
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
// passing validateEndpoints. subdomains is whether this Jam has a
// destinations domain (serve config destinations:); without one, no endpoint
// may have a subdomain. Rules across destinations are checkEndpointClashes'.
// Refusals are 400 WriteErrors.
func ValidateDestination(d Destination, credExists func(string) bool, subdomains bool) error {
	d.normalize()
	if d.Name == "" || len(d.Endpoints) == 0 {
		return writeErr(http.StatusBadRequest, "name, route and upstream are required")
	}
	if d.LegacyOAuthBeta {
		return writeErr(http.StatusBadRequest, "oauth_beta was removed: the broker now adds the oauth-2025-04-20 anthropic-beta to every request carrying a subscription-pool credential — see docs/modules/ROOT/pages/usage/jam/pool.adoc (a non-pool subscription token takes a model-spec principal header rule instead)")
	}
	if d.CredName != "" && !credExists(d.CredName) {
		return writeErr(http.StatusBadRequest, "credential %q does not resolve to a configured credential", d.CredName)
	}
	if len(d.Note) > MaxDestinationNote {
		return writeErr(http.StatusBadRequest, "note is %d bytes; at most %d", len(d.Note), MaxDestinationNote)
	}
	if err := d.ValidateEnv(); err != nil {
		return writeErr(http.StatusBadRequest, "%s", err.Error())
	}
	if err := d.validateEndpoints(true, subdomains); err != nil {
		return writeErr(http.StatusBadRequest, "%s", err.Error())
	}
	return nil
}

// validateEndpoints checks d's endpoints: at most MaxEndpoints, each named
// (uniquely in d) with an upstream and a subdomain or route; a subdomain one
// valid DNS label (ValidSubdomain), and only when subdomains (this Jam has a
// destinations domain); no route on the Jam host claiming a path Jam serves
// on the cove listener (ReservedRoute: a subdomain never reaches those); and
// each endpoint's header specs, allow_paths and header logging. An error
// names the endpoint when d has more than one. Without addresses, the
// subdomain's domain, route and upstream rules are skipped: a backup is
// checked so (validateSnapshotContext), as it was before endpoints, so
// whatever a Jam stored imports (the domain is serve config, like
// credentials).
func (d Destination) validateEndpoints(addresses, subdomains bool) error {
	if len(d.Endpoints) > MaxEndpoints {
		return fmt.Errorf("%d endpoints; at most %d", len(d.Endpoints), MaxEndpoints)
	}
	seen := map[string]bool{}
	for _, e := range d.Endpoints {
		where := ""
		if len(d.Endpoints) > 1 {
			where = fmt.Sprintf("endpoint %q: ", e.Name)
		}
		switch {
		case e.Name == "":
			return errors.New("every endpoint needs a name")
		case seen[e.Name]:
			return fmt.Errorf("endpoint name %q is used twice", e.Name)
		case e.Subdomain != "" && ValidSubdomain(e.Subdomain) != nil:
			return fmt.Errorf("%s%w", where, ValidSubdomain(e.Subdomain))
		case !addresses:
		case e.Subdomain != "" && !subdomains:
			return fmt.Errorf("%ssubdomain %q: this Jam has no destinations domain", where, e.Subdomain)
		case e.Subdomain == "" && e.Route == "":
			return fmt.Errorf("%sa subdomain or a route is required", where)
		case e.Upstream == "":
			return fmt.Errorf("%supstream is required", where)
		case e.Subdomain == "" && ReservedRoute(e.Route) != "":
			return fmt.Errorf("%sroute %s claims %s, which Jam serves", where, e.Route, ReservedRoute(e.Route))
		}
		seen[e.Name] = true
		for _, check := range []func() error{e.validateHeaderSpecs, e.ValidateAllowPaths, e.ValidateHeaderLogging} {
			if err := check(); err != nil {
				return fmt.Errorf("%s%w", where, err)
			}
		}
	}
	return nil
}

// checkEndpointClashes refuses d when two of its endpoints, or one of its and
// one of another destination in all (d's own entry there skipped), have the
// same subdomain and route: each address belongs to one endpoint in the
// whole Jam. A 400 WriteError.
func checkEndpointClashes(d Destination, all []Destination) error {
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
	for _, e := range d.Endpoints {
		a := addr{e.Subdomain, e.Route}
		if prev, ok := owner[a]; ok {
			return writeErr(http.StatusBadRequest, "endpoint %q: %s is already %s", e.Name, endpointAddr(e), prev)
		}
		owner[a] = fmt.Sprintf("endpoint %q", e.Name)
	}
	return nil
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
// new destination; an existing name is a 409 WriteError.
func CreateDestination(store Store, d Destination, credExists func(string) bool, subdomains bool) error {
	if err := ValidateDestination(d, credExists, subdomains); err != nil {
		return err
	}
	destMu.Lock()
	defer destMu.Unlock()
	if destinationExists(store, d.Name) {
		return writeErr(http.StatusConflict, "destination %q already exists", d.Name)
	}
	if err := checkEndpointClashes(d, store.ListDestinations()); err != nil {
		return err
	}
	return store.AddDestination(d)
}

// UpdateDestination validates and replaces an existing destination (every
// field; the name is the key); a missing one is a 404 WriteError. subdomains
// is as ValidateDestination's.
func UpdateDestination(store Store, d Destination, credExists func(string) bool, subdomains bool) error {
	return updateDestination(store, d, credExists, subdomains, false)
}

// UpdateDestinationKeepSpecs is UpdateDestination for an editor that can't
// edit header specs, operator notes or more than one endpoint (the admin UI
// form): d's one endpoint replaces the stored first endpoint's fields but its
// name and subdomain, the stored destination's other endpoints are kept, and
// where an endpoint keeps identity_in / apply "custom" with no spec, its
// stored spec is kept; the stored operator notes always are. The lookup and
// the write are one step under destMu, so a concurrent change can't be
// reverted.
func UpdateDestinationKeepSpecs(store Store, d Destination, credExists func(string) bool, subdomains bool) error {
	return updateDestination(store, d, credExists, subdomains, true)
}

func updateDestination(store Store, d Destination, credExists func(string) bool, subdomains, keepSpecs bool) error {
	destMu.Lock()
	defer destMu.Unlock()
	cur, ok := findDestination(store, d.Name)
	if !ok {
		if err := ValidateDestination(d, credExists, subdomains); err != nil {
			return err
		}
		return writeErr(http.StatusNotFound, "destination %q does not exist", d.Name)
	}
	if keepSpecs {
		d.Endpoints = slices.Clone(d.Endpoints)
		if len(d.Endpoints) == 1 && len(cur.Endpoints) > 0 {
			d.Endpoints[0].Name, d.Endpoints[0].Subdomain = cur.Endpoints[0].Name, cur.Endpoints[0].Subdomain
			d.Endpoints = append(d.Endpoints, cur.Endpoints[1:]...)
		}
		for i, e := range d.Endpoints {
			c, ok := cur.Endpoint(e.Name)
			if !ok {
				continue
			}
			if e.IdentityIn == ApplyCustom && e.IdentityInSpec == nil && c.IdentityIn == ApplyCustom {
				d.Endpoints[i].IdentityInSpec = c.IdentityInSpec
			}
			if e.Apply == ApplyCustom && e.ApplySpec == nil && c.Apply == ApplyCustom {
				d.Endpoints[i].ApplySpec = c.ApplySpec
			}
		}
		d.OperatorNotes = cur.OperatorNotes
	}
	if err := ValidateDestination(d, credExists, subdomains); err != nil {
		return err
	}
	if err := checkEndpointClashes(d, store.ListDestinations()); err != nil {
		return err
	}
	return store.AddDestination(d)
}
