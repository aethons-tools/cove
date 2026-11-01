package adminui

import "time"

// This file holds every type the /app/admin/api JSON carries. tygo reads only
// this file (tygo.yaml) to generate web/app/src/admin/api/types.ts: keep JSON
// types here, give every field a json tag, and run
// `go generate ./internal/jam/adminui/` after a change. Lists are never nil in
// a response (types.ts says T[]).

// APIError is every /app/admin/api error body (userui's envelope).
type APIError struct {
	Error string `json:"error"` // for people
	Code  string `json:"code"`  // for code: forbidden, not_found, bad_request, internal
}

// Bootstrap is what the admin app needs before its first render: injected
// into index.html by webapp, and served at GET /app/admin/api/bootstrap.
type Bootstrap struct {
	Brand   string   `json:"brand"`
	Name    string   `json:"name"` // appui.Principal.DisplayName()
	Initial string   `json:"initial"`
	Roles   string   `json:"roles"`   // "admin" or "member · admin"
	Member  bool     `json:"member"`  // a user too: the Inbox tab shows
	SignOut bool     `json:"signOut"` // offer Sign out (/app/auth/logout)
	Tabs    []NavTab `json:"tabs"`    // the title bar's links
}

// NavTab is one title-bar link; the same JSON as the user area's.
type NavTab struct {
	Key        string `json:"key"`
	Label      string `json:"label"`
	Href       string `json:"href"`
	Badge      int    `json:"badge"`
	BadgeRed   bool   `json:"badgeRed"`
	BadgeTitle string `json:"badgeTitle"`
}

// TreeNode is one node of the Config tree.
type TreeNode struct {
	Path       string     `json:"path"` // under the app's mount, e.g. "config/projects/acme"
	Label      string     `json:"label"`
	Group      bool       `json:"group"` // expandable (even when empty)
	Badge      int        `json:"badge"` // config attention items at or under it
	BadgeRed   bool       `json:"badgeRed"`
	BadgeTitle string     `json:"badgeTitle"`
	Children   []TreeNode `json:"children"` // never nil
}

// TreeResponse is GET /app/admin/api/tree.
type TreeResponse struct {
	Nodes []TreeNode `json:"nodes"`
}

// DiffLine is one line of a line diff (linediff.Line): Op is " " (context),
// "+", "-" or "…" (a collapsed run of unchanged lines).
type DiffLine struct {
	Op   string `json:"op"`
	Text string `json:"text"`
}

// Created answers a create: the new object's id and its page (under the
// app's mount).
type Created struct {
	ID      string `json:"id"`
	Path    string `json:"path"`
	Message string `json:"message,omitempty"` // what it did, when worth saying (an import)
}

// AccountRow is one of a user's accounts on a connected service.
type AccountRow struct {
	ID         string `json:"id"`
	Connection string `json:"connection"` // the connection's name
	ServiceUID string `json:"serviceUid"`
	Handle     string `json:"handle"`
}

// UserRow is one user.
type UserRow struct {
	ID       string       `json:"id"`
	Name     string       `json:"name"`
	Logins   []string     `json:"logins"`
	Accounts []AccountRow `json:"accounts"`
	Projects []string     `json:"projects"`
}

// UsersResponse is GET /app/admin/api/users.
type UsersResponse struct {
	Users []UserRow `json:"users"`
}

// ConnectionChoice is a connection an account can be added on.
type ConnectionChoice struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
}

// UserDetail is a user's page; a removed user's is read-only.
type UserDetail struct {
	User        UserRow            `json:"user"`
	Removed     bool               `json:"removed"`
	LoginSpec   string             `json:"loginSpec"` // the logins, one per line
	OIDCSpec    string             `json:"oidcSpec"`  // the OIDC identities, one per line
	Connections []ConnectionChoice `json:"connections"`
	AppTokens   []AppTokenRow      `json:"appTokens"` // newest first
}

// AppTokenRow is one of a user's app tokens (docs/usage/jam/app-tokens.md).
type AppTokenRow struct {
	ID       string    `json:"id"`
	Label    string    `json:"label"`
	Created  time.Time `json:"created"`
	LastUsed time.Time `json:"lastUsed"`
	Expires  time.Time `json:"expires"`
	Operator bool      `json:"operator"` // carries operator standing
}

// TrackingSource is the config repo this Jam follows.
type TrackingSource struct {
	Label      string `json:"label"` // <repo>[//<dir>]@<ref>; "invalid" when it doesn't parse
	Repo       string `json:"repo"`
	RepoLink   string `json:"repoLink"`
	Dir        string `json:"dir"`
	Ref        string `json:"ref"`
	Commit     string `json:"commit"` // 7 chars
	CommitLink string `json:"commitLink"`
	Applied    string `json:"applied"` // the last apply from it; "" = none since tracked
}

// ManagedRow is one object the config repo manages.
type ManagedRow struct {
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	Path    string `json:"path"` // its page under the app's mount; "" = none
	Commit  string `json:"commit"`
	Applied string `json:"applied"`
}

// ConfigKeyRow names a config object.
type ConfigKeyRow struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

// KitSourceRow is a kit's git source in a plan.
type KitSourceRow struct {
	Path   string `json:"path"`
	Source string `json:"source"`
	Commit string `json:"commit"`
}

// PlanOpRow is one change a plan makes.
type PlanOpRow struct {
	Action   string     `json:"action"`
	Kind     string     `json:"kind"`
	Name     string     `json:"name"`
	Path     string     `json:"path"`     // the object's page; "" for a create or none
	File     string     `json:"file"`     // in the repo
	Lines    []DiffLine `json:"lines"`    // Jam → repo
	TooLarge bool       `json:"tooLarge"` // too large to diff here
	Diff     string     `json:"diff"`     // a kit update's version diff (text)
}

