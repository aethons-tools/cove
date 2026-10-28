package adminui

import (
	"log/slog"
	"net/http"

	"github.com/aethons-tools/cove/internal/jam"
)

// The JSON API of the admin's own pages: the dashboard and the Jam context
// card's writes — dashboard.html's and context_edit.go's twins.

// dashAPI is what the dashboard reads beside the store: the Studio Managers
// (nil: no Managers panel), the supervisor (the studio table) and
// Alertmanager's URL (Health's Silences link).
type dashAPI struct {
	managers func() []jam.ManagerStatus
	sup      *jam.Supervisor
	amURL    string
}

func attnRowsOf(items []attnItem) []AttnRow {
	out := []AttnRow{}
	for _, it := range items {
		out = append(out, AttnRow{Kind: string(it.Kind), Why: it.Why, Path: appPath(it.Href)})
	}
	return out
}

func managerRowsOf(ms []jam.ManagerStatus) []ManagerRow {
	out := []ManagerRow{}
	for _, m := range ms {
		row := ManagerRow{Name: m.Name, Connected: m.Connected, Version: m.Version, Studios: m.Studios, Capacity: m.Capacity, Strays: []string{}}
		if !m.LastSeen.IsZero() {
			row.LastSeen = m.LastSeen.Format("15:04:05")
		}
		for _, s := range m.Strays {
			row.Strays = append(row.Strays, s.ActorID)
		}
		out = append(out, row)
	}
	return out
}

func apiDash(api *http.ServeMux, src frameSource, da dashAPI, log *slog.Logger) {
	store := src.store
	jamContext := func() ContextPanel { return contextPanelOf(jamPanel(store), src.marks()) }
	api.HandleFunc("GET "+apiPrefix+"dashboard", func(w http.ResponseWriter, r *http.Request) {
		items := src.items()
		s := dashboardStats(store)
		d := Dashboard{
			Stats: DashStats{Studios: s.Studios, Running: s.Running, Waiting: s.Waiting, SettingUp: s.SettingUp, Idled: s.Idled, Attention: s.Attention,
				Projects: s.Projects, Agents: s.Agents, Users: s.Users, Specs: s.Specs},
			Attention: attnRowsOf(inScope(items, "", "")), JamContext: jamContext(),
			HasManagers: da.managers != nil, Managers: []ManagerRow{}, Coves: coveRows(jam.CoveSummaries(store, da.sup), items),
		}
		if da.managers != nil {
			d.Managers = managerRowsOf(da.managers())
		}
		writeJSON(w, http.StatusOK, d)
	})
	api.HandleFunc("POST "+apiPrefix+"jam/context", func(w http.ResponseWriter, r *http.Request) {
		var f struct {
			YAML string `json:"yaml"`
		}
		if !decodeJSON(w, r, &f) {
			return
		}
		b, err := jam.ParseContextYAML([]byte(f.YAML), nil)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		if err := setJamContext(store, log, r, "set", b); err != nil {
			apiFail(w, err, http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, jamContext())
	})
	api.HandleFunc("DELETE "+apiPrefix+"jam/context", func(w http.ResponseWriter, r *http.Request) {
		if err := setJamContext(store, log, r, "cleared", jam.ContextBody{}); err != nil {
			apiFail(w, err, http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, jamContext())
	})
}
