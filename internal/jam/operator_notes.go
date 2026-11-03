package jam

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/aethons-tools/cove/internal/ident"
)

// SetOperatorNotes replaces the operator notes of the object key names: a
// user or connection by name or id, a role as "<project>/<role>". Kits and
// the Jam itself keep theirs in their files (kit.yml, jam.yml), so they are
// not set here. The notes are written with the rest of the object unchanged,
// under the kind's write lock where it has one.
func SetOperatorNotes(store Store, key ConfigKey, notes string) error {
	var w ConfigWrite
	notFound := func() error { return &WriteError{Status: http.StatusNotFound, Msg: "no such " + key.Kind} }
	switch key.Kind {
	case ConfigKindUser:
		id, err := ResolveRegistryRef(store, ident.User, key.Name)
		if err != nil {
			return &WriteError{Status: http.StatusNotFound, Msg: err.Error()}
		}
		u, ok := store.GetUser(id)
		if !ok || u.Status == StatusRemoved {
			return notFound()
		}
		u.OperatorNotes = notes
		w.Users = []User{u}
	case ConfigKindConnection:
		id, err := ResolveRegistryRef(store, ident.Connection, key.Name)
		if err != nil {
			return &WriteError{Status: http.StatusNotFound, Msg: err.Error()}
		}
		c, ok := store.GetConnection(id)
		if !ok || c.Status == StatusRemoved {
			return notFound()
		}
		c.OperatorNotes = notes
		w.Connections = []Connection{c}
	case ConfigKindDestination:
		destMu.Lock()
		defer destMu.Unlock()
		d, ok := findDestination(store, key.Name)
		if !ok {
			return notFound()
		}
		d.OperatorNotes = notes
		w.Destinations = []Destination{d}
	case ConfigKindModelSpec:
		modelSpecMu.Lock()
		defer modelSpecMu.Unlock()
		ms, ok := store.GetModelSpec(key.Name)
		if !ok {
			return notFound()
		}
		ms.OperatorNotes = notes
		w.ModelSpecs = []ModelSpec{ms}
	case ConfigKindProject:
		p, ok := store.GetProject(key.Name)
		if !ok {
			return notFound()
		}
		p.OperatorNotes = notes
		w.Projects = []Project{p}
	case ConfigKindRole:
		pname, rname, _ := strings.Cut(key.Name, "/")
		return UpdateRole(store, pname, rname, func(r *Role) error { r.OperatorNotes = notes; return nil })
	default:
		return fmt.Errorf("%s operator notes are edited in its file", key.Kind)
	}
	return store.ApplyConfig(w)
}
