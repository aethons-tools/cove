package appui

import (
	"context"
	_ "embed"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/aethons-tools/cove/internal/jam/uiassets"
)

// TitlebarTemplate defines {{define "app-titlebar"}} over a Titlebar; the
// admin and user UIs parse it into their page templates.
//
//go:embed titlebar.html
var TitlebarTemplate string

// Badge is a count pill on a tab; N == 0 renders nothing.
type Badge struct {
	N     int
	Red   bool
	Title string
}

// Tab is one title-bar link.
type Tab struct {
	Key, Label, Href string
	Current          bool
	Badge            Badge
}

// Titlebar is the data the "app-titlebar" template renders.
type Titlebar struct {
	Brand   string // "{display name} Jam", else "Jam"
	Tabs    []Tab
	Name    string // Principal.DisplayName()
	Initial string // first letter of Name for the avatar; "?" when empty
	Roles   string // "member", "admin" or "member · admin"
	SignOut bool   // false for a Local admin or with no browser login
	// PushScript is push.js's URL when the menu offers this device's
	// Notifications toggle (a member, push configured); "" hides it.
	PushScript string
	PushUser   string // the member's user id, so push.js keys its per-session state by user
	// SoundsScript is sounds.js's URL for a member: the menu's per-device
	// Sounds settings, and the chimes the Inbox plays.
	SoundsScript string
}

// Shell is what the gate puts on the context for the title bar.
type Shell struct {
	Brand       string
	SignOut     bool                                 // browser login configured
	AdminBadges func(*http.Request) map[string]Badge // nil = none
	Push        bool                                 // Web Push configured: members get the Notifications toggle
}

type shellKey struct{}

// WithShell carries s on each request to next.
func WithShell(next http.Handler, s Shell) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), shellKey{}, s)))
	})
}

var userTabs = []Tab{{Key: "inbox", Label: "Inbox", Href: "/app/user/"}}

var adminTabs = []Tab{
	{Key: "dashboard", Label: "Dashboard", Href: "/app/admin/"},
	{Key: "projects", Label: "Projects", Href: "/app/admin/projects"},
	{Key: "agents", Label: "Agents", Href: "/app/admin/agents"},
	{Key: "users", Label: "Users", Href: "/app/admin/users"},
	{Key: "specs", Label: "Specs", Href: "/app/admin/specs"},
	{Key: "config", Label: "Config", Href: "/app/admin/config"},
	{Key: "health", Label: "Health", Href: "/app/admin/health"},
}

// TitlebarFor builds the title bar for r's principal, marking current (a tab
// key) as the open page.
func TitlebarFor(r *http.Request, current string) Titlebar {
	p, _ := PrincipalFrom(r)
	sh, _ := r.Context().Value(shellKey{}).(Shell)
	tb := Titlebar{Brand: brandOr(sh.Brand), Name: p.DisplayName(), SignOut: sh.SignOut && !p.Local}
	tb.Initial = "?"
	if c, _ := utf8.DecodeRuneInString(tb.Name); c != utf8.RuneError {
		tb.Initial = strings.ToUpper(string(c))
	}
	var roles []string
	if p.User != nil {
		roles = append(roles, "member")
		tb.Tabs = append(tb.Tabs, userTabs...)
		tb.SoundsScript = uiassets.ScriptHref("/app/static/", "sounds.js")
		if sh.Push {
			tb.PushScript = uiassets.ScriptHref("/app/static/", "push.js")
			tb.PushUser = string(p.User.UserID)
		}
	}
	if p.Admin {
		roles = append(roles, "admin")
		var badges map[string]Badge
		if sh.AdminBadges != nil {
			badges = sh.AdminBadges(r)
		}
		for _, t := range adminTabs {
			t.Badge = badges[t.Key]
			tb.Tabs = append(tb.Tabs, t)
		}
	}
	tb.Roles = strings.Join(roles, " · ")
	for i := range tb.Tabs {
		tb.Tabs[i].Current = tb.Tabs[i].Key == current
	}
	return tb
}
