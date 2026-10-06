package jam

import (
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
)

// Decision is the outcome of the three-question pipeline for one request.
type Decision struct {
	Dest     Destination
	NeedCred bool
	CredName string
	// Apply is how the broker applies the credential (with Dest.ApplySpec
	// when custom); the proxy reads it from here.
	Apply ApplyMethod
}

// EffectiveScope layers a grant's overrides over its role's scope. Each field is
// the override when set, else the role's. (Override replaces, never merges.)
func EffectiveScope(g Grant, r Role) Scope {
	s := r.Scope
	if g.Overrides != nil {
		if g.Overrides.Destinations != nil {
			s.Destinations = g.Overrides.Destinations
		}
		if g.Overrides.Addressing != nil {
			s.Addressing = g.Overrides.Addressing
		}
		if g.Overrides.Credentials != nil {
			s.Credentials = g.Overrides.Credentials
		}
	}
	return s
}

// CredentialFor is the credential name this scope injects for d: the scope's
// mapping when set, else the destination's default.
func (s Scope) CredentialFor(d Destination) string {
	if c := s.Credentials[d.Name]; c != "" {
		return c
	}
	return d.CredName
}

// Decide answers, for an actor and the effective scopes its grants resolve to,
// whether dest is permitted, and which credential to inject. It is additive
// across grants: a request passes iff SOME scope lists the destination. The
// credential comes from the authorizing scope; grants that authorize the
// destination but map it to different credentials deny rather than pick one.
// Repo reach is the injected credential's own scope, not broker policy.
// Fails closed.
func Decide(a Actor, scopes []Scope, dest Destination, now time.Time) (Decision, error) {
	if !a.Expiry.IsZero() && now.After(a.Expiry) {
		return Decision{}, fmt.Errorf("actor %q expired", a.ID)
	}
	cred, found := "", false
	for _, s := range scopes {
		if !slices.Contains(s.Destinations, dest.Name) {
			continue
		}
		c := s.CredentialFor(dest)
		if found && c != cred {
			return Decision{}, fmt.Errorf("actor %q: grants map destination %q to different credentials", a.ID, dest.Name)
		}
		cred, found = c, true
	}
	if !found {
		return Decision{}, fmt.Errorf("actor %q not authorized for destination %q", a.ID, dest.Name)
	}
	return Decision{Dest: dest, NeedCred: cred != "", CredName: cred, Apply: dest.Apply}, nil
}

// SendTarget is a resolved comms recipient: how Jam should deliver a send.
type SendTarget struct {
	Kind    string   // "human" (a user; the log's kind until slice 2) | "channel"
	Name    string   // the user's or channel's name
	UserID  ident.ID // the user (Kind=="human")
	Handle  string   // human @-mention handle (Kind=="human")
	Ref     string   // channel thread identifier (Kind=="channel")
	Project string   // the project whose grant authorized+resolved this target
}

// ErrSendDenied means no grant's addressing authorizes the target's form (403);
// it never reveals whether the target exists. ErrSendUnresolved means the target
// was authorized-in-form but is absent from the roster of every authorizing
// grant's project (404).
var (
	ErrSendDenied     = errors.New("comms: send target not authorized")
	ErrSendUnresolved = errors.New("comms: send target not found")
)

// parseTarget splits a send target. People are "user:<name|usr_id>";
// "human:<name>" is accepted as an alias (one release) and read as "user:".
func parseTarget(target string) (kind, ref string, ok bool) {
	k, n, found := strings.Cut(target, ":")
	if !found || n == "" {
		return "", "", false
	}
	switch k {
	case "user", "human":
		return "user", n, true
	case "channel":
		return k, n, true
	}
	return "", "", false
}

// normalizeGlob reads a pre-registry "human:" addressing glob as "user:".
func normalizeGlob(g string) string {
	if rest, ok := strings.CutPrefix(g, "human:"); ok {
		return "user:" + rest
	}
	return g
}

