package adminui

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/aethons-tools/cove/internal/jam"
)

// scopeKind is what a page belongs to in the rail: Jam, one project, or —
// search — neither.
type scopeKind int

const (
	scopeNone scopeKind = iota
	scopeJam
	scopeProject
)

// pageMeta places a page in the frame: its scope kind, the scope tab it sits
// under, and — for Specs — the sub-tab (an Href). A project page's project and
// tab come from its URL (frameFor).
type pageMeta struct {
	Kind   scopeKind
	Tab    string
	SubTab string
}

// tabDef is one tab of a scope.
type tabDef struct{ Key, Label, Href string }

// jamTabs are Jam's tabs, in order.
var jamTabs = []tabDef{
	{"dashboard", "Dashboard", "/ui/"},
	{"agents", "Agents", "/ui/agents"},
	{"users", "Users", "/ui/users"},
	{"specs", "Specs", "/ui/specs"},
	{"intercom", "Intercom", "/ui/intercom"},
}

// subTab is one sub-tab of a tab; Current marks the page's own.
type subTab struct {
	Label, Href string
	Current     bool
}

// specsSubTabs are the Specs tab's sub-tabs; each Href is also its key.
var specsSubTabs = []subTab{{Label: "Kits", Href: "/ui/kits"}, {Label: "Destinations", Href: "/ui/destinations"}, {Label: "Model-specs", Href: "/ui/model-specs"}}

// tabView is one rendered tab.
type tabView struct {
	Label, Href string
	Current     bool
	Badge       attnBadge
}

// railEntry is one rail row: Jam or a project.
type railEntry struct {
	Name, Href string
	Current    bool
	Badge      attnBadge
}

// railView is the rail. Poll is the URL its entry lists re-fetch themselves
// from, carrying the current scope: ?jam=1 for Jam, ?scope=<project> for a
// project, neither for none.
type railView struct {
	Jam      railEntry
	Projects []railEntry
	Poll     string
}

// frame is everything around a page's content: the rail, the scope's title
// and tabs (with their badges), the Specs sub-tabs, and the attention items —
// All of them (row flags) and the scope's own (Needs attention).
type frame struct {
	Kind    scopeKind
	Project string // the project scope's name
	Title   string // the scope's title
	Tabs    []tabView
	SubTabs []subTab
	Rail    railView
	All     []attnItem
	Items   []attnItem
}

// buildRail lays out the rail with the scope kind current (for a project,
// the named one); items are every attention item.
func buildRail(store jam.Store, kind scopeKind, project string, items []attnItem) railView {
	q := url.Values{}
	switch kind {
	case scopeJam:
		q.Set("jam", "1")
	case scopeProject:
		q.Set("scope", project)
	}
	rv := railView{Poll: "/ui/rail", Jam: railEntry{Name: "Jam", Href: "/ui/", Current: kind == scopeJam, Badge: badgeOf(inScope(items, "", ""))}}
	if len(q) > 0 {
		rv.Poll += "?" + q.Encode()
	}
	for _, p := range store.ListProjects() {
		rv.Projects = append(rv.Projects, railEntry{Name: p, Href: projectURL(p), Current: kind == scopeProject && project == p, Badge: badgeOf(inScope(items, p, ""))})
	}
	return rv
}

// railFor is the rail a poll asks for (see railView.Poll).
func railFor(r *http.Request, store jam.Store, items []attnItem) railView {
	q := r.URL.Query()
	switch {
	case q.Get("jam") == "1":
		return buildRail(store, scopeJam, "", items)
	case q.Has("scope"):
		return buildRail(store, scopeProject, q.Get("scope"), items)
	}
	return buildRail(store, scopeNone, "", items)
}

// frameFor builds a page's frame from its meta and request. A project page
// names its project in its route (/ui/projects/{name}[/section] or
// /ui/projects/{project}/roles/{name}) — a missing project leaves the page
// in no scope; an agent page is in a project's scope when linked with
// ?project=.
func frameFor(r *http.Request, meta pageMeta, src frameSource) frame {
	items := attention(src.store, src.img)
	f := frame{Kind: meta.Kind, All: items}
	if meta.Kind == scopeJam && meta.Tab == "agents" {
		if p := r.URL.Query().Get("project"); p != "" {
			if _, ok := src.store.GetProject(p); ok {
				f.Kind, f.Project = scopeProject, p
			}
		}
	}
	tab := meta.Tab
	if meta.Kind == scopeProject {
		f.Project, tab = projectFromRoute(r)
		if _, ok := src.store.GetProject(f.Project); !ok {
			f.Kind, f.Project = scopeNone, ""
		}
	}
	switch f.Kind {
	case scopeJam:
		f.Title, f.Items = "Jam", inScope(items, "", "")
		for _, t := range jamTabs {
			f.Tabs = append(f.Tabs, tabView{Label: t.Label, Href: t.Href, Current: t.Key == tab, Badge: badgeOf(inScope(items, "", t.Key))})
		}
		f.Rail = buildRail(src.store, scopeJam, "", items)
	case scopeProject:
		f.Title, f.Items = f.Project, inScope(items, f.Project, "")
		for _, s := range projectSections {
			key := string(s.Section)
			f.Tabs = append(f.Tabs, tabView{Label: s.Label, Href: projectSectionURL(f.Project, s.Section), Current: key == tab,
				Badge: badgeOf(inScope(items, f.Project, key))})
		}
		f.Rail = buildRail(src.store, scopeProject, f.Project, items)
	default:
		f.Rail = buildRail(src.store, scopeNone, "", items)
	}
	if meta.SubTab != "" {
		for _, s := range specsSubTabs {
			s.Current = s.Href == meta.SubTab
			f.SubTabs = append(f.SubTabs, s)
		}
	}
	return f
}

// projectFromRoute reads a project page's project and tab from its route:
// /ui/projects/{name} (overview), /ui/projects/{name}/{section}, and
// /ui/projects/{project}/roles/{name} (roles). The project is the route's
// decoded {project} wildcard, else its {name} — never re-unescaped; the tab
// is the pattern's segment after it.
func projectFromRoute(r *http.Request) (project, tab string) {
	if project = r.PathValue("project"); project == "" {
		project = r.PathValue("name")
	}
	pat := r.Pattern
	if i := strings.Index(pat, " "); i >= 0 {
		pat = pat[i+1:]
	}
	tab = string(sectionOverview)
	if rest, ok := strings.CutPrefix(pat, "/ui/projects/{"); ok {
		_, rest, _ = strings.Cut(rest, "}")
		if seg, _, _ := strings.Cut(strings.TrimPrefix(rest, "/"), "/"); seg != "" {
			tab = seg
		}
	}
	return project, tab
}

// frameSource is what frameFor reads, carried on each request's context by
// Handler so the renderer can build the frame.
type frameSource struct {
	store jam.Store
	img   jam.ImageResolver
}

type frameKey struct{}

func withFrameSource(r *http.Request, src frameSource) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), frameKey{}, src))
}

func frameSourceOf(r *http.Request) (frameSource, bool) {
	src, ok := r.Context().Value(frameKey{}).(frameSource)
	return src, ok
}

// redirect answers a moved GET page with 301 to target, keeping the query.
func redirect(w http.ResponseWriter, r *http.Request, target string) {
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	http.Redirect(w, r, target, http.StatusMovedPermanently)
}
