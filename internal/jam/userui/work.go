package userui

import (
	"net/http"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
)

// A unit of work's record, read by its issue key
// (docs/modules/ROOT/pages/usage/jam/work-units.adoc#api).

func (h *handler) workRoutes(mux *http.ServeMux) {
	// {key...}: a github key, owner/repo#<n>, has a slash.
	mux.HandleFunc("GET /app/user/api/work/{key...}", h.apiWork)
}

// apiWork is GET api/work/{key}: the unit of work key in the viewer's
// projects, for one who may read its log; any other key is not found.
func (h *handler) apiWork(w http.ResponseWriter, r *http.Request) {
	p, ok := jam.ParticipantFrom(r)
	if !ok {
		writeAPIError(w, http.StatusForbidden, "forbidden", "sign in as a project member")
		return
	}
	log, ok := h.workLog(p, r.PathValue("key"))
	if !ok {
		writeAPIError(w, http.StatusNotFound, "not_found", "no such unit of work")
		return
	}
	writeJSON(w, http.StatusOK, h.workUnit(r, p, log))
}

// workLog is the live work log of issue key in p's projects that p may read.
func (h *handler) workLog(p jam.Participant, key string) (jam.Channel, bool) {
	if h.d.Intercom == nil || key == "" {
		return jam.Channel{}, false
	}
	for _, name := range p.Projects {
		proj, ok := h.d.Store.GetProject(name)
		if !ok {
			continue
		}
		for _, ch := range h.d.Store.ListChannels(proj.ID, jam.SourceWork) {
			if workKey(ch) == key && h.d.Intercom.CanSee(p.UserID, ch) {
				return ch, true
			}
		}
	}
	return jam.Channel{}, false
}

// workUnit is log's unit as viewer p sees it: each worker's view linked when
// p may open it. The title falls back to the current worker's issue title.
func (h *handler) workUnit(r *http.Request, p jam.Participant, log jam.Channel) WorkUnit {
	rec, _ := h.d.Store.WorkUnit(log.ID)
	u := WorkUnit{Key: workKey(log), Title: rec.Issue.Title, URL: rec.Issue.URL, Log: string(log.ID), State: rec.State,
		Workers: []UnitWorker{}, PullRequests: []UnitPR{}}
	if u.Title == "" && log.Worker != "" {
		if inst, ok := h.d.Store.GetInstance(string(log.Worker)); ok {
			u.Title = inst.UnitTitle
		}
	}
	for _, wk := range rec.Workers {
		row := UnitWorker{Session: string(wk.Session), Label: wk.Label, Started: timePtr(wk.Started), Current: !wk.Stopped}
		if wk.Stopped {
			row.Stopped = timePtr(wk.StoppedAt)
		}
		if _, ok := h.viewedSession(r, p, string(wk.Session)); ok {
			row.View = sessionViewPath(wk.Session)
		}
		u.Workers = append(u.Workers, row)
	}
	for _, pr := range rec.PRs {
		u.PullRequests = append(u.PullRequests, UnitPR{URL: pr.URL, Number: pr.Number, Title: pr.Title, State: string(pr.State), ReportedAt: pr.ReportedAt})
	}
	return u
}

// sessionViewPath is session id's view in the app.
func sessionViewPath(id ident.ID) string { return "/app/user/s/" + string(id) }

// timePtr is t, nil when zero.
func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}
