package main

import (
	"fmt"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
)

// resolveServeConnection resolves the connection a serve-config block
// (field, e.g. "runtime.requisitioner") uses for a service of kind, and the
// demanded credential it authenticates with. name is the block's
// `connection`; legacyCred its deprecated `*-token-cred`, which binds the
// implicit connection named kind — created when absent, its credential set
// when it has none. Fails closed: an unknown connection, another kind, or a
// credential serve doesn't demand is an error.
func resolveServeConnection(st jam.Store, field, kind, name, legacyCred string, demanded map[string]credSpec) (jam.Connection, string, error) {
	var c jam.Connection
	if name == "" {
		id, ok := st.LookupName(ident.Connection, kind)
		if !ok {
			created, err := st.CreateConnection(jam.Connection{Kind: kind, Name: kind, CredName: legacyCred})
			if err != nil {
				return jam.Connection{}, "", fmt.Errorf("%s: create the %s connection: %w", field, kind, err)
			}
			return created, legacyCred, nil
		}
		c, _ = st.GetConnection(id)
		if c.CredName == "" {
			if err := st.SetConnectionCred(id, legacyCred); err != nil {
				return jam.Connection{}, "", fmt.Errorf("%s: bind the %s connection's credential: %w", field, kind, err)
			}
			c.CredName = legacyCred
		}
		name = c.Name
	} else {
		id, ok := st.LookupName(ident.Connection, name)
		if !ok {
			return jam.Connection{}, "", fmt.Errorf("%s.connection: no connection %q (add it with `at-jam connection add --kind %s --name %s --cred <credential>`)", field, name, kind, name)
		}
		c, _ = st.GetConnection(id)
	}
	if c.Kind != kind {
		return jam.Connection{}, "", fmt.Errorf("%s.connection: %q is a %s connection, want %s", field, name, c.Kind, kind)
	}
	if c.CredName == "" {
		return jam.Connection{}, "", fmt.Errorf("%s.connection: %q has no credential (set one with `at-jam connection cred %s <credential>`)", field, name, name)
	}
	if _, ok := demanded[c.CredName]; !ok {
		return jam.Connection{}, "", fmt.Errorf("%s.connection: %q's credential %q is not a demanded credential", field, name, c.CredName)
	}
	return c, c.CredName, nil
}
