# Connect Keep-Awake Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** While an `at-cove connect` session is active,
prevent the host Mac from idle-sleeping
so the Colima VM and the agent's work keep running.

**Architecture:** A new `internal/awake` package exposes a platform-isolated `Inhibitor`
(build-tagged: macOS uses `caffeinate -i`, other OSes are a no-op).
`connect.Connect` acquires the assertion immediately before `Transport.Launch`
and releases it when the session returns.
Failure to assert is a warning, never a hard error.

**Tech Stack:** Go 1.22, standard library only (`os/exec`, build tags).
No new dependencies.

## Global Constraints

- Go version floor: `go 1.22` (from `go.mod`); standard library only — do not add dependencies.
- macOS is the only host implementation; all other platforms must compile and run unchanged (no-op).
- Prevent **idle system sleep only**: `caffeinate -i`. No display/battery/AC flags.
- Sleep prevention is **best-effort**: on failure, warn on stderr and continue; never block or fail a connect.
- The assertion covers **only** `Transport.Launch`, not secret resolution, dial, or `ensureAuthenticated`.
- Follow existing package conventions: `Runner`/collaborator dependencies passed as parameters, config in `Options`, fakes in `_test.go`.

---

### Task 1: `internal/awake` package

**Files:**
- Create: `internal/awake/awake.go`
- Create: `internal/awake/inhibitor_darwin.go`
- Create: `internal/awake/inhibitor_other.go`
- Test: `internal/awake/awake_test.go`

**Interfaces:**
- Consumes: nothing (leaf package, standard library only).
- Produces:
  - `type Inhibitor interface { Inhibit() (release func(), err error) }`
  - `func New() Inhibitor` — returns the caffeinate-backed inhibitor on darwin, a no-op elsewhere.

- [ ] **Step 1: Write the failing test**

Create `internal/awake/awake_test.go`:

```go
package awake

import "testing"

// New() must return a usable Inhibitor on every platform: Inhibit returns no
// error, a non-nil release, and release is safe to call more than once.
func TestNewInhibitContract(t *testing.T) {
	release, err := New().Inhibit()
	if err != nil {
		t.Fatalf("Inhibit: %v", err)
	}
	if release == nil {
		t.Fatal("release must not be nil")
	}
	release()
	release() // idempotent: a second release must not panic
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/awake/`
Expected: FAIL — build error, `undefined: New` (package/files do not exist yet).

- [ ] **Step 3: Write the interface**

Create `internal/awake/awake.go`:

```go
// Package awake lets cove ask the host OS to stay awake for the duration of a
// session, so the machine does not idle-sleep while an agent is working.
package awake

// Inhibitor asks the host OS to stay awake until the returned release func is
// called. A nil error means the assertion is held and release tears it down;
// a non-nil error means no assertion is held (the caller decides what to do).
type Inhibitor interface {
	Inhibit() (release func(), err error)
}
```

- [ ] **Step 4: Write the darwin implementation**

Create `internal/awake/inhibitor_darwin.go`:

```go
//go:build darwin

package awake

import (
	"os"
	"os/exec"
	"strconv"
	"sync"
)

// New returns an Inhibitor backed by macOS's caffeinate(8).
func New() Inhibitor { return caffeinate{} }

type caffeinate struct{}

// Inhibit starts `caffeinate -i -w <pid>`, which asserts against idle system
// sleep until killed. -i prevents idle system sleep (the display may still
// sleep). -w <pid> ties caffeinate's lifetime to cove's own pid as a crash
// safety-net; release also kills it explicitly for prompt teardown on a clean
// exit. The returned release is guarded by sync.Once so it is safe to call
// more than once.
func (caffeinate) Inhibit() (func(), error) {
	cmd := exec.Command("caffeinate", "-i", "-w", strconv.Itoa(os.Getpid()))
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		})
	}, nil
}
```

- [ ] **Step 5: Write the non-darwin implementation**

Create `internal/awake/inhibitor_other.go`:

```go
//go:build !darwin

package awake

// New returns a no-op Inhibitor on platforms without a sleep-prevention
// implementation. connect runs unchanged; the host's own power settings apply.
func New() Inhibitor { return noop{} }

type noop struct{}

func (noop) Inhibit() (func(), error) { return func() {}, nil }
```

- [ ] **Step 6: Run the test to verify it passes**

Run: `go test ./internal/awake/`
Expected: PASS (`ok  github.com/aethons-tools/cove/internal/awake`). On macOS this briefly spawns and kills `caffeinate`; on Linux it exercises the no-op.

- [ ] **Step 7: Vet and commit**

Run: `go vet ./internal/awake/`
Expected: no output.