// ServeDiff compares the repo's serve config with this serve's.
type ServeDiff struct {
	State string   `json:"state"` // same, differs, absent, unknown; "" = not compared
	Keys  []string `json:"keys"`
}

// Plan is a config plan for review; Apply posts its Digest back.
type Plan struct {
	Source     string         `json:"source"`
	Commit     string         `json:"commit"`
	Digest     string         `json:"digest"`
	Changes    int            `json:"changes"`
	Unchanged  int            `json:"unchanged"`
	Ops        []PlanOpRow    `json:"ops"`
	Forget     []ConfigKeyRow `json:"forget"`
	Unmanaged  []ConfigKeyRow `json:"unmanaged"`
	KitSources []KitSourceRow `json:"kitSources"`
	Refusals   []string       `json:"refusals"` // any one blocks Apply
	Serve      ServeDiff      `json:"serve"`
}

// Tracking is Config › Tracking: the followed repo and the follower's last
// check, the managed objects and (after a Diff) the plan.
type Tracking struct {
	Source      *TrackingSource `json:"source" tstype:"TrackingSource | null"` // the followed repo; nil = none configured
	Rows        []ManagedRow    `json:"rows"`
	LastCommit  string          `json:"lastCommit"`
	LastApplied string          `json:"lastApplied"`
	CanGit      bool            `json:"canGit"` // Diff and Apply are offered
	Plan        *Plan           `json:"plan" tstype:"Plan | null"`
	Follow      FollowStatus    `json:"follow"`
}

// FollowStatus is the config follower's last check (jam.ConfigFollowStatus),
// its commits 7 chars.
type FollowStatus struct {
	State   string `json:"state"` // off | awaiting-confirm | in-sync | failed
	Head    string `json:"head"`
	Applied string `json:"applied"`
	Error   string `json:"error"`
	Checked string `json:"checked"` // when; "" = not yet
}

// TrackingResult answers a tracking write: the page after it and its message.
type TrackingResult struct {
	Tracking Tracking `json:"tracking"`
	Message  string   `json:"message"`
}

// ProjectRow is one project in the list.
type ProjectRow struct {
	Name    string `json:"name"`
	Members int    `json:"members"`
	Roles   int    `json:"roles"`
	Rooms   int    `json:"rooms"`
}

// ProjectsResponse is GET /app/admin/api/projects.
type ProjectsResponse struct {
	Projects []ProjectRow `json:"projects"`
}

// ContextLeaf is one leaf of an agent context: read when ReadWhen.
type ContextLeaf struct {
	Name     string `json:"name"`
	ReadWhen string `json:"readWhen"`
}

// ContextResource is one resource of a project's agent context.
type ContextResource struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	Ref  string `json:"ref"`
	Note string `json:"note"`
}

// ContextPanel is an agent-context card (Jam, a project or a role): what
// every agent there is told when raised, and its YAML for the editor.
type ContextPanel struct {
	Scope         string            `json:"scope"` // "Jam-wide", "project", "role"
	Core          string            `json:"core"`
	CoreBytes     int               `json:"coreBytes"` // as delivered
	Budget        int               `json:"budget"`
	Over          bool              `json:"over"`
	Empty         bool              `json:"empty"`
	ShowResources bool              `json:"showResources"`
	Leaves        []ContextLeaf     `json:"leaves"`
	Resources     []ContextResource `json:"resources"`
	YAML          string            `json:"yaml"`
}

// DeliveryRow is where a member gets DMs: service:address.
type DeliveryRow struct {
	Service string `json:"service"`
	Address string `json:"address"`
}

// MemberRow is one project member.
type MemberRow struct {
	UserID       string        `json:"userId"`
	Name         string        `json:"name"`
	Handle       string        `json:"handle"` // tracker @-handle; "" = none
	Delivery     []DeliveryRow `json:"delivery"`
	DeliverySpec string        `json:"deliverySpec"` // the edit form's lines
}

// RoomRow is one room of a project.
type RoomRow struct {
	Name       string `json:"name"`
	Connection string `json:"connection"`
	Kind       string `json:"kind"`
	Ref        string `json:"ref"`
	PostOnly   bool   `json:"postOnly"` // another channel receives this ref's replies
}

// TargetRow is one escalation target; Unknown names nobody on the roster.
type TargetRow struct {
	Text    string `json:"text"`
	Unknown bool   `json:"unknown"`
}

// TierRow is one escalation tier: its targets, then the wait.
type TierRow struct {
	Targets []TargetRow `json:"targets"`
	Timeout string      `json:"timeout"`
}

// ChainRow is one escalation chain ("" category = the default).
type ChainRow struct {
	Category string    `json:"category"`
	Tiers    []TierRow `json:"tiers"`
	Spec     string    `json:"spec"` // the editor's lines
}

// SquawkRow is one logged message.
type SquawkRow struct {
	At        string   `json:"at"`
	From      string   `json:"from"`
	FromID    string   `json:"fromId"`
	AgentID   string   `json:"agentId"` // the author's agent, else ""
	Channel   string   `json:"channel"`
	ChannelID string   `json:"channelId"`
	Project   string   `json:"project"`
	Body      string   `json:"body"` // squawkrender's sanitized HTML
	Plain     bool     `json:"plain"`
	To        []ToCell `json:"to"` // a legacy squawk's recipients
}

// ToCell is a legacy squawk's recipient and its reach.
type ToCell struct {
	Target   string `json:"target"` // kind:ref
	External bool   `json:"external"`
}

