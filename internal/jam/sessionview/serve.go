package sessionview

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/jam/sessionevents"
)

const (
	backfillPage = 500
	subBuffer    = 256
)

// KeepAlive is how often Serve writes a keep-alive comment and re-checks
// Source.Allowed (a var: tests shorten it).
var KeepAlive = 25 * time.Second

// isNewest reports whether stream is the actor's newest (by first event).
// Ingest persists before it publishes, so a live event's stream is listed.
func isNewest(src Source, stream string) bool {
	streams, err := src.Store.Streams(src.ActorID)
	return err == nil && len(streams) > 0 && streams[0].StreamID == stream
}

// sseLines normalizes CRLF and CR to LF: SSE parsers treat all three as line
// terminators, so any of them inside a field value would end the line early.
var sseLines = strings.NewReplacer("\r\n", "\n", "\r", "\n")

// WriteSSE emits one SSE message (CR-safe; id/event one line). Multi-line
// data gets one data: line each, split on every SSE line terminator, so agent
// text can never inject fields.
func WriteSSE(w io.Writer, event, id, data string) {
	oneLine := func(v string) string { return strings.ReplaceAll(sseLines.Replace(v), "\n", " ") }
	var b strings.Builder
	if id != "" {
		fmt.Fprintf(&b, "id: %s\n", oneLine(id))
	}
	fmt.Fprintf(&b, "event: %s\n", oneLine(event))
	for _, line := range strings.Split(sseLines.Replace(data), "\n") {
		fmt.Fprintf(&b, "data: %s\n", line)
	}
	b.WriteString("\n")
	_, _ = io.WriteString(w, b.String())
}

// Source is what Serve streams and how it renders it. Store and Hub must be
// non-nil (callers answer 204 when sessions are not configured).
type Source struct {
	Store   sessionevents.Store
	Hub     *sessionevents.Hub
	ActorID string
	// Tail > 0: backfill only the newest Tail events; on a fresh connect,
	// then `more` with the lowest seq sent, when older exist (a resume sends
	// neither: the client holds the rest, or `skipped` marks the hole).
	// 0 = the whole stream (admin).
	Tail int
	// FollowNewest: with no ?stream=, follow the actor's newest stream. A
	// resume via Last-Event-ID continues its stream only while that is still
	// the newest; otherwise Serve sends `newstream` (data = the newest id)
	// before any backfill and returns. Likewise, when a live event arrives
	// from another stream that is now the newest, send `newstream` and
	// return. Events from other, older streams are ignored. A stream pinned
	// by ?stream= never triggers newstream.
	FollowNewest bool
	Event        func(EventView) string            // the `ev` fragment
	Totals       func(sessionevents.Totals) string // the `totals` fragment; nil = none sent
	// Allowed, re-checked on each keep-alive tick, ends the feed once it
	// reports false (the viewer lost access); nil = always allowed.
	Allowed func() bool
}

