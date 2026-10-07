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

// projectRef is the first thing (deterministically) that keeps project from
// being removed: a member, a standing-session entry, a role in it, a session
// not yet gone, or an actor's grant into it. Mirrors the store's refusal.
func projectRef(store jam.Store, project string) string {
	p, ok := store.GetProject(project)
	if !ok {
		return ""
	}
	if n := len(store.ListMembers(p.ID)); n > 0 {
		return fmt.Sprintf("%d member(s)", n)
	}
	for _, e := range store.ListStandingSessions() {
		if e.ProjectID == p.ID {
			return fmt.Sprintf("standing agent %s/%s", e.Role, e.Name)
		}
	}
	if roles := store.ListRoles(project); len(roles) > 0 {
		return fmt.Sprintf("role %s/%s", project, roles[0].Name)
	}
	for _, inst := range store.ListInstances() {
		if inst.Phase != jam.PhaseGone && jam.SameProject(store, inst.Project, project) {
			return "running agent " + inst.ActorID
		}
	}
	for _, a := range store.ListActors() {
		for _, g := range a.Grants {
			if jam.ProjectName(store, g.Project) == project {
				return fmt.Sprintf("actor %s's grant of %s/%s", a.ID, project, g.Role)
			}
		}
	}
	return ""
}

// crumb is one breadcrumb segment.
type crumb struct{ Label, Href string }

// projectCrumbs is the trail below a project's tab: on a role page, Roles /
// dev; none on a tab's own page (the tab strip names it).
func projectCrumbs(project string, section projectSection, role string) []crumb {
	if role == "" {
		return nil
	}
	return []crumb{{"Roles", projectSectionURL(project, sectionRoles)}, {role, roleURL(project, role)}}
}

// projectDetail is the payload of every project page: Section picks which
// section's content renders under the project's tabs.
type projectDetail struct {
	Title        string
	Section      projectSection
	Crumbs       []crumb
	Project      jam.Project
	Roles        []roleRow
	Coves        []jam.CoveSummary
	LiveCoves    int
	Agents       int               // len(AgentRows)
	AgentRows    []projectAgentRow // the Agents tab: running in the project or holding a grant into it
	CanEdit      bool              // always false: the page's studio table is read-only
	CanRequest   bool              // the Roles section offers Request (a supervisor runs)
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
	// The Intercom section's recent log: the newest squawks in this project.
	LogConfigured bool
	Squawks       squawkTable
}

// recentProjectSquawks caps the Intercom section's log; the full, filterable
// log is the Intercom page.
const recentProjectSquawks = 50

func buildProjectDetail(store jam.Store, img jam.ImageResolver, msgs SquawkReader, name string, section projectSection) (projectDetail, bool) {
	p, ok := store.GetProject(name)
	if !ok || p.Status == jam.StatusRemoved {
		return projectDetail{}, false
	}
	d := projectDetail{Title: name, Section: section, Project: p, InUseBy: projectRef(store, name)}
	d.Crumbs = projectCrumbs(name, section, "")
	d.Context = newContextPanel("project", "/ui/projects/"+name+"/context", "project", p.Context, p.Resources, sessionctx.BudgetProject, true)
	for _, r := range roleRows(store) {
		if r.Project == name {
			d.Roles = append(d.Roles, r)
		}
	}
	for _, c := range jam.CoveSummaries(store, img) {
		if orDefaultProject(c.Project) == name {
			d.Coves = append(d.Coves, c)
			if ph := jam.Phase(c.Phase); ph == jam.PhaseLive || ph == jam.PhaseRaising {
				d.LiveCoves++
			}
		}
	}
	d.AgentRows = projectAgents(store, img, name)
	d.Agents = len(d.AgentRows)
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
	if section == sectionIntercom && msgs != nil {
		d.LogConfigured = true
		d.Squawks = filterSquawks(msgs, squawkFilter{Project: name}, recentProjectSquawks)
	}
	return d, true
}

func registerProjects(mux *http.ServeMux, store jam.Store, img jam.ImageResolver, msgs SquawkReader, canRequest bool, log *slog.Logger, guardWrite func(http.ResponseWriter, *http.Request) bool) {
	// The rail is the project list.
	mux.HandleFunc("GET /ui/projects", func(w http.ResponseWriter, r *http.Request) {
		redirect(w, r, "/ui/")
	})

	page := func(section projectSection) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			name := r.PathValue("name")
			d, ok := buildProjectDetail(store, img, msgs, name, section)
			if !ok {
				renderStatus(w, r, http.StatusNotFound, "project", projectDetail{Title: "Project not found", NotFound: true, NotFoundFor: name})
				return
			}
			d.CanRequest = canRequest
			render(w, r, "project", d)
		}
	}
	mux.HandleFunc("GET /ui/projects/{name}", page(sectionOverview))
	for _, s := range projectSections[1:] {
		mux.HandleFunc("GET /ui/projects/{name}/"+string(s.Section), page(s.Section))
	}

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
		// The new project's page; htmx follows the redirect.
		w.Header().Set("HX-Redirect", projectURL(name))
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("POST /ui/projects/{name}/rename", func(w http.ResponseWriter, r *http.Request) {
		if !guardWrite(w, r) {
			return
		}
		if err := r.ParseForm(); err != nil {
			renderError(w, http.StatusBadRequest, "invalid form")
			return
		}
		old, name := r.PathValue("name"), strings.TrimSpace(r.FormValue("name"))
		if err := store.RenameProject(old, name); err != nil {
			renderError(w, jam.WriteStatus(err, http.StatusBadRequest), err.Error())
			return
		}
		log.Info("ui project renamed", "operator", jam.OperatorID(r), "project", old, "name", name)
		w.Header().Set("HX-Redirect", projectURL(name))
		w.WriteHeader(http.StatusOK)
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
		// The project page navigates to Jam on success.
		w.WriteHeader(http.StatusOK)
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
