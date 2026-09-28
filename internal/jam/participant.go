package jam

import (
	"context"
	"net/http"
)

// Participant is the identity behind a /me (participant intercom) request: the
// person authenticated by a browser OIDC session and matched to the roster by
// (Issuer, Subject). A participant is a *global* person — the same subject bound
// in several projects is the same person — so Projects lists every project whose
// roster binds this identity. This is a separate plane from Operator (admin
// identity); the two never mix.
type Participant struct {
	Issuer  string
	Subject string
	// Projects are the projects whose roster binds (Issuer, Subject), in
	// ListProjects order. Never empty for a resolved participant.
	Projects []string
	// Name is a display name from the first matched roster Human (best-effort).
	// Handles may differ per project; the send path resolves the outgoing ref
	// per target rather than trusting this.
	Name string
}

// ParticipantStore is the slice of Store that participant resolution reads. It
// is exported so the browser-auth gate can resolve a session to a participant.
type ParticipantStore interface {
	ListProjects() []string
	GetRoster(project string) (Roster, bool)
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

// ParticipantByIdentity resolves the person bound to the browser OIDC identity
// (issuer, subject) across all projects. A participant is authenticated if the
// identity is bound in ANY project roster; Projects lists every such project in
// ListProjects order (global person). The empty issuer or subject never matches,
// and an identity bound in no roster resolves to nobody — fail closed, never
// guess an identity.
func ParticipantByIdentity(store ParticipantStore, issuer, subject string) (Participant, bool) {
	if issuer == "" || subject == "" {
		return Participant{}, false
	}
	p := Participant{Issuer: issuer, Subject: subject}
	for _, project := range store.ListProjects() {
		rr, ok := store.GetRoster(project)
		if !ok {
			continue
		}
		for _, h := range rr.Humans {
			if h.HasIdentity(issuer, subject) {
				p.Projects = append(p.Projects, project)
				if p.Name == "" {
					p.Name = h.Name
				}
				break // count each project once
			}
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
