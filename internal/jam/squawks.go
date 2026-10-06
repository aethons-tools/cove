package jam

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
)

// maxSquawkBodyBytes caps a POST body to bound abuse (~16 KiB).
const maxSquawkBodyBytes = 16 * 1024

// Squawk is one squawk in a session's inbox, as returned by GET /squawks.
// Channel and From are the channel model's (intercom slice 2); Author stays,
// holding From's label, for clients that predate them (a studio runs the
// cove-master baked into its image). A legacy entry (before the cutover) has
// no Channel. At is a pointer so an unset timestamp is omitted from the wire.
type Squawk struct {
	ID     string     `json:"id,omitempty"`
	Author string     `json:"author"`
	Body   string     `json:"body"`
	At     *time.Time `json:"at,omitempty"`
	// ContentType is how Body is meant to be read: text/markdown (default) or
	// text/plain (show literally).
	ContentType string `json:"content_type"`
	Channel     *Party `json:"channel,omitempty"`
	From        *Party `json:"from,omitempty"`
}

// SquawksHandler is Jam's brokered messaging endpoint for sessions. Every
// request is self-scoped by construction: the session is the authenticated
// token's, resolved server-side. A send is planned by the intercom (address
// → channel, the role's addressing as the ceiling, the audience) and
// appended to the channel log; relays render it onto surfaces later. A read
// is the session's inbox (SessionInbox) as a durable queue with an explicit
// commit cursor. Implements http.Handler.
type SquawksHandler struct {
	store Store
	ic    *Intercom
	inbox SessionInbox
	log   *slog.Logger
	now   func() time.Time
}

// NewSquawksHandler constructs a SquawksHandler. A nil ic or lg leaves
// messaging unconfigured: sends, reads and commits answer 503.
func NewSquawksHandler(store Store, ic *Intercom, lg intercom.Store, legacy LegacyInbox, log *slog.Logger) *SquawksHandler {
	return &SquawksHandler{store: store, ic: ic, inbox: SessionInbox{Log: lg, Legacy: legacy}, log: log, now: time.Now}
}

func (h *SquawksHandler) configured(w http.ResponseWriter) bool {
	if h.ic == nil || h.inbox.Log == nil {
		http.Error(w, "messaging not configured", http.StatusServiceUnavailable)
		return false
	}
	return true
}

func (h *SquawksHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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
	// exact lookup primitive (internal/jam/proxy.go) — a hash-map lookup,
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

	// GET /squawks/targets — the actor's addressable targets. Handled before
	// ticket resolution: targets needs no ticket, so a targets request must
	// never resolve one (and must never fail if the ticket is unavailable).
	if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/targets") {
		h.handleTargets(w, r, actor, inst)
		return
	}

	// POST /squawks/commit — advances the cove's durable commit cursor.
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

