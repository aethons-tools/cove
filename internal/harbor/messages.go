package harbor

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/msglog"
)

// maxMessageBodyBytes caps a POST body to bound abuse (~16 KiB).
const maxMessageBodyBytes = 16 * 1024

// Comment is one message in a cove's inbox, as returned by GET /messages. ID
// and At are best-effort: a reader that cannot supply them leaves them unset.
// At is a pointer so an unset timestamp is omitted from the wire (json
// omitempty is ineffective for a time.Time value, which would serialize a
// bogus zero time).
type Comment struct {
	ID     string     `json:"id,omitempty"`
	Author string     `json:"author"`
	Body   string     `json:"body"`
	At     *time.Time `json:"at,omitempty"`
}

// inboxReader is the narrow read side of the message Log the /messages GET
// path needs — seekable in both directions, so the handler never needs the
// unbounded ReadInbox. Satisfied by *msglog.Log and *msglogpg.Store; nil
// disables reads (GET → 503).
type inboxReader interface {
	ReadInboxSince(t msglog.Target, afterID string, limit int) []msglog.Message
	ReadInboxBefore(t msglog.Target, beforeID string, limit int) []msglog.Message
}

// messagesStore is the narrow slice of Store the /messages handler needs.
// harbor.Store satisfies it.
type messagesStore interface {
	Lookup(tokenHash string) (Actor, bool)
	GetInstance(actorID string) (Instance, bool)
	GetRole(project, name string) (Role, bool)
	GetRoster(project string) (Roster, bool)
	AdvanceCommitCursor(actorID, upTo string) (Instance, error)
}

// appender is the narrow write side of the message Log — the authoritative
// send path for /messages POST. Satisfied by *msglog.Log; nil means messaging
// is unconfigured and a send fails with 503.
type appender interface {
	Append(m msglog.Message) (msglog.Message, error)
}

// MessagesHandler is harbor's brokered messaging endpoint. Reads, and sends
// with no `to`, are self-scoped by construction: the ticket identifier comes
// solely from the caller's own Instance.Unit (server-derived, resolved after
// authentication). A send may instead carry a `to` target; that path is
// authorized by the comms access-graph (DecideSend). A send only appends the
// logical message to the Log — it never talks to the tracker directly; a
// separate egress engine renders and delivers it (an @-mention on the cove's
// own ticket for a human target, a comment on the channel's own thread for a
// channel target). Implements http.Handler.
type MessagesHandler struct {
	store  messagesStore
	reader inboxReader
	lg     appender
	log    *slog.Logger
}

// NewMessagesHandler constructs a MessagesHandler. reader and lg may each be
// nil: a nil lg makes a send fail 503, a nil reader makes a read fail 503.
func NewMessagesHandler(store messagesStore, reader inboxReader, lg appender, log *slog.Logger) *MessagesHandler {
	return &MessagesHandler{store: store, reader: reader, lg: lg, log: log}
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

	// POST /messages/commit — advances the cove's durable commit cursor.
	// Handled before ticket resolution: commit needs no ticket. A non-POST
	// method on this path must never fall through to handleGet (a silent
	// read) — reject it explicitly instead.
	if strings.HasSuffix(r.URL.Path, "/commit") {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h.handleCommit(w, r, actor)
		return
	}

	switch r.Method {
	case http.MethodPost:
		h.handlePost(w, r, actor, inst)
	case http.MethodGet:
		h.handleGet(w, r, actor, inst)
	}
}

func (h *MessagesHandler) handlePost(w http.ResponseWriter, r *http.Request, actor Actor, inst Instance) {
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

	// Resolve the logical target (authz via the comms access-graph). No `to` →
	// the cove's own ticket, modeled as channel:<Unit>. A human/channel target
	// is authorized here; rendering + ticket resolution happen at egress.
	logicalTo := msglog.Target{Kind: "channel", Ref: inst.Unit}
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
	}

	// The Log is the authoritative delivery path (egress delivers it to Linear).
	// A send requires a configured Log; an append failure fails the send.
	if h.lg == nil {
		http.Error(w, "messaging not configured", http.StatusServiceUnavailable)
		return
	}
	if _, err := h.lg.Append(msglog.Message{
		From:    msglog.Target{Kind: "actor", Ref: actor.ID},
		To:      []msglog.Target{logicalTo},
		Body:    req.Body, // raw — @handle rendering is the adapter's job at egress
		Project: inst.Project,
	}); err != nil {
		h.log.Error("messages: append failed", "actor", actor.ID, "ticket", inst.Unit, "error", err.Error())
		http.Error(w, "send failed", http.StatusBadGateway)
		return
	}
	h.log.Info("messages", "actor", actor.ID, "ticket", inst.Unit, "op", "send", "to", req.To, "bytes", len(req.Body))
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