// anyAllowed reports whether some glob matches some of a target's forms (a
// person is "user:<name>" and "user:<usr_id>"; a channel "channel:<name>").
// An id form is matched only by that exact id, "user:*" or "*": a name glob
// never matches an id ("usr_…"), so it can't reach every member.
func anyAllowed(forms []string, globs []string) bool {
	for _, g := range globs {
		g = normalizeGlob(g)
		for _, f := range forms {
			if isIDForm(f) {
				if g == f || g == "user:*" || g == "*" {
					return true
				}
				continue
			}
			if ok, _ := path.Match(g, f); ok {
				return true
			}
		}
	}
	return false
}

// isIDForm reports whether a target form names a person by user id.
func isIDForm(f string) bool {
	ref, ok := strings.CutPrefix(f, "user:")
	if !ok {
		return false
	}
	id, err := ident.Parse(ref)
	return err == nil && id.Kind() == ident.User
}

// humanForms are the addressable forms of a roster person.
func humanForms(h Human) []string {
	forms := []string{"user:" + h.Name}
	if h.UserID != "" {
		forms = append(forms, "user:"+string(h.UserID))
	}
	return forms
}

func resolveInRoster(kind, ref string, r Roster) (SendTarget, []string, bool) {
	switch kind {
	case "user":
		for _, h := range r.Humans {
			if h.Name == ref || (h.UserID != "" && string(h.UserID) == ref) {
				return SendTarget{Kind: "human", Name: h.Name, UserID: h.UserID, Handle: h.Handle}, humanForms(h), true
			}
		}
	case "channel":
		for _, c := range r.Channels {
			if c.Name == ref {
				return SendTarget{Kind: "channel", Name: ref, Ref: c.Ref}, []string{"channel:" + ref}, true
			}
		}
	}
	return SendTarget{}, nil, false
}

// DecideSend authorizes actor a to send to target and resolves delivery. Live,
// additive across grants, per-grant existential, fail-closed. See ErrSendDenied
// / ErrSendUnresolved for the 403/404 split (authz checked before existence).
// A grant authorizes a target when one of its addressing globs matches the
// target as written, or (once resolved in its project) any form of whom it
// names — so an id-form glob covers the name and vice versa, and an id never
// widens what a name glob grants.
func DecideSend(a Actor, getRole func(project, role string) (Role, bool), getRoster func(project string) (Roster, bool), target string, now time.Time) (SendTarget, error) {
	if !a.Expiry.IsZero() && now.After(a.Expiry) {
		return SendTarget{}, fmt.Errorf("actor %q expired", a.ID)
	}
	kind, ref, ok := parseTarget(target)
	if !ok {
		return SendTarget{}, ErrSendDenied
	}
	written := kind + ":" + ref
	authorized := false
	for _, g := range a.Grants {
		role, ok := getRole(g.Project, g.Role)
		if !ok {
			continue
		}
		globs := EffectiveScope(g, role).Addressing
		roster, hasRoster := getRoster(g.Project)
		var st SendTarget
		var forms []string
		found := false
		if hasRoster {
			st, forms, found = resolveInRoster(kind, ref, roster)
		}
		if found && anyAllowed(forms, globs) {
			st.Project = g.Project
			return st, nil
		}
		if anyAllowed([]string{written}, globs) {
			authorized = true
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
			key := "user:" + h.Name
			if !seen[key] && anyAllowed(humanForms(h), globs) {
				seen[key] = true
				out = append(out, SendTarget{Kind: "human", Name: h.Name, UserID: h.UserID, Handle: h.Handle, Project: g.Project})
			}
		}
		for _, c := range roster.Channels {
			key := "channel:" + c.Name
			if !seen[key] && anyAllowed([]string{key}, globs) {
				seen[key] = true
				out = append(out, SendTarget{Kind: "channel", Name: c.Name, Ref: c.Ref, Project: g.Project})
			}
		}
	}
	return out
}
