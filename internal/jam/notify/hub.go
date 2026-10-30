package notify

import (
	"log/slog"
	"sync"

	"github.com/aethons-tools/cove/internal/ident"
)

type sub struct{ ch chan Event }

// Hub is the in-process per-user fan-out of Events.
type Hub struct {
	mu   sync.Mutex
	subs map[ident.ID]map[*sub]struct{}
	log  *slog.Logger
}

// NewHub returns an empty Hub. A nil log discards.
func NewHub(log *slog.Logger) *Hub {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Hub{subs: map[ident.ID]map[*sub]struct{}{}, log: log}
}

// Subscribe opens a subscription of user: a channel buffered to SubBuffer and
// a cancel that removes it. cancel is idempotent and never closes the
// channel, so a reader selecting on it is never woken by a close.
func (h *Hub) Subscribe(user ident.ID) (<-chan Event, func()) {
	s := &sub{ch: make(chan Event, SubBuffer)}
	h.mu.Lock()
	if h.subs[user] == nil {
		h.subs[user] = map[*sub]struct{}{}
	}
	h.subs[user][s] = struct{}{}
	h.mu.Unlock()
	var once sync.Once
	return s.ch, func() {
		once.Do(func() {
			h.mu.Lock()
			defer h.mu.Unlock()
			delete(h.subs[user], s)
			if len(h.subs[user]) == 0 {
				delete(h.subs, user)
			}
		})
	}
}

// Publish hands e to each of user's subscribers. It never blocks: a
// subscriber whose buffer is full misses e (logged at Debug, without the body).
func (h *Hub) Publish(user ident.ID, e Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.subs[user] {
		select {
		case s.ch <- e:
		default:
			h.log.Debug("notify: subscriber full; dropping event", "user", string(user), "id", e.ID)
		}
	}
}

// Subscribers is the number of open subscriptions of user.
func (h *Hub) Subscribers(user ident.ID) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs[user])
}
