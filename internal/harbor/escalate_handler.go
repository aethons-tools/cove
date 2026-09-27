package harbor

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
)

// maxEscalateBodyBytes caps the /escalate POST body (category is short).
const maxEscalateBodyBytes = 1024

// escalateStore is the narrow slice of Store the /escalate handler needs.
// harbor.Store satisfies it.
type escalateStore interface {
	Lookup(tokenHash string) (Actor, bool)
	GetInstance(actorID string) (Instance, bool)
}

// categorySetter is the narrow slice of *Supervisor the /escalate handler
// needs.
type categorySetter interface {
	SetEscalationCategory(actorID, category string) error
}

// EscalateHandler is harbor's brokered escalation-category endpoint: an
// authenticated cove declares its block category, which harbor stamps on the
// caller's OWN instance (self-scoped by construction — no target parameter).
// The escalation engine reads it to pick the tier chain. Implements
// http.Handler.
type EscalateHandler struct {
	store  escalateStore
	setter categorySetter
	log    *slog.Logger
}

// NewEscalateHandler constructs an EscalateHandler.
func NewEscalateHandler(store escalateStore, setter categorySetter, log *slog.Logger) *EscalateHandler {
	return &EscalateHandler{store: store, setter: setter, log: log}
}

func (h *EscalateHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || tok == "" {
		http.Error(w, "missing identity", http.StatusUnauthorized)
		return
	}
	// Fail closed: an unrecognized token hash denies. Same lookup primitive as
	// /squawks (internal/harbor/proxy.go) — a hash-map lookup, never a manual
	// token comparison.
	actor, ok := h.store.Lookup(HashToken(tok))
	if !ok {
		http.Error(w, "unknown identity", http.StatusUnauthorized)
		return
	}
	if _, ok := h.store.GetInstance(actor.ID); !ok {
		http.Error(w, "no instance", http.StatusForbidden)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxEscalateBodyBytes)
	var req struct {
		Category string `json:"category"`
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
	if err := h.setter.SetEscalationCategory(actor.ID, req.Category); err != nil {
		h.log.Error("escalate: set category failed", "actor", actor.ID, "error", err.Error())
		http.Error(w, "set category failed", http.StatusBadGateway)
		return
	}
	h.log.Info("escalate", "actor", actor.ID, "category", req.Category)
	w.WriteHeader(http.StatusNoContent)
}
