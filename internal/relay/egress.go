package relay

import (
	"context"

	"github.com/aethons-tools/cove/internal/intercom"
)

// egressBatch caps the number of messages read per egressTick from
// ListSince, bounding per-tick work; a backlog larger than this drains
// across multiple ticks. A message near the head with a permanently-failing
// surface holds LastSeq (head-of-line blocking); transient failures heal on
// the next tick.
const egressBatch = 500

// egressTick delivers every not-yet-delivered surface of each squawk above
// the low-water on this Service, exactly once, and advances the bounded
// EgressMark. Every author's squawks are rendered — sessions', people's,
// ingested ones' — except back onto the surface a squawk came from, which
// the Directory leaves out.
func (e *Engine) egressTick(ctx context.Context) {
	service := e.surf.Service()
	mark := e.mk.Egress(service)
	if mark.Pending == nil {
		mark.Pending = map[string]map[string]bool{}
	}
	msgs := e.lg.ListSince(mark.LastSeq, egressBatch)

	// Pass 1: deliver undelivered surfaces.
	for _, m := range msgs {
		surfaces := e.owned(service, m)
		if len(surfaces) == 0 {
			continue
		}
		got := mark.Pending[m.ID]
		if got == nil {
			got = map[string]bool{}
			mark.Pending[m.ID] = got
		}
		for _, d := range surfaces {
			if got[d.Address] {
				continue
			}
			if _, err := e.surf.Deliver(ctx, d, m); err != nil {
				e.log.Warn("relay: egress deliver failed", "service", service, "msg", m.ID, "surface", d.Address, "error", err.Error())
				continue // leave unmarked → retried next tick
			}
			got[d.Address] = true // mark AFTER deliver
		}
	}

	// Pass 2: advance LastSeq across the contiguous fully-done prefix, GC'ing Pending.
	for _, m := range msgs {
		if !e.egressDone(service, m, mark.Pending) {
			break
		}
		mark.LastSeq = m.Seq
		delete(mark.Pending, m.ID)
	}

	if err := e.mk.SetEgress(service, mark); err != nil {
		e.log.Warn("relay: set egress mark failed", "service", service, "error", err.Error())
	}
}

// owned returns m's surfaces on THIS Service.
func (e *Engine) owned(service string, m intercom.Squawk) []Delivery {
	var out []Delivery
	for _, d := range e.dir.Surfaces(service, m) {
		if d.Service == service {
			out = append(out, d)
		}
	}
	return out
}

// egressDone reports whether every surface of m has been delivered.
func (e *Engine) egressDone(service string, m intercom.Squawk, pending map[string]map[string]bool) bool {
	got := pending[m.ID]
	for _, d := range e.owned(service, m) {
		if !got[d.Address] {
			return false
		}
	}
	return true
}
