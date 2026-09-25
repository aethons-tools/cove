package allocator

import (
	"context"
	"testing"
)

type fakeCounter struct{ n int }

func (f fakeCounter) LiveCount(project, role string) int { return f.n }

func TestAdmit_BelowBudget_Grants(t *testing.T) {
	a := New(fakeCounter{n: 2}, StaticBudget{{Project: "acme", Role: "worker"}: 3}, nil)
	if !a.Admit("acme", "worker") {
		t.Fatal("expected admit when live (2) < budget (3)")
	}
}

func TestAdmit_AtBudget_Denies(t *testing.T) {
	a := New(fakeCounter{n: 3}, StaticBudget{{Project: "acme", Role: "worker"}: 3}, nil)
	if a.Admit("acme", "worker") {
		t.Fatal("expected deny when live (3) >= budget (3)")
	}
}

func TestAdmit_NoBudget_FailsClosed(t *testing.T) {
	a := New(fakeCounter{n: 0}, StaticBudget{}, nil)
	if a.Admit("acme", "worker") {
		t.Fatal("expected deny when no budget configured (fail closed)")
	}
}

type fakeRecorder struct{ events []Event }

func (r *fakeRecorder) Record(_ context.Context, ev Event) error {
	r.events = append(r.events, ev)
	return nil
}

func TestRecordGrant_AppendsEvent(t *testing.T) {
	rec := &fakeRecorder{}
	a := New(fakeCounter{n: 0}, StaticBudget{{Project: "acme", Role: "worker"}: 3}, rec)
	if err := a.RecordGrant(context.Background(), "acme", "worker", "cove-AET-1"); err != nil {
		t.Fatal(err)
	}
	if len(rec.events) != 1 {
		t.Fatalf("recorded %d events, want 1", len(rec.events))
	}
	got := rec.events[0]
	if got.Kind != KindReservationGranted || got.Project != "acme" || got.Role != "worker" || got.ReservationID != "cove-AET-1" || got.Category != "acme" {
		t.Fatalf("unexpected event: %+v", got)
	}
}

func TestRecordGrant_NilRecorder_NoOp(t *testing.T) {
	a := New(fakeCounter{n: 0}, StaticBudget{{Project: "acme", Role: "worker"}: 3}, nil)
	if err := a.RecordGrant(context.Background(), "acme", "worker", "cove-AET-1"); err != nil {
		t.Fatalf("nil recorder should be a no-op, got %v", err)
	}
}

func TestRecordRelease_AppendsEvent(t *testing.T) {
	rec := &fakeRecorder{}
	a := New(fakeCounter{}, StaticBudget{{Project: "acme", Role: "worker"}: 3}, rec)
	if err := a.RecordRelease(context.Background(), "acme", "worker", "cove-AET-1"); err != nil {
		t.Fatal(err)
	}
	if len(rec.events) != 1 || rec.events[0].Kind != KindReservationReleased || rec.events[0].ReservationID != "cove-AET-1" {
		t.Fatalf("unexpected: %+v", rec.events)
	}
	if got := rec.events[0]; got.Project != "acme" || got.Role != "worker" || got.Category != "acme" {
		t.Fatalf("unexpected event: %+v", got)
	}
}

func TestRecordRelease_NilRecorder_NoOp(t *testing.T) {
	a := New(fakeCounter{}, StaticBudget{}, nil)
	if err := a.RecordRelease(context.Background(), "acme", "worker", "x"); err != nil {
		t.Fatalf("nil recorder should no-op, got %v", err)
	}
}
