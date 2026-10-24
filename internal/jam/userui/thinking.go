package userui

import (
	"bytes"
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

// thinkingEvents is the session's live Thinking feed (sessionview.Serve,
// following its newest stream), at most maxThinkingStreams per viewer.
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
	sessionview.Serve(w, r, sessionview.Source{Store: h.d.Events, Hub: h.d.Hub, ActorID: inst.ActorID,
		Tail: thinkingTail, FollowNewest: true, Event: h.thinkingFragment})
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
	evs, err := h.d.Events.List(sessionevents.Filter{ActorID: inst.ActorID, StreamID: stream, BeforeSeq: before, Newest: true, Limit: thinkingPageSize})
	if err != nil {
		h.lg.Error("thinking page: listing events failed", "session", inst.ActorID, "error", err.Error())
		http.Error(w, "listing events failed; see the jam log", http.StatusInternalServerError)
		return
	}
	page := inboxPage{PaneEvents: make([]sessionview.EventView, 0, len(evs))}
	for _, ev := range evs {
		page.PaneEvents = append(page.PaneEvents, sessionview.View(ev))
	}
	if len(evs) > 0 && evs[0].Seq > 1 {
		page.PaneBefore = evs[0].Seq
	}
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
	evs, err := h.d.Events.List(sessionevents.Filter{ActorID: inst.ActorID, StreamID: stream, AfterSeq: seq - 1, Limit: 1})
	if err != nil {
		h.lg.Error("thinking raw: listing events failed", "session", inst.ActorID, "error", err.Error())
		http.Error(w, "listing events failed; see the jam log", http.StatusInternalServerError)
		return
	}
	if len(evs) == 0 || evs[0].Seq != seq {
		http.NotFound(w, r)
		return
	}
	raw := sessionview.View(evs[0]).Raw
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if len(raw) > rawMax {
		_, _ = io.WriteString(w, raw[:rawMax])
		_, _ = fmt.Fprintf(w, "\n… truncated %d bytes", len(raw)-rawMax)
		return
	}
	_, _ = io.WriteString(w, raw)
}
