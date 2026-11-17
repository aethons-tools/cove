package jam

import (
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
)

// A re-dispatched unit's record lists both its workers, from when each took
// it over to when it stopped, and its issue.
func TestIntercomRecordsUnitWorkers(t *testing.T) {
	f := newICFixture(t)
	t1, t2, t3 := time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC), time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC), time.Date(2026, 11, 17, 11, 0, 0, 0, time.UTC)
	clock := t1
	f.ic.now = func() time.Time { return clock }
	f.ticket.RaisedAt = time.Unix(1000, 0)
	f.ticket.UnitTitle, f.ticket.UnitURL = "Fix the thing", "https://linear.app/acme/issue/ACME-7"
	if err := f.ic.SetUp(f.ticket); err != nil {
		t.Fatal(err)
	}
	next := f.ticket
	next.ActorID, next.Name, next.RaisedAt = string(ident.New(ident.Session)), "bee", time.Unix(2000, 0)
	if err := f.store.PutInstance(next); err != nil {
		t.Fatal(err)
	}
	clock = t2
	if err := f.ic.SetUp(next); err != nil {
		t.Fatal(err)
	}
	// The old session set up again keeps no turn; the new one's again is a no-op.
	clock = t3
	if err := f.ic.SetUp(f.ticket); err != nil {
		t.Fatal(err)
	}
	if err := f.ic.SetUp(next); err != nil {
		t.Fatal(err)
	}
	log := f.workLog()
	want := WorkUnit{Log: log.ID, Issue: UnitIssue{Title: "Fix the thing", URL: "https://linear.app/acme/issue/ACME-7"}, Workers: []UnitWorker{
		{Session: ident.ID(f.ticket.ActorID), Label: f.ticket.ActorID, Started: t1, Stopped: true, StoppedAt: t2},
		{Session: ident.ID(next.ActorID), Label: "bee", Started: t2},
	}}
	if got, ok := f.store.WorkUnit(log.ID); !ok || !reflect.DeepEqual(got, want) {
		t.Fatalf("WorkUnit = %+v, %v\nwant %+v", got, ok, want)
	}
	// The worker ends: its turn stops.
	if err := f.ic.Ended(next); err != nil {
		t.Fatal(err)
	}
	want.Workers[1].Stopped, want.Workers[1].StoppedAt = true, t3
	if got, _ := f.store.WorkUnit(log.ID); !reflect.DeepEqual(got, want) {
		t.Fatalf("after Ended: WorkUnit = %+v\nwant %+v", got, want)
	}
}

// A report records the unit's state and its PR.
func TestIntercomRecordsUnitReport(t *testing.T) {
	f := newICFixture(t)
	if err := f.ic.SetUp(f.ticket); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)
	r := TicketReport{State: ReportInReview, Summary: "up", PR: "https://github.com/acme/app/pull/12", At: at}
	if err := f.ic.Reported(f.ticket, r); err != nil {
		t.Fatal(err)
	}
	u, ok := f.store.WorkUnit(f.workLog().ID)
	want := []UnitPR{{URL: r.PR, Number: 12, State: PROpen, ReportedAt: at}}
	if !ok || u.State != ReportInReview || !u.StateAt.Equal(at) || !reflect.DeepEqual(u.PRs, want) {
		t.Fatalf("WorkUnit = %+v, %v", u, ok)
	}
	// A session with no unit (or no log) records nothing.
	if err := f.ic.Reported(f.personal, r); err != nil {
		t.Fatal(err)
	}
}

func TestPRNumber(t *testing.T) {
	for url, want := range map[string]int{
		"https://github.com/acme/app/pull/42":              42,
		"https://github.com/acme/app/pull/42/files":        42,
		"https://gitlab.com/acme/app/-/merge_requests/7":   7,
		"https://example.com/acme/app/pull/x":              0,
		"https://github.com/acme/app/issues/3":             0,
		"https://github.com/acme/app/pull/42#discussion_r": 42,
	} {
		if got := PRNumber(url); got != want {
			t.Errorf("PRNumber(%q) = %d, want %d", url, got, want)
		}
	}
}

// SetReport hands the accepted report to the session's channels, which
// record it on the unit.
func TestSetReportRecordsOnTheUnit(t *testing.T) {
	sup, _, _ := raiseWithTurnEnd(t, TurnEndPolicy{})
	fc := &fakeSessionChannels{}
	sup.SetSessionChannels(fc)
	r := TicketReport{State: ReportInReview, Summary: "up", PR: "https://github.com/acme/app/pull/1", At: time.Unix(5, 0)}
	if err := sup.SetReport("w1", r); err != nil {
		t.Fatal(err)
	}
	if want := []string{"reported w1 in-review https://github.com/acme/app/pull/1"}; !slices.Equal(fc.calls, want) {
		t.Fatalf("calls = %q, want %q", fc.calls, want)
	}
}
