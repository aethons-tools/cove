package intercom

import (
	"sync"

	"github.com/aethons-tools/cove/internal/ident"
)

// Notifier wraps a Store and signals subscribers after each successful Append,
// so live views (the /me SSE stream) refresh on change instead of polling. The
// signal carries no message: subscribers re-read what they are authorized to
// see. Each subscriber's channel holds one pending signal, so a burst of
// appends coalesces and a slow subscriber never blocks a writer. In-process
// only — correct while the serve process is the log's sole writer.
type Notifier struct {
	Store
	mu   sync.Mutex
	subs map[chan struct{}]struct{}
}

// NewNotifier wraps s.
func NewNotifier(s Store) *Notifier {
	return &Notifier{Store: s, subs: map[chan struct{}]struct{}{}}
}

// Append appends through the wrapped store, then signals every subscriber.
func (n *Notifier) Append(m Squawk, audience []ident.ID) (Squawk, error) {
	got, err := n.Store.Append(m, audience)
	if err != nil {
		return got, err
	}
	n.mu.Lock()
	for ch := range n.subs {
		select {
		case ch <- struct{}{}:
		default: // already pending: coalesce
		}
	}
	n.mu.Unlock()
	return got, nil
}

// Subscribe returns a channel signalled after appends, and an idempotent
// cancel that unsubscribes it.
func (n *Notifier) Subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	n.mu.Lock()
	n.subs[ch] = struct{}{}
	n.mu.Unlock()
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			n.mu.Lock()
			delete(n.subs, ch)
			n.mu.Unlock()
		})
	}
}
