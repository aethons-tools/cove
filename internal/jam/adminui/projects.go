package adminui

import (
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/sessionctx"
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
	Members, Channels      int
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
	for _, c := range jam.CoveSummaries(store, nil) {
		studios[orDefaultProject(c.Project)]++
	}
	var out []projectRow
	for _, name := range store.ListProjects() {
		p, _ := store.GetProject(name)
		out = append(out, projectRow{
			Name: name, Roles: len(store.ListRoles(name)), Actors: len(projectHolders(store, name)),
			Studios: studios[name], Members: len(store.ListMembers(p.ID)), Channels: len(store.ListChannels(p.ID, jam.SourceRoom)),
			ChatService: chatServiceName(store, p), InUseBy: projectRef(store, name),
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

// projectDetail is the project page payload.
type projectDetail struct {
	Title        string
	Project      jam.Project
	Roles        []roleRow
	Holders      []projectHolder
	Coves        []jam.CoveSummary
	CanEdit      bool // always false: the page's studio table is read-only
	Members      []memberRow
	Rooms        []jam.RoomView
	Escalation   []chainView // chains with at least one tier
	DefaultChain chainView   // the default chain, possibly empty (its editor is always offered)
	ChatService  string      // the chat-service connection's name; "" = none
	ChatServices []string    // select options: "" plus the chat-kind connections
	InUseBy      string
	NotFound     bool
	NotFoundFor  string
	Context      contextPanel // the project's session-context card
}

func buildProjectDetail(store jam.Store, img jam.ImageResolver, name string) (projectDetail, bool) {
	p, ok := store.GetProject(name)
	if !ok {
		return projectDetail{}, false
	}
	d := projectDetail{Title: "Projects", Project: p, Holders: projectHolders(store, name), InUseBy: projectRef(store, name)}
	d.Context = newContextPanel("project", "/ui/projects/"+name+"/context", "project", p.Context, p.Resources, sessionctx.BudgetProject, true)
	for _, r := range roleRows(store) {
		if r.Project == name {
			d.Roles = append(d.Roles, r)
		}
	}
	for _, c := range jam.CoveSummaries(store, img) {
		if orDefaultProject(c.Project) == name {
			d.Coves = append(d.Coves, c)
		}
	}
	members := jam.MembersOf(store, p.ID)
	for _, m := range members {
		d.Members = append(d.Members, memberRow{UserID: m.User.ID, Name: m.User.Name, Handle: m.Handle,
			Delivery: m.Delivery, DeliverySpec: lines(m.Delivery, jam.FormatDeliverySpec)})
	}
	d.Rooms = jam.ListRooms(store, p)
	known := knownTargets(members, d.Rooms)
	d.DefaultChain = chain("", p.Escalation, known)
	if len(p.Escalation) > 0 {
		d.Escalation = append(d.Escalation, d.DefaultChain)
	}
	for _, c := range slices.Sorted(maps.Keys(p.EscalationByCategory)) {
		if tiers := p.EscalationByCategory[c]; len(tiers) > 0 { // a cleared chain has none
			d.Escalation = append(d.Escalation, chain(c, tiers, known))
		}
	}
	d.ChatService = chatServiceName(store, p)
	d.ChatServices = chatServiceChoices(store, d.ChatService)
	return d, true
}

func projectTableData(store jam.Store) map[string]any {
	return map[string]any{"Projects": projectRows(store)}
}

func registerProjects(mux *http.ServeMux, store jam.Store, img jam.ImageResolver, log *slog.Logger, guardWrite func(http.ResponseWriter, *http.Request) bool) {
	mux.HandleFunc("GET /ui/projects", func(w http.ResponseWriter, r *http.Request) {
		data := projectTableData(store)
		data["Title"] = "Projects"
		render(w, "projects", data)
	})

	mux.HandleFunc("GET /ui/projects/{name}", func(w http.ResponseWriter, r *http.Request) {
		d, ok := buildProjectDetail(store, img, r.PathValue("name"))
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

// chatServiceName is the name of p's chat-service connection ("" = none).
func chatServiceName(store jam.Store, p jam.Project) string {
	if p.ChatService == "" {
		return ""
	}
	if c, ok := store.GetConnection(ident.ID(p.ChatService)); ok {
		return c.Name
	}
	return p.ChatService
}

// chatServiceChoices are the chat-service select's options: none, every live
// chat-kind connection by name, the implicit kinds when no such connection
// exists yet (choosing one creates it), and the current value.
func chatServiceChoices(store jam.Store, current string) []string {
	out := []string{""}
	kinds := map[string]bool{}
	for _, c := range store.ListConnections() {
		if slices.Contains(jam.ChatKinds, c.Kind) {
			out = append(out, c.Name)
			kinds[c.Kind] = true
		}
	}
	for _, k := range jam.ChatKinds {
		if !kinds[k] && !slices.Contains(out, k) {
			out = append(out, k)
		}
	}
	if !slices.Contains(out, current) {
		out = append(out, current)
	}
	return out
}