```bash
git add internal/awake/
git commit -m "feat(awake): platform-isolated host sleep inhibitor"
```

---

### Task 2: Thread the inhibitor into `connect.Connect`

**Files:**
- Modify: `internal/connect/connect.go` (imports, `Options`, `Connect` signature + inhibit block)
- Test: `internal/connect/connect_test.go` (update existing call sites; add fake + two tests)

**Interfaces:**
- Consumes: `awake.Inhibitor`, `awake.New` from Task 1.
- Produces: new `Connect` signature, relied on by Task 3:
  `func Connect(b backend.Backend, r runner.Runner, t Transport, aw awake.Inhibitor, o Options) error`
  and a new field `Options.Stderr io.Writer` (nil ⇒ `os.Stderr`).

- [ ] **Step 1: Update existing tests for the new signature and add the fakes**

In `internal/connect/connect_test.go`,
add imports `bytes`, `errors`, `reflect`, and the awake import is not needed (tests use a local fake).
Add the recorder + fake inhibitor, and give `fakeTransport` an optional recorder.

Add these declarations (near `fakeTransport`):

```go
// rec records the ordered lifecycle events of a Connect so tests can assert
// the sleep assertion brackets the launch.
type rec struct{ ev []string }

type fakeInhibitor struct {
	r   *rec
	err error
}

func (f *fakeInhibitor) Inhibit() (func(), error) {
	if f.err != nil {
		return nil, f.err
	}
	f.r.ev = append(f.r.ev, "inhibit")
	return func() { f.r.ev = append(f.r.ev, "release") }, nil
}
```

Extend `fakeTransport` with a recorder field and record the launch event:

```go
type fakeTransport struct {
	launched bool
	gotEnv   map[string]string
	r        *rec // optional; records "launch" when set
}

func (t *fakeTransport) Launch(_ sshargs.Target, env map[string]string) error {
	t.launched = true
	t.gotEnv = env
	if t.r != nil {
		t.r.ev = append(t.r.ev, "launch")
	}
	return nil
}
```

Then update every existing `Connect(...)` call site to pass a fake inhibitor as the 4th argument.
There are 8 call sites;
each `Connect(b, r, tr, opts(...))` (or with `o`) becomes `Connect(b, r, tr, &fakeInhibitor{r: &rec{}}, opts(...))`.
The affected tests:
`TestConnectHappyPath`, `TestConnectFirstSessionRunsLogin`, `TestConnectSkipsLoginWhenAuthed`, `TestConnectSkipAuth`, `TestConnectAuthProbeFailureAborts`, `TestConnectSecretFailureAbortsBeforeDial`, `TestConnectRequiresRunning`, `TestConnectCreatesKnownHostsDir`.

Example — `TestConnectHappyPath` changes its call line to:

```go
	if err := Connect(b, r, tr, &fakeInhibitor{r: &rec{}}, opts(t.TempDir())); err != nil {
```

- [ ] **Step 2: Add the two new tests**

Append to `internal/connect/connect_test.go`:

```go
func TestConnectInhibitsSleepAroundLaunch(t *testing.T) {
	r := &rec{}
	b := &fakeBackend{state: backend.StateRunning}
	tr := &fakeTransport{r: r}
	run := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: "tok\n"}, {Stdout: "cove-authed\n"}}}
	if err := Connect(b, run, tr, &fakeInhibitor{r: r}, opts(t.TempDir())); err != nil {
		t.Fatal(err)
	}
	want := []string{"inhibit", "launch", "release"}
	if !reflect.DeepEqual(r.ev, want) {
		t.Fatalf("event order = %v, want %v", r.ev, want)
	}
}

func TestConnectInhibitFailureWarnsAndLaunches(t *testing.T) {
	b := &fakeBackend{state: backend.StateRunning}
	tr := &fakeTransport{}
	run := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: "tok\n"}, {Stdout: "cove-authed\n"}}}
	var errBuf bytes.Buffer
	o := opts(t.TempDir())
	o.Stderr = &errBuf
	if err := Connect(b, run, tr, &fakeInhibitor{err: errors.New("no caffeinate")}, o); err != nil {
		t.Fatal(err)
	}
	if !tr.launched {
		t.Fatal("must launch even when the sleep assertion fails")
	}
	if !strings.Contains(errBuf.String(), "could not prevent host sleep") {
		t.Fatalf("expected warning; stderr=%q", errBuf.String())
	}
}
```

- [ ] **Step 3: Run the new tests to verify they fail**

Run: `go test ./internal/connect/ -run 'TestConnectInhibits|TestConnectInhibitFailure'`
Expected: FAIL — build error, `Connect` takes 4 args / `Options` has no field `Stderr` (production code not yet updated). A build failure counts as the expected failing state for this TDD step.

