package msgport

import (
	"context"

	"github.com/aethons-tools/cove/internal/msglog"
)

// ingressTick polls each project on this Service and appends new, routable
// foreign events to the Log with a deterministic id, idempotently.
func (e *Engine) ingressTick(ctx context.Context) {
	service := e.surf.Service()
	for _, project := range e.dir.Projects(service) {
		evts, next, err := e.surf.Poll(ctx, project, e.cur.Ingress(service, project))
		if err != nil {
			e.log.Warn("msgport: ingress poll failed", "service", service, "project", project, "error", err.Error())
			continue // do NOT advance the cursor
		}
		for _, ev := range evts {
			id := "in:" + service + ":" + ev.ForeignID
			if e.seen[id] {
				continue
			}
			from, to, replyTo, ok := e.dir.Route(service, project, ev)
			if !ok {
				e.log.Warn("msgport: unrouted ingress event", "service", service, "foreign", ev.ForeignID)
				continue
			}
			if _, err := e.lg.Append(msglog.Message{ID: id, From: from, To: to, Body: ev.Body, At: ev.At, Project: project, ReplyTo: replyTo}); err != nil {
				e.log.Warn("msgport: ingress append failed", "service", service, "foreign", ev.ForeignID, "error", err.Error())
				continue
			}
			e.seen[id] = true
		}
		if err := e.cur.SetIngress(service, project, next); err != nil {
			e.log.Warn("msgport: set ingress cursor failed", "service", service, "project", project, "error", err.Error())
		}
	}
}
