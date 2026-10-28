package appui

import (
	"context"
	"net/http"
	"strings"
	"unicode/utf8"
)

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

// Titlebar is a principal's title bar: what both areas' bootstraps carry.
type Titlebar struct {
	Brand   string // "{display name} Jam", else "Jam"
	Tabs    []Tab
	Name    string // Principal.DisplayName()
	Initial string // first letter of Name for the avatar; "?" when empty
	Roles   string // "member", "admin" or "member · admin"
	SignOut bool   // false for a Local admin or with no browser login
}

// Shell is what the gate puts on the context for the title bar.
type Shell struct {
	Brand       string
	SignOut     bool                                 // browser login configured
	AdminBadges func(*http.Request) map[string]Badge // nil = none
}

type shellKey struct{}

// WithShell carries s on each request to next.
func WithShell(next http.Handler, s Shell) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), shellKey{}, s)))
	})
}

var userTabs = []Tab{{Key: "inbox", Label: "Inbox", Href: "/app/user/"}}

// adminTabs are the admin app's title-bar tabs, in both areas.
var adminTabs = []Tab{
	{Key: "dashboard", Label: "Dashboard", Href: "/app/admin/"},
	{Key: "config", Label: "Config", Href: "/app/admin/config/tracking"},
	{Key: "agents", Label: "Agents", Href: "/app/admin/agents"},
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
