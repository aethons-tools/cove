package sessionevents

import "sync"

// Hub fans live events out to per-actor subscribers (the admin UI's SSE now;
// automated feedback / the Studio view later). Publish never blocks: a
// subscriber whose buffer is full is dropped (its C is closed) and recovers by
// re-subscribing and backfilling from the Store.
type Hub struct {
	mu   sync.Mutex
	subs map[string]map[*Sub]struct{}
	obs  []func(Event)
}

// Sub is one live subscription; receive from C until it is closed.
type Sub struct {
	C      <-chan Event
	c      chan Event
	actor  string
	hub    *Hub
	closed bool // guarded by hub.mu
}

// NewHub returns an empty Hub.
func NewHub() *Hub { return &Hub{subs: map[string]map[*Sub]struct{}{}} }

// Subscribe registers a subscriber for actorID with a buffer of buf events.
func (h *Hub) Subscribe(actorID string, buf int) *Sub {
	if buf < 1 {
		buf = 1
	}
	c := make(chan Event, buf)
	s := &Sub{C: c, c: c, actor: actorID, hub: h}
	h.mu.Lock()
	if h.subs[actorID] == nil {
		h.subs[actorID] = map[*Sub]struct{}{}
	}
	h.subs[actorID][s] = struct{}{}
	h.mu.Unlock()
	return s
}

// Observe registers fn to see every published event, for every actor,
// synchronously and before subscribers (e.g. Presence.Observe). fn must be
// fast and must not call back into the Hub. Register observers before the
// first Publish.
func (h *Hub) Observe(fn func(Event)) {
	h.mu.Lock()
	h.obs = append(h.obs, fn)
	h.mu.Unlock()
}

// Publish hands ev to every observer, then delivers it to the actor's
// subscribers without blocking; a subscriber with a full buffer is dropped.
func (h *Hub) Publish(ev Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, fn := range h.obs {
		fn(ev)
	}
	for s := range h.subs[ev.ActorID] {
		select {
		case s.c <- ev:
		default:
			h.removeLocked(s)
		}
	}
}

// Close unsubscribes; safe to call more than once and after a drop.
func (s *Sub) Close() {
	s.hub.mu.Lock()
	defer s.hub.mu.Unlock()
	s.hub.removeLocked(s)
}

func (h *Hub) removeLocked(s *Sub) {
	if s.closed {
		return
	}
	s.closed = true
	delete(h.subs[s.actor], s)
	if len(h.subs[s.actor]) == 0 {
		delete(h.subs, s.actor)
	}
	close(s.c)
}
