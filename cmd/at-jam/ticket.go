package main

import (
	"context"
	"sync"

	"github.com/aethons-tools/cove/internal/dispatch/scheduler"
	"github.com/aethons-tools/cove/internal/jam"
)

// ticketTracker is the slice of *linear.Client a ticket session's reports
// need.
type ticketTracker interface {
	IssueByIdentifier(ctx context.Context, identifier string) (string, error)
	Transition(ctx context.Context, issueID string, role scheduler.Role) error
	PostComment(ctx context.Context, issueID, body string) error
}

// linearTicketer moves a ticket session's ticket for POST /report
// (jam.TicketReporter) and marks it blocked when wake-on ends the session
// without a final report (wakeon.TicketCloser).
type linearTicketer struct{ t ticketTracker }

// reportRole maps a report state to the tracker state it moves the ticket to.
func reportRole(state string) (scheduler.Role, bool) {
	switch state {
	case jam.ReportInProgress:
		return scheduler.RoleInProgress, true
	case jam.ReportInReview:
		return scheduler.RoleInReview, true
	case jam.ReportNeedsInput:
		return scheduler.RoleNeedsInput, true
	case jam.ReportBlocked:
		return scheduler.RoleBlocked, true
	case jam.ReportDone:
		return scheduler.RoleDone, true
	}
	return 0, false
}

func (l linearTicketer) Report(ctx context.Context, inst jam.Instance, r jam.TicketReport) error {
	role, _ := reportRole(r.State) // validated by the handler
	body := "**" + r.State + "** — " + r.Summary
	if r.PR != "" {
		body += "\n\nPR: " + r.PR
	}
	return l.move(ctx, inst.Unit, role, body)
}

func (l linearTicketer) BlockUnfinished(ctx context.Context, inst jam.Instance, reason string) error {
	if inst.Unit == "" || (inst.Report != nil && inst.Report.Terminal()) {
		return nil
	}
	return l.move(ctx, inst.Unit, scheduler.RoleBlocked, "**blocked** — session ended without a final report: "+reason)
}

func (l linearTicketer) move(ctx context.Context, unit string, role scheduler.Role, body string) error {
	id, err := l.t.IssueByIdentifier(ctx, unit)
	if err != nil {
		return err
	}
	if err := l.t.Transition(ctx, id, role); err != nil {
		return err
	}
	return l.t.PostComment(ctx, id, body)
}

// ticketHolder is the ticketer the cove endpoints and wake-on are built with
// before the Requisitioner's tracker exists (set once it does). Unset, a
// report answers jam.ErrNoTracker and a block is a no-op.
type ticketHolder struct {
	mu sync.RWMutex
	t  *linearTicketer
}

func (h *ticketHolder) set(t linearTicketer) {
	h.mu.Lock()
	h.t = &t
	h.mu.Unlock()
}

func (h *ticketHolder) get() *linearTicketer {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.t
}

func (h *ticketHolder) Report(ctx context.Context, inst jam.Instance, r jam.TicketReport) error {
	if t := h.get(); t != nil {
		return t.Report(ctx, inst, r)
	}
	return jam.ErrNoTracker
}

func (h *ticketHolder) BlockUnfinished(ctx context.Context, inst jam.Instance, reason string) error {
	if t := h.get(); t != nil {
		return t.BlockUnfinished(ctx, inst, reason)
	}
	return nil
}