// ProjectDetail is a project's pages: its overview and sections.
type ProjectDetail struct {
	Name          string       `json:"name"`
	InUseBy       string       `json:"inUseBy"` // what still references it ("" = deletable)
	Renamable     bool         `json:"renamable"`
	ChatService   string       `json:"chatService"`  // "" = none
	ChatServices  []string     `json:"chatServices"` // the select's options, "" first
	Members       []MemberRow  `json:"members"`
	Rooms         []RoomRow    `json:"rooms"`
	Roles         int          `json:"roles"`
	Agents        int          `json:"agents"`
	LiveAgents    int          `json:"liveAgents"`
	Escalation    []ChainRow   `json:"escalation"` // chains with tiers
	DefaultChain  ChainRow     `json:"defaultChain"`
	Context       ContextPanel `json:"context"`
	LogConfigured bool         `json:"logConfigured"`
	Squawks       []SquawkRow  `json:"squawks"` // ?section=intercom: the newest in this project
}

// AttnFlag is a row's attention: its worst item's kind and why.
type AttnFlag struct {
	Kind string `json:"kind"` // broken, stale, config, alert
	Why  string `json:"why"`
}

// CoveRow is one running studio (the studio table).
type CoveRow struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Project     string    `json:"project"`
	Role        string    `json:"role"`
	Unit        string    `json:"unit"`
	Status      string    `json:"status"` // the display status (jam.StatusOf)
	Connector   string    `json:"connector"`
	Image       string    `json:"image"`
	Manager     string    `json:"manager"`
	LeaseHolder string    `json:"leaseHolder"`
	RaisedAt    string    `json:"raisedAt"` // "Jan 2 15:04"
	LastSeen    string    `json:"lastSeen"` // "15:04:05"; "" = never
	Attn        *AttnFlag `json:"attn" tstype:"AttnFlag | null"`
}

// AgentRow is one agent of a project: running in it or holding a grant into it.
type AgentRow struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Kind     string    `json:"kind"`
	Unit     string    `json:"unit"`
	Status   string    `json:"status"`
	Image    string    `json:"image"`
	LastSeen string    `json:"lastSeen"` // "Jan 2 15:04:05"; "" = never
	Roles    []string  `json:"roles"`
	Attn     *AttnFlag `json:"attn" tstype:"AttnFlag | null"`
}

// DestCred is a destination in a role's scope and its credential ("" = the
// destination's default).
type DestCred struct {
	Name string `json:"name"`
	Cred string `json:"cred"`
}

// RoleRow is one role in a project's Agents view.
type RoleRow struct {
	Name         string     `json:"name"`
	Destinations []DestCred `json:"destinations"`
	TTL          string     `json:"ttl"` // "—" = none
	Kit          string     `json:"kit"` // "" = the default kit
	Attn         *AttnFlag  `json:"attn" tstype:"AttnFlag | null"`
}

// ProjectAgents is a project's Agents view: its roles and its agents.
type ProjectAgents struct {
	Project    string     `json:"project"`
	Roles      []RoleRow  `json:"roles"`
	Agents     []AgentRow `json:"agents"`
	CanRequest bool       `json:"canRequest"` // a supervisor runs: Request is offered
}

// DestRow is a destination in a role's scope with the credential the broker
// would inject.
type DestRow struct {
	Name       string `json:"name"`
	Cred       string `json:"cred"`
	CredIsRole bool   `json:"credIsRole"` // the role's mapping, not the destination default
	Route      string `json:"route"`
	Missing    bool   `json:"missing"` // not a registered destination
}

// Setting is an allocation value with how an unset one reads.
type Setting struct {
	Label string `json:"label"`
	Value string `json:"value"`
	Unset bool   `json:"unset"`
}

// StandingRow is a declared standing session and its studio, if any.
type StandingRow struct {
	Name    string `json:"name"`
	Prompt  string `json:"prompt"`
	ActorID string `json:"actorId"`
	Status  string `json:"status"` // "" = not running
	Image   string `json:"image"`
	Upgrade string `json:"upgrade"` // a pending upgrade's state; "" = none
}

// HolderRow is an agent holding a grant on the role.
type HolderRow struct {
	ID       string `json:"id"`
	Override bool   `json:"override"` // the grant replaces part of the role's scope
}

// RoleForm is a role's current values in its edit forms' syntax.
type RoleForm struct {
	Destinations        string `json:"destinations"`
	Addressing          string `json:"addressing"`
	TTL                 string `json:"ttl"`
	Kit                 string `json:"kit"`
	ModelSpec           string `json:"modelSpec"`
	MaxEphemeral        string `json:"maxEphemeral"`
	MaxPersonal         string `json:"maxPersonal"`
	MaxPersonalPerOwner string `json:"maxPersonalPerOwner"`
	IdleAfter           string `json:"idleAfter"`
	NagEvery            string `json:"nagEvery"`
	ReclaimAfter        string `json:"reclaimAfter"`
	Egress              string `json:"egress"` // one domain per line
}

// RoleDetail is a role's page.
type RoleDetail struct {
	Project          string        `json:"project"`
	Name             string        `json:"name"`
	Kit              string        `json:"kit"`       // "" = no kit
	ModelSpec        string        `json:"modelSpec"` // the spec it runs (the default's name when unset)
	ModelSpecDefault bool          `json:"modelSpecDefault"`
	TTL              string        `json:"ttl"`
	Dests            []DestRow     `json:"dests"`
	Addressing       []string      `json:"addressing"`
	EgressManaged    bool          `json:"egressManaged"`
	EgressDomains    []string      `json:"egressDomains"`
	Allocation       []Setting     `json:"allocation"`
	Standing         []StandingRow `json:"standing"`
	Holders          []HolderRow   `json:"holders"`
	Running          []CoveRow     `json:"running"`
	Form             RoleForm      `json:"form"`
	Context          ContextPanel  `json:"context"`
	CanRequest       bool          `json:"canRequest"`
	SetupEnabled     bool          `json:"setupEnabled"`
	Attn             *AttnFlag     `json:"attn" tstype:"AttnFlag | null"`
}

// RoleResult answers a role write: the role after it and a message ("" = none).
type RoleResult struct {
	Role    RoleDetail `json:"role"`
	Message string     `json:"message"`
}

// Requested answers a Request: the personal session raised.
type Requested struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Message string `json:"message"`
}

