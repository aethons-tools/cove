package jam

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

const (
	maxTurnEndBodyBytes = 2048
	maxEndReason        = 1000
	maxIdleOverride     = 30 * 24 * time.Hour
)

// turnEndSetter is the narrow slice of *Supervisor the turn-end handler needs.
type turnEndSetter interface {
	SetEndRequested(actorID, reason string) error
	SetIdleOverride(actorID string, o IdleOverride) error
	SetMessagesMuted(actorID string, on bool) error
	SetKeptProcesses(actorID string, n int) error
}

// maxKeptProcesses bounds PUT /kept's count (the cove keeps at most 8).
const maxKeptProcesses = 32

// TurnEndHandler is Jam's brokered turn-end endpoint pair, self-scoped to the
// caller's OWN instance like /escalate: POST /end records that the session
// asked to end (wake-on tears it down once it is Waiting), PUT /idle sets
// its idle-timeout override, PUT /mute stops new messages waking it until
// it next runs, and PUT /kept records how many processes it keeps running
// (while any run, wake-on doesn't pause it). Implements http.Handler.
type TurnEndHandler struct {
	store  escalateStore
	setter turnEndSetter
	log    *slog.Logger
}

// NewTurnEndHandler constructs a TurnEndHandler.
func NewTurnEndHandler(store escalateStore, setter turnEndSetter, log *slog.Logger) *TurnEndHandler {
	return &TurnEndHandler{store: store, setter: setter, log: log}
}

func (h *TurnEndHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	method := http.MethodPost
	if r.URL.Path == "/idle" || r.URL.Path == "/mute" || r.URL.Path == "/kept" {
		method = http.MethodPut
	}
	if r.Method != method {
		w.Header().Set("Allow", method)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	actor, ok := authenticateCove(w, r, h.store)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxTurnEndBodyBytes)
	var req struct {
		Reason   string `json:"reason"`
		Duration string `json:"duration"`
		Scope    string `json:"scope"`
		On       *bool  `json:"on"`
		Count    *int   `json:"count"`
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
	var err error
	switch r.URL.Path {
	case "/kept":
		if req.Count == nil || *req.Count < 0 || *req.Count > maxKeptProcesses {
			http.Error(w, fmt.Sprintf(`"count" is required, 0 to %d`, maxKeptProcesses), http.StatusBadRequest)
			return
		}
		err = h.setter.SetKeptProcesses(actor.ID, *req.Count)
		h.log.Info("turn-end: kept processes", "actor", actor.ID, "count", *req.Count)
	case "/mute":
		if req.On == nil {
			http.Error(w, `"on" is required`, http.StatusBadRequest)
			return
		}
		err = h.setter.SetMessagesMuted(actor.ID, *req.On)
		h.log.Info("turn-end: messages muted", "actor", actor.ID, "on", *req.On)
	case "/idle":
		o, perr := parseIdle(req.Duration, req.Scope)
		if perr != nil {
			http.Error(w, perr.Error(), http.StatusBadRequest)
			return
		}
		err = h.setter.SetIdleOverride(actor.ID, o)
		h.log.Info("turn-end: idle override", "actor", actor.ID, "duration", o.Duration.String(), "scope", o.Scope)
	default:
		req.Reason = strings.TrimSpace(req.Reason)
		if req.Reason == "" || len(req.Reason) > maxEndReason {
			http.Error(w, fmt.Sprintf("reason is required, at most %d bytes", maxEndReason), http.StatusBadRequest)
			return
		}
		err = h.setter.SetEndRequested(actor.ID, req.Reason)
		h.log.Info("turn-end: end requested", "actor", actor.ID)
	}
	if err != nil {
		h.log.Error("turn-end: update failed", "actor", actor.ID, "path", r.URL.Path, "error", err.Error())
		http.Error(w, "update failed", http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// authenticateCove resolves the caller's identity token to an actor with a
// live instance, writing the 401/403 itself. Same lookup primitive as
// /escalate. Shared by /end, /idle, /mute, /kept and /alarms.
func authenticateCove(w http.ResponseWriter, r *http.Request, store escalateStore) (Actor, bool) {
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || tok == "" {
		http.Error(w, "missing identity", http.StatusUnauthorized)
		return Actor{}, false
	}
	actor, ok := store.Lookup(HashToken(tok))
	if !ok {
		http.Error(w, "unknown identity", http.StatusUnauthorized)
		return Actor{}, false
	}
	if _, ok := store.GetInstance(actor.ID); !ok {
		http.Error(w, "no instance", http.StatusForbidden)
		return Actor{}, false
	}
	return actor, true
}

// parseIdle turns a PUT /idle body into an override: "off" → 0, else a positive
// Go duration up to maxIdleOverride; scope next|always.
func parseIdle(duration, scope string) (IdleOverride, error) {
	if scope != IdleScopeNext && scope != IdleScopeAlways {
		return IdleOverride{}, fmt.Errorf("scope must be %q or %q", IdleScopeNext, IdleScopeAlways)
	}
	if duration == "off" {
		return IdleOverride{Scope: scope}, nil
	}
	d, err := time.ParseDuration(duration)
	if err != nil || d <= 0 || d > maxIdleOverride {
		return IdleOverride{}, fmt.Errorf(`duration must be "off" or a positive duration up to 720h`)
	}
	return IdleOverride{Duration: d, Scope: scope}, nil
}
