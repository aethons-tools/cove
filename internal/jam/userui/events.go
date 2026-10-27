package userui

import (
	"fmt"
	"net/http"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
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

// WithChanges enables GET /app/user/events, the live-push stream: each log change is
// sent as a payload-free `changed` event, and the app refetches what it
// shows through the API. Without it the stream answers 204 and the app polls.
func WithChanges(c Changes) Option {
	return func(h *handler) { h.changes = c }
}

// keepAlive is how often an idle stream sends an SSE comment, so proxies and
// browsers don't time the connection out.
const keepAlive = 25 * time.Second

// presenceGap is the least time between two `presence` events on one stream:
// a busy session can change status several times a second, and each event
// makes the app refetch its strip. A change inside the gap is sent when the
// gap ends, so the last status always arrives.
const presenceGap = 250 * time.Millisecond

// boardAccessTTL is how long a stream trusts its check that the viewer may
// read a board before checking again.
const boardAccessTTL = 30 * time.Second

// events streams `changed` (the log grew) and `presence` (a session status
// changed) to the participant until they disconnect. No message content or
// status crosses this stream — only that something changed; the app refetches
// through the API, which checks access again.
func (h *handler) events(w http.ResponseWriter, r *http.Request) {
	p, ok := jam.ParticipantFrom(r)
	if !ok {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if h.changes == nil && h.presence == nil && h.d.Boards == nil {
		w.WriteHeader(http.StatusNoContent) // EventSource stops retrying; the page polls
		return
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	var changed, presence <-chan struct{} // nil channels never fire
	if h.changes != nil {
		ch, cancel := h.changes.Subscribe()
		defer cancel()
		changed = ch
	}
	if h.presence != nil {
		ch, cancel := h.presence.Subscribe()
		defer cancel()
		presence = ch
	}
	var boards <-chan ident.ID
	if h.d.Boards != nil {
		ch, cancel := h.d.Boards.Subscribe()
		defer cancel()
		boards = ch
	}
	type access struct {
		ok bool
		at time.Time
	}
	readable := map[ident.ID]access{}
	mayRead := func(id ident.ID) bool {
		if a, hit := readable[id]; hit && time.Since(a.at) < boardAccessTTL {
			return a.ok
		}
		_, _, ok := h.d.Boards.CanRead(p.UserID, id)
		readable[id] = access{ok, time.Now()}
		return ok
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprint(w, "retry: 3000\n\n")
	fl.Flush()
	tick := time.NewTicker(keepAlive)
	defer tick.Stop()
	var lastPresence time.Time
	var held <-chan time.Time // fires when a held-back presence may be sent
	sendPresence := func() {
		_, _ = fmt.Fprint(w, "event: presence\ndata: 1\n\n")
		lastPresence, held = time.Now(), nil
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case <-changed:
			// An event needs a data line to be dispatched by EventSource.
			_, _ = fmt.Fprint(w, "event: changed\ndata: 1\n\n")
		case <-presence:
			if wait := presenceGap - time.Since(lastPresence); wait > 0 {
				if held == nil {
					held = time.After(wait)
				}
				continue
			}
			sendPresence()
		case <-held:
			sendPresence()
		case id := <-boards:
			// Only boards this viewer may read (checked at most every
			// boardAccessTTL per board, not on every stroke); the editor
			// refetches by seq.
			if !mayRead(id) {
				continue
			}
			_, _ = fmt.Fprintf(w, "event: board\ndata: %s\n\n", id)
		case <-tick.C:
			_, _ = fmt.Fprint(w, ": keep-alive\n\n")
		}
		fl.Flush()
	}
}