// SetupFile is one context file of a setup section.
type SetupFile struct {
	Path  string `json:"path"`
	Body  string `json:"body"`
	Bytes int    `json:"bytes"`
}

// SetupSection is one section of a studio's or a role's setup. Its text has
// been through jam.SetupDisplayText.
type SetupSection struct {
	Name         string      `json:"name"`
	Source       string      `json:"source"`
	Reason       string      `json:"reason"`
	At           string      `json:"at"` // the snapshot time; "" = none
	Stale        bool        `json:"stale"`
	Open         bool        `json:"open"`
	Body         string      `json:"body"`
	Head         string      `json:"head"`  // context: the file table, budgets and warnings
	Files        []SetupFile `json:"files"` // context: each file (none: Body is whole)
	Diffed       bool        `json:"diffed"`
	DiffTooLarge bool        `json:"diffTooLarge"`
	DiffSame     bool        `json:"diffSame"`
	Diff         []DiffLine  `json:"diff"`
	DiffNote     string      `json:"diffNote"`
	DiffPath     string      `json:"diffPath"` // a stale section's diff, under the API; "" = none
}

// SetupResponse is a setup: its sections and the selectors' choices.
type SetupResponse struct {
	Note         string         `json:"note"`
	Stale        int            `json:"stale"`
	Sections     []SetupSection `json:"sections"`
	SectionNames []string       `json:"sectionNames"`
	Managers     []string       `json:"managers"` // two or more: the image's manager is chosen
	Manager      string         `json:"manager"`  // the chosen one
}

// DestListRow is one destination in the list.
type DestListRow struct {
	Name       string    `json:"name"`
	Route      string    `json:"route"`
	Upstream   string    `json:"upstream"`
	IdentityIn string    `json:"identityIn"`
	Apply      string    `json:"apply"`
	CredName   string    `json:"credName"`
	EnvKeys    []string  `json:"envKeys"`
	GitRouted  bool      `json:"gitRouted"`
	UsedBy     int       `json:"usedBy"` // roles whose scope lists it
	Attn       *AttnFlag `json:"attn" tstype:"AttnFlag | null"`
}

// DestinationsResponse is GET /app/admin/api/destinations.
type DestinationsResponse struct {
	Destinations []DestListRow `json:"destinations"`
	Methods      []string      `json:"methods"` // the identity-in / apply presets
}

// EnvRow is one client-env entry an agent sets for a destination.
type EnvRow struct {
	Key      string `json:"key"`
	Template string `json:"template"`
}

// DestUse is a role whose scope lists the destination, and its credential.
type DestUse struct {
	Project    string `json:"project"`
	Role       string `json:"role"`
	Cred       string `json:"cred"`
	CredIsRole bool   `json:"credIsRole"`
}

// DestConflict is a role whose connector this destination breaks.
type DestConflict struct {
	Project string `json:"project"`
	Role    string `json:"role"`
	Other   string `json:"other"` // the other destination
	What    string `json:"what"`
}

// DestFields are a destination's form values ("none" = header logging off).
type DestFields struct {
	Route         string `json:"route"`
	Upstream      string `json:"upstream"`
	IdentityIn    string `json:"identityIn"`
	Apply         string `json:"apply"`
	CredName      string `json:"credName"`
	Env           string `json:"env"`        // KEY=TEMPLATE lines, the declared env only
	AllowPaths    string `json:"allowPaths"` // one pattern per line
	Note          string `json:"note"`
	LogHeadersIn  string `json:"logHeadersIn"`
	LogHeadersOut string `json:"logHeadersOut"`
	Git           bool   `json:"git"`
}

// DestDetail is a destination's page.
type DestDetail struct {
	Name           string         `json:"name"`
	Fields         DestFields     `json:"fields"`
	Methods        []string       `json:"methods"`
	IdentityInSpec string         `json:"identityInSpec"` // a custom spec as the Broker section shows it; "" = none
	ApplySpec      string         `json:"applySpec"`
	AllowPaths     []string       `json:"allowPaths"`
	Env            []EnvRow       `json:"env"`
	EnvImplied     bool           `json:"envImplied"` // no env declared: the route's legacy default applies
	GitRouted      bool           `json:"gitRouted"`
	GitImplied     bool           `json:"gitImplied"` // routed only by the legacy /git/ rule
	Uses           []DestUse      `json:"uses"`
	Conflicts      []DestConflict `json:"conflicts"`
	ConflictRoles  []string       `json:"conflictRoles"`
}

// SpecListRow is one model-spec in the list.
type SpecListRow struct {
	Name              string `json:"name"`
	Type              string `json:"type"`
	Version           string `json:"version"`
	VersionConstraint string `json:"versionConstraint"`
	Principal         string `json:"principal"`
	ModelID           string `json:"modelId"`
	Effort            string `json:"effort"`
	Mode              string `json:"mode"`
	Provider          string `json:"provider"` // "" = no claude body
	UsedBy            int    `json:"usedBy"`
}

// SpecChoices are the model-spec form's select options; principals are
// credential names only.
type SpecChoices struct {
	Types      []string `json:"types"`
	Principals []string `json:"principals"`
	Modes      []string `json:"modes"`
	Providers  []string `json:"providers"`
}

// SpecFields are a model-spec's form values.
type SpecFields struct {
	Type              string `json:"type"`
	Version           string `json:"version"`
	VersionConstraint string `json:"versionConstraint"`
	Principal         string `json:"principal"`
	ModelID           string `json:"modelId"`
	Effort            string `json:"effort"`
	Mode              string `json:"mode"`
	Provider          string `json:"provider"`
	Headers           string `json:"headers"` // NAME = VALUE / NAME += ITEM lines
	Allow             string `json:"allow"`
	Deny              string `json:"deny"`
	ProviderEnv       string `json:"providerEnv"` // KEY=VALUE lines (non-secret by rule)
	Plugins           string `json:"plugins"`
	Settings          string `json:"settings"` // a JSON object
	Note              string `json:"note"`
}

