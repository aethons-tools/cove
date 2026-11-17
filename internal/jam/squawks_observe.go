package jam

import (
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/jam/sessionevents"
)

// The observe endpoints (docs/modules/ROOT/pages/usage/jam/session-observe.adoc):
// a session whose role has scope.observe reads other sessions, read-only —
// the listing, a session's activity, its streams and their events. Every
// route is a GET; nothing here writes to the intercom or the session store.

// Observe limits.
const (
	observeWindow = 24 * time.Hour // sessions' default window
	// A session's activity pages as the session view's does.
	observeActivityDefault = 50
	observeActivityMax     = 500
	observeEventsDefault   = 500
	observeEventsMax       = 2000
	// observeScanMax bounds the events one filtered page reads.
	observeScanMax = 20000
)

// SetObserve turns on the observe routes over the session-event store; nil
// (no store) answers them 503.
func (h *SquawksHandler) SetObserve(events sessionevents.Store) {
	h.events = events
	h.totals = nil
	if events != nil {
		h.totals = sessionevents.NewTotalsCache(events)
	}
}

// ObservedTotals are a session's lifetime totals (sessionevents.Totals).
type ObservedTotals struct {
	Turns        uint32  `json:"turns"`
	Episodes     uint32  `json:"episodes"`
	ToolCalls    int     `json:"tool_calls"`
	Errors       int     `json:"errors"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
}

// ObservedSession is one GET /squawks/observe/sessions entry.
type ObservedSession struct {
	ID       string         `json:"id"`
	Label    string         `json:"label"`
	Kind     string         `json:"kind"`
	Project  string         `json:"project"`
	Role     string         `json:"role"`
	Unit     string         `json:"unit,omitempty"`
	Owner    string         `json:"owner,omitempty"`
	RaisedAt time.Time      `json:"raised_at"`
	FirstAt  time.Time      `json:"first_at"`
	LastAt   time.Time      `json:"last_at"`
	Live     bool           `json:"live"`
	Totals   ObservedTotals `json:"totals"`
}

type observedSessions struct {
	Sessions []ObservedSession `json:"sessions"`
}

// ObservedStream is one of a session's streams.
type ObservedStream struct {
	StreamID string    `json:"stream_id"`
	FirstAt  time.Time `json:"first_at"`
	LastAt   time.Time `json:"last_at"`
	LastSeq  uint64    `json:"last_seq"`
	Events   int       `json:"events"`
}

type observedStreams struct {
	Streams []ObservedStream `json:"streams"`
}

// observedEvents is a page of one stream's events: NextAfterSeq is the
// after_seq that continues it, More whether the stream has more after it.
type observedEvents struct {
	StreamID     string            `json:"stream_id"`
	Events       []json.RawMessage `json:"events"`
	NextAfterSeq uint64            `json:"next_after_seq"`
	More         bool              `json:"more"`
}

// ObservedSquawk is one message of a session's activity; Out is whether the
// session sent it.
type ObservedSquawk struct {
	Squawk
	Out bool `json:"out"`
}

type observedActivity struct {
	Messages  []ObservedSquawk `json:"messages"`
	PageFirst string           `json:"page_first"`
	PageLast  string           `json:"page_last"`
	Earlier   bool             `json:"earlier"`
	Later     bool             `json:"later"`
}

// observeRoute serves /squawks/observe{rest} for actor's session inst: 403
// unless its role has scope.observe, then the listing or one session's view.
func (h *SquawksHandler) observeRoute(w http.ResponseWriter, r *http.Request, inst Instance, rest string) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Read live, so a revoked grant stops the next request.
	role, ok := h.store.GetRole(orDefaultProject(inst.Project), inst.Role)
	if !ok || !role.Scope.Observes() {
		http.Error(w, "the role does not observe sessions (scope.observe)", http.StatusForbidden)
		return
	}
	if h.events == nil {
		http.Error(w, "session events aren't recorded on this jam", http.StatusServiceUnavailable)
		return
	}
	if rest == "/sessions" {
		h.observeSessions(w, r)
		return
	}
	id, view, ok := strings.Cut(strings.TrimPrefix(rest, "/sessions/"), "/")
	if !ok || id == "" || !strings.HasPrefix(rest, "/sessions/") {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	streams, err := h.events.Streams(id)
	if err != nil {
		h.observeFailed(w, "streams", id, err)
		return
	}
	if _, known := h.store.GetInstance(id); !known && len(streams) == 0 {
		http.Error(w, "unknown session", http.StatusNotFound)
		return
	}
	switch view {
	case "activity":
		h.observeActivity(w, r, id)
	case "streams":
		out := observedStreams{Streams: []ObservedStream{}}
		for _, s := range slices.Backward(streams) { // the store's are newest first
			out.Streams = append(out.Streams, ObservedStream{StreamID: s.StreamID, FirstAt: s.FirstAt, LastAt: s.LastAt, LastSeq: s.LastSeq, Events: s.Events})
		}
		writeJSON(w, http.StatusOK, out)
	case "events":
		h.observeEvents(w, r, id, streams)
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

func (h *SquawksHandler) observeFailed(w http.ResponseWriter, what, id string, err error) {
	h.log.Error("observe: reading session events failed", "what", what, "session", id, "error", err.Error())
	http.Error(w, "reading session events failed", http.StatusInternalServerError)
}

// observeTime is query parameter name as RFC 3339, or def when absent.
func observeTime(r *http.Request, name string, def time.Time) (time.Time, bool) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def, true
	}
	t, err := time.Parse(time.RFC3339, v)
	return t, err == nil
}

func (h *SquawksHandler) observeSessions(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	until, ok := observeTime(r, "until", now)
	if !ok {
		http.Error(w, "until: not an RFC 3339 time", http.StatusBadRequest)
		return
	}
	since, ok := observeTime(r, "since", until.Add(-observeWindow))
	if !ok {
		http.Error(w, "since: not an RFC 3339 time", http.StatusBadRequest)
		return
	}
	if since.After(until) {
		http.Error(w, "since is after until", http.StatusBadRequest)
		return
	}
	ids, err := h.events.ActiveActors(since, until)
	if err != nil {
		h.observeFailed(w, "sessions", "", err)
		return
	}
	out := observedSessions{Sessions: []ObservedSession{}}
	for _, id := range ids {
		s, err := h.observedSession(id)
		if err != nil {
			h.observeFailed(w, "sessions", id, err)
			return
		}
		out.Sessions = append(out.Sessions, s)
	}
	// The most recently active first.
	slices.SortStableFunc(out.Sessions, func(a, b ObservedSession) int { return b.LastAt.Compare(a.LastAt) })
	writeJSON(w, http.StatusOK, out)
}

// observedSession is id's listing entry: its streams' span, its newest
// event's stamp, its intercom label, whether Jam runs it, and its totals.
func (h *SquawksHandler) observedSession(id string) (ObservedSession, error) {
	s := ObservedSession{ID: id}
	streams, err := h.events.Streams(id)
	if err != nil {
		return s, err
	}
	for i, st := range streams {
		if i == 0 || st.FirstAt.Before(s.FirstAt) {
			s.FirstAt = st.FirstAt
		}
		if st.LastAt.After(s.LastAt) {
			s.LastAt = st.LastAt
		}
	}
	if len(streams) > 0 {
		evs, err := h.events.List(sessionevents.Filter{ActorID: id, StreamID: streams[0].StreamID, Newest: true, Limit: 1})
		if err != nil {
			return s, err
		}
		if len(evs) > 0 {
			st := evs[0].Stamp
			s.Kind, s.Project, s.Role, s.Unit, s.Owner, s.RaisedAt = st.SessionKind, st.Project, st.Role, st.Unit, st.Owner, st.RaisedAt
		}
	}
	if h.ic != nil {
		s.Label = h.ic.PartyOf(ident.ID(id)).Label
	}
	if inst, ok := h.store.GetInstance(id); ok {
		s.Live = inst.Phase != PhaseGone
	}
	t, err := h.totals.Of(id)
	if err != nil {
		return s, err
	}
	s.Totals = ObservedTotals{Turns: t.Turns, Episodes: t.Episodes, ToolCalls: t.ToolCalls, Errors: t.Errors,
		InputTokens: t.InputTokens, OutputTokens: t.OutputTokens, CostUSD: t.CostUSD}
	return s, nil
}

func (h *SquawksHandler) observeEvents(w http.ResponseWriter, r *http.Request, id string, streams []sessionevents.StreamInfo) {
	q := r.URL.Query()
	var after uint64
	if v := q.Get("after_seq"); v != "" {
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			http.Error(w, "after_seq: not a number", http.StatusBadRequest)
			return
		}
		after = n
	}
	limit := observeEventsDefault
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			http.Error(w, "limit: not a positive number", http.StatusBadRequest)
			return
		}
		limit = min(n, observeEventsMax)
	}
	var types []string
	if v := q.Get("types"); v != "" {
		types = strings.Split(v, ",")
	}
	stream := q.Get("stream")
	if stream != "" && !sessionevents.ValidStreamID(stream) {
		http.Error(w, "stream: not a stream id", http.StatusBadRequest)
		return
	}
	var info sessionevents.StreamInfo
	switch i := slices.IndexFunc(streams, func(s sessionevents.StreamInfo) bool { return s.StreamID == stream }); {
	case stream == "" && len(streams) > 0:
		info = streams[0] // newest
	case stream != "" && i >= 0:
		info = streams[i]
	default:
		http.Error(w, "unknown stream", http.StatusNotFound)
		return
	}
	out := observedEvents{StreamID: info.StreamID, Events: []json.RawMessage{}, NextAfterSeq: after}
	for scanned := 0; len(out.Events) < limit && out.NextAfterSeq < info.LastSeq && scanned < observeScanMax; {
		evs, err := h.events.List(sessionevents.Filter{ActorID: id, StreamID: info.StreamID, AfterSeq: out.NextAfterSeq, Limit: min(limit, observeScanMax-scanned)})
		if err != nil {
			h.observeFailed(w, "events", id, err)
			return
		}
		if len(evs) == 0 {
			break
		}
		scanned += len(evs)
		for _, ev := range evs {
			out.NextAfterSeq = ev.Seq
			if types != nil && !slices.Contains(types, ev.Index.Type) {
				continue
			}
			b, err := json.Marshal(ev)
			if err != nil {
				h.observeFailed(w, "events", id, err)
				return
			}
			out.Events = append(out.Events, b)
			if len(out.Events) == limit {
				break
			}
		}
	}
	out.More = out.NextAfterSeq < info.LastSeq
	writeJSON(w, http.StatusOK, out)
}

// observeActivity pages id's activity — every message it sent or was
// delivered, in every conversation — as the session view's activity does:
// anchor end (the default), start, or id; dir forward (the default) or
// backward, from an id; limit 50 by default, at most 500.
func (h *SquawksHandler) observeActivity(w http.ResponseWriter, r *http.Request, id string) {
	if !h.configured(w) {
		return
	}
	q := r.URL.Query()
	anchor, mid, dir := q.Get("anchor"), q.Get("id"), q.Get("dir")
	limit := observeActivityDefault
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			http.Error(w, "limit: not a positive number", http.StatusBadRequest)
			return
		}
		limit = min(n, observeActivityMax)
	}
	self, lg := ident.ID(id), h.inbox.Log
	var msgs []intercom.Squawk
	switch {
	case dir != "" && dir != "forward" && dir != "backward":
		http.Error(w, "dir: forward or backward", http.StatusBadRequest)
		return
	case anchor == "" || anchor == "end":
		msgs = lg.ActivityBefore(self, 0, limit)
	case anchor == "start":
		msgs = lg.ActivitySince(self, 0, limit)
	case anchor == "id":
		if mid == "" {
			http.Error(w, "anchor=id needs id", http.StatusBadRequest)
			return
		}
		seq, found := lg.SeqOf(mid)
		if !found {
			http.Error(w, "id: no such message", http.StatusBadRequest)
			return
		}
		if dir == "backward" {
			msgs = lg.ActivityBefore(self, seq, limit)
		} else {
			msgs = lg.ActivitySince(self, seq, limit)
		}
	default:
		http.Error(w, "anchor: start, end, or id", http.StatusBadRequest)
		return
	}
	out := observedActivity{Messages: []ObservedSquawk{}}
	for _, m := range msgs {
		at := m.At
		from := h.ic.PartyOf(m.From)
		sq := Squawk{ID: m.ID, Author: from.Label, Body: m.Body, At: &at, ContentType: m.ContentType, From: &from, Parts: m.Parts}
		if m.Channel != "" {
			ch := h.ic.ChannelParty(m.Channel)
			sq.Channel = &ch
		}
		out.Messages = append(out.Messages, ObservedSquawk{Squawk: sq, Out: m.From == self})
	}
	if len(msgs) > 0 {
		first, last := msgs[0], msgs[len(msgs)-1]
		out.PageFirst, out.PageLast = first.ID, last.ID
		out.Earlier = len(lg.ActivityBefore(self, first.Seq, 1)) > 0
		out.Later = len(lg.ActivitySince(self, last.Seq, 1)) > 0
	}
	writeJSON(w, http.StatusOK, out)
}
