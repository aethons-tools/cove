package push

import (
	"sync"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
)

// FocusTTL is how long a focus report holds; the page re-posts every 30 s
// while focused, so a crashed tab stops suppressing pushes within a minute.
const FocusTTL = 60 * time.Second

// Focus is which channel each user has open and focused in the app, in
// memory: a user focused on a channel gets no pushes for it.
type Focus struct {
	mu  sync.Mutex
	m   map[ident.ID]focusEntry
	now func() time.Time
}

type focusEntry struct {
	channel ident.ID
	expires time.Time
}

// NewFocus is an empty Focus on clock now (nil: time.Now).
func NewFocus(now func() time.Time) *Focus {
	if now == nil {
		now = time.Now
	}
	return &Focus{m: map[ident.ID]focusEntry{}, now: now}
}

// Set records user focused on channel until now+FocusTTL; channel "" clears.
func (f *Focus) Set(user, channel ident.ID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if channel == "" {
		delete(f.m, user)
		return
	}
	f.m[user] = focusEntry{channel: channel, expires: f.now().Add(FocusTTL)}
}

// Focused reports whether user is focused on channel now.
func (f *Focus) Focused(user, channel ident.ID) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.m[user]
	if ok && !f.now().Before(e.expires) {
		delete(f.m, user)
		return false
	}
	return ok && e.channel == channel
}
