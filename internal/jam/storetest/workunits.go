package storetest

import (
	"reflect"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
)

// runWorkUnitConformance exercises the WorkUnitStore contract: a unit's
// workers, its reported state and its pull requests.
func runWorkUnitConformance(t *testing.T, newStore func(t *testing.T) jam.Store) {
	const a, b = ident.ID("ses_01j9q3aaaaaaaaaaaaaaaaaaaa"), ident.ID("ses_01j9q3bbbbbbbbbbbbbbbbbbbb")
	t0 := time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)
	t1, t2, t3 := t0.Add(time.Hour), t0.Add(2*time.Hour), t0.Add(3*time.Hour)
	setup := func(t *testing.T) (jam.Store, jam.Channel) {
		t.Helper()
		s := newStore(t)
		if err := s.CreateProject("acme"); err != nil {
			t.Fatal(err)
		}
		p, _ := s.GetProject("acme")
		c, err := s.CreateConnection(jam.Connection{Kind: "linear", Name: "linear"})
		if err != nil {
			t.Fatal(err)
		}
		ch, err := s.CreateChannel(jam.Channel{ProjectID: p.ID, Kind: jam.SourceWork, Key: string(c.ID) + "/ACME-1", Label: "ACME-1"})
		if err != nil {
			t.Fatal(err)
		}
		return s, ch
	}
	issue := jam.UnitIssue{Title: "Fix the thing", URL: "https://linear.app/acme/issue/ACME-1"}

	t.Run("work_unit_absent", func(t *testing.T) {
		s, ch := setup(t)
		if u, ok := s.WorkUnit(ch.ID); ok {
			t.Fatalf("WorkUnit = %+v, want none", u)
		}
	})
	t.Run("work_unit_workers_swap", func(t *testing.T) {
		s, ch := setup(t)
		if err := s.StartUnitWorker(ch.ID, jam.UnitWorker{Session: a, Label: "a", Started: t0}, issue); err != nil {
			t.Fatal(err)
		}
		// Starting the current worker again is a no-op.
		if err := s.StartUnitWorker(ch.ID, jam.UnitWorker{Session: a, Label: "a", Started: t1}, jam.UnitIssue{}); err != nil {
			t.Fatal(err)
		}
		if err := s.StartUnitWorker(ch.ID, jam.UnitWorker{Session: b, Label: "bee", Started: t2}, jam.UnitIssue{}); err != nil {
			t.Fatal(err)
		}
		if err := s.StopUnitWorker(ch.ID, a, t3); err != nil { // not current: a no-op
			t.Fatal(err)
		}
		want := jam.WorkUnit{Log: ch.ID, Issue: issue, Workers: []jam.UnitWorker{
			{Session: a, Label: "a", Started: t0, Stopped: true, StoppedAt: t2},
			{Session: b, Label: "bee", Started: t2},
		}}
		if got, ok := s.WorkUnit(ch.ID); !ok || !reflect.DeepEqual(got, want) {
			t.Fatalf("WorkUnit = %+v, %v\nwant %+v", got, ok, want)
		}
		if err := s.StopUnitWorker(ch.ID, b, t3); err != nil {
			t.Fatal(err)
		}
		want.Workers[1].Stopped, want.Workers[1].StoppedAt = true, t3
		if got, _ := s.WorkUnit(ch.ID); !reflect.DeepEqual(got, want) {
			t.Fatalf("after stop: WorkUnit = %+v\nwant %+v", got, want)
		}
	})
	t.Run("work_unit_report", func(t *testing.T) {
		s, ch := setup(t)
		if err := s.ReportUnit(ch.ID, jam.ReportInProgress, "", t0); err != nil {
			t.Fatal(err)
		}
		if err := s.ReportUnit(ch.ID, jam.ReportInReview, "https://github.com/acme/app/pull/42", t1); err != nil {
			t.Fatal(err)
		}
		// The same PR reported again keeps its first report.
		if err := s.ReportUnit(ch.ID, jam.ReportDone, "https://github.com/acme/app/pull/42", t2); err != nil {
			t.Fatal(err)
		}
		want := jam.WorkUnit{Log: ch.ID, State: jam.ReportDone, StateAt: t2, PRs: []jam.UnitPR{
			{URL: "https://github.com/acme/app/pull/42", Number: 42, State: jam.PROpen, ReportedAt: t1},
		}}
		if got, ok := s.WorkUnit(ch.ID); !ok || !reflect.DeepEqual(got, want) {
			t.Fatalf("WorkUnit = %+v, %v\nwant %+v", got, ok, want)
		}
	})
	t.Run("work_unit_needs_a_work_log", func(t *testing.T) {
		s, ch := setup(t)
		room, err := s.CreateChannel(jam.Channel{ProjectID: ch.ProjectID, Kind: jam.SourceRoom, Key: "eng", Label: "eng"})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.ReportUnit(room.ID, jam.ReportDone, "", t0); err == nil {
			t.Fatal("ReportUnit on a room: want an error")
		}
		if err := s.StartUnitWorker("chn_01j9q3zzzzzzzzzzzzzzzzzzzz", jam.UnitWorker{Session: a, Started: t0}, jam.UnitIssue{}); err == nil {
			t.Fatal("StartUnitWorker on no channel: want an error")
		}
		if err := s.ReportUnit(ch.ID, "finished", "", t0); err == nil {
			t.Fatal("ReportUnit with an unknown state: want an error")
		}
	})
}
