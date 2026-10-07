package adminui

import "net/http"

// navSection is a top-nav section. Each page's template set is bound to one
// (mustParse), and the layout highlights the nav item whose Section matches —
// so a detail page highlights its section whatever its title.
type navSection string

const (
	navNone      navSection = "" // highlights nothing (search)
	navDashboard navSection = "dashboard"
	navProjects  navSection = "projects"
	navUsers     navSection = "users"
	navAgents    navSection = "agents"
	navSpecs     navSection = "specs"
	navIntercom  navSection = "intercom"
)

// navItem is one top-nav link.
type navItem struct {
	Section navSection
	Label   string
	Href    string
}

// navItems is the top nav, in order. Agents and Specs land on today's studio
// and kit lists until their own pages exist.
var navItems = []navItem{
	{navDashboard, "Dashboard", "/ui/"},
	{navProjects, "Projects", "/ui/projects"},
	{navUsers, "Users", "/ui/users"},
	{navAgents, "Agents", "/ui/coves"},
	{navSpecs, "Specs", "/ui/kits"},
	{navIntercom, "Intercom", "/ui/intercom"},
}

// redirect answers a moved GET page with 301 to target, keeping the query.
func redirect(w http.ResponseWriter, r *http.Request, target string) {
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	http.Redirect(w, r, target, http.StatusMovedPermanently)
}
