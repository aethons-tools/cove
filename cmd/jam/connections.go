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
// existing connection of kind (even renamed) read-only: absent, or with a
// different credential, is an error — it never creates or edits one. Fails closed: an unknown
// connection, another kind, or a credential serve doesn't demand is an error.
func resolveServeConnection(st jam.Store, field, kind, name, legacyCred string, demanded map[string]credSpec) (jam.Connection, string, error) {
	var c jam.Connection
	if name == "" {
		var ok bool
		if c, ok = st.ConnectionOfKind(kind); !ok {
			return jam.Connection{}, "", fmt.Errorf("%s: no %s connection (the deprecated *-token-cred no longer creates one; add connections/%s.yml to the config repo, or set %s.connection)", field, kind, kind, field)
		}
		if c.CredName != legacyCred {
			return jam.Connection{}, "", fmt.Errorf("%s: the %s connection's credential is %q, not %q (set it in the config repo, or set %s.connection)", field, kind, c.CredName, legacyCred, field)
		}
		name = c.Name
	} else {
		id, ok := st.LookupName(ident.Connection, name)
		if !ok {
			return jam.Connection{}, "", fmt.Errorf("%s.connection: no connection %q (add it to the config repo: connections/%s.yml)", field, name, name)
		}
		c, _ = st.GetConnection(id)
	}
	if c.Kind != kind {
		return jam.Connection{}, "", fmt.Errorf("%s.connection: %q is a %s connection, want %s", field, name, c.Kind, kind)
	}
	if c.CredName == "" {
		return jam.Connection{}, "", fmt.Errorf("%s.connection: %q has no credential (set its cred in the config repo: connections/%s.yml)", field, name, name)
	}
	if _, ok := demanded[c.CredName]; !ok {
		return jam.Connection{}, "", fmt.Errorf("%s.connection: %q's credential %q is not a demanded credential", field, name, c.CredName)
	}
	return c, c.CredName, nil
}
