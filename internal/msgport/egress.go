package msgport

import (
	"context"

	"github.com/aethons-tools/cove/internal/msglog"
)

// egressTick delivers every not-yet-delivered External target of each
// internal-authored message above the low-water, exactly once, and advances the
// bounded EgressMark.
func (e *Engine) egressTick(ctx context.Context) {
	service := e.surf.Service()
	mark := e.mk.Egress(service)
	if mark.Pending == nil {
		mark.Pending = map[string]map[string]bool{}
	}
	msgs := e.lg.List(msglog.Filter{})

	// Pass 1: deliver undelivered owned targets.
	seenLast := mark.LastMsg == ""
	for _, m := range msgs {
		if !seenLast {
			if m.ID == mark.LastMsg {
				seenLast = true
			}
			continue
		}
		if msglog.Classify(m.From) != msglog.Internal {
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
				e.log.Warn("msgport: egress deliver failed", "service", service, "msg", m.ID, "target", key, "error", err.Error())
				continue // leave unmarked → retried next tick
			}
			got[key] = true // mark AFTER deliver
		}
	}

	// Pass 2: advance LastMsg across the contiguous fully-done prefix, GC'ing Pending.
	seenLast = mark.LastMsg == ""
	for _, m := range msgs {
		if !seenLast {
			if m.ID == mark.LastMsg {
				seenLast = true
			}
			continue
		}
		if !e.egressDone(service, m, mark.Pending) {
			break
		}
		mark.LastMsg = m.ID
		delete(mark.Pending, m.ID)
	}

	if err := e.mk.SetEgress(service, mark); err != nil {
		e.log.Warn("msgport: set egress mark failed", "service", service, "error", err.Error())
	}
}

// owned returns m's External targets that resolve to THIS Service.
func (e *Engine) owned(service, project string, m msglog.Message) []msglog.Target {
	var out []msglog.Target
	for _, t := range m.To {
		if msglog.Classify(t) != msglog.External {
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
func (e *Engine) egressDone(service string, m msglog.Message, pending map[string]map[string]bool) bool {
	if msglog.Classify(m.From) != msglog.Internal {
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
