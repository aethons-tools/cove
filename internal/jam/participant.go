package jam

import (
	"context"
	"net/http"

	"github.com/aethons-tools/cove/internal/ident"
)

// Participant is the identity behind a /me (participant intercom) request: the
// person authenticated by a browser OIDC session, i.e. the user bound to
// (Issuer, Subject). Users are Jam-wide, so Projects lists every project the
// user is a member of. This is a separate plane from Operator (admin
// identity); the two never mix.
type Participant struct {
	Issuer  string
	Subject string
	// UserID is the user bound to (Issuer, Subject).
	UserID ident.ID
	// Projects are the projects the user is a member of, in ListProjects
	// order. Never empty for a resolved participant.
	Projects []string
	// Name is the user's name.
	Name string
}

// ParticipantStore is the slice of Store that participant resolution reads. It
// is exported so the browser-auth gate can resolve a session to a participant.
type ParticipantStore interface {
	ListProjects() []string
	LookupName(k ident.Kind, name string) (ident.ID, bool)
	UserByOIDC(issuer, subject string) (User, bool)
	IsMember(project, user ident.ID) bool
}

// HasIdentity reports whether h is bound to the browser OIDC identity
// (issuer, subject). The empty issuer or subject never matches.
func (h Human) HasIdentity(issuer, subject string) bool {
	if issuer == "" || subject == "" {
		return false
	}
	for _, id := range h.Identity {
		if id.Issuer == issuer && id.Subject == subject {
			return true
		}
	}
	return false
}

// ParticipantByIdentity resolves the user bound to the browser OIDC identity
// (issuer, subject). Projects lists every project the user is a member of, in
// ListProjects order. The empty issuer or subject never matches, and an
// identity bound to no user, or to one who is a member of no project, resolves
// to nobody — fail closed, never guess an identity.
func ParticipantByIdentity(store ParticipantStore, issuer, subject string) (Participant, bool) {
	if issuer == "" || subject == "" {
		return Participant{}, false
	}
	u, ok := store.UserByOIDC(issuer, subject)
	if !ok {
		return Participant{}, false
	}
	p := Participant{Issuer: issuer, Subject: subject, UserID: u.ID, Name: u.Name}
	for _, name := range store.ListProjects() {
		if pid, ok := store.LookupName(ident.Project, name); ok && store.IsMember(pid, u.ID) {
			p.Projects = append(p.Projects, name)
		}
	}
	if len(p.Projects) == 0 {
		return Participant{}, false
	}
	return p, true
}

type participantCtxKey struct{}

// WithParticipant returns r carrying the authenticated participant, so /me
// handlers can attribute a request to the resolved roster person.
func WithParticipant(r *http.Request, p Participant) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), participantCtxKey{}, p))
}

// ParticipantFrom returns the authenticated participant from the request context
// and whether one was set.
func ParticipantFrom(r *http.Request) (Participant, bool) {
	p, ok := r.Context().Value(participantCtxKey{}).(Participant)
	return p, ok
}
