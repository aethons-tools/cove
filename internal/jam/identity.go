// Package jam is the credential broker: it authenticates an enrolled identity,
// decides whether that identity may reach a configured destination and which
// credential to inject, and reverse-proxies the request with Jam's real
// credential swapped in. Downstream secrets live in Jam, never in the caller.
package jam

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam/sessionctx"
)

// Scope is a Role's security envelope: which destinations an actor granted this
// role may reach, the credential injected for each, and the default token
// lifetime applied at enrollment.
type Scope struct {
	Destinations []string `json:"destinations"`
	// Credentials maps a destination name to the credential the broker injects
	// for it; a destination absent here (or mapped to "") uses its own CredName.
	Credentials map[string]string `json:"credentials,omitempty"`
	Addressing  []string          `json:"addressing,omitempty"` // allowed comms targets (globs, kind-prefixed)
	TTL         time.Duration     `json:"ttl"`
	// Egress is the role's raw-egress policy, applied to its coves at raise; nil
	// = the kit's default list. Managed only by the egress endpoints
	// (`at-jam egress set|show|clear`); a role re-put keeps it.
	Egress *EgressPolicy `json:"egress,omitempty"`
}

// EgressPolicy is a role's raw-egress allow-list, applied to its coves at raise
// within the kit's ceiling. Domains may be empty (nothing beyond the sealed base
// and the kit's infra domains).
type EgressPolicy struct {
	Domains []string `json:"domains"`
}

// StandingSession is one operator-declared, named standing session of a role:
// Jam keeps exactly one cove running per declared name (see internal/standing).
type StandingSession struct {
	Name   string `json:"name"`
	Prompt string `json:"prompt"`
}

// RoleAllocation is a role's allocation policy: authored on the roster, read live
// by Jam's Allocator on each grant, by wake-on's personal-session idle ladder,
// and by the standing reconciler. A later slice adds requester grants.
type RoleAllocation struct {
	// MaxEphemeral caps the role's concurrent ephemeral (Requisitioner) sessions;
	// 0 = unset (the Requisitioner's max-concurrent applies as the fallback).
	MaxEphemeral int `json:"max_ephemeral,omitempty"`
	// MaxPersonal caps the role's concurrent personal sessions across all owners
	// (the pool); 0 = no personal sessions of this role.
	MaxPersonal int `json:"max_personal,omitempty"`
	// MaxPersonalPerOwner caps one owner's concurrent personal sessions of this
	// role; 0 = the pool cap only.
	MaxPersonalPerOwner int `json:"max_personal_per_owner,omitempty"`
	// IdleAfter is how long a personal session may wait on its owner before
	// Jam first nags them; 0 = the default (DefaultIdleAfter).
	IdleAfter time.Duration `json:"idle_after,omitempty"`
	// NagEvery is how often Jam re-nags the owner after the first nag;
	// 0 = the default (DefaultNagEvery).
	NagEvery time.Duration `json:"nag_every,omitempty"`
	// ReclaimAfter is how long a personal session may wait on its owner before
	// Jam reclaims it; 0 = never.
	ReclaimAfter time.Duration `json:"reclaim_after,omitempty"`
	// Standing is the role's declared standing sessions — the desired state the
	// standing reconciler keeps running, one cove per name. Managed only by the
	// standing endpoints (`at-jam standing add|rm|list`); a role re-put keeps it.
	Standing []StandingSession `json:"standing,omitempty"`
}

// The personal-session idle-ladder defaults for unset settings.
const (
	DefaultIdleAfter = 4 * time.Hour
	DefaultNagEvery  = 24 * time.Hour
)

// PersonalIdle returns the role's personal-session idle ladder with defaults
// applied: idle-after (DefaultIdleAfter when unset), nag-every
// (DefaultNagEvery when unset), and reclaim-after (0 = never reclaim).
func (a RoleAllocation) PersonalIdle() (idleAfter, nagEvery, reclaimAfter time.Duration) {
	idleAfter, nagEvery = a.IdleAfter, a.NagEvery
	if idleAfter <= 0 {
		idleAfter = DefaultIdleAfter
	}
	if nagEvery <= 0 {
		nagEvery = DefaultNagEvery
	}
	return idleAfter, nagEvery, max(a.ReclaimAfter, 0)
}

// Role is a named, reusable security class within a project.
type Role struct {
	Name  string `json:"name"`
	Scope Scope  `json:"scope"`
	Kit   string `json:"kit,omitempty"` // optional kit name; "" = no kit
	// ModelSpec binds the role to a model-spec by name: how its coves run their
	// agent (harness version, model, provider). "" = DefaultModelSpec. Checked
	// to exist at every role write; a bound spec cannot be deleted.
	ModelSpec  string         `json:"model_spec,omitempty"`
	Allocation RoleAllocation `json:"allocation,omitzero"` // zero = no role policy
	// TurnEnd is the role's turn-end policy (idle timeout + on-idle action).
	// Separate from Allocation, whose UI form rebuilds it on save.
	TurnEnd TurnEndPolicy `json:"turn_end,omitzero"`
	// Context is the role's authored session-context layer (rules for this
	// role). Managed only by the context endpoints (`at-jam context … --role`);
	// a role re-put keeps it.
	Context sessionctx.Layer `json:"context,omitzero"`
}