// SpecUse is a role that runs the model-spec; Implicit: it names none (the default).
type SpecUse struct {
	Project  string `json:"project"`
	Role     string `json:"role"`
	Implicit bool   `json:"implicit"`
}

// HeaderRuleRow is one principal header rule.
type HeaderRuleRow struct {
	Name  string `json:"name"`
	Op    string `json:"op"` // "=" or "+="
	Value string `json:"value"`
}

// SpecDetail is a model-spec's page.
type SpecDetail struct {
	Name            string          `json:"name"`
	Fields          SpecFields      `json:"fields"`
	Choices         SpecChoices     `json:"choices"`
	HeaderRules     []HeaderRuleRow `json:"headerRules"`
	Allow           []string        `json:"allow"`
	Deny            []string        `json:"deny"`
	HasClaude       bool            `json:"hasClaude"`
	ProviderEnvKeys []string        `json:"providerEnvKeys"`
	Plugins         []string        `json:"plugins"`
	SettingsKeys    []string        `json:"settingsKeys"`
	Uses            []SpecUse       `json:"uses"`
}

// SpecsResponse is GET /app/admin/api/model-specs; New seeds the create form.
type SpecsResponse struct {
	Specs   []SpecListRow `json:"specs"`
	New     SpecFields    `json:"new"`
	Choices SpecChoices   `json:"choices"`
}

// KitRow is one kit in the list.
type KitRow struct {
	Name    string    `json:"name"`
	Current int       `json:"current"`
	Count   int       `json:"count"`
	Base    string    `json:"base"` // the base's kind; "" when invalid
	Egress  int       `json:"egress"`
	UsedBy  int       `json:"usedBy"`
	Invalid bool      `json:"invalid"` // the current version isn't a valid studio kit
	Draft   bool      `json:"draft"`   // an open draft
	Attn    *AttnFlag `json:"attn" tstype:"AttnFlag | null"`
}

// KitsResponse is GET /app/admin/api/kits.
type KitsResponse struct {
	Kits      []KitRow `json:"kits"`
	CanSource bool     `json:"canSource"` // import, track and sync are offered
}

// SourceInfo is a git source: where a kit or version came from.
type SourceInfo struct {
	Repo       string `json:"repo"`
	RepoLink   string `json:"repoLink"`
	Dir        string `json:"dir"`
	Ref        string `json:"ref"`
	Commit     string `json:"commit"`
	CommitLink string `json:"commitLink"`
}

// KitVersionRow is one version in a kit's rail.
type KitVersionRow struct {
	N       int         `json:"n"`
	Prev    int         `json:"prev"` // the version before it; 0 = none
	Current bool        `json:"current"`
	Digest  string      `json:"digest"` // short build digest; "" = invalid
	Pushed  string      `json:"pushed"`
	Source  *SourceInfo `json:"source" tstype:"SourceInfo | null"`
}

// KV is a key and a value.
type KV struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// KitView is one version as shown, or (Effective) a chain merged.
type KitView struct {
	N          int      `json:"n"`
	ParseErr   string   `json:"parseErr"`
	RawShown   string   `json:"rawShown"` // an unparsable kit.yml as shown (masked)
	Masked     bool     `json:"masked"`
	Hidden     bool     `json:"hidden"`
	PushText   string   `json:"pushText"` // the push box's prefill
	YAML       string   `json:"yaml"`
	Base       string   `json:"base"`
	Image      string   `json:"image"`
	Egress     []string `json:"egress"`
	Ceiling    []string `json:"ceiling"`
	Excluded   []string `json:"excluded"`
	BuildArgs  []KV     `json:"buildArgs"`
	Secrets    []KV     `json:"secrets"`
	MCPServers []KV     `json:"mcpServers"`
	Notes      []KV     `json:"notes"`
	Prompt     string   `json:"prompt"`
	Digest     string   `json:"digest"`
}

// KitDiff compares two versions' kit.yml.
type KitDiff struct {
	From          int        `json:"from"`
	To            int        `json:"to"`
	Lines         []DiffLine `json:"lines"`
	DigestChanged bool       `json:"digestChanged"`
	TooLarge      bool       `json:"tooLarge"`
}

// KitUse is a role that raises the kit; Implicit: it names none (the default).
type KitUse struct {
	Project  string `json:"project"`
	Role     string `json:"role"`
	Implicit bool   `json:"implicit"`
}

// KitParent is one link of a kit's "Built on" chain.
type KitParent struct {
	Name    string `json:"name"`
	Version int    `json:"version"`
	Follow  bool   `json:"follow"`
}

// KitChild is a kit built on this one.
type KitChild struct {
	Name   string `json:"name"`
	Pinned bool   `json:"pinned"`
}

// FileRow is one file of a version's directory.
type FileRow struct {
	Path  string `json:"path"`  // stored: the file= value
	Label string `json:"label"` // as shown
	Mode  string `json:"mode"`
	Size  int64  `json:"size"`
}

// FileList is a version's directory.
type FileList struct {
	N      int       `json:"n"`
	Size   string    `json:"size"`
	Rows   []FileRow `json:"rows"`
	Hidden int       `json:"hidden"` // files not listed
}

// FileChange is one changed file between two versions.
type FileChange struct {
	Change string `json:"change"` // added, removed, modified, mode
	Path   string `json:"path"`
	Label  string `json:"label"`
	Mode   string `json:"mode"`
	Size   string `json:"size"`
}

// FilesDiff compares two versions' directories.
type FilesDiff struct {
	From      int          `json:"from"`
	To        int          `json:"to"`
	Rows      []FileChange `json:"rows"`
	More      int          `json:"more"` // changed, not listed
	Unchanged int          `json:"unchanged"`
}

