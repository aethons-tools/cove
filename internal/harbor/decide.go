package harbor

import (
	"fmt"
	"slices"
	"time"
)

// Decision is the outcome of the three-question pipeline for one request.
type Decision struct {
	Dest     Destination
	NeedCred bool
	CredName string
	Apply    ApplyMethod
}

// EffectiveScope layers a grant's overrides over its role's scope. Each field is
// the override when set, else the role's. (Override replaces, never merges.)
func EffectiveScope(g Grant, r Role) Scope {
	s := r.Scope
	if g.Overrides != nil {
		if g.Overrides.Destinations != nil {
			s.Destinations = g.Overrides.Destinations
		}
		if g.Overrides.Repos != nil {
			s.Repos = g.Overrides.Repos
		}
	}
	return s
}

// Decide answers, for an actor and the effective scopes its grants resolve to,
// whether (dest, ownerRepo) is permitted, and which credential to inject. It is
// additive across grants but per-grant existential: a request passes iff SOME
// single scope authorizes the whole (destination, repo) pair — so one grant's
// destination never recombines with another grant's repos. Fails closed.
func Decide(a Actor, scopes []Scope, dest Destination, ownerRepo string, now time.Time) (Decision, error) {
	if !a.Expiry.IsZero() && now.After(a.Expiry) {
		return Decision{}, fmt.Errorf("actor %q expired", a.ID)
	}
	if dest.RepoScoped && ownerRepo == "" {
		return Decision{}, fmt.Errorf("destination %q requires owner/repo", dest.Name)
	}
	for _, s := range scopes {
		if !slices.Contains(s.Destinations, dest.Name) {
			continue
		}
		if dest.RepoScoped && !repoAllowed(ownerRepo, s.Repos) {
			continue
		}
		return Decision{Dest: dest, NeedCred: dest.CredName != "", CredName: dest.CredName, Apply: dest.Apply}, nil
	}
	return Decision{}, fmt.Errorf("actor %q not authorized for destination %q", a.ID, dest.Name)
}
