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

// agentPhases are the list's filters, matching the dashboard's studio tiles.
var agentPhases = map[string][]jam.Phase{
	"live":      {jam.PhaseLive},
	"raising":   {jam.PhaseRaising},
	"idled":     {jam.PhaseIdled},
	"attention": {jam.PhaseLost, jam.PhaseTerminating},
}

// agentFilters is the filter strip, in order.
var agentFilters = []struct{ Key, Label string }{
	{"", "All"}, {"live", "Live"}, {"raising", "Raising"}, {"idled", "Idled"}, {"attention", "Lost / terminating"},
}

// agentRow is one row of the agents list: an actor, its studio, or both.
type agentRow struct {
	ID, Name, Kind  string
	Project, Role   string
	Unit            string
	Phase, Activity string
	Connector       string
	Image           string
	LastSeen        time.Time
	Grants          []jam.GrantSummary
	HasStudio       bool
}

// agentRows lists every agent — each enrolled actor and each studio, merged by
// id, sorted by id — keeping only those in phase's filter when one is given.
func agentRows(store jam.Store, img jam.ImageResolver, phase string) []agentRow {
	byID := map[string]*agentRow{}
	for _, a := range jam.RosterSummaries(store) {
		byID[a.ID] = &agentRow{ID: a.ID, Kind: "enrolled", Grants: a.Grants}
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
		row.Connector = c.Connector
	}
	want := agentPhases[phase]
	out := make([]agentRow, 0, len(byID))
	for _, r := range byID {
		if want != nil && !slices.Contains(want, jam.Phase(r.Phase)) {
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

// agentsData is the agents page and its polled table.
type agentsData struct {
	Title   string
	Phase   string // the active filter ("" = all)
	Filters []struct{ Key, Label string }
	Agents  []agentRow
	CanEdit bool // a runtime supervisor: raise and teardown
}

func newAgentsData(store jam.Store, img jam.ImageResolver, phase string, canEdit bool) agentsData {
	if _, ok := agentPhases[phase]; !ok {
		phase = ""
	}
	return agentsData{Title: "Agents", Phase: phase, Filters: agentFilters, Agents: agentRows(store, img, phase), CanEdit: canEdit}
}

func registerAgents(mux *http.ServeMux, store jam.Store, img jam.ImageResolver, canEdit bool) {
	mux.HandleFunc("GET /ui/agents", func(w http.ResponseWriter, r *http.Request) {
		data := newAgentsData(store, img, r.URL.Query().Get("phase"), canEdit)
		if r.Header.Get("HX-Request") == "true" {
			renderFragment(w, "agents", "agents-table", data)
			return
		}
		render(w, "agents", data)
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
