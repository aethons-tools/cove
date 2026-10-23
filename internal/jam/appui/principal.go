package appui

import (
	"context"
	"net/http"

	"github.com/aethons-tools/cove/internal/jam"
)

// Principal is who a /app request acts as: a roster user, an admin, or both.
type Principal struct {
	User     *jam.Participant // roster member of ≥1 project, else nil
	Admin    bool
	Local    bool         // admin by loopback trust with no login
	Operator jam.Operator // set when Admin
}

// DisplayName is what the principal menu shows.
func (p Principal) DisplayName() string {
	switch {
	case p.User != nil:
		return p.User.Name
	case p.Local:
		return "Local admin"
	default:
		return p.Operator.ID
	}
}

type principalKey struct{}

// WithPrincipal carries p on r, and the legacy per-plane identities the UIs'
// handlers read (jam.ParticipantFrom, jam.OperatorID).
func WithPrincipal(r *http.Request, p Principal) *http.Request {
	if p.User != nil {
		r = jam.WithParticipant(r, *p.User)
	}
	if p.Admin {
		r = jam.WithOperator(r, p.Operator)
	}
	return r.WithContext(context.WithValue(r.Context(), principalKey{}, p))
}

// PrincipalFrom returns the principal the gate resolved for r.
func PrincipalFrom(r *http.Request) (Principal, bool) {
	p, ok := r.Context().Value(principalKey{}).(Principal)
	return p, ok
}
