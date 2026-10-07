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

// ErrSendDenied means no grant's addressing authorizes the target's form (403);
// it never reveals whether the target exists. ErrSendUnresolved means the target
// was authorized-in-form but is absent from the roster of every authorizing
// grant's project (404).
var (
	ErrSendDenied     = errors.New("comms: send target not authorized")
	ErrSendUnresolved = errors.New("comms: send target not found")
)

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

// Target is one address a session may send to: a person ("user", by name),
// a room ("channel") or a session ("session", by label, or by id when its
// label is shared), in the project of the grant that allows it.
type Target struct {
	Kind    string // "user" | "channel" | "session"
	Name    string
	Project string
}

// ListTargets returns the actor's allowed-and-resolvable targets: the members,
// rooms and other live sessions of each grant's project its addressing allows
// (dedup by kind:name, grant then name order). An expired actor has none.
func ListTargets(store Store, a Actor, now time.Time) []Target {
	if !a.Expiry.IsZero() && now.After(a.Expiry) {
		return nil
	}
	seen := map[string]bool{}
	var out []Target
	for _, g := range a.Grants {
		role, ok := store.GetRole(g.Project, g.Role)
		if !ok {
			continue
		}
		globs := EffectiveScope(g, role).Addressing
		p, ok := store.GetProject(orDefaultProject(g.Project))
		if !ok {
			continue
		}
		for _, m := range MembersOf(store, p.ID) {
			key := "user:" + m.User.Name
			if !seen[key] && anyAllowed([]string{key, "user:" + string(m.User.ID)}, globs) {
				seen[key] = true
				out = append(out, Target{Kind: "user", Name: m.User.Name, Project: g.Project})
			}
		}
		for _, c := range store.ListChannels(p.ID, SourceRoom) {
			key := "channel:" + c.Key
			if !seen[key] && anyAllowed([]string{key}, globs) {
				seen[key] = true
				out = append(out, Target{Kind: "channel", Name: c.Key, Project: g.Project})
			}
		}
		for _, inst := range liveSessionsOf(store, p.Name) {
			if inst.ActorID == a.ID {
				continue
			}
			name := sessionAddressName(store, inst)
			key := "session:" + name
			if !seen[key] && anyAllowed([]string{"session:" + sessionLabel(inst), "session:" + inst.ActorID}, globs) {
				seen[key] = true
				out = append(out, Target{Kind: "session", Name: name, Project: g.Project})
			}
		}
	}
	return out
}

// liveSessionsOf lists the live sessions of the project named project, by id.
func liveSessionsOf(store Store, project string) []Instance {
	var out []Instance
	for _, inst := range store.ListInstances() {
		if inst.Phase != PhaseGone && orDefaultProject(inst.Project) == project {
			out = append(out, inst)
		}
	}
	slices.SortFunc(out, func(a, b Instance) int { return strings.Compare(a.ActorID, b.ActorID) })
	return out
}

// sessionAddressName is how a session:<name> address names inst: its label
// when no other live session of its project shares it, else its id.
func sessionAddressName(store Store, inst Instance) string {
	label := sessionLabel(inst)
	for _, other := range liveSessionsOf(store, orDefaultProject(inst.Project)) {
		if other.ActorID != inst.ActorID && sessionLabel(other) == label {
			return inst.ActorID
		}
	}
	return label
}
