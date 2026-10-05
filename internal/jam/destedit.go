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
// or custom with a valid spec, and env must pass ValidateEnv.
// Refusals are 400 WriteErrors.
func ValidateDestination(d Destination, credExists func(string) bool) error {
	if d.Name == "" || d.Route == "" || d.Upstream == "" {
		return writeErr(http.StatusBadRequest, "name, route and upstream are required")
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
	return nil
}

func destinationExists(store Store, name string) bool {
	return slices.ContainsFunc(store.ListDestinations(), func(d Destination) bool { return d.Name == name })
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
	if err := ValidateDestination(d, credExists); err != nil {
		return err
	}
	destMu.Lock()
	defer destMu.Unlock()
	if !destinationExists(store, d.Name) {
		return writeErr(http.StatusNotFound, "destination %q does not exist", d.Name)
	}
	return store.AddDestination(d)
}
