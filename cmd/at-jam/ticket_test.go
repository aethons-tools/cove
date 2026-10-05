package main

import (
	"context"
	"errors"
	"testing"

	"github.com/aethons-tools/cove/internal/dispatch/scheduler"
	"github.com/aethons-tools/cove/internal/jam"
)

type fakeTicketTracker struct {
	calls    []string
	transErr error
}

func (f *fakeTicketTracker) IssueByIdentifier(_ context.Context, ident string) (string, error) {
	f.calls = append(f.calls, "lookup "+ident)
	return "id-" + ident, nil
}

func (f *fakeTicketTracker) Transition(_ context.Context, id string, role scheduler.Role) error {
	f.calls = append(f.calls, "transition "+id+" "+roleName(role))
	return f.transErr
}

func (f *fakeTicketTracker) PostComment(_ context.Context, id, body string) error {
	f.calls = append(f.calls, "comment "+id+" "+body)
	return nil
}

func roleName(r scheduler.Role) string {
	for s, want := range map[string]scheduler.Role{"in-progress": scheduler.RoleInProgress, "in-review": scheduler.RoleInReview,
		"needs-input": scheduler.RoleNeedsInput, "blocked": scheduler.RoleBlocked, "done": scheduler.RoleDone} {
		if r == want {
			return s
		}
	}
	return "?"
}

func TestTicketerReport(t *testing.T) {
	tr := &fakeTicketTracker{}
	err := linearTicketer{tr}.Report(context.Background(), jam.Instance{Unit: "AET-1"},
		jam.TicketReport{State: "in-review", Summary: "PR is up", PR: "https://github.com/o/r/pull/7"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"lookup AET-1", "transition id-AET-1 in-review", "comment id-AET-1 **in-review** — PR is up\n\nPR: https://github.com/o/r/pull/7"}
	if len(tr.calls) != 3 || tr.calls[0] != want[0] || tr.calls[1] != want[1] || tr.calls[2] != want[2] {
		t.Fatalf("calls = %q", tr.calls)
	}
}

func TestTicketerReportStates(t *testing.T) {
	for _, s := range []string{"in-progress", "in-review", "needs-input", "blocked", "done"} {
		r, ok := reportRole(s)
		if !ok || roleName(r) != s {
			t.Errorf("%s → %v %v", s, roleName(r), ok)
		}
	}
	if _, ok := reportRole("merged"); ok {
		t.Error("mapped an unknown state")
	}
}

func TestTicketerBlockUnfinished(t *testing.T) {
	for _, c := range []struct {
		name  string
		inst  jam.Instance
		calls int
	}{
		{"no report", jam.Instance{Unit: "AET-1"}, 3},
		{"in-review", jam.Instance{Unit: "AET-1", Report: &jam.TicketReport{State: "in-review"}}, 3},
		{"done", jam.Instance{Unit: "AET-1", Report: &jam.TicketReport{State: "done"}}, 0},
		{"blocked", jam.Instance{Unit: "AET-1", Report: &jam.TicketReport{State: "blocked"}}, 0},
		{"no ticket", jam.Instance{}, 0},
	} {
		tr := &fakeTicketTracker{}
		if err := (linearTicketer{tr}).BlockUnfinished(context.Background(), c.inst, "wrapped up"); err != nil {
			t.Fatal(err)
		}
		if len(tr.calls) != c.calls {
			t.Errorf("%s: calls = %q", c.name, tr.calls)
		}
		if c.calls == 3 && (tr.calls[1] != "transition id-AET-1 blocked" || tr.calls[2] != "comment id-AET-1 **blocked** — session ended without a final report: wrapped up") {
			t.Errorf("%s: calls = %q", c.name, tr.calls)
		}
	}
}

func TestTicketerErrorsPropagate(t *testing.T) {
	tr := &fakeTicketTracker{transErr: errors.New("boom")}
	if err := (linearTicketer{tr}).Report(context.Background(), jam.Instance{Unit: "AET-1"}, jam.TicketReport{State: "done", Summary: "x"}); err == nil {
		t.Fatal("no error")
	}
	if len(tr.calls) != 2 {
		t.Fatalf("commented after a failed transition: %q", tr.calls)
	}
}

func TestTicketHolder(t *testing.T) {
	var h ticketHolder
	if err := h.Report(context.Background(), jam.Instance{Unit: "AET-1"}, jam.TicketReport{}); !errors.Is(err, jam.ErrNoTracker) {
		t.Fatalf("unset holder: %v", err)
	}
	if err := h.BlockUnfinished(context.Background(), jam.Instance{Unit: "AET-1"}, "x"); err != nil {
		t.Fatalf("unset holder block: %v", err)
	}
	tr := &fakeTicketTracker{}
	h.set(linearTicketer{tr})
	_ = h.Report(context.Background(), jam.Instance{Unit: "AET-1"}, jam.TicketReport{State: "done", Summary: "x"})
	if len(tr.calls) != 3 {
		t.Fatalf("set holder calls = %q", tr.calls)
	}
}
