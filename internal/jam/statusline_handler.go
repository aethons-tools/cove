package jam

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
)

// maxStatusLineBodyBytes caps a /mission or /focus PUT body (each is one short line).
const maxStatusLineBodyBytes = 2048

// missionSetter is the narrow slice of *Supervisor the /mission handler needs.
type missionSetter interface {
	SetMission(actorID, mission string) error
}

// focusSetter is the narrow slice of *Supervisor the /focus handler needs.
type focusSetter interface {
	SetFocus(actorID, focus string) error
}

// StatusLineHandler is one of Jam's brokered status-line endpoints (/mission,
// /focus): an authenticated cove declares one line, which Jam stamps
// (normalised) on the caller's OWN instance — self-scoped by construction, no
// target parameter. Implements http.Handler.
type StatusLineHandler struct {
	store escalateStore
	what  string // "mission" or "focus", for logs and errors
	set   func(actorID, text string) error
	log   *slog.Logger
}

// NewMissionHandler constructs the PUT /mission handler: the session's end
// goal or standing order.
func NewMissionHandler(store escalateStore, setter missionSetter, log *slog.Logger) *StatusLineHandler {
	return &StatusLineHandler{store: store, what: "mission", set: setter.SetMission, log: log}
}

// NewFocusHandler constructs the PUT /focus handler: what the session is on
// right now.
func NewFocusHandler(store escalateStore, setter focusSetter, log *slog.Logger) *StatusLineHandler {
	return &StatusLineHandler{store: store, what: "focus", set: setter.SetFocus, log: log}
}

func (h *StatusLineHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		w.Header().Set("Allow", "PUT")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	actor, ok := authenticateCove(w, r, h.store)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxStatusLineBodyBytes)
	var req struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if err := h.set(actor.ID, req.Text); err != nil {
		h.log.Error(h.what+": set failed", "actor", actor.ID, "error", err.Error())
		http.Error(w, "set "+h.what+" failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
