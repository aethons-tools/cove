package jam

import (
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
// the JSON admin API and the UI: name, route and upstream are required, a
// default credential must be configured, identity_in/apply must be a preset
// or custom with a valid spec, env must pass ValidateEnv, and allow_paths
// ValidateAllowPaths.
// Refusals are 400 WriteErrors.
func ValidateDestination(d Destination, credExists func(string) bool) error {
	if d.Name == "" || d.Route == "" || d.Upstream == "" {
		return writeErr(http.StatusBadRequest, "name, route and upstream are required")
	}
	if d.LegacyOAuthBeta {
		return writeErr(http.StatusBadRequest, "oauth_beta was removed: the broker now adds the oauth-2025-04-20 anthropic-beta to every request carrying a subscription-pool credential — see docs/usage/jam/pool.md (a non-pool subscription token takes a model-spec principal header rule instead)")
	}
	if d.CredName != "" && !credExists(d.CredName) {
		return writeErr(http.StatusBadRequest, "credential %q does not resolve to a configured credential", d.CredName)
	}
	if len(d.Note) > MaxDestinationNote {
		return writeErr(http.StatusBadRequest, "note is %d bytes; at most %d", len(d.Note), MaxDestinationNote)
	}
	if err := d.validateHeaderSpecs(); err != nil {
		return writeErr(http.StatusBadRequest, "%s", err.Error())
	}
	if err := d.ValidateEnv(); err != nil {
		return writeErr(http.StatusBadRequest, "%s", err.Error())
	}
	if err := d.ValidateAllowPaths(); err != nil {
		return writeErr(http.StatusBadRequest, "%s", err.Error())
	}
	return nil
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

// CreateDestination validates and stores a new destination; an existing name
// is a 409 WriteError.
func CreateDestination(store Store, d Destination, credExists func(string) bool) error {
	if err := ValidateDestination(d, credExists); err != nil {
		return err
	}
	destMu.Lock()
	defer destMu.Unlock()
	if destinationExists(store, d.Name) {
		return writeErr(http.StatusConflict, "destination %q already exists", d.Name)
	}
	return store.AddDestination(d)
}

// UpdateDestination validates and replaces an existing destination (every
// field; the name is the key); a missing one is a 404 WriteError.
func UpdateDestination(store Store, d Destination, credExists func(string) bool) error {
	return updateDestination(store, d, credExists, false)
}

// UpdateDestinationKeepSpecs is UpdateDestination for an editor that can't
// edit header specs (the admin UI form): where d keeps identity_in / apply
// "custom" with no spec, the stored spec is kept. The lookup and the write are
// one step under destMu, so a concurrent spec change can't be reverted.
func UpdateDestinationKeepSpecs(store Store, d Destination, credExists func(string) bool) error {
	return updateDestination(store, d, credExists, true)
}

func updateDestination(store Store, d Destination, credExists func(string) bool, keepSpecs bool) error {
	destMu.Lock()
	defer destMu.Unlock()
	cur, ok := findDestination(store, d.Name)
	if !ok {
		if err := ValidateDestination(d, credExists); err != nil {
			return err
		}
		return writeErr(http.StatusNotFound, "destination %q does not exist", d.Name)
	}
	if keepSpecs {
		if d.IdentityIn == ApplyCustom && d.IdentityInSpec == nil && cur.IdentityIn == ApplyCustom {
			d.IdentityInSpec = cur.IdentityInSpec
		}
		if d.Apply == ApplyCustom && d.ApplySpec == nil && cur.Apply == ApplyCustom {
			d.ApplySpec = cur.ApplySpec
		}
	}
	if err := ValidateDestination(d, credExists); err != nil {
		return err
	}
	return store.AddDestination(d)
}
