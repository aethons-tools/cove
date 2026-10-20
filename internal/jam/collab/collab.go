// Package collab decides who may reach a running studio (studio
// collaboration, docs/usage/jam/collaboration.md). Rights are independent;
// the first matching rule wins and every check is live.
package collab

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
)

// Right is one independently granted kind of studio access.
type Right string

const (
	Preview Right = "preview"
	SSH     Right = "ssh"

	// DefaultGrant is a share grant's lifetime when the caller names none;
	// MaxGrant is the longest a grant may run.
	DefaultGrant = 8 * time.Hour
	MaxGrant     = 7 * 24 * time.Hour
)

// Principal is who is asking.
type Principal struct {
	Operator string   // operator id when the caller is on the operator plane; "" if not
	UserID   ident.ID // roster user, when known
}

// Reason names the rule that allowed an access.
type Reason string

// Decide reports whether p may exercise right r on inst, and which rule says
// so: operator, the personal session's requester, the role's collaboration
// policy for project members, or a live share grant.
func Decide(store jam.Store, now time.Time, p Principal, inst jam.Instance, r Right) (Reason, bool) {
	if p.Operator != "" {
		return "operator", true
	}
	if p.UserID == "" {
		return "", false
	}
	if inst.SessionKind == jam.SessionKindPersonal && inst.OwnerID == p.UserID {
		return "requester", true
	}
	if role, ok := store.GetRole(inst.Project, inst.Role); ok && role.Collaborate.Allows(string(r)) && isMember(store, inst.Project, p.UserID) {
		return "role", true
	}
	for _, g := range store.ShareGrants(inst.ActorID) {
		if g.UserID == p.UserID && now.Before(g.ExpiresAt) && slices.Contains(g.Rights, string(r)) {
			return "grant", true
		}
	}
	return "", false
}

func isMember(store jam.Store, project string, user ident.ID) bool {
	p, ok := store.GetProject(project)
	if !ok {
		return false
	}
	_, ok = jam.MemberOf(store, p.ID, user)
	return ok
}

// ValidateGrant checks a share grant before it is stored.
func ValidateGrant(now time.Time, g jam.ShareGrant) error {
	if g.SessionID == "" || g.UserID == "" {
		return errors.New("grant needs a session and a user")
	}
	if len(g.Rights) == 0 {
		return errors.New("grant needs at least one right (preview, ssh)")
	}
	for _, r := range g.Rights {
		if r != string(Preview) && r != string(SSH) {
			return fmt.Errorf("unknown right %q (want preview or ssh)", r)
		}
	}
	if !g.ExpiresAt.After(now) {
		return errors.New("grant already expired")
	}
	if g.ExpiresAt.After(now.Add(MaxGrant)) {
		return fmt.Errorf("grant longer than %s", MaxGrant)
	}
	return nil
}
