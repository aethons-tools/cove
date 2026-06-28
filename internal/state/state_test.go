package state

import (
	"errors"
	"strings"
	"testing"
)

func TestSaveLoadDelete(t *testing.T) {
	dir := t.TempDir()
	if Exists(dir) {
		t.Fatal("should not exist yet")
	}
	if _, err := Load(dir); !errors.Is(err, ErrNotCreated) {
		t.Fatalf("Load missing: want ErrNotCreated, got %v", err)
	}

	want := State{
		Name: "box", Backend: "colima", Container: "box",
		Image: "at-cove-for-box", WorkspaceMode: "isolated",
		Secrets:   []Secret{{Name: "GITHUB_TOKEN", Command: []string{"op", "x"}}},
		CreatedAt: "2026-06-27T00:00:00Z",
	}
	if err := Save(dir, want); err != nil {
		t.Fatal(err)
	}
	if !Exists(dir) {
		t.Fatal("should exist after save")
	}
	got, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.SchemaVersion != schemaVersion {
		t.Errorf("schemaVersion = %d, want %d", got.SchemaVersion, schemaVersion)
	}
	if got.Container != "box" || got.Image != "at-cove-for-box" || got.WorkspaceMode != "isolated" {
		t.Errorf("roundtrip mismatch: %+v", got)
	}
	if len(got.Secrets) != 1 || got.Secrets[0].Name != "GITHUB_TOKEN" || got.Secrets[0].Command[0] != "op" {
		t.Errorf("secrets not round-tripped: %+v", got.Secrets)
	}

	if err := Delete(dir); err != nil {
		t.Fatal(err)
	}
	if Exists(dir) {
		t.Fatal("should be gone after delete")
	}
	if err := Delete(dir); err != nil {
		t.Fatalf("delete should be idempotent: %v", err)
	}
}

func TestLockSharedMultipleExclusiveBlocks(t *testing.T) {
	dir := t.TempDir()
	if err := Save(dir, State{Name: "x", Backend: "colima", Container: "x"}); err != nil {
		t.Fatal(err)
	}

	s1, err := AcquireShared(dir)
	if err != nil {
		t.Fatalf("first shared: %v", err)
	}
	s2, err := AcquireShared(dir)
	if err != nil {
		t.Fatalf("second shared should also succeed: %v", err)
	}
	if _, err := AcquireExclusive(dir); !errors.Is(err, ErrLocked) {
		t.Fatalf("exclusive must be blocked while shared held, got %v", err)
	}
	s1.Release()
	s2.Release()

	ex, err := AcquireExclusive(dir)
	if err != nil {
		t.Fatalf("exclusive after release: %v", err)
	}
	if _, err := AcquireShared(dir); !errors.Is(err, ErrLocked) {
		t.Fatalf("shared must be blocked while exclusive held, got %v", err)
	}
	ex.Release()
}

func TestAcquireMissingState(t *testing.T) {
	if _, err := AcquireShared(t.TempDir()); !errors.Is(err, ErrNotCreated) {
		t.Fatalf("want ErrNotCreated, got %v", err)
	}
}

func TestInstanceFilenames(t *testing.T) {
	dir := t.TempDir()
	if got := PathFor(dir, Interactive); got != Path(dir) {
		t.Fatalf("Interactive PathFor = %q, want Path() %q", got, Path(dir))
	}
	if got := PathFor(dir, Interactive); !strings.HasSuffix(got, "/.state/state.json") {
		t.Fatalf("interactive path = %q, want .../.state/state.json", got)
	}
	if got := PathFor(dir, LoopInstance("foo")); !strings.HasSuffix(got, "/.state/loop-foo.json") {
		t.Fatalf("loop path = %q, want .../.state/loop-foo.json", got)
	}
}

func TestNamedInstancesAreIsolated(t *testing.T) {
	dir := t.TempDir()
	foo := LoopInstance("foo")
	if err := SaveFor(dir, Interactive, State{Name: "box", Backend: "colima", Container: "box"}); err != nil {
		t.Fatal(err)
	}
	if err := SaveFor(dir, foo, State{Name: "box", Backend: "colima", Container: "box-loop-foo"}); err != nil {
		t.Fatal(err)
	}
	if !ExistsFor(dir, Interactive) || !ExistsFor(dir, foo) {
		t.Fatal("both instances should exist")
	}
	got, err := LoadFor(dir, foo)
	if err != nil {
		t.Fatal(err)
	}
	if got.Container != "box-loop-foo" {
		t.Fatalf("loop load returned wrong state: %+v", got)
	}
	// Deleting the loop must not touch the interactive instance.
	if err := DeleteFor(dir, foo); err != nil {
		t.Fatal(err)
	}
	if ExistsFor(dir, foo) {
		t.Fatal("loop instance should be gone")
	}
	if !ExistsFor(dir, Interactive) {
		t.Fatal("interactive instance must survive loop delete")
	}
}

func TestLoadForMissingInstance(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadFor(dir, LoopInstance("nope")); !errors.Is(err, ErrNotCreated) {
		t.Fatalf("missing loop load: want ErrNotCreated, got %v", err)
	}
}

func TestValidLoopName(t *testing.T) {
	good := []string{"foo", "ci-fixer", "queue_1", "A1", "state"}
	for _, n := range good {
		if err := ValidLoopName(n); err != nil {
			t.Errorf("ValidLoopName(%q) = %v, want nil", n, err)
		}
	}
	bad := []string{"", "foo/bar", "../etc", "foo/../../x", "-leading", "has space", "a/b", "x..y/../z"}
	for _, n := range bad {
		if err := ValidLoopName(n); err == nil {
			t.Errorf("ValidLoopName(%q) = nil, want error", n)
		}
	}
}

func TestHasLoopInstances(t *testing.T) {
	dir := t.TempDir()
	if HasLoopInstances(dir) {
		t.Fatal("no .state dir yet: should be false")
	}
	if err := SaveFor(dir, Interactive, State{Name: "x", Backend: "colima", Container: "x"}); err != nil {
		t.Fatal(err)
	}
	if HasLoopInstances(dir) {
		t.Fatal("only the interactive instance exists: should be false")
	}
	if err := SaveFor(dir, LoopInstance("foo"), State{Name: "x", Backend: "colima", Container: "x-loop-foo"}); err != nil {
		t.Fatal(err)
	}
	if !HasLoopInstances(dir) {
		t.Fatal("a loop instance exists: should be true")
	}
}

func TestOtherLoopInstancesExist(t *testing.T) {
	dir := t.TempDir()
	if OtherLoopInstancesExist(dir, LoopInstance("foo")) {
		t.Fatal("no dir yet: false")
	}
	if err := SaveFor(dir, LoopInstance("foo"), State{Name: "x", Backend: "colima", Container: "x-loop-foo"}); err != nil {
		t.Fatal(err)
	}
	if OtherLoopInstancesExist(dir, LoopInstance("foo")) {
		t.Fatal("only foo exists, excluding foo: false")
	}
	if err := SaveFor(dir, LoopInstance("bar"), State{Name: "x", Backend: "colima", Container: "x-loop-bar"}); err != nil {
		t.Fatal(err)
	}
	if !OtherLoopInstancesExist(dir, LoopInstance("foo")) {
		t.Fatal("bar exists besides foo: true")
	}
}
