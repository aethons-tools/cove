package allocator

import "testing"

type fakeCounter struct{ n int }

func (f fakeCounter) LiveCount(project, role string) int { return f.n }

func TestAdmit_BelowBudget_Grants(t *testing.T) {
	a := New(fakeCounter{n: 2}, StaticBudget{{Project: "acme", Role: "worker"}: 3})
	if !a.Admit("acme", "worker") {
		t.Fatal("expected admit when live (2) < budget (3)")
	}
}

func TestAdmit_AtBudget_Denies(t *testing.T) {
	a := New(fakeCounter{n: 3}, StaticBudget{{Project: "acme", Role: "worker"}: 3})
	if a.Admit("acme", "worker") {
		t.Fatal("expected deny when live (3) >= budget (3)")
	}
}

func TestAdmit_NoBudget_FailsClosed(t *testing.T) {
	a := New(fakeCounter{n: 0}, StaticBudget{})
	if a.Admit("acme", "worker") {
		t.Fatal("expected deny when no budget configured (fail closed)")
	}
}
