// Package harbor is the credential broker: it authenticates an enrolled identity,
// decides whether that identity may reach a configured destination and which
// credential to inject, and reverse-proxies the request with harbor's real
// credential swapped in. Downstream secrets live in harbor, never in the caller.
package harbor

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"time"
)

// Scope is a Role's security envelope: which destinations an actor granted this
// role may reach, which repos (for repo-scoped destinations), and the default
// token lifetime applied at enrollment.
type Scope struct {
	Destinations []string      `json:"destinations"`
	Repos        []string      `json:"repos"`
	Addressing   []string      `json:"addressing,omitempty"` // allowed comms targets (globs, kind-prefixed)
	TTL          time.Duration `json:"ttl"`
}

// Role is a named, reusable security class within a project.
type Role struct {
	Name  string `json:"name"`
	Scope Scope  `json:"scope"`
	Kit   string `json:"kit,omitempty"` // optional kit name; "" = no kit
}

// Kit is one named registry entry: immutable, monotonically-numbered versions of
// a kit config (config.yml text) behind a mutable Current pointer. A Role
// references a Kit by name; the name resolves to Current. Pushing a new version
// advances Current; pinning rolls it to an existing version.
type Kit struct {
	Name     string         `json:"name"`
	Current  int            `json:"current"`
	Versions map[int]string `json:"versions"` // version number → config.yml text (immutable)
}

// Override lets one grant narrow/replace fields of its Role's scope. A nil field
// inherits the Role; a set field REPLACES the Role's field (no merge).
type Override struct {
	Destinations []string `json:"destinations,omitempty"`
	Repos        []string `json:"repos,omitempty"`
	Addressing   []string `json:"addressing,omitempty"` // REPLACES Scope.Addressing when non-nil
}

// Grant assigns a Role (within a Project) to an Actor, optionally narrowed.
type Grant struct {
	Project   string    `json:"project"`
	Role      string    `json:"role"`
	Overrides *Override `json:"overrides,omitempty"`
}

// Actor is one enrolled top-level identity. The raw token is never stored — only
// its hash — so a leaked store yields no usable credentials. Grants are the
// Actor's role assignments across projects (RBAC).
type Actor struct {
	ID        string    `json:"id"`
	TokenHash string    `json:"token_hash"`
	Grants    []Grant   `json:"grants"`
	Expiry    time.Time `json:"expiry"` // zero = no expiry
}

// Human is a roster member reachable by @-mention on a tracker thread.
type Human struct {
	Name   string `json:"name"`   // roster-local name, e.g. "alice"
	Handle string `json:"handle"` // tracker @-mention handle
}

// Channel is a named conduit on a Service. C1: Service == "linear", Ref is a
// tracker issue identifier (e.g. "ACME-1") the channel posts to.
type Channel struct {
	Name    string `json:"name"`
	Service string `json:"service"`
	Ref     string `json:"ref"`
}

// Roster is a Project's addressable membership.
type Roster struct {
	Humans   []Human   `json:"humans,omitempty"`
	Channels []Channel `json:"channels,omitempty"`
}

// Project is the top of the config tree: it owns its Roster (and, in a later
// slice, its escalation policy). Roles remain keyed by (project, name).
type Project struct {
	Name   string `json:"name"`
	Roster Roster `json:"roster"`
}

// DefaultProject backs harbor-side default enrollment when no project is named.
const DefaultProject = "default"

// MintToken returns a new high-entropy bearer token (URL-safe, no padding).
func MintToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// HashToken returns the hex SHA-256 of a token — the stored/looked-up key.
func HashToken(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])
}
