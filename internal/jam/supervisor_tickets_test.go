package jam

import (
	"context"
	"testing"
)

// fakeTicketBlocker records the tickets a teardown marked blocked.
type fakeTicketBlocker struct{ calls []string }

func (f *fakeTicketBlocker) BlockUnfinished(_ context.Context, inst Instance, reason string) error {
	f.calls = append(f.calls, inst.Unit+":"+reason)
	return nil
}

// Every teardown of a ticket session without a terminal report marks its
// ticket blocked — an operator's or a lost studio's too, not only wake-on's —
// and records that as its report.
func TestTeardownBlocksUnfinishedTicket(t *testing.T) {
	sup, store, _ := supTestKit(t, &fakeLauncher{liveness: LivenessAlive})
	tb := &fakeTicketBlocker{}
	sup.SetTicketBlocker(tb)
	for _, inst := range []Instance{
		{ActorID: "open", Project: "acme", Role: "worker", Unit: "AET-1", Phase: PhaseLive},
		{ActorID: "done", Project: "acme", Role: "worker", Unit: "AET-2", Phase: PhaseLive, Report: &TicketReport{State: ReportDone}},
		{ActorID: "none", Project: "acme", Role: "worker", Phase: PhaseLive},
	} {
		if err := store.PutInstance(inst); err != nil {
			t.Fatal(err)
		}
		if err := sup.Teardown(context.Background(), inst.ActorID); err != nil {
			t.Fatal(err)
		}
	}
	if len(tb.calls) != 1 || tb.calls[0] != "AET-1:session torn down" {
		t.Fatalf("blocked = %v, want only the unfinished ticket", tb.calls)
	}
}
