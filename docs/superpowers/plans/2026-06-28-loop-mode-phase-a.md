# Loop Mode — Phase A: Named-Instance State

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Generalize the `state` package so a kit can hold multiple named instances
(the interactive `state.json` plus one `loop-<name>.json` per loop),
each independently saved, loaded, deleted, and locked —
with the existing interactive-instance behavior completely unchanged.

**Architecture:** Add an `Instance` value type whose zero value (`Interactive`) maps to today's `state.json`
and whose loop form maps to `loop-<name>.json`.
Add `*For(kitDir, inst, …)` variants of every path/IO/lock function;
keep the current exported functions as thin wrappers that delegate to `Interactive`,
so no existing call site or test changes.
This is the keystone for the loop lifecycle and per-instance locking in later phases;
it ships as pure plumbing with no user-facing change.

**Tech Stack:** Go 1.22, standard library only (`os`, `syscall`, `encoding/json`, `path/filepath`).

## Global Constraints

- Go version floor `go 1.22`; standard library only — no new dependencies.
- Tests are hermetic (no Docker/network/ssh); use `t.TempDir()`, follow the existing `internal/state` test style.
- **Zero behavior change** for the interactive instance: existing exported functions (`Path`, `Exists`, `Save`, `Load`, `Delete`, `AcquireShared`, `AcquireExclusive`) keep their signatures and semantics, delegating to `Interactive`.
- Locking stays advisory `flock`, Unix-only (darwin + linux).
- Instance file naming: interactive ⇒ `state.json`; loop named `foo` ⇒ `loop-foo.json`, both inside `<kitDir>/.state/`.
- This phase adds no new CLI surface and no backend changes; those come in Phases B and C.

---

### Task 1: `Instance` type and instance-aware file IO

**Files:**
- Modify: `internal/state/state.go`
- Test: `internal/state/state_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces (relied on by Task 2 and later phases):
  - `type Instance string`
  - `const Interactive Instance = ""`
  - `func LoopInstance(name string) Instance` → `Instance("loop-" + name)`
  - `func PathFor(kitDir string, inst Instance) string`
  - `func ExistsFor(kitDir string, inst Instance) bool`
  - `func SaveFor(kitDir string, inst Instance, s State) error`
  - `func LoadFor(kitDir string, inst Instance) (State, error)`
  - `func DeleteFor(kitDir string, inst Instance) error`
  - Existing `Path/Exists/Save/Load/Delete` remain, delegating to `Interactive`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/state/state_test.go`:

```go
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
```

Add `"strings"` to the test file's imports.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `/usr/local/go/bin/go test ./internal/state/ -run 'TestInstance|TestNamedInstances|TestLoadForMissing'`
Expected: FAIL — build error, `undefined: PathFor` / `undefined: LoopInstance` etc.

- [ ] **Step 3: Add the `Instance` type**

In `internal/state/state.go`, after the `State` struct and before `ErrNotCreated`, add:

```go
// Instance identifies one named cove instance within a kit. The zero value,
// Interactive, is the human-facing instance recorded in state.json; a loop
// named "foo" is a separate instance stored alongside it as loop-foo.json.
type Instance string

// Interactive is the default instance: the one create/connect/destroy/status
// operate on today.
const Interactive Instance = ""

// LoopInstance returns the Instance for the named loop.
func LoopInstance(name string) Instance { return Instance("loop-" + name) }

// file is the state filename for this instance, inside the kit's .state dir.
func (i Instance) file() string {
	if i == Interactive {
		return "state.json"
	}
	return string(i) + ".json"
}
```

- [ ] **Step 4: Add `PathFor` and reduce `Path` to a wrapper**

In `internal/state/state.go`, replace the existing `Path` function:

```go
// Path returns the state.json path inside the kit.
func Path(kitDir string) string { return filepath.Join(Dir(kitDir), "state.json") }
```

with:

```go
// PathFor returns the state-file path for the given instance inside the kit.
func PathFor(kitDir string, inst Instance) string {
	return filepath.Join(Dir(kitDir), inst.file())
}

// Path returns the interactive instance's state.json path.
func Path(kitDir string) string { return PathFor(kitDir, Interactive) }
```

- [ ] **Step 5: Add `*For` variants and reduce the rest to wrappers**

In `internal/state/state.go`, replace `Exists`, `Save`, `Load`, and `Delete` with instance-aware versions plus interactive wrappers:

```go
// ExistsFor reports whether the given instance's state file is present.
func ExistsFor(kitDir string, inst Instance) bool {
	_, err := os.Stat(PathFor(kitDir, inst))
	return err == nil
}

// Exists reports whether the interactive state file is present.
func Exists(kitDir string) bool { return ExistsFor(kitDir, Interactive) }

// SaveFor writes the given instance's state file (creating .state/), stamping
// the schema version.
func SaveFor(kitDir string, inst Instance, s State) error {
	s.SchemaVersion = schemaVersion
	if err := os.MkdirAll(Dir(kitDir), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(PathFor(kitDir, inst), append(b, '\n'), 0o600)
}

// Save writes the interactive instance's state file.
func Save(kitDir string, s State) error { return SaveFor(kitDir, Interactive, s) }

// LoadFor reads the given instance's state file. Returns ErrNotCreated if absent.
func LoadFor(kitDir string, inst Instance) (State, error) {
	b, err := os.ReadFile(PathFor(kitDir, inst))
	if errors.Is(err, os.ErrNotExist) {
		return State{}, ErrNotCreated
	}
	if err != nil {
		return State{}, err
	}
	var s State
	if err := json.Unmarshal(b, &s); err != nil {
		return State{}, err
	}
	return s, nil
}

// Load reads the interactive instance's state file.
func Load(kitDir string) (State, error) { return LoadFor(kitDir, Interactive) }

// DeleteFor removes the given instance's state file. Idempotent.
func DeleteFor(kitDir string, inst Instance) error {
	err := os.Remove(PathFor(kitDir, inst))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// Delete removes the interactive instance's state file. Idempotent.
func Delete(kitDir string) error { return DeleteFor(kitDir, Interactive) }
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `/usr/local/go/bin/go test ./internal/state/`
Expected: PASS — the three new tests plus the existing `TestSaveLoadDelete` and lock tests (the existing wrappers preserve their behavior).

- [ ] **Step 7: Build, vet, gofmt, and commit**

Run: `/usr/local/go/bin/go build ./... && /usr/local/go/bin/go vet ./internal/state/ && /usr/local/go/bin/gofmt -l internal/state/`
Expected: success, no vet output, `gofmt -l` prints nothing.

```bash
git add internal/state/state.go internal/state/state_test.go
git commit -m "feat(state): named instances (loop-<name>.json) alongside interactive state"
```

---

### Task 2: Instance-aware locking

**Files:**
- Modify: `internal/state/lock.go`
- Test: `internal/state/lock_test.go` (new file)

**Interfaces:**
- Consumes: `Instance`, `Interactive`, `LoopInstance`, `PathFor` from Task 1.
- Produces (relied on by later phases):
  - `func AcquireSharedFor(kitDir string, inst Instance) (*Lock, error)`
  - `func AcquireExclusiveFor(kitDir string, inst Instance) (*Lock, error)`
  - Existing `AcquireShared`/`AcquireExclusive` remain, delegating to `Interactive`.

- [ ] **Step 1: Write the failing test**

Create `internal/state/lock_test.go`:

```go
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
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `/usr/local/go/bin/go test ./internal/state/ -run 'TestLocksArePerInstance|TestAcquireForMissing'`
Expected: FAIL — build error, `undefined: AcquireSharedFor` / `AcquireExclusiveFor`.

- [ ] **Step 3: Make `acquire` instance-aware and add the exported `*For` functions**