- [ ] **Step 4: Update `connect.go` — imports and `Options`**

In `internal/connect/connect.go`, add `"io"` to the import block and add the awake import:

```go
import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/aethons-tools/cove/internal/awake"
	"github.com/aethons-tools/cove/internal/backend"
	"github.com/aethons-tools/cove/internal/runner"
	"github.com/aethons-tools/cove/internal/secret"
	"github.com/aethons-tools/cove/internal/sshargs"
)
```

Add the `Stderr` field to `Options` (after `SkipAuth`):

```go
	SkipAuth      bool      // skip the interactive `claude auth login` step (--no-auth)
	Stderr        io.Writer // where the host-sleep warning is written; nil => os.Stderr
```

- [ ] **Step 5: Update `connect.go` — signature and inhibit block**

Change the `Connect` signature to take the inhibitor:

```go
func Connect(b backend.Backend, r runner.Runner, t Transport, aw awake.Inhibitor, o Options) error {
```

Replace the final auth/launch tail (currently the `if !o.SkipAuth { ... }` block followed by `return t.Launch(tgt, env)`) with:

```go
	if !o.SkipAuth {
		if err := ensureAuthenticated(r, tgt); err != nil {
			return err
		}
	}

	// Keep the host awake for the session only: idle work happens between here
	// and Launch returning. A failed assertion is a warning, never fatal.
	stderr := o.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}
	if release, err := aw.Inhibit(); err != nil {
		fmt.Fprintf(stderr, "at-cove: warning: could not prevent host sleep: %v\n", err)
	} else {
		defer release()
	}
	return t.Launch(tgt, env)
```

- [ ] **Step 6: Run the connect tests to verify they pass**

Run: `go test ./internal/connect/`
Expected: PASS — all updated existing tests plus the two new tests.

- [ ] **Step 7: Vet and commit**

Run: `go vet ./internal/connect/`
Expected: no output.

```bash
git add internal/connect/connect.go internal/connect/connect_test.go
git commit -m "feat(connect): keep the host awake during the session"
```

---

### Task 3: Wire `awake.New()` into `main.doConnect`

**Files:**
- Modify: `main.go` (import + the `connect.Connect(...)` call near line 320)

**Interfaces:**
- Consumes: `awake.New()` (Task 1) and the new `Connect` signature / `Options.Stderr` (Task 2).
- Produces: nothing new; final wiring.

- [ ] **Step 1: Add the awake import**

In `main.go`, add to the import block:

```go
	"github.com/aethons-tools/cove/internal/awake"
```

- [ ] **Step 2: Pass the inhibitor and stderr into `Connect`**

Replace the `return connect.Connect(...)` call (currently at `main.go:320`) with:

```go
	return connect.Connect(b, r, connect.StdinScript{R: r, Cmd: cmd}, awake.New(), connect.Options{
		Container:     st.Container,
		Secrets:       specs,
		IdentityFile:  priv,
		KnownHostsDir: filepath.Join(configDir(), "known_hosts.d"),
		SkipAuth:      noAuth,
		Stderr:        stderr,
	})
```

(`stderr` is already a parameter of `doConnect`.)

- [ ] **Step 3: Build the whole module**

Run: `go build ./...`
Expected: success, no output.

- [ ] **Step 4: Run the full test suite**

Run: `go test ./...`
Expected: PASS across all packages.

- [ ] **Step 5: Vet and commit**

Run: `go vet ./...`
Expected: no output.

```bash
git add main.go
git commit -m "feat(connect): wire host sleep inhibitor into the connect command"
```

---

## Self-Review

**Spec coverage:**
- Idle-system-sleep-only via `caffeinate -i` → Task 1, darwin impl.
- Non-Mac no-op / compiles unchanged → Task 1, `inhibitor_other.go`.
- Assertion covers only `Launch` (not auth) → Task 2, Step 5 placement after `ensureAuthenticated`.
- Best-effort warn-and-continue → Task 2, inhibit block + `TestConnectInhibitFailureWarnsAndLaunches`.
- `Options.Stderr` for the warning → Task 2, Steps 4–5.
- Crash safety-net via `-w <pid>`, idempotent release via `sync.Once` → Task 1, darwin impl + contract test.
- Wiring into the connect command → Task 3.
- Tests (ordering, failure path, noop contract) → Tasks 1 & 2.

**Placeholder scan:** none — every code and command step is concrete.

**Type consistency:** `Inhibitor.Inhibit() (func(), error)` and `New() Inhibitor` are identical across Tasks 1–3; `Connect(b, r, t, aw, o)` and `Options.Stderr io.Writer` match between Task 2 (definition) and Task 3 (call site).
