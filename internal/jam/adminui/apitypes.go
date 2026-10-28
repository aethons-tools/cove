package adminui

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

// Managed marks an object the tracked config repo manages: the commit it was
// applied from (7 chars) and when.
type Managed struct {
	Commit  string `json:"commit"`
	Applied string `json:"applied"`
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
	ID   string `json:"id"`
	Path string `json:"path"`
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
	Managed  *Managed     `json:"managed" tstype:"Managed | null"` // nil: the config repo doesn't manage it
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
	State   string `json:"state"` // in-sync, drifted, missing; "" = unknown
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
	Action    string     `json:"action"`
	Kind      string     `json:"kind"`
	Name      string     `json:"name"`
	Path      string     `json:"path"` // the object's page; "" for a create or none
	File      string     `json:"file"` // in the repo
	Drift     bool       `json:"drift"`
	Unmanaged bool       `json:"unmanaged"`
	Lines     []DiffLine `json:"lines"`    // Jam → repo
	TooLarge  bool       `json:"tooLarge"` // too large to diff here
	Diff      string     `json:"diff"`     // a kit update's version diff (text)
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
	Forced     bool           `json:"forced"`
	HasDrift   bool           `json:"hasDrift"` // offer Force
	Changes    int            `json:"changes"`
	Unchanged  int            `json:"unchanged"`
	Ops        []PlanOpRow    `json:"ops"`
	Forget     []ConfigKeyRow `json:"forget"`
	Unmanaged  []ConfigKeyRow `json:"unmanaged"`
	KitSources []KitSourceRow `json:"kitSources"`
	Refusals   []string       `json:"refusals"` // any one blocks Apply
	Serve      ServeDiff      `json:"serve"`
}

// Tracking is Config › Tracking: the tracked source, the managed objects and
// (after a Track) the plan.
type Tracking struct {
	Source      *TrackingSource `json:"source" tstype:"TrackingSource | null"` // nil = none tracked
	Rows        []ManagedRow    `json:"rows"`
	Drifted     int             `json:"drifted"`
	StatesErr   bool            `json:"statesErr"` // could not compare with the live Jam
	LastCommit  string          `json:"lastCommit"`
	LastApplied string          `json:"lastApplied"`
	CanGit      bool            `json:"canGit"` // Diff, Apply and Track are offered
	Plan        *Plan           `json:"plan" tstype:"Plan | null"`
}

// TrackingResult answers a tracking write: the page after it and its message.
type TrackingResult struct {
	Tracking Tracking `json:"tracking"`
	Message  string   `json:"message"`
}

// ProjectRow is one project in the list.
type ProjectRow struct {
	Name    string   `json:"name"`
	Members int      `json:"members"`
	Roles   int      `json:"roles"`
	Rooms   int      `json:"rooms"`
	Managed *Managed `json:"managed" tstype:"Managed | null"`
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
	Managed       *Managed          `json:"managed" tstype:"Managed | null"` // its config object's mark
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
	At        string `json:"at"`
	From      string `json:"from"`
	FromID    string `json:"fromId"`
	AgentID   string `json:"agentId"` // the author's agent, else ""
	Channel   string `json:"channel"`
	ChannelID string `json:"channelId"`
	Project   string `json:"project"`
	Body      string `json:"body"` // squawkrender's sanitized HTML
	Plain     bool   `json:"plain"`
}

// ProjectDetail is a project's pages: its overview and sections.
type ProjectDetail struct {
	Name          string       `json:"name"`
	Managed       *Managed     `json:"managed" tstype:"Managed | null"`
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
