package relay

import (
	"context"

	"github.com/aethons-tools/cove/internal/intercom"
)

// ingressTick polls each project on this Service and appends new, routable
// foreign events to the Log with a deterministic id, idempotently.
func (e *Engine) ingressTick(ctx context.Context) {
	service := e.surf.Service()
	for _, project := range e.dir.Projects(service) {
		evts, next, err := e.surf.Poll(ctx, project, e.cur.Ingress(service, project))
		if err != nil {
			e.log.Warn("relay: ingress poll failed", "service", service, "project", project, "error", err.Error())
			continue // do NOT advance the cursor
		}
		failed := false
		for _, ev := range evts {
			id := "in:" + service + ":" + ev.ForeignID
			if e.seen[id] {
				continue
			}
			r, ok := e.dir.Route(service, project, ev)
			if !ok {
				e.log.Warn("relay: unrouted ingress event", "service", service, "foreign", ev.ForeignID)
				continue
			}
			m := intercom.Squawk{ID: id, From: r.From, Body: ev.Body, At: ev.At, ReplyTo: r.ReplyTo, ContentType: ev.ContentType,
				Origin: r.Origin, OriginRef: r.OriginRef}
			if err := e.dir.Post(r, m); err != nil {
				e.log.Warn("relay: ingress append failed", "service", service, "foreign", ev.ForeignID, "error", err.Error())
				failed = true
				continue
			}
			e.seen[id] = true
		}
		if failed {
			// A transient append failure must not advance the cursor: leave
			// it so the next tick re-Polls (over-reports) and retries the
			// failed append. Idempotency (via `seen`) makes the replay safe.
			continue
		}
		if err := e.cur.SetIngress(service, project, next); err != nil {
			e.log.Warn("relay: set ingress cursor failed", "service", service, "project", project, "error", err.Error())
		}
	}
}