// ModelSpecName is the model-spec the role resolves to: its binding, or
// DefaultModelSpec when unbound.
func (r Role) ModelSpecName() string {
	if r.ModelSpec == "" {
		return DefaultModelSpec
	}
	return r.ModelSpec
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
	Addressing   []string `json:"addressing,omitempty"` // REPLACES Scope.Addressing when non-nil
	// Credentials REPLACES Scope.Credentials when non-nil.
	Credentials map[string]string `json:"credentials,omitempty"`
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
	// Login links the human to their admin operator identity (OperatorID: the
	// OIDC sub, or "local" on loopback). It is how Jam knows which roster
	// human is behind an admin request, e.g. to own a personal session. "" =
	// unlinked. At most one human per project may hold a given login.
	Login    string            `json:"login,omitempty"`
	Delivery []DeliveryProfile `json:"delivery,omitempty"` // per-service DM delivery targets
	// Identity binds the human to one or more browser OIDC subjects, so a login
	// authenticated at an OIDC provider can later be mapped to this roster actor.
	// Data-model only here; the auth/session mapping lives in a later slice.
	Identity []OIDCIdentity `json:"identity,omitempty"`
}

// OIDCIdentity binds a Human to a browser OIDC subject: the provider's Issuer
// and the subject (`sub`) claim within it. Both are opaque identifiers, never
// secrets. A subject is unique only within its issuer, so both are needed.
type OIDCIdentity struct {
	Issuer  string `json:"issuer"`
	Subject string `json:"subject"`
}

// ValidateIdentity checks a human's OIDC identity bindings: both the issuer and
// the subject of each binding must be non-empty.
func ValidateIdentity(ids []OIDCIdentity) error {
	for _, id := range ids {
		if id.Issuer == "" {
			return fmt.Errorf("oidc identity issuer must be non-empty")
		}
		if id.Subject == "" {
			return fmt.Errorf("oidc identity subject must be non-empty")
		}
	}
	return nil
}

// rosterReader is the slice of Store HumanByLogin reads.
type rosterReader interface {
	GetRoster(project string) (Roster, bool)
	UserByLogin(login string) (User, bool)
}

// HumanByLogin returns the roster Human in project who is the user holding
// login (any of the user's logins). The empty login never matches, so an
// unlinked human is never an owner.
func HumanByLogin(store rosterReader, project, login string) (Human, bool) {
	if login == "" {
		return Human{}, false
	}
	u, ok := store.UserByLogin(login)
	if !ok {
		return Human{}, false
	}
	rr, ok := store.GetRoster(project)
	if !ok {
		return Human{}, false
	}
	for _, h := range rr.Humans {
		if h.Name == u.Name {
			return h, true
		}
	}
	return Human{}, false
}

// DeliveryProfile is how a Human receives messages on one non-tracker Service.
// Address is the service-native delivery target: for "discord", the id of the
// inbox channel Jam posts the human's DMs into.
type DeliveryProfile struct {
	Service string `json:"service"`
	Address string `json:"address"`
	// UserID binds the human to their account on the service: for "discord",
	// the human's Discord user id (a snowflake). "" = unbound. A bound human's
	// Discord replies are attributed by this id alone (see DiscordAuthor). At
	// most one human per project may hold a given id.
	UserID string `json:"user_id,omitempty"`
}

