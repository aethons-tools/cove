package browserauth

import (
	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
)

// DevIdentity makes LOOPBACK browser requests act as one user with no login —
// for UI development only (serve's `dev-identity`). It never applies
// off-loopback, and it composes inside Gate, so the loopback Host check still
// runs first. The user is resolved per request, so registry edits apply live.
type DevIdentity struct {
	Store DevIdentityStore
	User  string // the user's name or id
	// LegacyProject/LegacyHuman are the pre-registry {project, human} form:
	// the user that roster human became (legacy_human_aliases), exactly —
	// never whoever now holds the name.
	LegacyProject, LegacyHuman string
}

// DevIdentityStore is what DevIdentity reads: participant resolution plus the
// user registry.
type DevIdentityStore interface {
	jam.ParticipantStore
	GetUser(id ident.ID) (jam.User, bool)
	LegacyHumanAlias(project, name string) (ident.ID, bool)
}

func (d DevIdentity) user() (jam.User, bool) {
	if d.LegacyHuman != "" {
		id, ok := d.Store.LegacyHumanAlias(d.LegacyProject, d.LegacyHuman)
		if !ok {
			return jam.User{}, false
		}
		u, ok := d.Store.GetUser(id)
		return u, ok && u.Status == jam.StatusLive
	}
	id := ident.ID(d.User)
	if _, err := ident.Parse(d.User); err != nil {
		var ok bool
		if id, ok = d.Store.LookupName(ident.User, d.User); !ok {
			return jam.User{}, false
		}
	}
	u, ok := d.Store.GetUser(id)
	return u, ok && u.Status == jam.StatusLive
}

// Participant is the dev user's participant, resolved through their first
// OIDC identity as a real login would be.
func (d DevIdentity) Participant() (jam.Participant, bool) {
	u, ok := d.user()
	if !ok || len(u.OIDC) == 0 {
		return jam.Participant{}, false
	}
	return jam.ParticipantByIdentity(d.Store, u.OIDC[0].Issuer, u.OIDC[0].Subject)
}

// OperatorID is the dev user's first login, else "local".
func (d DevIdentity) OperatorID() string {
	if u, ok := d.user(); ok && len(u.Logins) > 0 {
		return u.Logins[0]
	}
	return "local"
}
