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

// ParticipantSendHandler is a person's send from the /app/user inbox (POST
// /app/user/send): into a conversation they're in ({"to": "<channel id>"}), or a
// new one ({"to": "user:<id|name>"} or {"to": "session:<id>"}: a chat with
// them). The sender is the gate's participant, never the body; the intercom
// decides whether they may post there and who hears it. Implements
// http.Handler.
type ParticipantSendHandler struct {
	store Store
	ic    *Intercom
	log   *slog.Logger
	att   *Attachments // nil: attachments off
}

// SetAttachments lets sends carry uploaded attachments.
func (h *ParticipantSendHandler) SetAttachments(a *Attachments) { h.att = a }

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

// SendRequest is a /app/user send (POST /app/user/send's JSON).
type SendRequest struct {
	To          string   `json:"to"`
	Body        string   `json:"body"`
	ContentType string   `json:"content_type,omitempty"` // "" = text/markdown
	ReplyTo     string   `json:"reply_to,omitempty"`     // an ask in the same channel (Discuss)
	Attachments []string `json:"attachments,omitempty"`  // uploaded blob refs
}

// SendError is a refused send: the HTTP status and text the /app/user/send
// route answers, and a code for the JSON API.
type SendError struct {
	Status    int
	Code, Msg string
}

func (e *SendError) Error() string { return e.Msg }

func sendErr(status int, code, msg string) error {
	return &SendError{Status: status, Code: code, Msg: msg}
}

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
	var req SendRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if _, err := h.Send(p, req); err != nil {
		var se *SendError
		if errors.As(err, &se) {
			http.Error(w, se.Msg, se.Status)
			return
		}
		http.Error(w, "send failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Send posts req as p, or refuses with a *SendError (see ServeHTTP).
func (h *ParticipantSendHandler) Send(p Participant, req SendRequest) (intercom.Squawk, error) {
	parts, err := h.att.RefParts(req.Attachments)
	if err != nil {
		return intercom.Squawk{}, sendErr(http.StatusBadRequest, "bad_request", "attachments are off")
	}
	if req.Body == "" && len(parts) > 0 {
		req.Body = h.att.Names(req.Attachments)
	}
	if req.Body == "" {
		return intercom.Squawk{}, sendErr(http.StatusBadRequest, "bad_request", "empty body")
	}
	if req.To == "" {
		return intercom.Squawk{}, sendErr(http.StatusBadRequest, "bad_request", "empty to")
	}
	if !intercom.ValidContentType(req.ContentType) {
		return intercom.Squawk{}, sendErr(http.StatusBadRequest, "bad_request", "unsupported content_type (want text/markdown or text/plain)")
	}
	if h.ic == nil {
		return intercom.Squawk{}, sendErr(http.StatusServiceUnavailable, "unavailable", "messaging not configured")
	}
	pl, err := h.plan(p, req.To)
	switch {
	case errors.Is(err, ErrSendDenied):
		return intercom.Squawk{}, sendErr(http.StatusForbidden, "forbidden", "not allowed")
	case errors.Is(err, ErrSendUnresolved), errors.Is(err, ErrRemoved):
		return intercom.Squawk{}, sendErr(http.StatusNotFound, "not_found", "recipient not found")
	case err != nil:
		h.log.Error("participant send: plan failed", "user", string(p.UserID), "error", err.Error())
		return intercom.Squawk{}, sendErr(http.StatusInternalServerError, "internal", "send failed")
	}
	if req.ReplyTo != "" {
		if a, ok := h.ic.GetAsk(req.ReplyTo); !ok || a.Channel != pl.Channel.ID {
			return intercom.Squawk{}, sendErr(http.StatusBadRequest, "bad_request", "reply_to must be an ask in this conversation")
		}
	}
	m, err := h.ic.Post(pl, intercom.Squawk{From: p.UserID, Body: req.Body, ContentType: req.ContentType, ReplyTo: req.ReplyTo, Parts: parts})
	if errors.Is(err, intercom.ErrInvalid) {
		return intercom.Squawk{}, sendErr(http.StatusBadRequest, "bad_request", "invalid attachments")
	}
	if err != nil {
		h.log.Error("participant send: append failed", "user", string(p.UserID), "channel", string(pl.Channel.ID), "error", err.Error())
		return intercom.Squawk{}, sendErr(http.StatusBadGateway, "unavailable", "send failed")
	}
	h.log.Info("participant send", "user", string(p.UserID), "channel", string(pl.Channel.ID), "audience", len(pl.Audience), "bytes", len(req.Body))
	return m, nil
}

// plan resolves a /me send: a channel id, a person to chat with, or a
// session to reach in its home channel.
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
		return h.ic.PlanHome(p.UserID, ident.ID(ref))
	}
	return Planned{}, ErrSendUnresolved
}
