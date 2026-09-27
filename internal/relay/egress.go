package relay

import (
	"context"

	"github.com/aethons-tools/cove/internal/intercom"
)

// egressBatch caps the number of messages read per egressTick from
// ListSince, bounding per-tick work; a backlog larger than this drains
// across multiple ticks. This is a deliberate trade-off, not
// behavior-preserving vs. the old whole-log scan: if a message near the head
// of the window has a permanently-failing target, LastSeq never advances, so
// messages beyond LastSeq+egressBatch aren't attempted until it clears
// (head-of-line blocking). Transient failures still self-heal on the next tick.
const egressBatch = 500

// egressTick delivers every not-yet-delivered External target of each
// internal-authored message above the low-water, exactly once, and advances the
// bounded EgressMark.
func (e *Engine) egressTick(ctx context.Context) {
	service := e.surf.Service()
	mark := e.mk.Egress(service)
	if mark.Pending == nil {
		mark.Pending = map[string]map[string]bool{}
	}
	msgs := e.lg.ListSince(mark.LastSeq, egressBatch)

	// Pass 1: deliver undelivered owned targets.
	for _, m := range msgs {
		if intercom.Classify(m.From) != intercom.Internal {
			continue // echo guard: never re-egress an externally-authored message
		}
		owned := e.owned(service, m.Project, m)
		if len(owned) == 0 {
			continue
		}
		got := mark.Pending[m.ID]
		if got == nil {
			got = map[string]bool{}
			mark.Pending[m.ID] = got
		}
		for _, t := range owned {
			key := t.String()
			if got[key] {
				continue
			}
			d, _ := e.dir.Resolve(service, m.Project, t, m.From)
			if _, err := e.surf.Deliver(ctx, d, m); err != nil {
				e.log.Warn("relay: egress deliver failed", "service", service, "msg", m.ID, "target", key, "error", err.Error())
				continue // leave unmarked → retried next tick
			}
			got[key] = true // mark AFTER deliver
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

// owned returns m's External targets that resolve to THIS Service.
func (e *Engine) owned(service, project string, m intercom.Squawk) []intercom.Target {
	var out []intercom.Target
	for _, t := range m.To {
		if intercom.Classify(t) != intercom.External {
			continue
		}
		d, ok := e.dir.Resolve(service, project, t, m.From)
		if !ok || d.Service != service {
			continue
		}
		out = append(out, t)
	}
	return out
}

// egressDone reports whether every owned target of m has been delivered.
func (e *Engine) egressDone(service string, m intercom.Squawk, pending map[string]map[string]bool) bool {
	if intercom.Classify(m.From) != intercom.Internal {
		return true // echo-guarded: nothing to deliver
	}
	got := pending[m.ID]
	for _, t := range e.owned(service, m.Project, m) {
		if !got[t.String()] {
			return false
		}
	}
	return true
}
