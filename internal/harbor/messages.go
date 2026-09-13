package harbor

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// maxMessageBodyBytes caps a POST body to bound abuse (~16 KiB).
const maxMessageBodyBytes = 16 * 1024

// Comment is one message on a cove's ticket, as returned by a Commenter. ID and
// At are best-effort: a Commenter that cannot supply them leaves them unset. At
// is a pointer so an unset timestamp is omitted from the wire (json omitempty is
// ineffective for a time.Time value, which would serialize a bogus zero time).
type Comment struct {
	ID     string     `json:"id,omitempty"`
	Author string     `json:"author"`
	Body   string     `json:"body"`
	At     *time.Time `json:"at,omitempty"`
}

// Commenter is the narrow ticket-comment capability the /messages handler
// needs. It is satisfied (via a small adapter at the wiring layer — see
// cmd/at-harbor) by *linear.Client. Keeping it as a local interface, rather
// than importing internal/dispatch/linear or internal/dispatch/scheduler here,
// keeps internal/harbor's core free of the kit/grpc import graph those packages
// pull in transitively.
type Commenter interface {
	// IssueByIdentifier resolves a human ticket identifier (e.g. "AET-42") to
	// the tracker's internal issue id.
	IssueByIdentifier(ctx context.Context, identifier string) (string, error)
	// PostComment adds a comment to the given issue.
	PostComment(ctx context.Context, issueID, body string) error
	// Comments returns the issue's comments.
	Comments(ctx context.Context, issueID string) ([]Comment, error)
}

// messagesStore is the narrow slice of Store the /messages handler needs.
// harbor.Store satisfies it.
type messagesStore interface {
	Lookup(tokenHash string) (Actor, bool)
	GetInstance(actorID string) (Instance, bool)
}

// MessagesHandler is harbor's self-scoped brokered messaging endpoint: an
// authenticated cove reads/sends comments on ONLY its own ticket. The ticket
// identifier comes solely from the caller's own Instance.Unit (server-derived,
// resolved after authentication) — the request carries no ticket/target
// parameter of any kind, so a cove has no way to name another cove's ticket.
// Implements http.Handler.
type MessagesHandler struct {
	store messagesStore
	cmt   Commenter
	log   *slog.Logger
}

// NewMessagesHandler constructs a MessagesHandler.
func NewMessagesHandler(store messagesStore, cmt Commenter, log *slog.Logger) *MessagesHandler {
	return &MessagesHandler{store: store, cmt: cmt, log: log}
}

func (h *MessagesHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || tok == "" {
		http.Error(w, "missing identity", http.StatusUnauthorized)
		return
	}
	// Fail closed: an unrecognized token hash denies. This is the broker's
	// exact lookup primitive (internal/harbor/proxy.go) — a hash-map lookup,
	// never a manual token comparison.
	actor, ok := h.store.Lookup(HashToken(tok))
	if !ok {
		http.Error(w, "unknown identity", http.StatusUnauthorized)
		return
	}
	inst, ok := h.store.GetInstance(actor.ID)
	if !ok {
		http.Error(w, "no instance", http.StatusForbidden)
		return
	}

	ctx := r.Context()
	issueID, err := h.cmt.IssueByIdentifier(ctx, inst.Unit)
	if err != nil {
		h.log.Error("messages: resolve ticket failed", "actor", actor.ID, "ticket", inst.Unit, "error", err.Error())
		http.Error(w, "ticket unavailable", http.StatusBadGateway)
		return
	}

	switch r.Method {
	case http.MethodPost:
		h.handlePost(w, r, actor, inst, issueID)
	case http.MethodGet:
		h.handleGet(w, r, actor, inst, issueID)
	}
}

func (h *MessagesHandler) handlePost(w http.ResponseWriter, r *http.Request, actor Actor, inst Instance, issueID string) {
	r.Body = http.MaxBytesReader(w, r.Body, maxMessageBodyBytes)
	var req struct {
		Body string `json:"body"`
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

	if err := h.cmt.PostComment(r.Context(), issueID, req.Body); err != nil {
		h.log.Error("messages: post comment failed", "actor", actor.ID, "ticket", inst.Unit, "error", err.Error())
		http.Error(w, "send failed", http.StatusBadGateway)
		return
	}
	h.log.Info("messages", "actor", actor.ID, "ticket", inst.Unit, "op", "send", "bytes", len(req.Body))
	w.WriteHeader(http.StatusNoContent)
}

func (h *MessagesHandler) handleGet(w http.ResponseWriter, r *http.Request, actor Actor, inst Instance, issueID string) {
	comments, err := h.cmt.Comments(r.Context(), issueID)
	if err != nil {
		h.log.Error("messages: read failed", "actor", actor.ID, "ticket", inst.Unit, "error", err.Error())
		http.Error(w, "read failed", http.StatusBadGateway)
		return
	}
	h.log.Info("messages", "actor", actor.ID, "ticket", inst.Unit, "op", "read", "count", len(comments))
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(struct {
		Messages []Comment `json:"messages"`
	}{Messages: comments}); err != nil {
		h.log.Error("messages: encode response failed", "actor", actor.ID, "ticket", inst.Unit, "error", err.Error())
	}
}
