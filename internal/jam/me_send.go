package jam

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
)

// ParticipantSendHandler is a person's send from the /me inbox (POST
// /me/send): into a conversation they're in ({"to": "<channel id>"}), or a
// new one ({"to": "user:<id|name>"} or {"to": "session:<id>"}: a chat with
// them). The sender is the gate's participant, never the body; the intercom
// decides whether they may post there and who hears it. Implements
// http.Handler.
type ParticipantSendHandler struct {
	store Store
	ic    *Intercom
	log   *slog.Logger
}

// NewParticipantSendHandler constructs a ParticipantSendHandler. ic may be
// nil: messaging unconfigured, a send fails 503.
func NewParticipantSendHandler(store Store, ic *Intercom, log *slog.Logger) *ParticipantSendHandler {
	if log == nil {
		log = slog.New(slog.NewTextHandler(discardWriter{}, nil))
	}
	return &ParticipantSendHandler{store: store, ic: ic, log: log}
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

func (h *ParticipantSendHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Identity comes solely from the gate-injected participant, never the body.
	// Fail closed: no participant (a gate misconfiguration) denies.
	p, ok := ParticipantFrom(r)
	if !ok {
		http.Error(w, "no participant", http.StatusUnauthorized)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxSquawkBodyBytes)
	var req struct {
		To          string `json:"to"`
		Body        string `json:"body"`
		ContentType string `json:"content_type,omitempty"` // "" = text/markdown
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
	if req.Body == "" {
		http.Error(w, "empty body", http.StatusBadRequest)
		return
	}
	if req.To == "" {
		http.Error(w, "empty to", http.StatusBadRequest)
		return
	}
	if !intercom.ValidContentType(req.ContentType) {
		http.Error(w, "unsupported content_type (want text/markdown or text/plain)", http.StatusBadRequest)
		return
	}

	if h.ic == nil {
		http.Error(w, "messaging not configured", http.StatusServiceUnavailable)
		return
	}
	pl, err := h.plan(p, req.To)
	switch {
	case errors.Is(err, ErrSendDenied):
		http.Error(w, "not allowed", http.StatusForbidden)
		return
	case errors.Is(err, ErrSendUnresolved), errors.Is(err, ErrRemoved):
		http.Error(w, "recipient not found", http.StatusNotFound)
		return
	case err != nil:
		h.log.Error("participant send: plan failed", "user", string(p.UserID), "error", err.Error())
		http.Error(w, "send failed", http.StatusInternalServerError)
		return
	}
	if _, err := h.ic.Post(pl, intercom.Squawk{From: p.UserID, Body: req.Body, ContentType: req.ContentType}); err != nil {
		h.log.Error("participant send: append failed", "user", string(p.UserID), "channel", string(pl.Channel.ID), "error", err.Error())
		http.Error(w, "send failed", http.StatusBadGateway)
		return
	}
	h.log.Info("participant send", "user", string(p.UserID), "channel", string(pl.Channel.ID), "audience", len(pl.Audience), "bytes", len(req.Body))
	w.WriteHeader(http.StatusNoContent)
}

// plan resolves a /me send: a channel id, or a person or session to chat with.
func (h *ParticipantSendHandler) plan(p Participant, to string) (Planned, error) {
	kind, ref, ok := strings.Cut(to, ":")
	switch {
	case !ok:
		if id, err := ident.Parse(to); err == nil && id.Kind() == ident.Channel {
			return h.ic.PlanChannel(Poster{ID: p.UserID}, id)
		}
	case kind == "user" || kind == "human":
		id, err := ident.Parse(ref)
		if err != nil {
			var found bool
			if id, found = h.store.LookupName(ident.User, ref); !found {
				return Planned{}, ErrSendUnresolved
			}
		}
		return h.ic.PlanPersonChat(p.UserID, id)
	case kind == "session":
		return h.ic.PlanPersonChat(p.UserID, ident.ID(ref))
	}
	return Planned{}, ErrSendUnresolved
}
