package collabhttp

import (
	"crypto/sha256"
	"sync"
	"time"

	"github.com/aethons-tools/cove/internal/jam/collab"
)

// FrameTicketTTL bounds a frame ticket; FrameSessionTTL a frame session.
const (
	FrameTicketTTL  = 60 * time.Second
	FrameSessionTTL = 8 * time.Hour
)

// frameTicket is what a redeemed ticket grants.
type frameTicket struct {
	principal collab.Principal
	name      string // display name for audit / X-Jam-User
	host      string // canonical preview host it is for
	right     collab.Right
	expires   time.Time
}

// FrameTickets mints and redeems one-time frame tickets
// (docs/usage/jam/previews.md#framing). Only the sha256 of a ticket is kept.
type FrameTickets struct {
	mu  sync.Mutex
	m   map[[sha256.Size]byte]frameTicket
	now func() time.Time
}

// NewFrameTickets returns an empty store using now as its clock.
func NewFrameTickets(now func() time.Time) *FrameTickets {
	return &FrameTickets{m: map[[sha256.Size]byte]frameTicket{}, now: now}
}

// Mint returns a new raw ticket (base64url of 32 random bytes) for host.
func (f *FrameTickets) Mint(p collab.Principal, name, host string, right collab.Right) (string, error) {
	raw, key, err := newSecret()
	if err != nil {
		return "", err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.m[key] = frameTicket{principal: p, name: name, host: host, right: right, expires: f.now().Add(FrameTicketTTL)}
	return raw, nil
}

// Redeem consumes raw if it exists, is unexpired and is for host. Any lookup
// that finds the ticket deletes it, even for the wrong host: a probed ticket
// is spent.
func (f *FrameTickets) Redeem(raw, host string) (frameTicket, bool) {
	key := sha256.Sum256([]byte(raw))
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.m[key]
	if !ok {
		return frameTicket{}, false
	}
	delete(f.m, key)
	if !f.now().Before(t.expires) || t.host != host {
		return frameTicket{}, false
	}
	return t, true
}

// Prune drops expired tickets.
func (f *FrameTickets) Prune() {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := f.now()
	for k, t := range f.m {
		if !now.Before(t.expires) {
			delete(f.m, k)
		}
	}
}
