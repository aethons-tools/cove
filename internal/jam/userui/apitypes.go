package userui

import "github.com/aethons-tools/cove/internal/jam"

// This file holds every type the /app/user/api JSON carries. tygo reads only
// this file (tygo.yaml) to generate web/app/src/api/types.ts: keep JSON types
// here, give every field a json tag, and run `go generate ./internal/jam/userui/`
// after a change. Lists are never nil in a response (types.ts says T[]).

// APIError is every /app/user/api error body.
type APIError struct {
	Error string `json:"error"` // for people
	Code  string `json:"code"`  // for code: forbidden, not_found, bad_request, internal
}

// Bootstrap is what the app needs before its first render: injected into
// index.html by webapp, and served at GET /app/user/api/bootstrap (the dev
// server's index has none).
type Bootstrap struct {
	Brand     string   `json:"brand"`
	User      BootUser `json:"user"`
	Roles     string   `json:"roles"` // "member" or "member · admin"
	Admin     bool     `json:"admin"`
	SignOut   bool     `json:"signOut"`   // offer Sign out (/app/auth/logout)
	Tabs      []NavTab `json:"tabs"`      // the title bar's links
	AttachMax int64    `json:"attachMax"` // per-file cap; 0 = attachments off
	PushKey   string   `json:"pushKey"`   // VAPID public key; "" = Web Push off
	Boards    bool     `json:"boards"`    // whiteboards on and the editor built
}

// BootUser is the signed-in member.
type BootUser struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Initial string `json:"initial"`
}

// NavTab is one title-bar link (appui.Tab, flattened).
type NavTab struct {
	Key        string `json:"key"`
	Label      string `json:"label"`
	Href       string `json:"href"`
	Badge      int    `json:"badge"`
	BadgeRed   bool   `json:"badgeRed"`
	BadgeTitle string `json:"badgeTitle"`
}

// RailResponse is GET /app/user/api/rail.
type RailResponse struct {
	Groups      []RailGroup `json:"groups"`
	UnreadTotal int         `json:"unreadTotal"`
}

// ChannelRow is one channel in the left rail, from the viewer's perspective.
type ChannelRow struct {
	ID       string          `json:"id"`
	Label    string          `json:"label"`
	Project  string          `json:"project"`
	Kind     jam.ChannelKind `json:"kind"`
	NeedsYou bool            `json:"needsYou"`
	Phase    string          `json:"phase"`
	Unread   int             `json:"unread"`
	OpenAsks int             `json:"openAsks"`
	LastSeq  int64           `json:"lastSeq"`
	Selected bool            `json:"selected"`
	Mission  string          `json:"mission"` // the backing session's one-line focus; "" = none
}

// RailGroup is one attention section of the rail (Needs you / Sessions /
// Channels / History), non-empty.
type RailGroup struct {
	Bucket jam.AttentionBucket `json:"bucket"`
	Title  string              `json:"title"`
	Rows   []ChannelRow        `json:"rows"`
}
