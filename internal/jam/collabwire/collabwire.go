// Package collabwire holds the studio-collaboration wire types the admin API
// answers with (internal/jam/collabhttp) and its clients decode
// (internal/jam/adminclient). It depends on nothing beyond ident, so a client
// need not pull in the server's dependencies.
package collabwire

import (
	"time"

	"github.com/aethons-tools/cove/internal/ident"
)

// GrantView is a share grant as the API and UI show it.
type GrantView struct {
	User          string    `json:"user"` // the grantee's name
	UserID        ident.ID  `json:"user_id"`
	Rights        []string  `json:"rights"`
	ExpiresAt     time.Time `json:"expires_at"`
	CreatedAt     time.Time `json:"created_at"`
	GrantedBy     string    `json:"granted_by"`      // op:<operator-id> or usr_…
	GrantedByName string    `json:"granted_by_name"` // the grantor's user name when a user, else GrantedBy
	Expired       bool      `json:"expired"`
}

// UserRef names a roster user.
type UserRef struct {
	ID   ident.ID `json:"id"`
	Name string   `json:"name"`
}

// RoleAccess is what the studio's role gives the project's members, per
// right: "members" or "none" (defaults spelled out).
type RoleAccess struct {
	Project string `json:"project"`
	Role    string `json:"role"`
	Preview string `json:"preview"`
	SSH     string `json:"ssh"`
}

// AccessView is who can reach a studio and why: operators always; the
// personal session's requester; project members as the role says; each share
// grant. Live lists the open SSH connections.
type AccessView struct {
	Session   string      `json:"session"`
	Requester *UserRef    `json:"requester,omitempty"`
	Role      RoleAccess  `json:"role"`
	Grants    []GrantView `json:"grants"`
	Live      []LiveConn  `json:"live"`
}

// LiveConn is one open SSH connection into a studio.
type LiveConn struct {
	Session string    `json:"session"`
	User    string    `json:"user"` // usr_… or op:<operator-id>, as in the audit lines
	Name    string    `json:"name"` // display name
	Since   time.Time `json:"since"`
}
