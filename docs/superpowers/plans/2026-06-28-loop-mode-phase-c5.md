# Loop Mode — Phase C-5: Loop Mechanics (check, seed, scheduler)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the three hermetic mechanisms the `loop` command will compose —
`RunCheck` (does the loop's check trigger?),
loop workspace seeding (sentinel + reset, so a partial seed self-heals and `fresh-workspace` re-seeds),
and the drain-then-poll scheduler.

**Architecture:** `RunCheck` and the loop seeding live in `internal/connect`,
reusing the established secret-injection (`envScript`/`runInjected`) and marker-probe patterns.
The scheduler is a new pure `internal/loop` package:
`Run` drains (calls `tick` until nothing triggers),
then sleeps `interval` and drains again until a stop,
with `tick` and `sleep` injected so it is fully testable without real time or ssh.
The `loop` command that wires these to a created instance is C-6.

**Tech Stack:** Go 1.22, standard library only; shell runs inside the sandbox VM over ssh.

## Global Constraints

- Go version floor `go 1.22`; standard library only — no new dependencies.
- Secrets never on disk/argv — injected via the `/dev/shm` script (`envScript`/`runInjected`); the injected run uses empty stdin (fail-fast).
- A check exit 0 ⇒ trigger; a non-zero check is a normal "no work" result, not an error; an ssh/connection failure IS an error. Distinguish via a stdout marker (the `authProbe`/`setupEmptyProbe` pattern).
- Loop seeding is sentinel-based: seed only when the completion sentinel `/agent-data/.cove-loop-seeded` is absent, clearing the workspace first so a partial/interrupted seed self-heals; an empty setup command is a no-op.
- The scheduler is pure — no `os/signal`, no real `time.Sleep`; the caller injects `tick` and `sleep`.
- Hermetic tests (`runner.Fake`); follow the existing `internal/connect` test style.

---

### Task 1: `RunCheck`

**Files:**
- Create: `internal/connect/check.go`
- Test: `internal/connect/check_test.go`

**Interfaces:**
- Consumes: `envScript`, `workspaceDir` (`transport.go`); `sshargs.Base`; `runner.Runner`/`runner.ExitError`.
- Produces (relied on by C-6): `func RunCheck(r runner.Runner, tgt sshargs.Target, env map[string]string, checkCmd string) (bool, error)`.

- [ ] **Step 1: Write the failing tests**

Create `internal/connect/check_test.go`:

```go
package connect

import (
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/runner"
)

func TestRunCheckTriggered(t *testing.T) {
	f := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: "cove-trigger\n"}}}
	got, err := RunCheck(f, rawTarget(), map[string]string{"GITHUB_TOKEN": "tok"}, "test -e x")
	if err != nil {
		t.Fatal(err)
	}
	if !got {
		t.Fatal("marker present => should trigger")
	}
	for _, c := range f.Calls {
		for _, a := range c.Args {
			if a == "tok" {
				t.Fatalf("secret leaked onto argv: %v", c.Args)
			}
		}
	}
	// Two calls: write env, then run the check (Output).
	last := f.Calls[1].Args[len(f.Calls[1].Args)-1]
	if !strings.Contains(last, "cd /home/agent/workspace && if test -e x") {
		t.Fatalf("check command wrong: %q", last)
	}
}

func TestRunCheckNotTriggered(t *testing.T) {
	f := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: ""}}}
	got, err := RunCheck(f, rawTarget(), nil, "false")
	if err != nil {
		t.Fatal(err)
	}
	if got {
		t.Fatal("no marker => no trigger")
	}
}

func TestRunCheckConnectionError(t *testing.T) {
	f := &runner.Fake{Outputs: []runner.FakeResult{{Err: &runner.ExitError{Code: 255}}}}
	if _, err := RunCheck(f, rawTarget(), nil, "x"); err == nil {
		t.Fatal("ssh connection failure must error")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `/usr/local/go/bin/go test ./internal/connect/ -run TestRunCheck`
Expected: FAIL — build error, `undefined: RunCheck`.

- [ ] **Step 3: Implement `RunCheck`**

Create `internal/connect/check.go`:

```go
package connect

import (
	"fmt"
	"strings"

	"github.com/aethons-tools/cove/internal/runner"
	"github.com/aethons-tools/cove/internal/sshargs"
)

// checkTriggerMark is echoed on stdout when the check exits 0, so a true trigger
// is distinguishable from an ssh connection failure (which yields no marker and
// a non-nil error). The check's own output is discarded.
const checkTriggerMark = "cove-trigger"

// RunCheck runs the loop's check command in the workspace with secrets injected
// and reports whether it triggered (the check exited 0). A non-zero check is a
// normal "no work" result (false, nil); only an ssh/connection failure returns
// an error.
func RunCheck(r runner.Runner, tgt sshargs.Target, env map[string]string, checkCmd string) (bool, error) {
	file := fmt.Sprintf("/dev/shm/cove-check-%s-%d", tgt.Host, tgt.Port)
	writeArgs := append(sshargs.Base(tgt), "umask 077; cat > "+file)
	if err := r.RunStdin(strings.NewReader(envScript(env)), "ssh", writeArgs...); err != nil {
		return false, fmt.Errorf("writing check env: %w", err)
	}
	remote := "set -a; . " + file + "; rm -f " + file + "; cd " + workspaceDir +
		" && if " + checkCmd + " >/dev/null 2>&1; then echo " + checkTriggerMark + "; fi"
	out, err := r.Output("ssh", append(sshargs.Base(tgt), remote)...)
	if err != nil {
		return false, fmt.Errorf("running check: %w", err)
	}
	return strings.Contains(out, checkTriggerMark), nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `/usr/local/go/bin/go test ./internal/connect/`
Expected: PASS — the three `RunCheck` tests plus all existing connect tests.

- [ ] **Step 5: Build, vet, gofmt, and commit**

Run: `/usr/local/go/bin/go build ./... && /usr/local/go/bin/go vet ./internal/connect/ && /usr/local/go/bin/gofmt -l internal/connect/`
Expected: success, no vet output, `gofmt -l` prints nothing.

```bash
git add internal/connect/check.go internal/connect/check_test.go
git commit -m "feat(connect): RunCheck reports whether a loop's check triggers"
```

---

### Task 2: Loop workspace seeding (sentinel + reset)

**Files:**
- Create: `internal/connect/seed.go`
- Test: `internal/connect/seed_test.go`

**Interfaces:**
- Consumes: `runInjected`, `envScript`, `workspaceDir` (`setup.go`/`transport.go`); `sshargs.Base`; `runner.Runner`.
- Produces (relied on by C-6):
  - `func SeedLoopWorkspace(r runner.Runner, tgt sshargs.Target, env map[string]string, setupCmd string) error`
  - `func ResetLoopWorkspace(r runner.Runner, tgt sshargs.Target) error`

- [ ] **Step 1: Write the failing tests**

Create `internal/connect/seed_test.go`:

```go
package connect

import (
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/runner"
)

func TestSeedLoopWorkspaceFreshRunsSetup(t *testing.T) {
	f := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: "cove-unseeded\n"}}}
	if err := SeedLoopWorkspace(f, rawTarget(), map[string]string{"GITHUB_TOKEN": "tok"}, "git clone https://x ."); err != nil {
		t.Fatal(err)
	}
	// Three calls: sentinel probe (Output), write env, run seed.
	if len(f.Calls) != 3 {
		t.Fatalf("want 3 calls, got %d: %+v", len(f.Calls), f.Calls)
	}
	for _, c := range f.Calls {
		for _, a := range c.Args {
			if a == "tok" {
				t.Fatalf("secret leaked onto argv: %v", c.Args)
			}
		}
	}
	last := f.Calls[2].Args[len(f.Calls[2].Args)-1]
	if !strings.Contains(last, "find /home/agent/workspace -mindepth 1 -delete") {
		t.Fatalf("seed must clear the workspace first: %q", last)
	}
	if !strings.Contains(last, "git clone https://x . && touch /agent-data/.cove-loop-seeded") {
		t.Fatalf("seed must run setup then write the sentinel on success: %q", last)
	}
}

func TestSeedLoopWorkspaceSkipsWhenSeeded(t *testing.T) {
	f := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: "cove-seeded\n"}}}
	if err := SeedLoopWorkspace(f, rawTarget(), nil, "git clone x ."); err != nil {
		t.Fatal(err)
	}
	if len(f.Calls) != 1 { // only the sentinel probe
		t.Fatalf("a present sentinel must skip seeding: %+v", f.Calls)
	}
}

func TestSeedLoopWorkspaceEmptyNoop(t *testing.T) {
	f := &runner.Fake{}
	if err := SeedLoopWorkspace(f, rawTarget(), nil, ""); err != nil {
		t.Fatal(err)
	}
	if len(f.Calls) != 0 {
		t.Fatalf("empty setup must do nothing: %+v", f.Calls)
	}
}

func TestResetLoopWorkspace(t *testing.T) {
	f := &runner.Fake{}
	if err := ResetLoopWorkspace(f, rawTarget()); err != nil {
		t.Fatal(err)
	}
	last := f.Calls[0].Args[len(f.Calls[0].Args)-1]
	if !strings.Contains(last, "rm -f /agent-data/.cove-loop-seeded") ||
		!strings.Contains(last, "find /home/agent/workspace -mindepth 1 -delete") {
		t.Fatalf("reset must remove the sentinel and clear the workspace: %q", last)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `/usr/local/go/bin/go test ./internal/connect/ -run 'TestSeedLoopWorkspace|TestResetLoopWorkspace'`
Expected: FAIL — build error, `undefined: SeedLoopWorkspace` / `ResetLoopWorkspace`.

- [ ] **Step 3: Implement seeding**

Create `internal/connect/seed.go`:

```go
package connect

import (
	"fmt"
	"strings"

	"github.com/aethons-tools/cove/internal/runner"
	"github.com/aethons-tools/cove/internal/sshargs"
)

// loopSentinel marks that a loop workspace has been fully seeded. It lives on the
// persistent state volume (not in the workspace), so it survives reconnects but
// is removed by ResetLoopWorkspace for a fresh-workspace re-seed.
const loopSentinel = "/agent-data/.cove-loop-seeded"

// SeedLoopWorkspace populates an unattended loop workspace exactly once: when the
// sentinel is absent it clears the workspace, runs setupCmd with secrets injected,
// and writes the sentinel only on success. A present sentinel means a prior seed
// completed, so it returns immediately. Clearing before seeding makes a partial or
// interrupted seed self-heal on the next attempt (no sentinel => reclear + retry).
// An empty setupCmd is a no-op.
func SeedLoopWorkspace(r runner.Runner, tgt sshargs.Target, env map[string]string, setupCmd string) error {
	if setupCmd == "" {
		return nil
	}
	probe := "[ -e " + loopSentinel + " ] && echo cove-seeded || echo cove-unseeded"
	out, err := r.Output("ssh", append(sshargs.Base(tgt), probe)...)
	if err != nil {
		return fmt.Errorf("checking seed sentinel: %w", err)
	}
	if strings.Contains(out, "cove-seeded") {
		return nil
	}
	tail := "find " + workspaceDir + " -mindepth 1 -delete; cd " + workspaceDir +
		" && " + setupCmd + " && touch " + loopSentinel
	return runInjected(r, tgt, env, "seed", tail)
}

// ResetLoopWorkspace removes the seed sentinel and clears the workspace so the
// next SeedLoopWorkspace re-seeds — used for a loop's fresh-workspace mode.
func ResetLoopWorkspace(r runner.Runner, tgt sshargs.Target) error {
	cmd := "rm -f " + loopSentinel + "; find " + workspaceDir + " -mindepth 1 -delete"
	if err := r.RunStdin(strings.NewReader(""), "ssh", append(sshargs.Base(tgt), cmd)...); err != nil {
		return fmt.Errorf("resetting loop workspace: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `/usr/local/go/bin/go test ./internal/connect/`
Expected: PASS — the four new tests plus all existing connect tests.

- [ ] **Step 5: Build, vet, gofmt, and commit**

Run: `/usr/local/go/bin/go build ./... && /usr/local/go/bin/go vet ./internal/connect/ && /usr/local/go/bin/gofmt -l internal/connect/`
Expected: success, no vet output, `gofmt -l` prints nothing.

```bash
git add internal/connect/seed.go internal/connect/seed_test.go
git commit -m "feat(connect): sentinel-based loop workspace seeding and reset"
```

---

### Task 3: Drain-then-poll scheduler

**Files:**
- Create: `internal/loop/loop.go`
- Test: `internal/loop/loop_test.go`

**Interfaces:**
- Consumes: `time` (stdlib).
- Produces (relied on by C-6): `func loop.Run(once bool, interval time.Duration, tick func() bool, sleep func(time.Duration) bool)`.

- [ ] **Step 1: Write the failing tests**

Create `internal/loop/loop_test.go`:

```go
package loop

import (
	"testing"
	"time"
)

func TestRunOnceDrainsThenStops(t *testing.T) {
	calls := 0
	tick := func() bool { calls++; return calls < 3 } // triggers twice, then idle
	slept := 0
	sleep := func(time.Duration) bool { slept++; return true }
	Run(true, time.Minute, tick, sleep)
	if calls != 3 {
		t.Fatalf("--once should drain until idle (3 ticks: 2 work + 1 idle), got %d", calls)
	}
	if slept != 0 {
		t.Fatalf("--once must not sleep, slept %d", slept)
	}
}

func TestRunContinuousPollsUntilStop(t *testing.T) {
	ticks := 0
	tick := func() bool { ticks++; return false } // always idle
	sleeps := 0
	sleep := func(time.Duration) bool { sleeps++; return sleeps < 2 } // stop on the 2nd sleep
	Run(false, time.Minute, tick, sleep)
	// drain(1 idle tick) -> sleep#1(true) -> drain(1 idle tick) -> sleep#2(false=stop)
	if ticks != 2 || sleeps != 2 {
		t.Fatalf("ticks=%d sleeps=%d, want 2 and 2", ticks, sleeps)
	}
}

func TestRunContinuousDrainsBeforeSleeping(t *testing.T) {
	// First wake drains 2 triggers then idles; then a stop is requested.
	seq := []bool{true, false} // tick returns: true, false, then false forever
	i := 0
	tick := func() bool {
		v := false
		if i < len(seq) {
			v = seq[i]
		}
		i++
		return v
	}
	sleep := func(time.Duration) bool { return false } // stop immediately after first drain
	Run(false, time.Minute, tick, sleep)
	if i != 2 {
		t.Fatalf("should drain (true then false) before sleeping, ticks=%d", i)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `/usr/local/go/bin/go test ./internal/loop/`
Expected: FAIL — build error, no package `loop` / `undefined: Run`.

- [ ] **Step 3: Implement the scheduler**

Create `internal/loop/loop.go`:

```go
// Package loop drives an at-cove loop: drain all available work, then poll on an
// interval for more. It is pure — the caller injects how to do one unit of work
// (tick) and how to wait (sleep) — so the scheduling is testable without real
// time or ssh.
package loop

import "time"

// Run drives the loop. It drains — calls tick until tick reports false (nothing
// triggered) — then, unless once, sleeps interval and drains again, repeating
// until sleep reports a stop.
//
// tick performs one check-and-maybe-run and returns whether it triggered (did
// work). sleep blocks for d and returns true normally, or false if a stop was
// requested (e.g. a signal), which ends the loop.
func Run(once bool, interval time.Duration, tick func() bool, sleep func(time.Duration) bool) {
	for {
		for tick() {
		}
		if once {
			return
		}
		if !sleep(interval) {
			return
		}
	}
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `/usr/local/go/bin/go test ./internal/loop/`
Expected: PASS — the three scheduler tests.

- [ ] **Step 5: Build, vet, gofmt, and commit**

Run: `/usr/local/go/bin/go build ./... && /usr/local/go/bin/go vet ./internal/loop/ && /usr/local/go/bin/gofmt -l internal/loop/`
Expected: success, no vet output, `gofmt -l` prints nothing.

```bash
git add internal/loop/loop.go internal/loop/loop_test.go
git commit -m "feat(loop): drain-then-poll scheduler"
```

---

## Self-Review

**Spec coverage (Phase C-5 slice):**
- Check triggers on exit 0; non-zero is no-op; connection failure errors → Task 1 (`RunCheck`, marker pattern) + its three tests.
- Loop seeding once, partial-seed self-heal, `fresh-workspace` reset, empty-setup no-op → Task 2 (`SeedLoopWorkspace`/`ResetLoopWorkspace`, sentinel) + four tests.
- Drain-then-poll: work until idle, then poll every interval; `--once` drains once without sleeping → Task 3 (`loop.Run`) + three tests.
- Secrets never on argv; fail-fast injected runs → reuse of `envScript`/`runInjected`.

Deferred to C-6 (the `loop` command): `createLoopInstance` (build shared image via `CreateContext.Kit` + `<kit>-loop-<name>` naming + resolved per-loop `setup` + the `ANTHROPIC_API_KEY` declared-secret check), the `doLoop` command (`loop [<name>] [--once] [--keep] [--interval]`), the tick closure (reset?→seed→check→agent) wired to a dialed instance, keep-awake for the loop's duration, real signal-watching `sleep`, auto-create/destroy, and the C-4 carry-in (remove the shared image when the last instance is torn down).

**Placeholder scan:** none — every code and command step is concrete.

**Type consistency:** `RunCheck(...) (bool, error)`, `SeedLoopWorkspace(...) error`, `ResetLoopWorkspace(...) error`, and `loop.Run(once, interval, tick, sleep)` are defined once and consumed by their tests with matching signatures. `runInjected`, `envScript`, `workspaceDir` are existing `internal/connect` symbols.
