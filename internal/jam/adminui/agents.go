package adminui

import (
	"net/http"
	"net/url"
	"slices"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
)

// agentURL is an agent's page.
func agentURL(id string) string { return "/ui/agents/" + url.PathEscape(id) }

// agentKind is how an agent came to be: a standing or personal session, a
// ticket's session (an ephemeral session with a unit), a manual raise, or —
// with no studio — an identity enrolled by hand.
func agentKind(inst jam.Instance, running bool) string {
	if !running {
		return "enrolled"
	}
	switch inst.SessionKind {
	case "standing", "personal":
		return inst.SessionKind
	}
	if inst.Unit != "" {
		return "ticket"
	}
	return "manual"
}

// statusGroups are the list's filters (?status=), the same groups the
// dashboard's tiles count.
var statusGroups = map[string][]jam.AgentStatus{
	"running":    {jam.StatusRunning, jam.StatusHolding},
	"waiting":    {jam.StatusWaiting, jam.StatusBlocked},
	"setting-up": {jam.StatusSettingUp, jam.StatusOrienting},
	"idled":      {jam.StatusIdled},
	"attention":  {jam.StatusLost, jam.StatusTerminating},
}

// legacyPhaseFilters maps the old ?phase= filter values onto status groups, so
// old links keep working.
var legacyPhaseFilters = map[string]string{"live": "running", "raising": "setting-up", "idled": "idled", "attention": "attention"}

// agentFilters is the filter strip, in order.
var agentFilters = []struct{ Key, Label string }{
	{"", "All"}, {"running", "Running"}, {"waiting", "Waiting"}, {"setting-up", "Setting up"}, {"idled", "Idled"}, {"attention", "Lost / terminating"},
}

// statusFilter is the request's status group ("" = all): ?status=, else an old
// ?phase= value.
func statusFilter(r *http.Request) string {
	q := r.URL.Query()
	if s := q.Get("status"); s != "" {
		return s
	}
	return legacyPhaseFilters[q.Get("phase")]
}

// agentRow is one row of the agents list: an actor, its studio, or both.
type agentRow struct {
	ID, Name, Kind  string
	Project, Role   string
	Unit            string
	Phase, Activity string
	Status          jam.AgentStatus // the display axis (jam.StatusOf)
	Connector       string
	Image           string
	LastSeen        time.Time
	Grants          []jam.GrantSummary
	HasStudio       bool
}

// agentRows lists every agent — each enrolled actor and each studio, merged by
// id, sorted by id — keeping only those in status's group when one is given.
func agentRows(store jam.Store, img jam.ImageResolver, status string) []agentRow {
	byID := map[string]*agentRow{}
	for _, a := range jam.RosterSummaries(store) {
		byID[a.ID] = &agentRow{ID: a.ID, Kind: "enrolled", Grants: a.Grants, Status: jam.StatusPending}
		if len(a.Grants) > 0 {
			byID[a.ID].Project, byID[a.ID].Role = a.Grants[0].Project, a.Grants[0].Role
		}
	}
	for _, c := range jam.CoveSummaries(store, img) {
		row := byID[c.ID]
		if row == nil {
			row = &agentRow{ID: c.ID}
			byID[c.ID] = row
		}
		inst, _ := store.GetInstance(c.ID)
		row.Name, row.Kind, row.HasStudio = c.Name, agentKind(inst, true), true
		row.Project, row.Role, row.Unit = c.Project, c.Role, c.Unit
		row.Phase, row.Activity, row.Image, row.LastSeen = c.Phase, c.Activity, c.Image, c.LastSeen
		row.Status = jam.StatusOf(jam.Phase(c.Phase), jam.Activity(c.Activity), true)
		row.Connector = c.Connector
	}
	want := statusGroups[status]
	out := make([]agentRow, 0, len(byID))
	for _, r := range byID {
		if want != nil && !slices.Contains(want, r.Status) {
			continue
		}
		out = append(out, *r)
	}
	slices.SortFunc(out, func(a, b agentRow) int {
		switch {
		case a.ID < b.ID:
			return -1
		case a.ID > b.ID:
			return 1
		}
		return 0
	})
	return out
}

// projectAgentRow is one row of a project's Agents tab: an agent running in
// the project or holding a grant into it, with its roles there.
type projectAgentRow struct {
	agentRow
	Roles []string
}

// projectAgents lists project's agents — those whose studio runs in it and
// those holding a grant into it, one row per id, in id order.
func projectAgents(store jam.Store, img jam.ImageResolver, project string) []projectAgentRow {
	var out []projectAgentRow
	for _, r := range agentRows(store, img, "") {
		var roles []string
		if r.HasStudio && orDefaultProject(r.Project) == project {
			roles = append(roles, r.Role)
		}
		for _, g := range r.Grants {
			if orDefaultProject(g.Project) == project && !slices.Contains(roles, g.Role) {
				roles = append(roles, g.Role)
			}
		}
		if roles != nil {
			out = append(out, projectAgentRow{agentRow: r, Roles: roles})
		}
	}
	return out
}

// agentsData is the agents page and its polled table.
type agentsData struct {
	Title   string
	Status  string // the active status filter ("" = all)
	Filters []struct{ Key, Label string }
	Agents  []agentRow
	CanEdit bool // a runtime supervisor: raise and teardown
}

func newAgentsData(store jam.Store, img jam.ImageResolver, status string, canEdit bool) agentsData {
	if _, ok := statusGroups[status]; !ok {
		status = ""
	}
	return agentsData{Title: "Agents", Status: status, Filters: agentFilters, Agents: agentRows(store, img, status), CanEdit: canEdit}
}

func registerAgents(mux *http.ServeMux, store jam.Store, img jam.ImageResolver, canEdit bool) {
	mux.HandleFunc("GET /ui/agents", func(w http.ResponseWriter, r *http.Request) {
		data := newAgentsData(store, img, statusFilter(r), canEdit)
		if r.Header.Get("HX-Request") == "true" {
			renderFragment(w, r, "agents", "agents-table", data)
			return
		}
		render(w, r, "agents", data)
	})
	// The studio and actor lists and the studio page became the agents list
	// and the agent page.
	mux.HandleFunc("GET /ui/coves", func(w http.ResponseWriter, r *http.Request) { redirect(w, r, "/ui/agents") })
	mux.HandleFunc("GET /ui/actors", func(w http.ResponseWriter, r *http.Request) { redirect(w, r, "/ui/agents") })
	mux.HandleFunc("GET /ui/coves/{id}", func(w http.ResponseWriter, r *http.Request) {
		redirect(w, r, agentURL(r.PathValue("id")))
	})
	mux.HandleFunc("GET /ui/coves/{id}/session", func(w http.ResponseWriter, r *http.Request) {
		redirect(w, r, agentURL(r.PathValue("id"))+"/session")
	})
}
