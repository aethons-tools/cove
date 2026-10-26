package jam

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
)

// maxMissionBodyBytes caps the /mission PUT body (a mission is one short line).
const maxMissionBodyBytes = 2048

// missionSetter is the narrow slice of *Supervisor the /mission handler needs.
type missionSetter interface {
	SetMission(actorID, mission string) error
}

// MissionHandler is Jam's brokered mission endpoint: an authenticated cove
// declares what it is focused on, which Jam stamps (normalised) on the
// caller's OWN instance — self-scoped by construction, no target parameter.
// Implements http.Handler.
type MissionHandler struct {
	store  escalateStore
	setter missionSetter
	log    *slog.Logger
}

// NewMissionHandler constructs a MissionHandler.
func NewMissionHandler(store escalateStore, setter missionSetter, log *slog.Logger) *MissionHandler {
	return &MissionHandler{store: store, setter: setter, log: log}
}

func (h *MissionHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		w.Header().Set("Allow", "PUT")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	actor, ok := authenticateCove(w, r, h.store)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxMissionBodyBytes)
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
	if err := h.setter.SetMission(actor.ID, req.Text); err != nil {
		h.log.Error("mission: set failed", "actor", actor.ID, "error", err.Error())
		http.Error(w, "set mission failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
