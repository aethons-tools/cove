package state

import (
	"errors"
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
