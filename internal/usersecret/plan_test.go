package usersecret

import (
	"testing"

	"github.com/aethons-tools/cove/internal/secret"
)

func TestPlanKitCommandWins(t *testing.T) {
	store := Store{"T": {Value: "fromfile"}}
	got, unresolved := store.Plan([]secret.Spec{{Name: "T", Command: []string{"kit"}}})
	if len(unresolved) != 0 || len(got) != 1 {
		t.Fatalf("got=%+v unresolved=%v", got, unresolved)
	}
	if got[0].Literal || len(got[0].Command) != 1 || got[0].Command[0] != "kit" {
		t.Fatalf("kit command must win: %+v", got[0])
	}
}

func TestPlanStoreValueBecomesLiteral(t *testing.T) {
	store := Store{"T": {Value: "v"}}
	got, unresolved := store.Plan([]secret.Spec{{Name: "T"}})
	if len(unresolved) != 0 || len(got) != 1 {
		t.Fatalf("got=%+v unresolved=%v", got, unresolved)
	}
	if !got[0].Literal || got[0].Value != "v" || len(got[0].Command) != 0 {
		t.Fatalf("store value must become a literal spec: %+v", got[0])
	}
}

func TestPlanStoreCommand(t *testing.T) {
	store := Store{"T": {Command: []string{"op", "read"}}}
	got, _ := store.Plan([]secret.Spec{{Name: "T"}})
	if got[0].Literal || len(got[0].Command) != 2 {
		t.Fatalf("store command must become a command spec: %+v", got[0])
	}
}

func TestPlanUnresolved(t *testing.T) {
	got, unresolved := Store{}.Plan([]secret.Spec{{Name: "MISSING"}})
	if len(got) != 0 || len(unresolved) != 1 || unresolved[0] != "MISSING" {
		t.Fatalf("got=%+v unresolved=%v", got, unresolved)
	}
}

func TestPlanIgnoresUndemandedEntries(t *testing.T) {
	store := Store{"DEMANDED": {Value: "a"}, "EXTRA": {Value: "b"}}
	got, _ := store.Plan([]secret.Spec{{Name: "DEMANDED"}})
	if len(got) != 1 || got[0].Name != "DEMANDED" {
		t.Fatalf("undemanded entries must be ignored: %+v", got)
	}
}

func TestPlanPreservesDemandOrder(t *testing.T) {
	store := Store{"A": {Value: "1"}, "B": {Value: "2"}}
	got, _ := store.Plan([]secret.Spec{{Name: "B"}, {Name: "A"}})
	if len(got) != 2 || got[0].Name != "B" || got[1].Name != "A" {
		t.Fatalf("order not preserved: %+v", got)
	}
}