In `internal/state/lock.go`, replace the existing `acquire` function and the two `Acquire*` functions:

```go
func acquire(kitDir string, how int) (*Lock, error) {
	f, err := os.OpenFile(Path(kitDir), os.O_RDWR, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotCreated
	}
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), how|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrLocked
		}
		return nil, err
	}
	return &Lock{f: f}, nil
}

// AcquireShared takes a shared (read) lock — many connects may hold it at once.
func AcquireShared(kitDir string) (*Lock, error) { return acquire(kitDir, syscall.LOCK_SH) }

// AcquireExclusive takes an exclusive (write) lock; returns ErrLocked if any
// shared or exclusive lock is held (i.e. while connections are open).
func AcquireExclusive(kitDir string) (*Lock, error) { return acquire(kitDir, syscall.LOCK_EX) }
```

with:

```go
func acquireFor(kitDir string, inst Instance, how int) (*Lock, error) {
	f, err := os.OpenFile(PathFor(kitDir, inst), os.O_RDWR, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotCreated
	}
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), how|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrLocked
		}
		return nil, err
	}
	return &Lock{f: f}, nil
}

// AcquireSharedFor takes a shared (read) lock on the given instance's state
// file — many holders may share it at once.
func AcquireSharedFor(kitDir string, inst Instance) (*Lock, error) {
	return acquireFor(kitDir, inst, syscall.LOCK_SH)
}

// AcquireExclusiveFor takes an exclusive (write) lock on the given instance's
// state file; returns ErrLocked if any shared or exclusive lock is held.
func AcquireExclusiveFor(kitDir string, inst Instance) (*Lock, error) {
	return acquireFor(kitDir, inst, syscall.LOCK_EX)
}

// AcquireShared takes a shared lock on the interactive instance.
func AcquireShared(kitDir string) (*Lock, error) { return AcquireSharedFor(kitDir, Interactive) }

// AcquireExclusive takes an exclusive lock on the interactive instance.
func AcquireExclusive(kitDir string) (*Lock, error) { return AcquireExclusiveFor(kitDir, Interactive) }
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `/usr/local/go/bin/go test ./internal/state/`
Expected: PASS — the two new lock tests plus the existing `TestLockSharedMultipleExclusiveBlocks` (its `AcquireShared`/`AcquireExclusive` calls now route through `Interactive`, unchanged behavior).

- [ ] **Step 5: Build, vet, gofmt, and commit**

Run: `/usr/local/go/bin/go build ./... && /usr/local/go/bin/go vet ./internal/state/ && /usr/local/go/bin/gofmt -l internal/state/`
Expected: success, no vet output, `gofmt -l` prints nothing.

```bash
git add internal/state/lock.go internal/state/lock_test.go
git commit -m "feat(state): per-instance advisory locking"
```

---

## Self-Review

**Spec coverage (Phase A slice):**
- Named instances with `loop-<name>.json` state files alongside interactive `state.json` → Task 1 (`Instance`, `LoopInstance`, `PathFor`, `*For` IO).
- Per-instance locking so a running loop blocks destroy/recreate of its own instance but not others → Task 2 (`AcquireSharedFor`/`AcquireExclusiveFor`; the isolation test proves a foo lock doesn't block interactive).
- Zero behavior change for existing commands → both tasks keep the original exported functions as `Interactive` wrappers; existing `state_test.go` tests are untouched and must still pass.

Phases B (`setup` workspace seeding) and C (the loop scheduler, lifecycle, name-aware `destroy`/`status`, `loops:` config, drain-then-poll, keep-awake, `ANTHROPIC_API_KEY`) are planned separately, against the code this phase lands.

**Placeholder scan:** none — every code and command step is concrete.

**Type consistency:** `Instance`, `Interactive`, `LoopInstance`, `PathFor`, and the `*For` function names are defined in Task 1 and consumed verbatim in Task 2. The `State` struct fields used in tests (`Name`, `Backend`, `Container`) match `state.go`.