// KitFile is one file: its content, its diff, or why neither.
type KitFile struct {
	Path     string     `json:"path"`
	Diff     bool       `json:"diff"`
	From     int        `json:"from"`
	To       int        `json:"to"`
	Lines    []DiffLine `json:"lines"`
	TooLarge bool       `json:"tooLarge"`
	Content  string     `json:"content"`
	Note     string     `json:"note"`
	Missing  bool       `json:"missing"`
	Masked   bool       `json:"masked"`
	Hidden   bool       `json:"hidden"`
}

// DraftInfo is a kit's open draft.
type DraftInfo struct {
	Base int `json:"base"`
}

// KitDetail is a kit's page at a version (?v=), a diff (?diff=) and a file (?file=).
type KitDetail struct {
	Name       string          `json:"name"`
	Current    int             `json:"current"`
	Versions   []KitVersionRow `json:"versions"`
	View       KitView         `json:"view"`
	Diff       *KitDiff        `json:"diff" tstype:"KitDiff | null"`
	Uses       []KitUse        `json:"uses"`
	Chain      []KitParent     `json:"chain"`
	ChainErr   string          `json:"chainErr"`
	Effective  *KitView        `json:"effective" tstype:"KitView | null"`
	UsedByKits []KitChild      `json:"usedByKits"`
	IsDefault  bool            `json:"isDefault"`
	Source     *SourceInfo     `json:"source" tstype:"SourceInfo | null"`
	SetWith    string          `json:"setWith"`
	CanSource  bool            `json:"canSource"`
	Draft      *DraftInfo      `json:"draft" tstype:"DraftInfo | null"`
	Files      *FileList       `json:"files" tstype:"FileList | null"`
	FilesDiff  *FilesDiff      `json:"filesDiff" tstype:"FilesDiff | null"`
	File       *KitFile        `json:"file" tstype:"KitFile | null"`
	Attn       *AttnFlag       `json:"attn" tstype:"AttnFlag | null"`
}

// KitResult answers a kit write: the kit after it and its message.
type KitResult struct {
	Kit     KitDetail `json:"kit"`
	Message string    `json:"message"`
}

// DraftRow is one row of a draft's file tree: a directory or a file.
type DraftRow struct {
	Depth  int    `json:"depth"`
	Name   string `json:"name"` // the last segment, as shown
	Dir    bool   `json:"dir"`
	Path   string `json:"path"`  // stored (the file= value); "" for a directory
	Label  string `json:"label"` // the whole path, as shown
	Mode   string `json:"mode"`
	Size   int64  `json:"size"`
	Change string `json:"change"` // against the base: added, modified, mode, removed; "" unchanged
}

// DraftEditor is one draft file, opened: its text, or why it isn't editable.
type DraftEditor struct {
	Path      string `json:"path"` // stored
	Label     string `json:"label"`
	Mode      string `json:"mode"`
	Exec      bool   `json:"exec"`
	Size      int64  `json:"size"`
	ETag      string `json:"etag"` // the draft's etag it was read at
	Editable  bool   `json:"editable"`
	Text      string `json:"text"`
	Invisible bool   `json:"invisible"` // the text holds control or format characters
	Note      string `json:"note"`      // why it isn't editable
}

// DraftYAMLDiff is kit.yml's diff against the draft's base.
type DraftYAMLDiff struct {
	From     int        `json:"from"`
	Lines    []DiffLine `json:"lines"`
	TooLarge bool       `json:"tooLarge"`
	Hidden   bool       `json:"hidden"`
	Note     string     `json:"note"`
}

// Published is a just-published draft.
type Published struct {
	Version int      `json:"version"`
	Base    int      `json:"base"`
	Pinned  []string `json:"pinned"`
	Warning string   `json:"warning"`
}

// DraftView is a kit's open draft.
type DraftView struct {
	Base        int            `json:"base"`
	Current     int            `json:"current"`
	Superseded  bool           `json:"superseded"` // the kit's current version isn't the base
	ETag        string         `json:"etag"`
	Actor       string         `json:"actor"`
	Updated     string         `json:"updated"`
	MaxFile     string         `json:"maxFile"`
	Problems    []string       `json:"problems"`
	Size        string         `json:"size"`
	Tree        []DraftRow     `json:"tree"`
	Removed     []DraftRow     `json:"removed"`
	Editor      *DraftEditor   `json:"editor" tstype:"DraftEditor | null"`
	FileMissing bool           `json:"fileMissing"`
	Changes     *FilesDiff     `json:"changes" tstype:"FilesDiff | null"`
	YAMLDiff    *DraftYAMLDiff `json:"yamlDiff" tstype:"DraftYAMLDiff | null"`
	FileDiff    *KitFile       `json:"fileDiff" tstype:"KitFile | null"`
}

// DraftPage is a kit's Draft section; Draft nil = no draft.
type DraftPage struct {
	Name      string     `json:"name"`
	Current   int        `json:"current"`
	Versions  []int      `json:"versions"`
	Draft     *DraftView `json:"draft" tstype:"DraftView | null"`
	Published *Published `json:"published" tstype:"Published | null"`
}

// DraftResult answers a draft write: the section after it and its message.
type DraftResult struct {
	Page    DraftPage `json:"page"`
	Message string    `json:"message"`
}

// AgentListRow is one agent in the Agents list: an enrolled identity, a
// studio, or both.
type AgentListRow struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Kind       string    `json:"kind"`
	Project    string    `json:"project"`
	Role       string    `json:"role"`
	Unit       string    `json:"unit"`
	Status     string    `json:"status"`
	Connector  string    `json:"connector"`
	Image      string    `json:"image"`
	LastSeen   string    `json:"lastSeen"`   // "Jan 2 15:04:05"; "" = never
	MoreGrants int       `json:"moreGrants"` // grants beyond the first
	HasStudio  bool      `json:"hasStudio"`
	Attn       *AttnFlag `json:"attn" tstype:"AttnFlag | null"`
}

