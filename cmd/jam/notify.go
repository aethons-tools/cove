package main

import (
	"sync"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/notify"
	"github.com/aethons-tools/cove/internal/jam/push"
	"github.com/aethons-tools/cove/internal/relay"
)

// notifyEvent is the notify surface's Event: push's payload for m and user
// (pushPayload, so the send-time re-check is push's), with the squawk's id
// (even when Build cleared it for size) and the channel's live session.
// ok=false: user is no longer one of m's recipients.
func notifyEvent(r pushReader, focus *push.Focus, unread func(ident.ID) int, preview bool) func(intercom.Squawk, ident.ID) (notify.Event, bool) {
	payload := pushPayload(r, focus, unread, preview)
	return func(m intercom.Squawk, user ident.ID) (notify.Event, bool) {
		p, ok := payload(m, user)
		if !ok {
			return notify.Event{}, false
		}
		p.ID = m.ID
		e := notify.Event{Payload: p}
		if inst, ok := jam.HomeSession(r.store, m.Channel); ok {
			e.Session = inst.ActorID
		}
		return e, true
	}
}

// notifySurfaces lists one delivery per recipient of m by the push rule
// (push.Recipients over the live audience, minus people focused on its
// channel), addressed by user id. Asks notify: not paused for parts.
func (d *directory) notifySurfaces(m intercom.Squawk) []relay.Delivery {
	if !d.notifyOn {
		return nil
	}
	var focused func(user, channel ident.ID) bool
	if d.pushFocus != nil {
		focused = d.pushFocus.Focused
	}
	var out []relay.Delivery
	for _, rc := range push.Recipients(pushReader{store: d.store, ic: d.ic}, m, focused) {
		out = append(out, relay.Delivery{Service: notify.Service, Address: string(rc.User)})
	}
	return out
}

// memMarkers is relay.Markers in memory: nothing is persisted. The notify
// engine's mark is seeded at the log tail on every start (a notification is
// for an open stream now, never a backlog).
type memMarkers struct {
	mu sync.Mutex
	m  map[string]relay.EgressMark
}

var _ relay.Markers = (*memMarkers)(nil)

// newMemMarkers holds seed as service's mark.
func newMemMarkers(service string, seed relay.EgressMark) *memMarkers {
	return &memMarkers{m: map[string]relay.EgressMark{service: copyEgressMark(seed)}}
}

// Egress implements relay.Markers.
func (mm *memMarkers) Egress(service string) relay.EgressMark {
	mm.mu.Lock()
	defer mm.mu.Unlock()
	return copyEgressMark(mm.m[service])
}

// SetEgress implements relay.Markers.
func (mm *memMarkers) SetEgress(service string, mk relay.EgressMark) error {
	mm.mu.Lock()
	defer mm.mu.Unlock()
	mm.m[service] = copyEgressMark(mk)
	return nil
}

// noCursors is relay.Cursors for an egress-only service: notify has no
// ingress, so there is no cursor to keep.
type noCursors struct{}

var _ relay.Cursors = noCursors{}

// Ingress implements relay.Cursors.
func (noCursors) Ingress(string, string) string { return "" }

// SetIngress implements relay.Cursors.
func (noCursors) SetIngress(string, string, string) error { return nil }
