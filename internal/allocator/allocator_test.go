package allocator

import (
	"context"
	"testing"
	"time"
)

type fakeCounter struct {
	n    int
	live map[string]bool
}

func (f fakeCounter) LiveCount(project, role string) int { return f.n }

func (f fakeCounter) IsLive(actorID string) bool { return f.live[actorID] }

// fakeLedger stands in for *allocpg.Store: it records the budget Grant was called
// with (returning a configured result) and the events routed through Record, and
// serves a fixed outstanding set to the reconcile sweep.
type fakeLedger struct {
	grantResult bool
	grantBudget int
	grantCalls  int
	records     []Event
	outstanding []Reservation
	outErr      error
}

func (f *fakeLedger) Grant(_ context.Context, _, _, _ string, budget int) (bool, error) {
	f.grantCalls++
	f.grantBudget = budget
	return f.grantResult, nil
}

func (f *fakeLedger) Record(_ context.Context, ev Event) error {
	f.records = append(f.records, ev)
	return nil
}

func (f *fakeLedger) OutstandingReservations(_ context.Context, _ time.Time) ([]Reservation, error) {
	return f.outstanding, f.outErr
}

// With a ledger, Grant is the authoritative OCC admission: it delegates to
// ledger.Grant with the configured budget and returns its result.
func TestGrant_LedgerPath(t *testing.T) {
	fl := &fakeLedger{grantResult: true}
	a := New(fakeCounter{n: 99}, StaticBudget{{Project: "acme", Role: "worker"}: 3}, fl)
	got, err := a.Grant(context.Background(), "acme", "worker", "cove-1")
	if err != nil || !got {
		t.Fatalf("got %v, %v; want true, nil", got, err)
	}
	if fl.grantBudget != 3 {
		t.Fatalf("budget passed = %d, want 3", fl.grantBudget)
	}
}

// Without a ledger (file-store dev), Grant falls back to the registry live count
// vs budget — the Slice-1 global behavior.
func TestGrant_RegistryFallback_NilLedger(t *testing.T) {
	a := New(fakeCounter{n: 2}, StaticBudget{{Project: "acme", Role: "worker"}: 3}, nil)
	got, err := a.Grant(context.Background(), "acme", "worker", "cove-1")
	if err != nil || !got {
		t.Fatalf("expected grant (2<3), got %v, %v", got, err)
	}
	a2 := New(fakeCounter{n: 3}, StaticBudget{{Project: "acme", Role: "worker"}: 3}, nil)
	if got, _ := a2.Grant(context.Background(), "acme", "worker", "c"); got {
		t.Fatal("expected deny (3>=3)")
	}
}

// Grant fails closed when no budget is configured for the (project, role), on both
// the ledger and the fallback path.
func TestGrant_NoBudget_FailsClosed(t *testing.T) {
	fl := &fakeLedger{grantResult: true}
	a := New(fakeCounter{n: 0}, StaticBudget{}, fl)
	if got, _ := a.Grant(context.Background(), "acme", "worker", "c"); got {
		t.Fatal("expected deny when no budget configured (fail closed)")
	}
	if fl.grantCalls != 0 {
		t.Fatalf("ledger.Grant must not be called without a budget, calls = %d", fl.grantCalls)
	}
	a2 := New(fakeCounter{n: 0}, StaticBudget{}, nil)
	if got, _ := a2.Grant(context.Background(), "acme", "worker", "c"); got {
		t.Fatal("expected deny when no budget configured (fail closed, nil ledger)")
	}
}

// RecordRelease routes through the ledger's Record when present.
func TestRecordRelease_AppendsEvent(t *testing.T) {
	fl := &fakeLedger{}
	a := New(fakeCounter{}, StaticBudget{{Project: "acme", Role: "worker"}: 3}, fl)
	if err := a.RecordRelease(context.Background(), "acme", "worker", "cove-AET-1"); err != nil {
		t.Fatal(err)
	}
	if len(fl.records) != 1 || fl.records[0].Kind != KindReservationReleased || fl.records[0].ReservationID != "cove-AET-1" {
		t.Fatalf("unexpected: %+v", fl.records)
	}
	if got := fl.records[0]; got.Project != "acme" || got.Role != "worker" || got.Category != "acme" {
		t.Fatalf("unexpected event: %+v", got)
	}
}

// RecordRelease is a no-op with no ledger (file-store dev).
func TestRecordRelease_NilLedger_NoOp(t *testing.T) {
	a := New(fakeCounter{}, StaticBudget{}, nil)
	if err := a.RecordRelease(context.Background(), "acme", "worker", "x"); err != nil {
		t.Fatalf("nil ledger should no-op, got %v", err)
	}
}

// Sweep releases only outstanding reservations whose actor has no live instance;
// a reservation backed by a real (live) session is left alone.
func TestSweep_ReleasesOnlyDanglingReservations(t *testing.T) {
	fl := &fakeLedger{outstanding: []Reservation{
		{Project: "acme", Role: "worker", ReservationID: "live-1"},
		{Project: "acme", Role: "worker", ReservationID: "dangling-1"},
	}}
	fc := fakeCounter{live: map[string]bool{"live-1": true}} // dangling-1 not live
	a := New(fc, StaticBudget{}, fl)
	n, err := a.Sweep(context.Background(), 5*time.Minute)
	if err != nil || n != 1 {
		t.Fatalf("swept %d,%v want 1", n, err)
	}
	if len(fl.records) != 1 || fl.records[0].ReservationID != "dangling-1" || fl.records[0].Kind != KindReservationReleased {
		t.Fatalf("unexpected sweep releases: %+v", fl.records)
	}
	if got := fl.records[0]; got.Project != "acme" || got.Role != "worker" || got.Category != "acme" {
		t.Fatalf("release keyed wrong: %+v", got)
	}
}

// Sweep applies the grace window via the injected clock: its cutoff is now−grace,
// which it passes to the ledger's OutstandingReservations.
func TestSweep_PassesGraceCutoff(t *testing.T) {
	fl := &fakeLedger{}
	a := New(fakeCounter{}, StaticBudget{}, fl)
	fixed := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return fixed }
	var gotCutoff time.Time
	fl2 := &recordingLedger{fakeLedger: fl, seen: &gotCutoff}
	a.ledger = fl2
	if _, err := a.Sweep(context.Background(), 5*time.Minute); err != nil {
		t.Fatal(err)
	}
	if want := fixed.Add(-5 * time.Minute); !gotCutoff.Equal(want) {
		t.Fatalf("cutoff = %v, want %v", gotCutoff, want)
	}
}

// recordingLedger captures the cutoff passed to OutstandingReservations.
type recordingLedger struct {
	*fakeLedger
	seen *time.Time
}

func (r *recordingLedger) OutstandingReservations(ctx context.Context, olderThan time.Time) ([]Reservation, error) {
	*r.seen = olderThan
	return r.fakeLedger.OutstandingReservations(ctx, olderThan)
}

// No ledger (file-store dev) ⇒ Sweep is a no-op: 0 swept, nothing recorded.
func TestSweep_NilLedger_NoOp(t *testing.T) {
	a := New(fakeCounter{}, StaticBudget{}, nil)
	if n, err := a.Sweep(context.Background(), time.Minute); err != nil || n != 0 {
		t.Fatalf("nil ledger sweep = %d,%v", n, err)
	}
}
