package userui

import (
	"encoding/json"
	"net/http"

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

// thinkingEventOf is v as the app's Thinking tab reads it (a gap shows its
// summary; kind other keeps no detail).
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
// ends once the viewer loses access to the session. Each event is
// ThinkingEvent JSON.
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
	sessionview.Serve(w, r, sessionview.Source{Store: h.d.Events, Hub: h.d.Hub, ActorID: inst.ActorID,
		Tail: thinkingTail, FollowNewest: true, Event: h.thinkingJSON, Allowed: allowed})
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