// Serve streams one actor's events over SSE (see the package doc for the
// event names): stream, ev (id "<stream>:<seq>"), totals, more, skipped,
// newstream; keep-alive every KeepAlive (25s), when it also re-checks
// src.Allowed.
//
// It subscribes FIRST (so nothing published during backfill is missed),
// backfills from the store, then drains live events, skipping any seq already
// handled. Totals are computed over the whole stream even when resuming via
// Last-Event-ID or tailing.
func Serve(w http.ResponseWriter, r *http.Request, src Source) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	stream := r.URL.Query().Get("stream")
	pinned := stream != ""
	var resumeAfter uint64
	resumed := false // stream came from Last-Event-ID
	if lid := r.Header.Get("Last-Event-ID"); lid != "" {
		if s, n, ok := strings.Cut(lid, ":"); ok && sessionevents.ValidStreamID(s) {
			if seq, err := strconv.ParseUint(n, 10, 64); err == nil {
				stream, resumeAfter, resumed = s, seq, true
			}
		}
	}
	if stream != "" && !sessionevents.ValidStreamID(stream) {
		http.Error(w, "invalid stream id", http.StatusBadRequest)
		return
	}
	sub := src.Hub.Subscribe(src.ActorID, subBuffer)
	defer sub.Close()
	var newest string // the actor's newest stream, when following it
	if src.FollowNewest && !pinned {
		if streams, err := src.Store.Streams(src.ActorID); err == nil && len(streams) > 0 {
			newest = streams[0].StreamID // newest first
		}
	}
	if stream == "" {
		stream = newest
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprint(w, "retry: 3000\n\n")
	if resumed && newest != "" && newest != stream { // resuming a stream that is no longer the newest
		WriteSSE(w, "newstream", "", newest)
		fl.Flush()
		return
	}

	var tot sessionevents.Totals
	var last uint64 // highest seq handled (sent or counted)
	count := func(ev sessionevents.Event) {
		if src.Totals != nil {
			tot.Add(ev)
		}
		last = ev.Seq
	}
	emit := func(ev sessionevents.Event) {
		count(ev)
		if ev.Seq > resumeAfter {
			WriteSSE(w, "ev", fmt.Sprintf("%s:%d", ev.StreamID, ev.Seq), src.Event(View(ev)))
		}
	}
	sendTotals := func() {
		if src.Totals != nil {
			WriteSSE(w, "totals", "", src.Totals(tot))
		}
	}
	// replay pages the store from `last`, below `before` (0 = no bound),
	// through fn; false means the store failed.
	replay := func(before uint64, fn func(sessionevents.Event)) bool {
		for {
			evs, err := src.Store.List(sessionevents.Filter{ActorID: src.ActorID, StreamID: stream,
				AfterSeq: last, BeforeSeq: before, Limit: backfillPage})
			if err != nil {
				return false
			}
			for _, ev := range evs {
				fn(ev)
			}
			if len(evs) < backfillPage {
				return true
			}
		}
	}
	// tail sends the newest Tail events after resumeAfter: a `skipped` range
	// first when it had to leave a hole, then `more` when older events exist.
	tail := func() bool {
		evs, err := src.Store.List(sessionevents.Filter{ActorID: src.ActorID, StreamID: stream,
			AfterSeq: resumeAfter, Newest: true, Limit: src.Tail})
		if err != nil {
			return false
		}
		bound := resumeAfter + 1 // nothing after resumeAfter: count what precedes it
		if len(evs) > 0 {
			bound = evs[0].Seq
		}
		if src.Totals != nil && !replay(bound, count) { // the whole stream, for totals
			return false
		}
		if len(evs) == 0 {
			return true
		}
		if first := evs[0].Seq; resumeAfter > 0 && first > resumeAfter+1 {
			WriteSSE(w, "skipped", "", fmt.Sprintf("%d-%d", resumeAfter+1, first-1))
		}
		for _, ev := range evs {
			emit(ev)
		}
		if first := evs[0].Seq; resumeAfter == 0 && first > 1 {
			WriteSSE(w, "more", "", strconv.FormatUint(first, 10))
		}
		return true
	}
	backfill := func() bool {
		if src.Tail > 0 {
			return tail()
		}
		return replay(0, emit)
	}
	if stream != "" {
		WriteSSE(w, "stream", "", stream)
		if !backfill() {
			return
		}
		sendTotals()
	}
	fl.Flush()

	tick := time.NewTicker(KeepAlive)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-sub.C:
			if !ok {
				return // dropped as a slow subscriber; EventSource reconnects with Last-Event-ID
			}
			if stream == "" { // no stream yet: follow the first one that appears
				stream = ev.StreamID
				WriteSSE(w, "stream", "", stream)
				if !backfill() { // the subscription is live; ev.Seq <= last dedups below
					return
				}
				sendTotals()
			} else if ev.StreamID != stream && src.FollowNewest && !pinned && isNewest(src, ev.StreamID) {
				WriteSSE(w, "newstream", "", ev.StreamID)
				fl.Flush()
				return
			}
			if ev.StreamID != stream || ev.Seq <= last {
				continue
			}
			emit(ev)
			sendTotals()
		case <-tick.C:
			if src.Allowed != nil && !src.Allowed() {
				return // access revoked; the client's reconnect gets the route's 404
			}
			_, _ = fmt.Fprint(w, ": keep-alive\n\n")
		}
		fl.Flush()
	}
}