// ValidDiscordUserID reports whether id is shaped like a Discord user id: a
// snowflake, i.e. 1–20 ASCII digits.
func ValidDiscordUserID(id string) bool {
	if id == "" || len(id) > 20 {
		return false
	}
	for _, c := range id {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// ValidateDelivery checks a human's delivery profiles: a user id may be given
// only on a discord profile, and must be a snowflake.
func ValidateDelivery(ps []DeliveryProfile) error {
	for _, p := range ps {
		if p.UserID == "" {
			continue
		}
		if p.Service != "discord" {
			return fmt.Errorf("a user id is only supported for discord delivery, not %q", p.Service)
		}
		if !ValidDiscordUserID(p.UserID) {
			return fmt.Errorf("discord user id %q is not a Discord user id (want digits only)", p.UserID)
		}
	}
	return nil
}

// discordUserIDs returns the Discord user ids h is bound to (normally at most one).
func (h Human) discordUserIDs() []string {
	var ids []string
	for _, d := range h.Delivery {
		if d.Service == "discord" && d.UserID != "" {
			ids = append(ids, d.UserID)
		}
	}
	return ids
}

// DiscordBound reports whether h is bound to a Discord user id.
func (h Human) DiscordBound() bool { return len(h.discordUserIDs()) > 0 }

// HumanByDiscordUser returns the one roster human bound to the Discord user id
// userID. The empty id never matches, and an id somehow held by more than one
// human matches nobody (fail closed: never guess an identity).
func HumanByDiscordUser(r Roster, userID string) (Human, bool) {
	if userID == "" {
		return Human{}, false
	}
	var found Human
	n := 0
	for _, h := range r.Humans {
		for _, id := range h.discordUserIDs() {
			if id == userID {
				found = h
				n++
				break
			}
		}
	}
	if n != 1 {
		return Human{}, false
	}
	return found, true
}

// DeliveryFor returns the human's profile for service, if present.
func (h Human) DeliveryFor(service string) (DeliveryProfile, bool) {
	for _, d := range h.Delivery {
		if d.Service == service {
			return d, true
		}
	}
	return DeliveryProfile{}, false
}

// DiscordInboxOwner returns the one roster human whose discord delivery
// address is channel; ok=false when none or more than one human uses it (a
// shared inbox), when channel is also a roster discord channel (a shared
// conduit, not an inbox), or when channel is "". It is DiscordAuthor's channel
// rule: a reply posted there is attributed to that human while they are not
// bound to a Discord user id — the channel, not the Discord display name
// (which anyone can set), is what proves who sent it.
func DiscordInboxOwner(r Roster, channel string) (name string, ok bool) {
	if channel == "" {
		return "", false
	}
	for _, c := range r.Channels {
		if c.Service == "discord" && c.Ref == channel {
			return "", false
		}
	}
	for _, h := range r.Humans {
		p, has := h.DeliveryFor("discord")
		if !has || p.Address != channel {
			continue
		}
		if ok {
			return "", false // a second human shares it
		}
		name, ok = h.Name, true
	}
	return name, ok
}

// DiscordAuthor returns the roster human a Discord message in project roster r
// is from, and how it was decided (by "id" or by "channel"). ok=false means
// nobody: the caller falls back to the display name, so the message is an
// ordinary reply. In order:
//
//  1. a bot author is never a roster human;
//  2. an author id bound to exactly one human (HumanByDiscordUser) is that
//     human, whatever the channel;
//  3. a channel that is uniquely one human's inbox (DiscordInboxOwner) is that
//     human — but only while they are NOT bound: once bound, only their own
//     Discord account counts as them, so a stranger in their inbox is not;
//  4. otherwise nobody.
//
// Every doubt fails toward nobody, never toward an owner.
func DiscordAuthor(r Roster, channel, authorID string, isBot bool) (name, by string, ok bool) {
	if isBot {
		return "", "", false
	}
	if h, ok := HumanByDiscordUser(r, authorID); ok {
		return h.Name, "id", true
	}
	owner, ok := DiscordInboxOwner(r, channel)
	if !ok {
		return "", "", false
	}
	for _, h := range r.Humans {
		if h.Name == owner && h.DiscordBound() {
			return "", "", false
		}
	}
	return owner, "channel", true
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

// EscalationTier is one rung of a Project's escalation policy: the targets to
// ping and how long to wait for an answer before advancing to the next tier.
type EscalationTier struct {
	Targets []string      `json:"targets"` // kind-prefixed human names, e.g. "human:alice"
	Timeout time.Duration `json:"timeout"` // wait after pinging this tier before advancing
}

// Project is the top of the config tree: it owns its Roster, its escalation
// policy, and (by key) its Roles and the Grants into them. A project exists only
// once created (CreateProject) — every project-scoped write into an unknown
// project fails with ErrProjectNotFound — except DefaultProject, which a write
// naming it (or naming no project) materializes on first use.
type Project struct {
	// ID is the project's surrogate id (internal/ident), minted when the
	// record is created. Store methods still key projects by Name until 1b.
	ID                   ident.ID                    `json:"id,omitempty"`
	Name                 string                      `json:"name"`
	Roster               Roster                      `json:"roster"`
	Escalation           []EscalationTier            `json:"escalation,omitempty"`
	EscalationByCategory map[string][]EscalationTier `json:"escalation_by_category,omitempty"` // category → chain; overrides Escalation (the default)
	// ChatService is the service backing human DMs (e.g. "discord"); ""
	// means tracker @-mentions only.
	ChatService string `json:"chat_service,omitempty"`
	// Context is the project's authored session-context layer (its goals) and
	// Resources the repos/docs/trackers sessions should know; both managed by
	// the context endpoints (`at-jam context … --project`).
	Context   sessionctx.Layer      `json:"context,omitzero"`
	Resources []sessionctx.Resource `json:"resources,omitempty"`
}

// DefaultProject backs Jam-side default enrollment when no project is named.
const DefaultProject = "default"

// Project lifecycle errors, shared by every Store so callers can map them with
// errors.Is (e.g. to HTTP 404/409).
var (
	ErrProjectNotFound = errors.New("project not found")
	ErrProjectExists   = errors.New("project already exists")
	ErrProjectInUse    = errors.New("project is still referenced")
	// ErrModelSpecInUse refuses deleting a model-spec a role resolves to.
	ErrModelSpecInUse = errors.New("model-spec is still referenced")
)

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
