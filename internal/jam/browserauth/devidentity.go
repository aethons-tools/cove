package browserauth

import (
	"net/http"

	"github.com/aethons-tools/cove/internal/jam"
)

// DevIdentity makes LOOPBACK browser requests act as one roster human with no
// login — for UI development only (serve's `dev-identity`). It never applies
// off-loopback, and it composes inside Gate, so the loopback Host check still
// runs first. The human is resolved per request, so roster edits apply live.
type DevIdentity struct {
	Store   jam.ParticipantStore
	Project string
	Human   string
}

func (d DevIdentity) human() (jam.Human, bool) {
	rr, ok := d.Store.GetRoster(d.Project)
	if !ok {
		return jam.Human{}, false
	}
	for _, h := range rr.Humans {
		if h.Name == d.Human {
			return h, true
		}
	}
	return jam.Human{}, false
}

// OperatorLoopbackTrust is the /ui LoopbackTrust hook under a dev identity: a
// loopback request is the operator with the human's linked login, or plain
// "local" when the human is unknown or unlinked.
func (d DevIdentity) OperatorLoopbackTrust() func(*http.Request) *http.Request {
	return func(r *http.Request) *http.Request {
		id := "local"
		if h, ok := d.human(); ok && h.Login != "" {
			id = h.Login
		}
		return jam.WithOperator(r, jam.Operator{ID: id})
	}
}

// ParticipantSession wraps the /me Session hook next (nil = none): a loopback
// request is the human's participant, resolved through their first roster OIDC
// identity (as a real login would be). Off-loopback, or for a human without an
// OIDC identity, it defers to next.
func (d DevIdentity) ParticipantSession(next func(*http.Request) (*http.Request, SessionOutcome)) func(*http.Request) (*http.Request, SessionOutcome) {
	return func(r *http.Request) (*http.Request, SessionOutcome) {
		if jam.IsLoopbackRequest(r) {
			if h, ok := d.human(); ok && len(h.Identity) > 0 {
				id := h.Identity[0]
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
