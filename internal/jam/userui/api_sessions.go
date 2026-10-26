package userui

import (
	"net/http"
	"slices"

	"github.com/aethons-tools/cove/internal/jam"
)

// apiPaneTabs are the pane tabs the app reads so far (Thinking and Studio
// come in slice 6).
var apiPaneTabs = []string{"previews", "artifacts", "status"}

func (h *handler) sessionRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /app/user/api/sessions/{id}", h.apiSessionHead)
	mux.HandleFunc("GET /app/user/api/sessions/{id}/pane/{tab}", h.apiSessionPane)
}

// noSession is every "no such session": the same for one the viewer may
// not see.
func noSession(w http.ResponseWriter) {
	writeAPIError(w, http.StatusNotFound, "not_found", "no such session")
}

// openSession is the request's participant and the session {id}, with its
// home channel, or it has answered (403 / 404).
func (h *handler) openSession(w http.ResponseWriter, r *http.Request) (jam.Participant, jam.Instance, string, bool) {
	p, ok := jam.ParticipantFrom(r)
	if !ok {
		writeAPIError(w, http.StatusForbidden, "forbidden", "sign in as a project member")
		return p, jam.Instance{}, "", false
	}
	inst, home, ok := h.sessionFor(p, r.PathValue("id"))
	if !ok {
		noSession(w)
		return p, jam.Instance{}, "", false
	}
	return p, inst, home, true
}

func (h *handler) apiSessionHead(w http.ResponseWriter, r *http.Request) {
	p, inst, _, ok := h.openSession(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, h.sessionHead(r, p, inst))
}

func (h *handler) apiSessionPane(w http.ResponseWriter, r *http.Request) {
	tab := r.PathValue("tab")
	p, inst, home, ok := h.openSession(w, r)
	if !ok {
		return
	}
	if !slices.Contains(apiPaneTabs, tab) {
		writeAPIError(w, http.StatusNotFound, "not_found", "no such tab")
		return
	}
	writeJSON(w, http.StatusOK, paneLists(h.buildPane(r, p, inst, home, tab)))
}
