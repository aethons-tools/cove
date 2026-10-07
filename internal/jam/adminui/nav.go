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

// railView is the rail. Scope is the current scope as the rail's poll query
// carries it: "" none, "~" Jam, else a project name.
type railView struct {
	Jam      railEntry
	Projects []railEntry
	Scope    string
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

// railScopeJam is the rail poll's scope value for Jam.
const railScopeJam = "~"

// buildRail lays out the rail with scope ("" none, railScopeJam, or a project)
// current; items are every attention item.
func buildRail(store jam.Store, scope string, items []attnItem) railView {
	rv := railView{Scope: scope, Jam: railEntry{Name: "Jam", Href: "/ui/", Current: scope == railScopeJam, Badge: badgeOf(inScope(items, "", ""))}}
	for _, p := range store.ListProjects() {
		rv.Projects = append(rv.Projects, railEntry{Name: p, Href: projectURL(p), Current: scope == p, Badge: badgeOf(inScope(items, p, ""))})
	}
	return rv
}

// frameFor builds a page's frame from its meta and request. A project page
// names its project in the URL (/ui/projects/{name}[/section] or
// /ui/projects/{project}/roles/{name}); an agent page is in a project's scope
// when linked with ?project=.
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
		f.Project, tab = projectFromPath(r.URL.Path)
	}
	switch f.Kind {
	case scopeJam:
		f.Title, f.Items = "Jam", inScope(items, "", "")
		for _, t := range jamTabs {
			f.Tabs = append(f.Tabs, tabView{Label: t.Label, Href: t.Href, Current: t.Key == tab, Badge: badgeOf(inScope(items, "", t.Key))})
		}
		f.Rail = buildRail(src.store, railScopeJam, items)
	case scopeProject:
		f.Title, f.Items = f.Project, inScope(items, f.Project, "")
		for _, s := range projectSections {
			key := string(s.Section)
			f.Tabs = append(f.Tabs, tabView{Label: s.Label, Href: projectSectionURL(f.Project, s.Section), Current: key == tab,
				Badge: badgeOf(inScope(items, f.Project, key))})
		}
		f.Rail = buildRail(src.store, f.Project, items)
	default:
		f.Rail = buildRail(src.store, "", items)
	}
	if meta.SubTab != "" {
		for _, s := range specsSubTabs {
			s.Current = s.Href == meta.SubTab
			f.SubTabs = append(f.SubTabs, s)
		}
	}
	return f
}

// projectFromPath reads a project page's project and tab from its path:
// /ui/projects/{p} (overview), /ui/projects/{p}/{section}, and
// /ui/projects/{p}/roles/{role} (roles).
func projectFromPath(path string) (project, tab string) {
	parts := strings.Split(strings.TrimPrefix(path, "/ui/projects/"), "/")
	project, _ = url.PathUnescape(parts[0])
	tab = string(sectionOverview)
	if len(parts) > 1 && parts[1] != "" {
		tab = parts[1]
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
