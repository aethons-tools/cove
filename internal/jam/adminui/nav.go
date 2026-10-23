package adminui

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/condition"
)

// scopeKind is what a page belongs to: Jam (a title-bar tab, full width),
// one project, the projects list, or — search — none.
type scopeKind int

const (
	scopeNone scopeKind = iota
	scopeJam
	scopeProject
	scopeProjects
)

// pageMeta places a page in the frame: its scope kind, the title-bar tab it
// sits under (a Jam page's), and — for Specs — the sub-tab (an Href). A
// project page's project and tab come from its URL (frameFor).
type pageMeta struct {
	Kind   scopeKind
	Tab    string
	SubTab string
}

// subTab is one sub-tab of a tab; Current marks the page's own.
type subTab struct {
	Label, Href string
	Current     bool
}

// specsSubTabs are the Specs tab's sub-tabs; each Href is also its key.
var specsSubTabs = []subTab{{Label: "Kits", Href: "/app/admin/kits"}, {Label: "Destinations", Href: "/app/admin/destinations"}, {Label: "Model-specs", Href: "/app/admin/model-specs"}}

// tabView is one rendered project tab.
type tabView struct {
	Label, Href string
	Current     bool
	Badge       attnBadge
}

// railEntry is one rail row: a project.
type railEntry struct {
	Name, Href string
	Current    bool
	Badge      attnBadge
}

// railView is the rail: the projects. Poll is the URL its entry list
// re-fetches itself from, carrying the selected project (?scope=<project>),
// if any.
type railView struct {
	Projects []railEntry
	Poll     string
}

// frame is everything around a page's content: the title-bar tab it sits
// under, the rail (Projects only), a project's title and tabs (with their
// badges), the Specs sub-tabs, and the attention items — All of them (row
// flags) and the scope's own (Needs attention).
type frame struct {
	Kind     scopeKind
	Brand    string // the tab title's name, and the title bar's without a shell (frameSource.brand)
	TitleTab string // the title-bar tab key (appui.TitlebarFor)
	Project  string // the project scope's name
	Title    string // the project's title
	Tabs     []tabView
	SubTabs  []subTab
	Rail     *railView // nil: no rail, the page is full width
	All      []attnItem
	Items    []attnItem
	Marks    map[jam.ConfigKey]jam.ConfigMark // the managed marks: badges and drift warnings
}

// buildRail lays out the rail of projects with selected current (none when
// ""); items are every attention item.
func buildRail(src frameSource, selected string, items []attnItem) railView {
	rv := railView{Poll: "/app/admin/rail"}
	if selected != "" {
		rv.Poll += "?" + url.Values{"scope": {selected}}.Encode()
	}
	for _, p := range src.store.ListProjects() {
		rv.Projects = append(rv.Projects, railEntry{Name: p, Href: projectURL(p), Current: selected != "" && p == selected, Badge: badgeOf(inScope(items, p, ""))})
	}
	return rv
}

// railFor is the rail a poll asks for (see railView.Poll).
func railFor(r *http.Request, src frameSource, items []attnItem) railView {
	return buildRail(src, r.URL.Query().Get("scope"), items)
}

// frameFor builds a page's frame from its meta and request. A project page
// names its project in its route (/app/admin/projects/{name}[/section] or
// /app/admin/projects/{project}/roles/{name}) — a missing project leaves the page
// in no scope; an agent page is in a project's scope when linked with
// ?project=.
func frameFor(r *http.Request, meta pageMeta, src frameSource) frame {
	items := src.items()
	f := frame{Kind: meta.Kind, TitleTab: meta.Tab, All: items, Brand: src.brand(), Marks: src.marks()}
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
		if _, ok := src.store.GetProject(f.Project); !ok { // a missing project's page stays under Projects
			f.Kind, f.Project = scopeProjects, ""
		}
	}
	switch f.Kind {
	case scopeJam: // the title bar carries Jam's tabs
		f.Items = inScope(items, "", "")
	case scopeProject:
		f.TitleTab, f.Title, f.Items = "projects", f.Project, inScope(items, f.Project, "")
		for _, s := range projectSections {
			key := string(s.Section)
			f.Tabs = append(f.Tabs, tabView{Label: s.Label, Href: projectSectionURL(f.Project, s.Section), Current: key == tab,
				Badge: badgeOf(inScope(items, f.Project, key))})
		}
		rail := buildRail(src, f.Project, items)
		f.Rail = &rail
	case scopeProjects:
		f.TitleTab = "projects"
		rail := buildRail(src, "", items)
		f.Rail = &rail
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
// /app/admin/projects/{name} (overview), /app/admin/projects/{name}/{section}, and
// /app/admin/projects/{project}/roles/{name} (roles). The project is the route's
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
	if rest, ok := strings.CutPrefix(pat, "/app/admin/projects/{"); ok {
		_, rest, _ = strings.Cut(rest, "}")
		if seg, _, _ := strings.Cut(strings.TrimPrefix(rest, "/"), "/"); seg != "" {
			tab = seg
		}
	}
	if tab == string(sectionRoles) { // a role page sits under Agents
		tab = string(sectionAgents)
	}
	return project, tab
}

// frameSource is what frameFor reads, carried on each request's context by
// Handler so the renderer can build the frame.
type frameSource struct {
	store jam.Store
	img   jam.ImageResolver
	name  string // the Jam's display name ("" = none)
	conds *condition.Tracker
}

// items is every attention item: Jam's open operator conditions first, then
// attention()'s.
func (s frameSource) items() []attnItem {
	return append(conditionItems(s.conds.Open()), attention(s.store, s.img)...)
}

// marks is every managed mark by object.
func (s frameSource) marks() map[jam.ConfigKey]jam.ConfigMark {
	out := map[jam.ConfigKey]jam.ConfigMark{}
	for _, mk := range s.store.ListConfigMarks() {
		out[mk.Key()] = mk
	}
	return out
}

// brand is the tab title's (and, without a shell, the title bar's) name: "<display name> Jam", else
// "Jam".
func (s frameSource) brand() string {
	if s.name != "" {
		return s.name + " Jam"
	}
	return "Jam"
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
