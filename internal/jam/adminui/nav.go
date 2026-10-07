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

// subTab is one sub-tab of a section; Current marks the page's own.
type subTab struct {
	Label, Href string
	Current     bool
}

// navSubTabs are the sub-tabs of the sections that group several list pages,
// shown under the top bar on each of the section's pages. Each tab's Href is
// also its key: a page names the tab it belongs to (mustParseTab).
var navSubTabs = map[navSection][]subTab{
	navAgents: {{Label: "Studios", Href: "/ui/coves"}, {Label: "Actors", Href: "/ui/actors"}},
	navSpecs:  {{Label: "Kits", Href: "/ui/kits"}, {Label: "Destinations", Href: "/ui/destinations"}, {Label: "Model-specs", Href: "/ui/model-specs"}},
}

// subTabsFor is section's sub-tabs with tab (an Href) marked current; none for
// a section without sub-tabs or a page that names no tab.
func subTabsFor(section navSection, tab string) []subTab {
	if tab == "" {
		return nil
	}
	tabs := append([]subTab(nil), navSubTabs[section]...)
	for i := range tabs {
		tabs[i].Current = tabs[i].Href == tab
	}
	return tabs
}

// redirect answers a moved GET page with 301 to target, keeping the query.
func redirect(w http.ResponseWriter, r *http.Request, target string) {
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	http.Redirect(w, r, target, http.StatusMovedPermanently)
}
