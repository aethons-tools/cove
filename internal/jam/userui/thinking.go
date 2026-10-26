package userui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/sessionevents"
	"github.com/aethons-tools/cove/internal/jam/sessionview"
)

// The Thinking tab (docs/usage/jam/intercom-ui.md#the-session-view): a
// member's live view of what a session is doing — its newest stream's events
// over SSE, older pages on demand, and one event's raw JSON. Every route
// re-checks that the viewer may see the session's home channel.

// thinkingTail is how many of the newest events a fresh feed backfills;
// thinkingPageSize how many a "Load earlier" page holds (vars: tests shrink
// them).
var (
	thinkingTail     = 300
	thinkingPageSize = 300
)

const (
	// rawMax caps one event's raw JSON as served.
	rawMax = 1 << 20
	// maxThinkingStreams caps a viewer's open Thinking feeds.
	maxThinkingStreams = 4
)

// thinkingSession resolves the session for a Thinking route: 401 without a
// participant, 404 when unseen, 204 when events aren't recorded.
func (h *handler) thinkingSession(w http.ResponseWriter, r *http.Request) (jam.Participant, jam.Instance, bool) {
	p, ok := jam.ParticipantFrom(r)
	if !ok {
		http.Error(w, "no participant", http.StatusUnauthorized)
		return p, jam.Instance{}, false
	}
	inst, _, ok := h.sessionFor(p, r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return p, inst, false
	}
	if h.d.Events == nil || h.d.Hub == nil {
		w.WriteHeader(http.StatusNoContent)
		return p, inst, false
	}
	return p, inst, true
}

// thinkingFragment renders one event as the thinking-event element.
func (h *handler) thinkingFragment(v sessionview.EventView) string {
	var b bytes.Buffer
	if err := pages["inbox"].ExecuteTemplate(&b, "thinking-event", inboxPage{PaneEvent: &v}); err != nil {
		h.lg.Error("userui thinking render failed", "error", err.Error())
		return ""
	}
	// html/template leaves CR raw; keep it as an entity (SSE framing is
	// separately CR-safe in sessionview.WriteSSE).
	return strings.ReplaceAll(b.String(), "\r", "&#13;")
}

// thinkingEventOf is v as the app's Thinking tab reads it: the
// thinking-event template's fields (a gap shows its summary; kind other
// keeps no detail).
func thinkingEventOf(v sessionview.EventView) ThinkingEvent {
	ev := ThinkingEvent{Stream: v.StreamID, Seq: v.Seq, Turn: v.Turn, Kind: string(v.Kind), Tool: v.Tool, ToolUseID: v.ToolUseID,
		Text: v.Narrative, Detail: v.Detail, DetailTruncated: v.DetailTruncated, IsError: v.IsError, TruncatedBytes: v.TruncatedBytes,
		CostUSD: v.CostUSD, DurationMS: v.DurationMS, ClaudeSession: v.ClaudeSession}
	if !v.At.IsZero() {
		ev.At = v.At.UnixMilli()
	}
	if v.Gap {
		ev.Text = v.Summary
	}
	if v.Kind == sessionview.KindOther {
		ev.Detail, ev.DetailTruncated = "", 0
	}
	return ev
}

// thinkingJSON is one event as the JSON feed's ev data (one line).
func (h *handler) thinkingJSON(v sessionview.EventView) string {
	b, err := json.Marshal(thinkingEventOf(v))
	if err != nil {
		h.lg.Error("userui thinking json failed", "error", err.Error())
		return "{}"
	}
	return string(b)
}

// thinkingEvents is the session's live Thinking feed (sessionview.Serve,
// following its newest stream), at most maxThinkingStreams per viewer; it
// ends once the viewer loses access to the session. ?format=json sends each
// event as ThinkingEvent JSON (the app) rather than the HTML fragment.
func (h *handler) thinkingEvents(w http.ResponseWriter, r *http.Request) {
	p, inst, ok := h.thinkingSession(w, r)
	if !ok {
		return
	}
	h.mu.Lock()
	if h.streams[p.UserID] >= maxThinkingStreams {
		h.mu.Unlock()
		http.Error(w, "too many open Thinking feeds; close one", http.StatusTooManyRequests)
		return
	}
	if h.streams == nil {
		h.streams = map[ident.ID]int{}
	}
	h.streams[p.UserID]++
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		if h.streams[p.UserID]--; h.streams[p.UserID] <= 0 {
			delete(h.streams, p.UserID)
		}
		h.mu.Unlock()
	}()
	id := r.PathValue("id")
	// Re-checked on each keep-alive: a viewer who loses the session (left
	// its home channel, the session gone) stops receiving it.
	allowed := func() bool {
		again, _, ok := h.sessionFor(p, id)
		return ok && again.ActorID == inst.ActorID
	}
	event := h.thinkingFragment
	if r.URL.Query().Get("format") == "json" {
		event = h.thinkingJSON // the Jam app (spec §2)
	}
	sessionview.Serve(w, r, sessionview.Source{Store: h.d.Events, Hub: h.d.Hub, ActorID: inst.ActorID,
		Tail: thinkingTail, FollowNewest: true, Event: event, Allowed: allowed})
}