// AgentsResponse is GET /app/admin/api/agents.
type AgentsResponse struct {
	Agents  []AgentListRow `json:"agents"`
	Filters []KV           `json:"filters"` // status group key → label, in order ("" = all)
	Status  string         `json:"status"`  // the group shown
	CanEdit bool           `json:"canEdit"` // a supervisor runs: Raise and Teardown
}

// Enrolled answers an enrollment: the identity token, shown this once.
type Enrolled struct {
	ID    string `json:"id"`
	Token string `json:"token"`
}

// GrantRow is one grant an agent's token carries.
type GrantRow struct {
	Project      string   `json:"project"`
	Role         string   `json:"role"`
	Destinations []string `json:"destinations"`
}

// ShareRow is one share grant on a studio.
type ShareRow struct {
	UserID    string   `json:"userId"`
	User      string   `json:"user"`
	Rights    []string `json:"rights"`
	ExpiresAt string   `json:"expiresAt"`
	Expired   bool     `json:"expired"`
	GrantedBy string   `json:"grantedBy"`
}

// LiveRow is one open connection into a studio.
type LiveRow struct {
	Name  string `json:"name"`
	Right string `json:"right"`
	Since string `json:"since"`
}

// PreviewRow is one preview a studio exposes.
type PreviewRow struct {
	Name string `json:"name"`
	Port int    `json:"port"`
	URL  string `json:"url"`
}

// UserRefRow names a user.
type UserRefRow struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// AccessCard is who can reach a running studio and why.
type AccessCard struct {
	Enabled        bool         `json:"enabled"` // studio collaboration is on
	Requester      *UserRefRow  `json:"requester" tstype:"UserRefRow | null"`
	MembersPreview string       `json:"membersPreview"`
	MembersSSH     string       `json:"membersSsh"`
	RoleProject    string       `json:"roleProject"`
	RoleName       string       `json:"roleName"`
	Shares         []ShareRow   `json:"shares"`
	Live           []LiveRow    `json:"live"`
	PreviewsOn     bool         `json:"previewsOn"`
	Previews       []PreviewRow `json:"previews"`
}

// StreamRow is one captured session event stream.
type StreamRow struct {
	StreamID  string `json:"streamId"`
	Started   string `json:"started"`
	LastEvent string `json:"lastEvent"`
	Events    int    `json:"events"`
}

// AgentDetail is an agent's page: its identity, its studio while it runs,
// and its session's audit trail.
type AgentDetail struct {
	ID                 string      `json:"id"`
	Running            bool        `json:"running"`
	Status             string      `json:"status"`
	Kind               string      `json:"kind"`
	Owner              string      `json:"owner"`
	OwnerID            string      `json:"ownerId"`
	Name               string      `json:"name"`
	Project            string      `json:"project"`
	Role               string      `json:"role"`
	Unit               string      `json:"unit"`
	HasActor           bool        `json:"hasActor"`
	Grants             []GrantRow  `json:"grants"`
	TokenExpiry        string      `json:"tokenExpiry"` // "" = none
	Raised             string      `json:"raised"`
	LastSeen           string      `json:"lastSeen"`
	Lease              string      `json:"lease"`
	Backend            string      `json:"backend"`
	Location           string      `json:"location"`
	WaitingSince       string      `json:"waitingSince"` // "" = not waiting
	WaitSeq            int64       `json:"waitSeq"`
	EscalationTier     int         `json:"escalationTier"`
	TierPingedAt       string      `json:"tierPingedAt"` // "" = none open
	EscalationCategory string      `json:"escalationCategory"`
	Nags               int         `json:"nags"`
	LastNagAt          string      `json:"lastNagAt"`
	CommitSeq          int64       `json:"commitSeq"`
	EgressID           string      `json:"egressId"`
	EgressKnown        bool        `json:"egressKnown"`
	EgressFailures     int         `json:"egressFailures"`
	Access             AccessCard  `json:"access"`
	SessionsEnabled    bool        `json:"sessionsEnabled"`
	Streams            []StreamRow `json:"streams"`
	SquawksConfigured  bool        `json:"squawksConfigured"`
	Squawks            []SquawkRow `json:"squawks"`
	SquawksMore        bool        `json:"squawksMore"`
	CanEdit            bool        `json:"canEdit"`
	SetupEnabled       bool        `json:"setupEnabled"`
}

// SessionEvent is one event of an agent's session timeline: the same JSON as
// the user area's Thinking tab rows, so both render through one component.
type SessionEvent struct {
	Stream          string  `json:"stream"`
	Seq             uint64  `json:"seq"`
	Turn            uint32  `json:"turn"`
	At              int64   `json:"at"`   // unix ms; 0 = unknown
	Kind            string  `json:"kind"` // thinking | text | prompt | tool_call | tool_result | result | progress | gap | other
	Tool            string  `json:"tool"`
	ToolUseID       string  `json:"toolUseId"`
	Text            string  `json:"text"`
	Detail          string  `json:"detail"`
	DetailTruncated int     `json:"detailTruncated"`
	IsError         bool    `json:"isError"`
	TruncatedBytes  uint64  `json:"truncatedBytes"`
	CostUSD         float64 `json:"costUsd"`
	DurationMS      int64   `json:"durationMs"`
	ClaudeSession   string  `json:"claudeSession"`
}

// SessionTotals is a session's running totals.
type SessionTotals struct {
	Turns        uint32  `json:"turns"`
	Episodes     uint32  `json:"episodes"`
	ToolCalls    int     `json:"toolCalls"`
	InputTokens  int64   `json:"inputTokens"`
	OutputTokens int64   `json:"outputTokens"`
	CostUSD      float64 `json:"costUsd"`
}

