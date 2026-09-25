package allocator

import (
	"context"
	"testing"
)

type fakeCounter struct{ n int }

func (f fakeCounter) LiveCount(project, role string) int { return f.n }

// fakeLedger stands in for *allocpg.Store: it records the budget Grant was called
// with (returning a configured result) and the events routed through Record.
type fakeLedger struct {
	grantResult bool
	grantBudget int
	grantCalls  int
	records     []Event
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
