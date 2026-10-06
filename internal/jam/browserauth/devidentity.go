package browserauth

import (
	"net/http"

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
}

// DevIdentityStore is what DevIdentity reads: participant resolution plus the
// user registry.
type DevIdentityStore interface {
	jam.ParticipantStore
	LookupName(k ident.Kind, name string) (ident.ID, bool)
	GetUser(id ident.ID) (jam.User, bool)
}

func (d DevIdentity) user() (jam.User, bool) {
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

// OperatorLoopbackTrust is the /ui LoopbackTrust hook under a dev identity: a
// loopback request is the operator with the user's first login, or plain
// "local" when the user is unknown or has none.
func (d DevIdentity) OperatorLoopbackTrust() func(*http.Request) *http.Request {
	return func(r *http.Request) *http.Request {
		id := "local"
		if u, ok := d.user(); ok && len(u.Logins) > 0 {
			id = u.Logins[0]
		}
		return jam.WithOperator(r, jam.Operator{ID: id})
	}
}

// ParticipantSession wraps the /me Session hook next (nil = none): a loopback
// request is the user's participant, resolved through their first OIDC
// identity (as a real login would be). Off-loopback, or for a user without an
// OIDC identity, it defers to next.
func (d DevIdentity) ParticipantSession(next func(*http.Request) (*http.Request, SessionOutcome)) func(*http.Request) (*http.Request, SessionOutcome) {
	return func(r *http.Request) (*http.Request, SessionOutcome) {
		if jam.IsLoopbackRequest(r) {
			if u, ok := d.user(); ok && len(u.OIDC) > 0 {
				id := u.OIDC[0]
				if p, ok := jam.ParticipantByIdentity(d.Store, id.Issuer, id.Subject); ok {
					return jam.WithParticipant(r, p), SessionOK
				}
			}
		}
		if next == nil {
			return r, SessionNone
		}
		return next(r)
	}
}
