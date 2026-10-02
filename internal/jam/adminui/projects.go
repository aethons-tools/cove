package adminui

import (
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/aethons-tools/cove/internal/jam"
)

// projectURL is the page path for a project.
func projectURL(name string) string { return "/ui/projects/" + url.PathEscape(orDefaultProject(name)) }

// projectChoices is what a project picker offers: every project, plus
// default, which is always valid (a write naming it creates it).
func projectChoices(store jam.Store) []string {
	out := store.ListProjects()
	if !slices.Contains(out, jam.DefaultProject) {
		out = append(out, jam.DefaultProject)
		slices.Sort(out)
	}
	return out
}

// projectRow is one projects-table row.
type projectRow struct {
	Name                   string
	Roles, Actors, Studios int
	Humans, Channels       int
	ChatService            string
	InUseBy                string // what blocks removal; "" when removable
}

// projectRef is the first thing (deterministically) that keeps project from
// being removed: a role in it, else an actor's grant into it. Mirrors the
// store's refusal, which only roles and grants trigger.
func projectRef(store jam.Store, project string) string {
	if roles := store.ListRoles(project); len(roles) > 0 {
		return fmt.Sprintf("role %s/%s", project, roles[0].Name)
	}
	for _, a := range store.ListActors() {
		for _, g := range a.Grants {
			if orDefaultProject(g.Project) == project {
				return fmt.Sprintf("actor %s's grant of %s/%s", a.ID, project, g.Role)
			}
		}
	}
	return ""
}

func projectRows(store jam.Store) []projectRow {
	studios := map[string]int{}
	for _, c := range jam.CoveSummaries(store) {
		studios[orDefaultProject(c.Project)]++
	}
	var out []projectRow
	for _, name := range store.ListProjects() {
		p, _ := store.GetProject(name)
		out = append(out, projectRow{
			Name: name, Roles: len(store.ListRoles(name)), Actors: len(projectHolders(store, name)),
			Studios: studios[name], Humans: len(p.Roster.Humans), Channels: len(p.Roster.Channels),
			ChatService: p.ChatService, InUseBy: projectRef(store, name),
		})
	}
	return out
}

// projectHolder is one actor with grants into the project.
type projectHolder struct {
	ID    string
	Roles []string
}

func projectHolders(store jam.Store, project string) []projectHolder {
	var out []projectHolder
	for _, a := range store.ListActors() {
		var roles []string
		for _, g := range a.Grants {
			if orDefaultProject(g.Project) == project {
				roles = append(roles, g.Role)
			}
		}
		if roles != nil {
			out = append(out, projectHolder{ID: a.ID, Roles: roles})
		}
	}
	return out
}

// escalationChain is one named chain of the project's escalation policy.
type escalationChain struct {
	Category string // "" = the default chain
	Tiers    []jam.EscalationTier
}

// projectDetail is the project page payload.
type projectDetail struct {
	Title       string
	Project     jam.Project
	Roles       []roleRow
	Holders     []projectHolder
	Coves       []jam.CoveSummary
	CanEdit     bool // always false: the page's studio table is read-only
	Escalation  []escalationChain
	InUseBy     string
	NotFound    bool
	NotFoundFor string
}

func buildProjectDetail(store jam.Store, name string) (projectDetail, bool) {
	p, ok := store.GetProject(name)
	if !ok {
		return projectDetail{}, false
	}
	d := projectDetail{Title: "Projects", Project: p, Holders: projectHolders(store, name), InUseBy: projectRef(store, name)}
	for _, r := range roleRows(store) {
		if r.Project == name {
			d.Roles = append(d.Roles, r)
		}
	}
	for _, c := range jam.CoveSummaries(store) {
		if orDefaultProject(c.Project) == name {
			d.Coves = append(d.Coves, c)
		}
	}
	if len(p.Escalation) > 0 {
		d.Escalation = append(d.Escalation, escalationChain{Tiers: p.Escalation})
	}
	for _, c := range slices.Sorted(maps.Keys(p.EscalationByCategory)) {
		d.Escalation = append(d.Escalation, escalationChain{Category: c, Tiers: p.EscalationByCategory[c]})
	}
	return d, true
}

func projectTableData(store jam.Store) map[string]any {
	return map[string]any{"Projects": projectRows(store)}
}

func registerProjects(mux *http.ServeMux, store jam.Store, log *slog.Logger, guardWrite func(http.ResponseWriter, *http.Request) bool) {
	mux.HandleFunc("GET /ui/projects", func(w http.ResponseWriter, r *http.Request) {
		data := projectTableData(store)
		data["Title"] = "Projects"
		render(w, "projects", data)
	})

	mux.HandleFunc("GET /ui/projects/{name}", func(w http.ResponseWriter, r *http.Request) {
		d, ok := buildProjectDetail(store, r.PathValue("name"))
		if !ok {
			renderStatus(w, http.StatusNotFound, "project", projectDetail{Title: "Projects", NotFound: true, NotFoundFor: r.PathValue("name")})
			return
		}
		render(w, "project", d)
	})

	mux.HandleFunc("POST /ui/projects", func(w http.ResponseWriter, r *http.Request) {
		if !guardWrite(w, r) {
			return
		}
		if err := r.ParseForm(); err != nil {
			renderError(w, http.StatusBadRequest, "invalid form")
			return
		}
		name := strings.TrimSpace(r.FormValue("name"))
		if err := store.CreateProject(name); err != nil {
			renderError(w, jam.WriteStatus(err, http.StatusBadRequest), err.Error())
			return
		}
		log.Info("ui project created", "operator", jam.OperatorID(r), "project", name)
		w.Header().Set("HX-Redirect", projectURL(name))
		renderFragment(w, "projects", "projects-table", projectTableData(store))
	})

	mux.HandleFunc("DELETE /ui/projects/{name}", func(w http.ResponseWriter, r *http.Request) {
		if !guardWrite(w, r) {
			return
		}
		name := r.PathValue("name")
		if err := store.RemoveProject(name); err != nil {
			renderError(w, jam.WriteStatus(err, http.StatusBadRequest), err.Error())
			return
		}
		log.Info("ui project removed", "operator", jam.OperatorID(r), "project", name)
		renderFragment(w, "projects", "projects-table", projectTableData(store))
	})
}
