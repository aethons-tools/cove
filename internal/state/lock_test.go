package state

import (
	"errors"
	"testing"
)

// A lock on one instance must not affect a different instance, since each has
// its own state file. A loop holding a shared lock must still block an
// exclusive lock on the SAME instance.
func TestLocksArePerInstance(t *testing.T) {
	dir := t.TempDir()
	foo := LoopInstance("foo")
	if err := SaveFor(dir, Interactive, State{Name: "x", Backend: "colima", Container: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := SaveFor(dir, foo, State{Name: "x", Backend: "colima", Container: "x-loop-foo"}); err != nil {
		t.Fatal(err)
	}

	shFoo, err := AcquireSharedFor(dir, foo)
	if err != nil {
		t.Fatalf("shared on foo: %v", err)
	}
	defer shFoo.Release()

	// Different instance is unaffected: exclusive on interactive succeeds.
	exMain, err := AcquireExclusiveFor(dir, Interactive)
	if err != nil {
		t.Fatalf("exclusive on interactive must be independent of foo's lock, got %v", err)
	}
	exMain.Release()

	// Same instance: exclusive on foo is blocked while foo's shared lock is held.
	if _, err := AcquireExclusiveFor(dir, foo); !errors.Is(err, ErrLocked) {
		t.Fatalf("exclusive on foo must be blocked by foo's shared lock, got %v", err)
	}
}

func TestAcquireForMissingInstance(t *testing.T) {
	dir := t.TempDir()
	if _, err := AcquireSharedFor(dir, LoopInstance("nope")); !errors.Is(err, ErrNotCreated) {
		t.Fatalf("acquire on missing instance: want ErrNotCreated, got %v", err)
	}
}