func (h *SquawksHandler) handlePost(w http.ResponseWriter, r *http.Request, actor Actor, inst Instance) {
	r.Body = http.MaxBytesReader(w, r.Body, maxSquawkBodyBytes)
	var req struct {
		Body        string `json:"body"`
		To          string `json:"to"`
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
	if !intercom.ValidContentType(req.ContentType) {
		http.Error(w, "unsupported content_type (want text/markdown or text/plain)", http.StatusBadRequest)
		return
	}
	if !h.configured(w) {
		return
	}
	// No `to`: the session's default channel (its ticket's, or a chat with the
	// user who started it). A `to` is planned against the role's addressing.
	pl, err := h.ic.Plan(Poster{ID: ident.ID(actor.ID), Session: &inst, Actor: &actor}, req.To, h.now())
	switch {
	case errors.Is(err, ErrSendDenied):
		http.Error(w, "target not authorized", http.StatusForbidden)
		return
	case errors.Is(err, ErrSendUnresolved), errors.Is(err, ErrRemoved):
		http.Error(w, "target not found", http.StatusNotFound)
		return
	case errors.Is(err, ErrNoDefaultChannel):
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	case err != nil:
		h.log.Error("intercom: plan failed", "actor", actor.ID, "error", err.Error())
		http.Error(w, "send failed", http.StatusInternalServerError)
		return
	}
	m, err := h.ic.Post(pl, intercom.Squawk{From: ident.ID(actor.ID), Body: req.Body, ContentType: req.ContentType})
	if err != nil {
		h.log.Error("intercom: append failed", "actor", actor.ID, "channel", string(pl.Channel.ID), "error", err.Error())
		http.Error(w, "send failed", http.StatusBadGateway)
		return
	}
	h.log.Info("intercom", "actor", actor.ID, "op", "send", "to", req.To, "channel", string(pl.Channel.ID), "audience", len(pl.Audience), "bytes", len(req.Body))
	ch := h.ic.ChannelParty(pl.Channel.ID)
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(struct {
		ID      string `json:"id"`
		Channel Party  `json:"channel"`
	}{ID: m.ID, Channel: ch}); err != nil {
		h.log.Error("intercom: encode send response failed", "actor", actor.ID, "error", err.Error())
	}
}

// targetOut is one entry in the GET /squawks/targets response. Handles are
// deliberately omitted: they are roster config, not something the agent needs
// to address a target — the agent addresses by "user:<name>"/"channel:<name>".
type targetOut struct {
	Target string `json:"target"` // "user:alice"
	Kind   string `json:"kind"`
	Name   string `json:"name"`
}

func (h *SquawksHandler) handleTargets(w http.ResponseWriter, r *http.Request, actor Actor, inst Instance) {
	targets := ListTargets(h.store, actor, h.now())
	out := make([]targetOut, 0, len(targets)+1)
	if inst.Unit != "" {
		out = append(out, targetOut{Target: "ticket:" + inst.Unit, Kind: "ticket", Name: inst.Unit})
	}
	for _, t := range targets {
		out = append(out, targetOut{Target: t.Kind + ":" + t.Name, Kind: t.Kind, Name: t.Name})
	}
	h.log.Info("intercom", "actor", actor.ID, "op", "targets", "count", len(out))
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(struct {
		Targets []targetOut `json:"targets"`
	}{Targets: out}); err != nil {
		h.log.Error("intercom: encode targets failed", "actor", actor.ID, "error", err.Error())
	}
}

// defaultReadLimit and maxReadLimit bound GET /squawks page size: the
// default page when the caller omits limit, and the hard cap a caller cannot
// exceed regardless of what they ask for.
const (
	defaultReadLimit = 50
	maxReadLimit     = 500
)

// handleGet serves a seekable page of the session's inbox. anchor selects
// where the page starts — "" or "cursor" (the durable CommitSeq, the
// default), "start"/"end", or "id" (?id=, resolved to its seq in either log)
// — and dir which way it reads: "" or "forward", or "backward". A read never
// advances the commit cursor; only POST /squawks/commit does. The wire stays
// id-based (seq is internal, never returned).
func (h *SquawksHandler) handleGet(w http.ResponseWriter, r *http.Request, actor Actor, inst Instance) {
	if !h.configured(w) {
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
	limit = min(limit, maxReadLimit)
	self := ident.ID(actor.ID)
	page := func(seq int64) []intercom.Squawk {
		if dir == "backward" {
			return h.inbox.Before(self, seq, limit)
		}
		return h.inbox.Since(self, seq, limit)
	}

	var msgs []intercom.Squawk
	switch anchor {
	case "", "cursor":
		msgs = page(inst.CommitSeq)
	case "start":
		msgs = h.inbox.Since(self, 0, limit)
	case "end":
		msgs = h.inbox.Before(self, 0, limit)
	case "id":
		id := q.Get("id")
		if id == "" {
			http.Error(w, "anchor=id requires id", http.StatusBadRequest)
			return
		}
		seq, ok := h.inbox.Log.SeqOf(id)
		if !ok {
			http.Error(w, "unknown message id", http.StatusBadRequest)
			return
		}
		msgs = page(seq)
	default:
		http.Error(w, "invalid anchor", http.StatusBadRequest)
		return
	}

	out := make([]Squawk, 0, len(msgs))
	for _, m := range msgs {
		at := m.At
		from := h.ic.PartyOf(m.From)
		sq := Squawk{ID: m.ID, Author: from.Label, Body: m.Body, At: &at, ContentType: m.ContentType, From: &from}
		if m.Channel != "" {
			ch := h.ic.ChannelParty(m.Channel)
			sq.Channel = &ch
		}
		out = append(out, sq)
	}
	pageFirst, pageLast := "", ""
	if len(msgs) > 0 {
		pageFirst, pageLast = msgs[0].ID, msgs[len(msgs)-1].ID
	}
	h.log.Info("intercom", "actor", actor.ID, "op", "read", "anchor", anchor, "dir", dir, "count", len(out))
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(struct {
		Squawks         []Squawk `json:"squawks"`
		CommittedCursor string   `json:"committed_cursor"`
		PageFirst       string   `json:"page_first"`
		PageLast        string   `json:"page_last"`
	}{Squawks: out, CommittedCursor: inst.CommitCursor, PageFirst: pageFirst, PageLast: pageLast}); err != nil {
		h.log.Error("intercom: encode response failed", "actor", actor.ID, "error", err.Error())
	}
}

// handleCommit advances the caller's durable commit cursor to up_to (POST
// /squawks/commit {"up_to": "<message id>"}). The session comes solely from
// the token, never the body. up_to resolves to its seq in either log first;
// an unknown id is a 400 and never advances anything.
func (h *SquawksHandler) handleCommit(w http.ResponseWriter, r *http.Request, actor Actor) {
	r.Body = http.MaxBytesReader(w, r.Body, maxSquawkBodyBytes)
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
	if !h.configured(w) {
		return
	}
	seq, ok := h.inbox.Log.SeqOf(req.UpTo)
	if !ok {
		http.Error(w, "unknown message id", http.StatusBadRequest)
		return
	}
	inst, err := h.store.AdvanceCommitCursor(actor.ID, req.UpTo, seq)
	if err != nil {
		http.Error(w, "no instance", http.StatusForbidden)
		return
	}
	h.log.Info("intercom", "actor", actor.ID, "op", "commit", "up_to", req.UpTo, "committed", inst.CommitCursor)
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(struct {
		CommittedCursor string `json:"committed_cursor"`
	}{CommittedCursor: inst.CommitCursor}); err != nil {
		h.log.Error("intercom: encode commit response failed", "actor", actor.ID, "error", err.Error())
	}
}
