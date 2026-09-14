package harbor

import (
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
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
		if g.Overrides.Addressing != nil {
			s.Addressing = g.Overrides.Addressing
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

// SendTarget is a resolved comms recipient: how harbor should deliver a send.
type SendTarget struct {
	Kind    string // "human" | "channel"
	Name    string // roster-local name
	Handle  string // human @-mention handle (Kind=="human")
	Ref     string // channel thread identifier (Kind=="channel")
	Project string // the project whose grant authorized+resolved this target
}

// ErrSendDenied means no grant's addressing authorizes the target's form (403);
// it never reveals whether the target exists. ErrSendUnresolved means the target
// was authorized-in-form but is absent from the roster of every authorizing
// grant's project (404).
var (
	ErrSendDenied     = errors.New("comms: send target not authorized")
	ErrSendUnresolved = errors.New("comms: send target not found")
)

func parseTarget(target string) (kind, name string, ok bool) {
	k, n, found := strings.Cut(target, ":")
	if !found || n == "" || (k != "human" && k != "channel") {
		return "", "", false
	}
	return k, n, true
}

func targetAllowed(target string, globs []string) bool {
	for _, g := range globs {
		if ok, _ := path.Match(g, target); ok {
			return true
		}
	}
	return false
}

func resolveInRoster(kind, name string, r Roster) (SendTarget, bool) {
	switch kind {
	case "human":
		for _, h := range r.Humans {
			if h.Name == name {
				return SendTarget{Kind: "human", Name: name, Handle: h.Handle}, true
			}
		}
	case "channel":
		for _, c := range r.Channels {
			if c.Name == name {
				return SendTarget{Kind: "channel", Name: name, Ref: c.Ref}, true
			}
		}
	}
	return SendTarget{}, false
}

// DecideSend authorizes actor a to send to target and resolves delivery. Live,
// additive across grants, per-grant existential, fail-closed. See ErrSendDenied
// / ErrSendUnresolved for the 403/404 split (authz checked before existence).
func DecideSend(a Actor, getRole func(project, role string) (Role, bool), getRoster func(project string) (Roster, bool), target string, now time.Time) (SendTarget, error) {
	if !a.Expiry.IsZero() && now.After(a.Expiry) {
		return SendTarget{}, fmt.Errorf("actor %q expired", a.ID)
	}
	kind, name, ok := parseTarget(target)
	if !ok {
		return SendTarget{}, ErrSendDenied
	}
	authorized := false
	for _, g := range a.Grants {
		role, ok := getRole(g.Project, g.Role)
		if !ok {
			continue
		}
		if !targetAllowed(target, EffectiveScope(g, role).Addressing) {
			continue
		}
		authorized = true
		roster, ok := getRoster(g.Project)
		if !ok {
			continue
		}
		if st, ok := resolveInRoster(kind, name, roster); ok {
			st.Project = g.Project
			return st, nil
		}
	}
	if authorized {
		return SendTarget{}, ErrSendUnresolved
	}
	return SendTarget{}, ErrSendDenied
}

// ListTargets returns the actor's authorized-and-resolvable targets (dedup by
// kind:name). Order is grant-then-roster order.
func ListTargets(a Actor, getRole func(project, role string) (Role, bool), getRoster func(project string) (Roster, bool), now time.Time) []SendTarget {
	if !a.Expiry.IsZero() && now.After(a.Expiry) {
		return nil
	}
	seen := map[string]bool{}
	var out []SendTarget
	for _, g := range a.Grants {
		role, ok := getRole(g.Project, g.Role)
		if !ok {
			continue
		}
		globs := EffectiveScope(g, role).Addressing
		roster, ok := getRoster(g.Project)
		if !ok {
			continue
		}
		for _, h := range roster.Humans {
			key := "human:" + h.Name
			if !seen[key] && targetAllowed(key, globs) {
				seen[key] = true
				out = append(out, SendTarget{Kind: "human", Name: h.Name, Handle: h.Handle, Project: g.Project})
			}
		}
		for _, c := range roster.Channels {
			key := "channel:" + c.Name
			if !seen[key] && targetAllowed(key, globs) {
				seen[key] = true
				out = append(out, SendTarget{Kind: "channel", Name: c.Name, Ref: c.Ref, Project: g.Project})
			}
		}
	}
	return out
}