// SessionInfo is an agent's timeline: its streams (newest first), the one
// shown, and its totals.
type SessionInfo struct {
	Enabled bool          `json:"enabled"` // session events are recorded on this Jam
	Streams []StreamRow   `json:"streams"`
	Stream  string        `json:"stream"`
	Totals  SessionTotals `json:"totals"`
}

// SessionPage is a page of a stream's events below a seq.
type SessionPage struct {
	Events []SessionEvent `json:"events"`
	Before uint64         `json:"before"` // older ones exist below it; 0 = none
}

// SessionRaw is one event's raw JSON (Truncated: bytes cut).
type SessionRaw struct {
	Raw       string `json:"raw"`
	Truncated int    `json:"truncated"`
}

// AttnRow is one Needs-attention item: its kind (broken, stale, config,
// alert), why, and the app path where it's fixed.
type AttnRow struct {
	Kind string `json:"kind"`
	Why  string `json:"why"`
	Path string `json:"path"`
}

// ManagerRow is one Studio Manager on the dashboard.
type ManagerRow struct {
	Name      string   `json:"name"`
	Connected bool     `json:"connected"`
	Version   string   `json:"version"`
	Studios   int      `json:"studios"`
	Capacity  int      `json:"capacity"` // 0 = unbounded
	LastSeen  string   `json:"lastSeen"` // "15:04:05"; "" = never
	Strays    []string `json:"strays"`   // its stray studios' actor ids
}

// DashStats is the dashboard's tiles: studios by status group (Attention:
// lost or terminating) and the control-plane objects.
type DashStats struct {
	Studios   int `json:"studios"`
	Running   int `json:"running"`
	Waiting   int `json:"waiting"`
	SettingUp int `json:"settingUp"`
	Idled     int `json:"idled"`
	Attention int `json:"attention"`
	Projects  int `json:"projects"`
	Agents    int `json:"agents"`
	Users     int `json:"users"`
	Specs     int `json:"specs"` // kits + destinations + model-specs
}

// Dashboard is the admin area's root page.
type Dashboard struct {
	Stats       DashStats    `json:"stats"`
	Attention   []AttnRow    `json:"attention"` // Jam-scope items
	JamContext  ContextPanel `json:"jamContext"`
	HasManagers bool         `json:"hasManagers"`
	Managers    []ManagerRow `json:"managers"`
	Coves       []CoveRow    `json:"coves"`
}

// HealthRow is one operator condition: open (Age: for how long) or resolved
// (Age: since when; Lasted: how long it was open).
type HealthRow struct {
	Key      string `json:"key"`
	Severity string `json:"severity"` // critical, warning, info
	Summary  string `json:"summary"`
	Fix      string `json:"fix"`
	Detail   string `json:"detail"`
	Age      string `json:"age"`
	Lasted   string `json:"lasted"`
}

// Health is the Health tab: open conditions and the last 7 days' resolved.
type Health struct {
	Open     []HealthRow `json:"open"`
	Resolved []HealthRow `json:"resolved"`
	Silences string      `json:"silences"` // Alertmanager's silences page; "" = not configured
}

// ChannelLog is the channel log (or the frozen legacy log), newest first,
// as its filters select.
type ChannelLog struct {
	Configured bool        `json:"configured"`
	Legacy     bool        `json:"legacy"`
	Filtered   bool        `json:"filtered"` // which empty state
	BadDate    bool        `json:"badDate"`  // a date didn't parse (left unbounded)
	Rows       []SquawkRow `json:"rows"`
}

// SearchHit is one search result: its name, context and app path. Exact:
// its name is the whole query.
type SearchHit struct {
	Title string `json:"title"`
	Sub   string `json:"sub"`
	Path  string `json:"path"`
	Exact bool   `json:"exact"`
}

// SearchGroup is one kind's hits: Count is every match, Hits capped; More
// is where the rest are (squawks: the channel log), else "".
type SearchGroup struct {
	Key   string      `json:"key"`
	Name  string      `json:"name"`
	Hits  []SearchHit `json:"hits"`
	Count int         `json:"count"`
	More  string      `json:"more"`
}

// SearchResult is a search. Jump is the app path of the single exact hit,
// else "".
type SearchResult struct {
	Q        string        `json:"q"`
	TooShort bool          `json:"tooShort"`
	Total    int           `json:"total"`
	Groups   []SearchGroup `json:"groups"`
	Jump     string        `json:"jump"`
}

// ConfigBranches is the branch bar's list: the followed branch first.
type ConfigBranches struct {
	Branches    []ConfigBranch `json:"branches"`
	Followed    string         `json:"followed"`    // the branch the Jam follows (read-only)
	DefaultName string         `json:"defaultName"` // Create's suggestion: jam/<operator>
	CanPR       bool           `json:"canPR"`
	RepoLink    string         `json:"repoLink"` // the repo's web page
	TreeLink    string         `json:"treeLink"` // a branch's page, less the branch name ("" = none)
	PRNoun      string         `json:"prNoun"`   // "pull request" (GitHub) or "merge request" (GitLab)
}

// ConfigBranch is one branch of the config repo.
type ConfigBranch struct {
	Name     string `json:"name"`
	Head     string `json:"head"`
	Short    string `json:"short"` // 7 chars
	Followed bool   `json:"followed"`
}

// ConfigBranchResult answers Create branch.
type ConfigBranchResult struct {
	Branch  ConfigBranch `json:"branch"`
	Message string       `json:"message"`
}

// ConfigMerge is a merge's outcome.
type ConfigMerge struct {
	Head        string `json:"head"` // the followed branch's head after it, 7 chars
	FastForward bool   `json:"fastForward"`
	NoOp        bool   `json:"noOp"`
}

// ConfigMergeResult answers Merge.
type ConfigMergeResult struct {
	Merge   ConfigMerge `json:"merge"`
	Message string      `json:"message"`
}

// ConfigPR is a branch's pull request ("" = none).
type ConfigPR struct {
	URL string `json:"url"`
}
