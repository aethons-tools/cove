package userui

import (
	"net/http"
	"slices"
	"strconv"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/sessionevents"
)

func (h *handler) sessionRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /app/user/api/sessions/{id}", h.apiSessionHead)
	mux.HandleFunc("GET /app/user/api/sessions/{id}/pane/{tab}", h.apiSessionPane)
	mux.HandleFunc("POST /app/user/api/sessions/{id}/frame", h.apiFrame) // more specific than {action}
	mux.HandleFunc("POST /app/user/api/sessions/{id}/{action}", h.apiSessionAction)
	mux.HandleFunc("GET /app/user/api/sessions/{id}/thinking", h.apiThinkingPage)
	mux.HandleFunc("GET /app/user/api/sessions/{id}/thinking/raw", h.apiThinkingRaw)
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
	p, inst, home, ok := h.openSession(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, h.sessionHead(r, p, inst, home))
}

func (h *handler) apiSessionPane(w http.ResponseWriter, r *http.Request) {
	tab := r.PathValue("tab")
	p, inst, home, ok := h.openSession(w, r)
	if !ok {
		return
	}
	if !slices.Contains(paneTabs, tab) {
		writeAPIError(w, http.StatusNotFound, "not_found", "no such tab")
		return
	}
	writeJSON(w, http.StatusOK, paneLists(h.buildPane(r, p, inst, home, tab)))
}

// apiSessionAction runs a session action (see sessionOutcome); the app
// refetches the head, which 404s once the session is gone.
func (h *handler) apiSessionAction(w http.ResponseWriter, r *http.Request) {
	var req struct{}
	if !decodeJSON(w, r, &req) {
		return
	}
	p, inst, _, ok := h.openSession(w, r)
	if !ok {
		return
	}
	if h.d.Ops == nil {
		noSession(w)
		return
	}
	status, code, msg := h.sessionOutcome(r, p, inst, r.PathValue("action"))
	if status == http.StatusNoContent {
		w.WriteHeader(status)
		return
	}
	writeAPIError(w, status, code, msg)
}

// openThinking is openSession for the Thinking routes: 404 when this jam
// records no session events, else the session and the request's ?stream=.
func (h *handler) openThinking(w http.ResponseWriter, r *http.Request) (jam.Instance, string, bool) {
	_, inst, _, ok := h.openSession(w, r)
	if !ok {
		return inst, "", false
	}
	if h.d.Events == nil {
		writeAPIError(w, http.StatusNotFound, "not_found", "session events aren't recorded on this jam")
		return inst, "", false
	}
	s := r.URL.Query().Get("stream")
	if !sessionevents.ValidStreamID(s) {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "stream: not a stream id")
		return inst, "", false
	}
	return inst, s, true
}

// positive is the request's query parameter name as a positive integer.
func positive(w http.ResponseWriter, r *http.Request, name string) (uint64, bool) {
	n, err := strconv.ParseUint(r.URL.Query().Get(name), 10, 64)
	if err != nil || n == 0 {
		writeAPIError(w, http.StatusBadRequest, "bad_request", name+": not a positive number")
		return 0, false
	}
	return n, true
}

// apiThinkingPage is a page of a session's events below ?before= (the
// Thinking tab's Load earlier).
func (h *handler) apiThinkingPage(w http.ResponseWriter, r *http.Request) {
	inst, stream, ok := h.openThinking(w, r)
	if !ok {
		return
	}
	before, ok := positive(w, r, "before")
	if !ok {
		return
	}
	evs, below, err := h.thinkingPageOf(inst.ActorID, stream, before)
	if err != nil {
		h.lg.Error("api thinking page: listing events failed", "session", inst.ActorID, "error", err.Error())
		writeAPIError(w, http.StatusInternalServerError, "internal", "listing events failed")
		return
	}
	page := ThinkingPage{Events: make([]ThinkingEvent, 0, len(evs)), Before: below}
	for _, v := range evs {
		page.Events = append(page.Events, thinkingEventOf(v))
	}
	writeJSON(w, http.StatusOK, page)
}

// apiThinkingRaw is one event's raw JSON (the Thinking tab's Raw format).
func (h *handler) apiThinkingRaw(w http.ResponseWriter, r *http.Request) {
	inst, stream, ok := h.openThinking(w, r)
	if !ok {
		return
	}
	seq, ok := positive(w, r, "seq")
	if !ok {
		return
	}
	raw, cut, found, err := h.thinkingRawOf(inst.ActorID, stream, seq)
	switch {
	case err != nil:
		h.lg.Error("api thinking raw: listing events failed", "session", inst.ActorID, "error", err.Error())
		writeAPIError(w, http.StatusInternalServerError, "internal", "listing events failed")
	case !found:
		writeAPIError(w, http.StatusNotFound, "not_found", "no such event")
	default:
		writeJSON(w, http.StatusOK, ThinkingRaw{Raw: raw, Truncated: cut})
	}
}

// apiFrame mints a frame URL for a preview's View here
// (see frameOutcome); a view the viewer can't have, like framing off,
// answers as a missing session.
func (h *handler) apiFrame(w http.ResponseWriter, r *http.Request) {
	var req FrameRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	p, inst, _, ok := h.openSession(w, r)
	if !ok {
		return
	}
	if h.d.Frames == nil {
		noSession(w)
		return
	}
	fu, status, code, msg := h.frameOutcome(r, p, inst, req.Name)
	if status != http.StatusOK {
		writeAPIError(w, status, code, msg)
		return
	}
	writeJSON(w, http.StatusOK, fu)
}
