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
	Managed  *Managed     `json:"managed"` // nil: the config repo doesn't manage it
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