// thinkingStream is the request's ?stream=, or a 400.
func thinkingStream(w http.ResponseWriter, r *http.Request) (string, bool) {
	s := r.URL.Query().Get("stream")
	if !sessionevents.ValidStreamID(s) {
		http.Error(w, "bad stream", http.StatusBadRequest)
		return "", false
	}
	return s, true
}

// thinkingUint is the request's query parameter name as a positive integer,
// or a 400.
func thinkingUint(w http.ResponseWriter, r *http.Request, name string) (uint64, bool) {
	n, err := strconv.ParseUint(r.URL.Query().Get(name), 10, 64)
	if err != nil || n == 0 {
		http.Error(w, "bad "+name, http.StatusBadRequest)
		return 0, false
	}
	return n, true
}

// thinkingPage renders the thinkingPageSize events of stream below ?before=,
// with a "Load earlier" button on top when older ones exist.
func (h *handler) thinkingPage(w http.ResponseWriter, r *http.Request) {
	_, inst, ok := h.thinkingSession(w, r)
	if !ok {
		return
	}
	stream, ok := thinkingStream(w, r)
	if !ok {
		return
	}
	before, ok := thinkingUint(w, r, "before")
	if !ok {
		return
	}
	evs, below, err := h.thinkingPageOf(inst.ActorID, stream, before)
	if err != nil {
		h.lg.Error("thinking page: listing events failed", "session", inst.ActorID, "error", err.Error())
		http.Error(w, "listing events failed; see the jam log", http.StatusInternalServerError)
		return
	}
	page := inboxPage{PaneEvents: evs, PaneBefore: below}
	var b bytes.Buffer
	if err := pages["inbox"].ExecuteTemplate(&b, "thinkingpage", page); err != nil {
		h.lg.Error("userui render failed", "tmpl", "thinkingpage", "error", err.Error())
		http.Error(w, "render failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(bytes.ReplaceAll(b.Bytes(), []byte("\r"), []byte("&#13;")))
}

// thinkingRaw serves one event's pretty JSON as plain text, capped at rawMax.
func (h *handler) thinkingRaw(w http.ResponseWriter, r *http.Request) {
	_, inst, ok := h.thinkingSession(w, r)
	if !ok {
		return
	}
	stream, ok := thinkingStream(w, r)
	if !ok {
		return
	}
	seq, ok := thinkingUint(w, r, "seq")
	if !ok {
		return
	}
	raw, cut, found, err := h.thinkingRawOf(inst.ActorID, stream, seq)
	if err != nil {
		h.lg.Error("thinking raw: listing events failed", "session", inst.ActorID, "error", err.Error())
		http.Error(w, "listing events failed; see the jam log", http.StatusInternalServerError)
		return
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = io.WriteString(w, raw)
	if cut > 0 {
		_, _ = fmt.Fprintf(w, "\n… truncated %d bytes", cut)
	}
}

// thinkingPageOf is the thinkingPageSize events of actor's stream below
// before, in seq order, and the seq below which older ones exist (0 = none).
func (h *handler) thinkingPageOf(actor, stream string, before uint64) ([]sessionview.EventView, uint64, error) {
	evs, err := h.d.Events.List(sessionevents.Filter{ActorID: actor, StreamID: stream, BeforeSeq: before, Newest: true, Limit: thinkingPageSize})
	if err != nil {
		return nil, 0, err
	}
	out := make([]sessionview.EventView, 0, len(evs))
	for _, ev := range evs {
		out = append(out, sessionview.View(ev))
	}
	var below uint64
	if len(evs) > 0 && evs[0].Seq > 1 {
		below = evs[0].Seq
	}
	return out, below, nil
}

// thinkingRawOf is one event's pretty JSON, capped at rawMax (truncated:
// the bytes cut); found false when the stream has no such seq.
func (h *handler) thinkingRawOf(actor, stream string, seq uint64) (string, int, bool, error) {
	evs, err := h.d.Events.List(sessionevents.Filter{ActorID: actor, StreamID: stream, AfterSeq: seq - 1, Limit: 1})
	if err != nil || len(evs) == 0 || evs[0].Seq != seq {
		return "", 0, false, err
	}
	raw := sessionview.View(evs[0]).Raw
	if len(raw) > rawMax {
		return raw[:rawMax], len(raw) - rawMax, true, nil
	}
	return raw, 0, true, nil
}
