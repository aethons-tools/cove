package harbor

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/msglog"
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
	GetRole(project, name string) (Role, bool)
	GetRoster(project string) (Roster, bool)
}

// appender is the narrow write side of the message Log used for the outbound
// dual-write shadow. Satisfied by *msglog.Log; nil disables the shadow.
type appender interface {
	Append(m msglog.Message) (msglog.Message, error)
}

// MessagesHandler is harbor's brokered messaging endpoint. Reads, and sends
// with no `to`, are self-scoped by construction: the ticket identifier comes
// solely from the caller's own Instance.Unit (server-derived, resolved after
// authentication). A send may instead carry a `to` target; that path is
// authorized by the comms access-graph (DecideSend) and, once authorized, may
// deliver to a human — an @-mention posted on the cove's own ticket — or to a
// channel — a comment on the channel's own thread (resolved from the roster's
// Channel.Ref), i.e. a different ticket than the caller's own. Implements
// http.Handler.
type MessagesHandler struct {
	store messagesStore
	cmt   Commenter
	lg    appender
	log   *slog.Logger
}

// NewMessagesHandler constructs a MessagesHandler. lg may be nil, which
// disables the outbound shadow-write to the message Log.
func NewMessagesHandler(store messagesStore, cmt Commenter, lg appender, log *slog.Logger) *MessagesHandler {
	return &MessagesHandler{store: store, cmt: cmt, lg: lg, log: log}
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

	// GET /messages/targets — the actor's addressable targets. Handled before
	// ticket resolution: targets needs no ticket, so a targets request must
	// never resolve one (and must never fail if the ticket is unavailable).
	if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/targets") {
		h.handleTargets(w, r, actor)
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
		To   string `json:"to"`
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

	// deliverIssue defaults to the cove's own ticket; a channel target overrides
	// it. body may be prefixed with an @-mention for a human target. logicalTo
	// mirrors the same default/override shape for the shadow-write below, but
	// stays in terms of the logical target (never the resolved ticket id).
	deliverIssue, body := issueID, req.Body
	logicalTo := msglog.Target{Kind: "channel", Ref: inst.Unit} // no `to` → own ticket-as-channel
	if req.To != "" {
		st, err := DecideSend(actor, h.store.GetRole, h.store.GetRoster, req.To, time.Now())
		switch {
		case errors.Is(err, ErrSendDenied):
			http.Error(w, "target not authorized", http.StatusForbidden)
			return
		case errors.Is(err, ErrSendUnresolved):
			http.Error(w, "target not found", http.StatusNotFound)
			return
		case err != nil:
			http.Error(w, "target error", http.StatusForbidden)
			return
		}
		logicalTo = msglog.Target{Kind: st.Kind, Ref: st.Name}
		switch st.Kind {
		case "human":
			body = "@" + st.Handle + " " + req.Body // reply lands on own ticket → existing wake-on
		case "channel":
			chID, err := h.cmt.IssueByIdentifier(r.Context(), st.Ref)
			if err != nil {
				h.log.Error("messages: resolve channel failed", "actor", actor.ID, "target", req.To, "error", err.Error())
				http.Error(w, "channel unavailable", http.StatusBadGateway)
				return
			}
			deliverIssue = chID
		}
	}

	if err := h.cmt.PostComment(r.Context(), deliverIssue, body); err != nil {
		h.log.Error("messages: post comment failed", "actor", actor.ID, "ticket", inst.Unit, "error", err.Error())
		http.Error(w, "send failed", http.StatusBadGateway)
		return
	}
	h.log.Info("messages", "actor", actor.ID, "ticket", inst.Unit, "op", "send", "to", req.To, "bytes", len(req.Body))

	// Best-effort shadow-write: the Log stores the logical, raw message (the
	// live PostComment above is the source of truth for delivery). An append
	// failure never fails the send — it is warn-logged (actor + error only,
	// never the body) and swallowed.
	if h.lg != nil {
		if _, err := h.lg.Append(msglog.Message{
			From:    msglog.Target{Kind: "actor", Ref: actor.ID},
			To:      []msglog.Target{logicalTo},
			Body:    req.Body, // raw — @handle rendering is the adapter's job at egress (Slice 3)
			Project: inst.Project,
		}); err != nil {
			h.log.Warn("messages: shadow append failed", "actor", actor.ID, "error", err.Error())
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

// targetOut is one entry in the GET /messages/targets response. Handles are
// deliberately omitted: they are roster config, not something the agent needs
// to address a target — the agent addresses by "human:<name>"/"channel:<name>".
type targetOut struct {
	Target string `json:"target"` // "human:alice"
	Kind   string `json:"kind"`
	Name   string `json:"name"`
}

func (h *MessagesHandler) handleTargets(w http.ResponseWriter, r *http.Request, actor Actor) {
	targets := ListTargets(actor, h.store.GetRole, h.store.GetRoster, time.Now())
	out := make([]targetOut, 0, len(targets))
	for _, t := range targets {
		out = append(out, targetOut{Target: t.Kind + ":" + t.Name, Kind: t.Kind, Name: t.Name})
	}
	h.log.Info("messages", "actor", actor.ID, "op", "targets", "count", len(out))
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(struct {
		Targets []targetOut `json:"targets"`
	}{Targets: out}); err != nil {
		h.log.Error("messages: encode targets failed", "actor", actor.ID, "error", err.Error())
	}
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