// defaultReadLimit and maxReadLimit bound GET /messages page size: the
// default page when the caller omits limit, and the hard cap a caller cannot
// exceed regardless of what they ask for.
const (
	defaultReadLimit = 50
	maxReadLimit     = 500
)

// handleGet serves a seekable page of the cove's own inbox. anchor selects
// where the page starts — "" or "cursor" (the cove's durable CommitCursor,
// the default), "start"/"end" (the log's bounds), or "id" (an explicit
// message id via ?id=) — and dir selects which way the page reads from
// there: "" or "forward" (ReadInboxSince) or "backward" (ReadInboxBefore). A
// read never advances CommitCursor; only POST /messages/commit does that.
func (h *MessagesHandler) handleGet(w http.ResponseWriter, r *http.Request, actor Actor, inst Instance) {
	if h.reader == nil {
		http.Error(w, "messaging not configured", http.StatusServiceUnavailable)
		return
	}
	q := r.URL.Query()
	anchor := q.Get("anchor") // "", cursor, start, end, id
	dir := q.Get("dir")       // "", forward, backward
	limit := defaultReadLimit
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			http.Error(w, "invalid limit", http.StatusBadRequest)
			return
		}
		limit = n
	}
	if limit > maxReadLimit {
		limit = maxReadLimit
	}
	target := msglog.Target{Kind: "actor", Ref: actor.ID}

	var msgs []msglog.Message
	switch anchor {
	case "", "cursor":
		if dir == "backward" {
			msgs = h.reader.ReadInboxBefore(target, inst.CommitCursor, limit)
		} else {
			msgs = h.reader.ReadInboxSince(target, inst.CommitCursor, limit)
		}
	case "start":
		msgs = h.reader.ReadInboxSince(target, "", limit)
	case "end":
		msgs = h.reader.ReadInboxBefore(target, "", limit)
	case "id":
		id := q.Get("id")
		if id == "" {
			http.Error(w, "anchor=id requires id", http.StatusBadRequest)
			return
		}
		if dir == "backward" {
			msgs = h.reader.ReadInboxBefore(target, id, limit)
		} else {
			msgs = h.reader.ReadInboxSince(target, id, limit)
		}
	default:
		http.Error(w, "invalid anchor", http.StatusBadRequest)
		return
	}

	out := make([]Comment, 0, len(msgs))
	for i := range msgs {
		m := msgs[i]
		at := m.At
		out = append(out, Comment{ID: m.ID, Author: m.From.Ref, Body: m.Body, At: &at})
	}
	pageFirst, pageLast := "", ""
	if len(msgs) > 0 {
		pageFirst, pageLast = msgs[0].ID, msgs[len(msgs)-1].ID
	}
	h.log.Info("messages", "actor", actor.ID, "ticket", inst.Unit, "op", "read", "anchor", anchor, "dir", dir, "count", len(out))
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(struct {
		Messages        []Comment `json:"messages"`
		CommittedCursor string    `json:"committed_cursor"`
		PageFirst       string    `json:"page_first"`
		PageLast        string    `json:"page_last"`
	}{Messages: out, CommittedCursor: inst.CommitCursor, PageFirst: pageFirst, PageLast: pageLast}); err != nil {
		h.log.Error("messages: encode response failed", "actor", actor.ID, "ticket", inst.Unit, "error", err.Error())
	}
}

// handleCommit advances the caller's durable commit cursor to up_to (POST
// /messages/commit {"up_to": "<message id>"}). The actor comes solely from
// the authenticated token, never the request body — there is no way for a
// cove to advance another cove's cursor.
func (h *MessagesHandler) handleCommit(w http.ResponseWriter, r *http.Request, actor Actor) {
	r.Body = http.MaxBytesReader(w, r.Body, maxMessageBodyBytes)
	var req struct {
		UpTo string `json:"up_to"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "up_to required", http.StatusBadRequest)
		return
	}
	if req.UpTo == "" {
		http.Error(w, "up_to required", http.StatusBadRequest)
		return
	}
	inst, err := h.store.AdvanceCommitCursor(actor.ID, req.UpTo)
	if err != nil {
		http.Error(w, "no instance", http.StatusForbidden)
		return
	}
	h.log.Info("messages", "actor", actor.ID, "op", "commit", "up_to", req.UpTo, "committed", inst.CommitCursor)
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(struct {
		CommittedCursor string `json:"committed_cursor"`
	}{CommittedCursor: inst.CommitCursor}); err != nil {
		h.log.Error("messages: encode commit response failed", "actor", actor.ID, "error", err.Error())
	}
}
