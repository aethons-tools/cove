package jam

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
)

// maxMissionBodyBytes caps the /mission PUT body (a mission is one short line).
const maxMissionBodyBytes = 2048

// missionStore is the narrow slice of Store the /mission handler needs.
// jam.Store satisfies it.
type missionStore interface {
	Lookup(tokenHash string) (Actor, bool)
	GetInstance(actorID string) (Instance, bool)
}

// missionSetter is the narrow slice of *Supervisor the /mission handler needs.
type missionSetter interface {
	SetMission(actorID, mission string) error
}

// MissionHandler is Jam's brokered mission endpoint: an authenticated cove
// declares what it is focused on, which Jam stamps (normalised) on the
// caller's OWN instance — self-scoped by construction, no target parameter.
// Implements http.Handler.
type MissionHandler struct {
	store  missionStore
	setter missionSetter
	log    *slog.Logger
}

// NewMissionHandler constructs a MissionHandler.
func NewMissionHandler(store missionStore, setter missionSetter, log *slog.Logger) *MissionHandler {
	return &MissionHandler{store: store, setter: setter, log: log}
}

func (h *MissionHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		w.Header().Set("Allow", "PUT")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || tok == "" {
		http.Error(w, "missing identity", http.StatusUnauthorized)
		return
	}
	// Fail closed: an unrecognized token hash denies (hash-map lookup, never a
	// manual token comparison), as in /escalate.
	actor, ok := h.store.Lookup(HashToken(tok))
	if !ok {
		http.Error(w, "unknown identity", http.StatusUnauthorized)
		return
	}
	if _, ok := h.store.GetInstance(actor.ID); !ok {
		http.Error(w, "no instance", http.StatusForbidden)
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
