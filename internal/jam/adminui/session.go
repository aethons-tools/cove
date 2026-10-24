package adminui

import (
	"bytes"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aethons-tools/cove/internal/jam/sessionevents"
	"github.com/aethons-tools/cove/internal/jam/sessionview"
)

const (
	sessionBackfillPage = 500
	sessionSubBuffer    = 256
)

func registerSession(mux *http.ServeMux, store sessionevents.Store, hub *sessionevents.Hub) {
	mux.HandleFunc("GET /app/admin/agents/{id}/session", func(w http.ResponseWriter, r *http.Request) {
		data := map[string]any{"Title": r.PathValue("id") + " session", "ActorID": r.PathValue("id"), "Enabled": store != nil}
		if store != nil {
			streams, _ := store.Streams(r.PathValue("id"))
			selected := r.URL.Query().Get("stream")
			if !sessionevents.ValidStreamID(selected) && len(streams) > 0 {
				selected = streams[0].StreamID
			}
			data["Streams"], data["Stream"] = streams, selected
		}
		render(w, r, "session", data)
	})
	events := func(w http.ResponseWriter, r *http.Request) {
		if store == nil || hub == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		serveSessionEvents(w, r, store, hub)
	}
	mux.HandleFunc("GET /app/admin/agents/{id}/session/events", events)
	// The pre-agents path, for timelines already open in a browser.
	mux.HandleFunc("GET /app/admin/coves/{id}/session/events", events)
}

// sseLines normalizes CRLF and CR to LF: SSE parsers treat all three as line
// terminators, so any of them inside a field value would end the line early.
var sseLines = strings.NewReplacer("\r\n", "\n", "\r", "\n")

// sseWrite emits one SSE message; multi-line data gets one data: line each.
// Event and id are collapsed to a single line; data is split on every SSE
// line terminator so agent text can never inject fields.
func sseWrite(w http.ResponseWriter, event, id, data string) {
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
	_, _ = w.Write([]byte(b.String()))
}

// sessionFragments is a clone of the session page's set for rendering event
// fragments (the master set is only ever cloned, never executed).
var sessionFragments = sync.OnceValue(func() *template.Template {
	return template.Must(pages["session"].t.Clone())
})

func fragment(name string, data any) string {
	var b bytes.Buffer
	if err := sessionFragments().ExecuteTemplate(&b, name, data); err != nil {
		return ""
	}
	// html/template leaves CR raw; keep it visible to the operator as an entity
	// (SSE framing is separately CR-safe in sseWrite).
	return strings.ReplaceAll(b.String(), "\r", "&#13;")
}

// serveSessionEvents streams one cove's events: subscribe FIRST (so nothing
// published during backfill is missed), backfill from the store, then drain
// live events, skipping any seq already sent. Totals are computed over the
// whole stream even when resuming via Last-Event-ID.
func serveSessionEvents(w http.ResponseWriter, r *http.Request, store sessionevents.Store, hub *sessionevents.Hub) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	actor := r.PathValue("id")
	stream := r.URL.Query().Get("stream")
	var resumeAfter uint64
	if lid := r.Header.Get("Last-Event-ID"); lid != "" {
		if s, n, ok := strings.Cut(lid, ":"); ok && sessionevents.ValidStreamID(s) {
			if seq, err := strconv.ParseUint(n, 10, 64); err == nil {
				stream, resumeAfter = s, seq
			}
		}
	}
	if stream != "" && !sessionevents.ValidStreamID(stream) {
		http.Error(w, "invalid stream id", http.StatusBadRequest)
		return
	}
	sub := hub.Subscribe(actor, sessionSubBuffer)
	defer sub.Close()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprint(w, "retry: 3000\n\n")

	var tot sessionevents.Totals
	var last uint64 // highest seq handled (sent or counted)
	emit := func(ev sessionevents.Event) {
		tot.Add(ev)
		last = ev.Seq
		if ev.Seq > resumeAfter {
			sseWrite(w, "ev", fmt.Sprintf("%s:%d", ev.StreamID, ev.Seq), fragment("session-event", sessionview.View(ev)))
		}
	}
	// backfill replays the store from `last`; false means the store failed.
	backfill := func() bool {
		for {
			evs, err := store.List(sessionevents.Filter{ActorID: actor, StreamID: stream, AfterSeq: last, Limit: sessionBackfillPage})
			if err != nil {
				return false
			}
			for _, ev := range evs {
				emit(ev)
			}
			if len(evs) < sessionBackfillPage {
				return true
			}
		}
	}
	if stream != "" {
		sseWrite(w, "stream", "", stream)
		if !backfill() {
			return
		}
		sseWrite(w, "totals", "", fragment("session-totals", tot))
	}
	fl.Flush()

	tick := time.NewTicker(25 * time.Second)
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
				sseWrite(w, "stream", "", stream)
				if !backfill() { // the subscription is live; ev.Seq <= last dedups below
					return
				}
				sseWrite(w, "totals", "", fragment("session-totals", tot))
			}
			if ev.StreamID != stream || ev.Seq <= last {
				continue
			}
			emit(ev)
			sseWrite(w, "totals", "", fragment("session-totals", tot))
		case <-tick.C:
			_, _ = fmt.Fprint(w, ": keep-alive\n\n")
		}
		fl.Flush()
	}
}
