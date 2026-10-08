package jam

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// alarmSetter is the narrow slice of *Supervisor the /alarms handler needs.
type alarmSetter interface {
	SetAlarm(actorID, name, schedule, note, gate string) (Alarm, error)
	ClearAlarm(actorID, name string) error
}

// AlarmHandler is Jam's brokered alarm endpoint, self-scoped to the caller's
// OWN instance like /end: GET /alarms lists its alarms, PUT /alarms/{name}
// sets (or replaces) one, DELETE /alarms/{name} removes one. See
// docs/usage/jam/turn-end.md#alarms. Implements http.Handler.
type AlarmHandler struct {
	store  escalateStore
	setter alarmSetter
	log    *slog.Logger
}

// NewAlarmHandler constructs an AlarmHandler.
func NewAlarmHandler(store escalateStore, setter alarmSetter, log *slog.Logger) *AlarmHandler {
	return &AlarmHandler{store: store, setter: setter, log: log}
}

// alarmView is one alarm on the wire.
type alarmView struct {
	Name     string    `json:"name"`
	Schedule string    `json:"schedule"`
	Note     string    `json:"note,omitempty"`
	NextAt   string    `json:"next_at,omitempty"` // RFC 3339; empty once a one-shot fired
	Fired    bool      `json:"fired,omitempty"`   // fired, awaiting the session's next run
	Gate     string    `json:"gate,omitempty"`
	LastGate *gateView `json:"last_gate,omitempty"`
}

// gateView is an alarm's latest gate result on the wire.
type gateView struct {
	At      string `json:"at"`
	Verdict string `json:"verdict"` // pass | failed | not-yet
	Exit    int    `json:"exit"`
	Output  string `json:"output,omitempty"`
}

func viewAlarm(a Alarm) alarmView {
	v := alarmView{Name: a.Name, Schedule: a.Schedule, Note: a.Note, Fired: !a.FiredAt.IsZero()}
	if !a.NextAt.IsZero() {
		v.NextAt = a.NextAt.UTC().Format(time.RFC3339)
	}
	v.Gate = a.Gate
	if g := a.LastGate; g != nil {
		v.LastGate = &gateView{At: g.At.UTC().Format(time.RFC3339), Verdict: g.Verdict(), Exit: g.Exit, Output: g.Output}
	}
	return v
}

func (h *AlarmHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name, named := strings.CutPrefix(r.URL.Path, "/alarms/")
	switch {
	case r.URL.Path == "/alarms":
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
	case named && name != "" && !strings.Contains(name, "/"):
		if r.Method != http.MethodPut && r.Method != http.MethodDelete {
			w.Header().Set("Allow", "PUT, DELETE")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
	default:
		http.NotFound(w, r)
		return
	}
	actor, ok := authenticateCove(w, r, h.store)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		inst, _ := h.store.GetInstance(actor.ID)
		out := struct {
			Alarms []alarmView `json:"alarms"`
		}{Alarms: []alarmView{}}
		for _, a := range inst.Alarms {
			out.Alarms = append(out.Alarms, viewAlarm(a))
		}
		writeJSON(w, http.StatusOK, out)
	case http.MethodDelete:
		if err := h.setter.ClearAlarm(actor.ID, name); err != nil {
			if errors.Is(err, ErrNoSuchAlarm) {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
			h.log.Error("alarms: clear failed", "actor", actor.ID, "error", err.Error())
			http.Error(w, "clear failed", http.StatusBadGateway)
			return
		}
		h.log.Info("alarms: cleared", "actor", actor.ID, "alarm", name)
		w.WriteHeader(http.StatusNoContent)
	default: // PUT
		r.Body = http.MaxBytesReader(w, r.Body, maxTurnEndBodyBytes)
		var req struct {
			Schedule string `json:"schedule"`
			Note     string `json:"note"`
			Gate     string `json:"gate"`
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
		a, err := h.setter.SetAlarm(actor.ID, name, req.Schedule, req.Note, req.Gate)
		if err != nil {
			// Validation and the alarm limit: the agent reads the reason.
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		h.log.Info("alarms: set", "actor", actor.ID, "alarm", name, "next", a.NextAt.String())
		writeJSON(w, http.StatusOK, viewAlarm(a))
	}
}
