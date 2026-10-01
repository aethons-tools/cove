package meui

import (
	"fmt"
	"net/http"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
)

// Changes is a source of "the intercom log changed" signals —
// *intercom.Notifier. Subscribe returns a coalescing channel and an
// unsubscribe func.
type Changes interface {
	Subscribe() (<-chan struct{}, func())
}

// Option configures Handler.
type Option func(*handler)

// WithChanges enables GET /me/events, the live-push stream: each log change is
// sent as a payload-free `changed` event, and the page refetches its own
// authorized fragments. Without it the stream answers 204 and the page polls.
func WithChanges(c Changes) Option {
	return func(h *handler) { h.changes = c }
}

// keepAlive is how often an idle stream sends an SSE comment, so proxies and
// browsers don't time the connection out.
const keepAlive = 25 * time.Second

// events streams `changed` events to the participant until they disconnect.
// No message content crosses this stream — only that something was appended.
func (h *handler) events(w http.ResponseWriter, r *http.Request) {
	if _, ok := jam.ParticipantFrom(r); !ok {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if h.changes == nil {
		w.WriteHeader(http.StatusNoContent) // EventSource stops retrying; the page polls
		return
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	ch, cancel := h.changes.Subscribe()
	defer cancel()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprint(w, "retry: 3000\n\n")
	fl.Flush()
	tick := time.NewTicker(keepAlive)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ch:
			// An event needs a data line to be dispatched by EventSource.
			_, _ = fmt.Fprint(w, "event: changed\ndata: 1\n\n")
		case <-tick.C:
			_, _ = fmt.Fprint(w, ": keep-alive\n\n")
		}
		fl.Flush()
	}
}
