# cove-master Agent Wrapper Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace cove-master's `stubWorkload` with a real `covemaster.Workload` (`internal/agentrun`) that runs the `claude` agent as a headless one-shot and maps its lifecycle onto the Attach `Activity` stream.

**Architecture:** A new `internal/agentrun` package implements `covemaster.Workload`. It spawns `claude -p --dangerously-skip-permissions "<prompt>"` through an injectable `Spawner` seam (real `os/exec` in production, fake in tests), reports `Running`, then on clean exit reads `.at-task/worker-result.json` (reusing `internal/dispatch/worker`) and maps `ok`/`needs-input`/`error` to the return/`Waiting` semantics. Teardown is delivered by ctx cancellation via `exec.CommandContext` + `Cancel`(SIGTERM)/`WaitDelay`(SIGKILL grace). `cmd/cove-master` builds it from env.

**Tech Stack:** Go 1.25 (`exec.Cmd.Cancel`/`WaitDelay` available), `log/slog`, existing `internal/covemaster` + `internal/dispatch/worker`.

## Global Constraints

- **Boundary gates (must stay true):**
  - `internal/covemaster` stays lean — imports only `attachpb` + grpc + stdlib. `agentrun` depends on `covemaster`, never the reverse.
  - `go list -deps ./cmd/at-cove | grep -iE 'oidc|grpc'` → **empty** (cove-master remains a separate, non-embedded binary; this plan doesn't touch at-cove).
  - `internal/dispatch/worker` pulls no oidc/ssh/grpc (verified: stdlib + `gopkg.in/yaml.v3` only). `agentrun` may import it.
- **Faithful to dispatch:** the agent command is exactly `claude -p --dangerously-skip-permissions "<prompt>"`; the binary name and flags are fixed in code, not configurable.
- **One-shot, terminal mapping:** `Run` reports `Running` always and `Waiting` only on `needs-input`; it never reports `Blocked` this slice (reserved). The client reports `Done` when `Run` returns.
- **Secrets/quiet logging:** do not log the full prompt at info level (task detail may be sensitive); log only lifecycle transitions. The identity token / launch secret are the client's concern, not `agentrun`'s.
- **TDD, hermetic, gofmt-clean.** Fake-Spawner tests need no real `claude` or network. `execSpawner`'s own test may use `/bin/sh` (local, deterministic) and skips if `sh` is absent. Run `gofmt -w` on every file (the lint gate fails otherwise).
- **Commit trailers** on every commit:
  ```
  Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_018DRQWUqrvQMzYD8o9ZeLHv
  ```

---

### Task 1: `internal/agentrun` — the Spawner seam + real `execSpawner`

**Files:**
- Create: `internal/agentrun/spawner.go`
- Test: `internal/agentrun/spawner_test.go`

**Interfaces:**
- Produces: `Spawner` interface (`Spawn(ctx, bin string, args []string, dir string) (Process, error)`), `Process` interface (`Wait() error`), and `execSpawner{grace time.Duration}` implementing `Spawner` with os/exec. Task 2's `Workload` consumes these.

- [ ] **Step 1: Write the failing test** — `internal/agentrun/spawner_test.go`

```go
package agentrun

import (
	"context"
	"errors"
	"os/exec"
	"testing"
	"time"
)

func needSh(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
}

func TestExecSpawnerCleanExit(t *testing.T) {
	needSh(t)
	p, err := execSpawner{grace: time.Second}.Spawn(context.Background(), "sh", []string{"-c", "exit 0"}, "")
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if err := p.Wait(); err != nil {
		t.Fatalf("Wait: want nil, got %v", err)
	}
}

func TestExecSpawnerNonzeroExit(t *testing.T) {
	needSh(t)
	p, err := execSpawner{grace: time.Second}.Spawn(context.Background(), "sh", []string{"-c", "exit 3"}, "")
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	var ee *exec.ExitError
	if err := p.Wait(); !errors.As(err, &ee) {
		t.Fatalf("Wait: want *exec.ExitError, got %v", err)
	}
}

func TestExecSpawnerCancelSIGTERM(t *testing.T) {
	needSh(t)
	ctx, cancel := context.WithCancel(context.Background())
	p, err := execSpawner{grace: 5 * time.Second}.Spawn(ctx, "sh", []string{"-c", "sleep 30"}, "")
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	cancel()
	done := make(chan error, 1)
	go func() { done <- p.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Wait: want non-nil after cancel (SIGTERM), got nil")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Wait did not return after SIGTERM")
	}
}

func TestExecSpawnerCancelSIGKILLAfterGrace(t *testing.T) {
	needSh(t)
	ctx, cancel := context.WithCancel(context.Background())
	// Ignores SIGTERM, so only the WaitDelay SIGKILL can stop it.
	p, err := execSpawner{grace: 200 * time.Millisecond}.Spawn(ctx, "sh", []string{"-c", "trap '' TERM; sleep 30"}, "")
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	cancel()
	done := make(chan error, 1)
	go func() { done <- p.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Wait: want non-nil after SIGKILL, got nil")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Wait did not return after grace SIGKILL")
	}
}
```

- [ ] **Step 2: Run the test, verify it fails**

Run: `go test ./internal/agentrun/ -run TestExecSpawner -v`
Expected: FAIL — `undefined: execSpawner` / `Process`.

- [ ] **Step 3: Write the implementation** — `internal/agentrun/spawner.go`

```go
// Package agentrun is cove-master's agent wrapper: a covemaster.Workload that
// runs the claude agent as a headless one-shot and maps its lifecycle onto the
// Attach Activity stream. It depends on internal/covemaster (the Workload seam)
// and internal/dispatch/worker (the worker-result.json contract); it never
// imports internal/harbor.
package agentrun

import (
	"context"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// Spawner launches the agent process. Production uses execSpawner; tests inject
// a fake. Spawn returns once the process has started (or failed to start).
type Spawner interface {
	Spawn(ctx context.Context, bin string, args []string, dir string) (Process, error)
}

// Process is a started agent process. Wait blocks until it exits, returning the
// process's exit error (nil on exit 0). If ctx is cancelled, the runtime sends
// SIGTERM then SIGKILL (after grace) and Wait returns a non-nil error.
type Process interface {
	Wait() error
}

// execSpawner launches the agent as a real child process. On ctx cancellation it
// sends SIGTERM, then SIGKILL after grace (via exec.Cmd.WaitDelay).
type execSpawner struct{ grace time.Duration }

type execProcess struct{ cmd *exec.Cmd }

func (s execSpawner) Spawn(ctx context.Context, bin string, args []string, dir string) (Process, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = s.grace
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return execProcess{cmd: cmd}, nil
}

func (p execProcess) Wait() error { return p.cmd.Wait() }
```

- [ ] **Step 4: Run the test, verify it passes**

Run: `go test ./internal/agentrun/ -run TestExecSpawner -v`
Expected: PASS (4 tests).

- [ ] **Step 5: gofmt + commit**

```bash
gofmt -w internal/agentrun/
git add internal/agentrun/spawner.go internal/agentrun/spawner_test.go
git commit -m "agentrun: Spawner seam + execSpawner with SIGTERM/SIGKILL teardown (COV-156)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_018DRQWUqrvQMzYD8o9ZeLHv"
```

---

### Task 2: `internal/agentrun` — the `Workload` (lifecycle mapping)

**Files:**
- Create: `internal/agentrun/workload.go`
- Test: `internal/agentrun/workload_test.go`

**Interfaces:**
- Consumes: `Spawner`/`Process` (Task 1); `covemaster.Handle`, `covemaster.Control`, `covemaster.Running/Waiting`, `covemaster.Wake/Teardown`; `worker.ReadWorkerResult`, `worker.WorkerResult` (`internal/dispatch/worker`).
- Produces: `Config{WorkDir, Prompt string; Grace time.Duration; Spawner Spawner}`, `New(Config, *slog.Logger) *Workload`, `(*Workload).Run(ctx, covemaster.Handle) error`, `(*Workload).Control(covemaster.Control)`. Task 3 consumes `Config`/`New`.

- [ ] **Step 1: Write the failing tests** — `internal/agentrun/workload_test.go`

```go
package agentrun

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/covemaster"
)

// recordHandle records the activities the workload reports.
type recordHandle struct{ got []covemaster.Activity }

func (h *recordHandle) Report(a covemaster.Activity) { h.got = append(h.got, a) }

// scriptedProc runs a closure as its Wait.
type scriptedProc struct{ wait func() error }

func (p scriptedProc) Wait() error { return p.wait() }

type fakeSpawner struct {
	bin, dir string
	args     []string
	proc     Process
	err      error
}

func (f *fakeSpawner) Spawn(ctx context.Context, bin string, args []string, dir string) (Process, error) {
	f.bin, f.args, f.dir = bin, args, dir
	if f.err != nil {
		return nil, f.err
	}
	return f.proc, nil
}

func writeResult(t *testing.T, dir, body string) {
	t.Helper()
	atTask := filepath.Join(dir, ".at-task")
	if err := os.MkdirAll(atTask, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(atTask, "worker-result.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newWL(t *testing.T, dir string, f *fakeSpawner) (*Workload, *recordHandle) {
	t.Helper()
	w := New(Config{WorkDir: dir, Prompt: "do the thing", Spawner: f}, nil)
	return w, &recordHandle{}
}

func TestRunOK(t *testing.T) {
	dir := t.TempDir()
	writeResult(t, dir, `{"status":{"ok":{}}}`)
	f := &fakeSpawner{proc: scriptedProc{wait: func() error { return nil }}}
	w, h := newWL(t, dir, f)
	if err := w.Run(context.Background(), h); err != nil {
		t.Fatalf("Run: want nil, got %v", err)
	}
	if len(h.got) != 1 || h.got[0] != covemaster.Running {
		t.Fatalf("activities: want [Running], got %v", h.got)
	}
}

func TestRunNeedsInput(t *testing.T) {
	dir := t.TempDir()
	writeResult(t, dir, `{"status":{"needs-input":{"doing":"x","blocker":"y","need":"z","tried":"w"}}}`)
	f := &fakeSpawner{proc: scriptedProc{wait: func() error { return nil }}}
	w, h := newWL(t, dir, f)
	if err := w.Run(context.Background(), h); err != nil {
		t.Fatalf("Run: want nil, got %v", err)
	}
	want := []covemaster.Activity{covemaster.Running, covemaster.Waiting}
	if len(h.got) != 2 || h.got[0] != want[0] || h.got[1] != want[1] {
		t.Fatalf("activities: want [Running Waiting], got %v", h.got)
	}
}

func TestRunErrorResult(t *testing.T) {
	dir := t.TempDir()
	writeResult(t, dir, `{"status":{"error":{"message":"boom"}}}`)
	f := &fakeSpawner{proc: scriptedProc{wait: func() error { return nil }}}
	w, h := newWL(t, dir, f)
	err := w.Run(context.Background(), h)
	if err == nil {
		t.Fatal("Run: want error, got nil")
	}
	if len(h.got) != 1 || h.got[0] != covemaster.Running {
		t.Fatalf("activities: want [Running], got %v", h.got)
	}
}

func TestRunNoResultFile(t *testing.T) {
	dir := t.TempDir() // no .at-task/worker-result.json
	f := &fakeSpawner{proc: scriptedProc{wait: func() error { return nil }}}
	w, h := newWL(t, dir, f)
	if err := w.Run(context.Background(), h); err == nil {
		t.Fatal("Run: want error for missing result, got nil")
	}
	_ = h
}

func TestRunUnparseableResult(t *testing.T) {
	dir := t.TempDir()
	writeResult(t, dir, `{"status":{}}`) // no variant set → Active() errors
	f := &fakeSpawner{proc: scriptedProc{wait: func() error { return nil }}}
	w, h := newWL(t, dir, f)
	if err := w.Run(context.Background(), h); err == nil {
		t.Fatal("Run: want error for empty status, got nil")
	}
	_ = h
}

func TestRunTeardownCancel(t *testing.T) {
	dir := t.TempDir()
	writeResult(t, dir, `{"status":{"ok":{}}}`) // present, but must NOT be consulted
	ctx, cancel := context.WithCancel(context.Background())
	f := &fakeSpawner{proc: scriptedProc{wait: func() error { <-ctx.Done(); return ctx.Err() }}}
	w, h := newWL(t, dir, f)
	errCh := make(chan error, 1)
	go func() { errCh <- w.Run(ctx, h) }()
	cancel()
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("Run: want ctx error on teardown, got nil")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	if len(h.got) != 1 || h.got[0] != covemaster.Running {
		t.Fatalf("activities: want [Running] (no Waiting from the ok file), got %v", h.got)
	}
	w.Control(covemaster.Control{Kind: covemaster.Teardown}) // must not panic
}

func TestRunSpawnArgs(t *testing.T) {
	dir := t.TempDir()
	writeResult(t, dir, `{"status":{"ok":{}}}`)
	f := &fakeSpawner{proc: scriptedProc{wait: func() error { return nil }}}
	w, h := newWL(t, dir, f)
	if err := w.Run(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	if f.bin != "claude" {
		t.Errorf("bin: want claude, got %q", f.bin)
	}
	want := []string{"-p", "--dangerously-skip-permissions", "do the thing"}
	if len(f.args) != 3 || f.args[0] != want[0] || f.args[1] != want[1] || f.args[2] != want[2] {
		t.Errorf("args: want %v, got %v", want, f.args)
	}
	if f.dir != dir {
		t.Errorf("dir: want %q, got %q", dir, f.dir)
	}
}

func TestRunSpawnFailure(t *testing.T) {
	dir := t.TempDir()
	f := &fakeSpawner{err: os.ErrPermission}
	w, h := newWL(t, dir, f)
	if err := w.Run(context.Background(), h); err == nil {
		t.Fatal("Run: want spawn error, got nil")
	}
	if len(h.got) != 0 {
		t.Fatalf("activities: want none (spawn failed before Running), got %v", h.got)
	}
}

func TestControlWakeNoop(t *testing.T) {
	w := New(Config{WorkDir: t.TempDir(), Prompt: "x", Spawner: &fakeSpawner{}}, nil)
	w.Control(covemaster.Control{Kind: covemaster.Wake}) // must not panic
}
```

- [ ] **Step 2: Run the tests, verify they fail**

Run: `go test ./internal/agentrun/ -run 'TestRun|TestControl' -v`
Expected: FAIL — `undefined: New` / `Config` / `Workload`.

- [ ] **Step 3: Write the implementation** — `internal/agentrun/workload.go`

```go
package agentrun

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/aethons-tools/cove/internal/covemaster"
	"github.com/aethons-tools/cove/internal/dispatch/worker"
)

const defaultGrace = 10 * time.Second

// Config configures the agent wrapper.
type Config struct {
	WorkDir string        // cwd for the agent + dir whose .at-task/worker-result.json is read
	Prompt  string        // the full prompt passed as claude's positional arg
	Grace   time.Duration // SIGTERM→SIGKILL grace on teardown; default 10s
	Spawner Spawner       // nil → the real execSpawner
}

// Workload runs the claude agent as a one-shot and maps its lifecycle onto the
// covemaster Activity stream. It implements covemaster.Workload.
type Workload struct {
	cfg     Config
	log     *slog.Logger
	spawner Spawner
}

// New builds a Workload. A nil Spawner uses the real os/exec-backed spawner; a
// non-positive Grace defaults to 10s.
func New(cfg Config, log *slog.Logger) *Workload {
	if cfg.Grace <= 0 {
		cfg.Grace = defaultGrace
	}
	sp := cfg.Spawner
	if sp == nil {
		sp = execSpawner{grace: cfg.Grace}
	}
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Workload{cfg: cfg, log: log, spawner: sp}
}

// Run spawns claude -p, reports Running, then maps the worker-result to the
// Activity stream. Returning nil or an error both lead the client to report
// Done; a nil error means the unit completed cleanly.
func (w *Workload) Run(ctx context.Context, h covemaster.Handle) error {
	args := []string{"-p", "--dangerously-skip-permissions", w.cfg.Prompt}
	proc, err := w.spawner.Spawn(ctx, "claude", args, w.cfg.WorkDir)
	if err != nil {
		return fmt.Errorf("agentrun: start claude: %w", err)
	}
	h.Report(covemaster.Running)
	w.log.Info("agentrun: agent started", "workdir", w.cfg.WorkDir)

	waitErr := proc.Wait()
	if ctx.Err() != nil {
		// Teardown / parent shutdown interrupted the run; the result (if any) is
		// not meaningful. The client's Done/exit path owns the ctx error.
		w.log.Info("agentrun: agent interrupted by context cancel", "err", ctx.Err())
		return ctx.Err()
	}

	wr, _, ok, rerr := worker.ReadWorkerResult(w.cfg.WorkDir)
	if rerr != nil {
		return fmt.Errorf("agentrun: read worker-result: %w", rerr)
	}
	if !ok {
		return fmt.Errorf("agentrun: agent wrote no worker-result (exit: %v)", waitErr)
	}
	status, serr := wr.Status.Active()
	if serr != nil {
		return fmt.Errorf("agentrun: %w", serr)
	}
	switch status {
	case "ok":
		w.log.Info("agentrun: agent completed ok")
		return nil
	case "needs-input":
		w.log.Info("agentrun: agent needs input; reporting Waiting")
		h.Report(covemaster.Waiting)
		return nil
	case "error":
		msg := ""
		if wr.Status.Error != nil {
			msg = wr.Status.Error.Message
		}
		return fmt.Errorf("agentrun: agent reported error: %s", msg)
	default:
		return fmt.Errorf("agentrun: unexpected worker status %q", status)
	}
}

// Control handles control messages. The client already cancels Run's ctx on
// Teardown (which SIGTERM/SIGKILLs the agent via execSpawner), so both cases are
// log-only for a one-shot.
func (w *Workload) Control(c covemaster.Control) {
	switch c.Kind {
	case covemaster.Teardown:
		w.log.Info("agentrun: teardown requested; run context cancelled, agent terminating")
	case covemaster.Wake:
		w.log.Info("agentrun: wake requested; no-op for a one-shot agent")
	}
}
```

- [ ] **Step 4: Run the tests, verify they pass**

Run: `go test ./internal/agentrun/ -v`
Expected: PASS (all Task 1 + Task 2 tests).

- [ ] **Step 5: Verify the seam compiles against covemaster.Workload**

Add to `workload.go` (compile-time assertion):

```go
var _ covemaster.Workload = (*Workload)(nil)
```

Run: `go build ./internal/agentrun/`
Expected: builds.

- [ ] **Step 6: gofmt + commit**

```bash
gofmt -w internal/agentrun/
git add internal/agentrun/workload.go internal/agentrun/workload_test.go
git commit -m "agentrun: Workload mapping claude one-shot lifecycle to Activity (COV-156)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_018DRQWUqrvQMzYD8o9ZeLHv"
```

---

### Task 3: `cmd/cove-master` — wire the wrapper from env, drop the stub

**Files:**
- Modify: `cmd/cove-master/main.go`
- Modify: `cmd/cove-master/main_test.go` — **remove** `TestStubWorkloadRunReturnsOnCancel` and the now-unused `noopHandle` type (they test `stubWorkload`, which this task deletes), and add `TestBuildAgentConfig` below. Keep `TestBuildConfigRequiresEnv`.

**Interfaces:**
- Consumes: `agentrun.Config`, `agentrun.New` (Task 2); `covemaster.New(...).Run(ctx, w)`.
- Produces: `buildAgentConfig(getenv func(string) string) (agentrun.Config, error)`.

- [ ] **Step 1: Write the failing test** — add to `cmd/cove-master/main_test.go`

```go
func TestBuildAgentConfig(t *testing.T) {
	dir := t.TempDir()
	promptPath := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(promptPath, []byte("do the work"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("missing prompt file env", func(t *testing.T) {
		_, err := buildAgentConfig(func(k string) string { return "" })
		if err == nil {
			t.Fatal("want error when AT_COVE_AGENT_PROMPT_FILE unset")
		}
	})

	t.Run("unreadable prompt file", func(t *testing.T) {
		env := map[string]string{"AT_COVE_AGENT_PROMPT_FILE": filepath.Join(dir, "nope.txt")}
		_, err := buildAgentConfig(func(k string) string { return env[k] })
		if err == nil {
			t.Fatal("want error when prompt file is unreadable")
		}
	})

	t.Run("defaults workdir, reads prompt", func(t *testing.T) {
		env := map[string]string{"AT_COVE_AGENT_PROMPT_FILE": promptPath}
		cfg, err := buildAgentConfig(func(k string) string { return env[k] })
		if err != nil {
			t.Fatalf("buildAgentConfig: %v", err)
		}
		if cfg.WorkDir != "/home/agent/workspace" {
			t.Errorf("WorkDir default: got %q", cfg.WorkDir)
		}
		if cfg.Prompt != "do the work" {
			t.Errorf("Prompt: got %q", cfg.Prompt)
		}
	})

	t.Run("explicit workdir honored", func(t *testing.T) {
		env := map[string]string{"AT_COVE_AGENT_PROMPT_FILE": promptPath, "AT_COVE_WORKDIR": "/tmp/work"}
		cfg, err := buildAgentConfig(func(k string) string { return env[k] })
		if err != nil {
			t.Fatalf("buildAgentConfig: %v", err)
		}
		if cfg.WorkDir != "/tmp/work" {
			t.Errorf("WorkDir: got %q", cfg.WorkDir)
		}
	})
}
```

Ensure the test file imports `os`, `path/filepath`, `testing`.

- [ ] **Step 2: Run the test, verify it fails**

Run: `go test ./cmd/cove-master/ -run TestBuildAgentConfig -v`
Expected: FAIL — `undefined: buildAgentConfig`.

- [ ] **Step 3: Edit `cmd/cove-master/main.go`**

Update the package doc comment's env list to add the agent vars:

```go
//	AT_HARBOR_RUNTIME_ADDR   harbor's runtime (Attach) listener, host:port
//	AT_HARBOR_IDENTITY_TOKEN the cove's identity token
//	AT_HARBOR_LAUNCH_SECRET  the per-instance launch secret
//	AT_COVE_WORKDIR          the agent's cwd + where .at-task/worker-result.json is read (default /home/agent/workspace)
//	AT_COVE_AGENT_PROMPT_FILE path to the file holding the agent's prompt (required)
```

Also update the top-of-file sentence: this binary now runs the real agent (claude `-p`) one-shot, not a stub.

Add the config builder and remove `stubWorkload`:

```go
func buildAgentConfig(getenv func(string) string) (agentrun.Config, error) {
	workdir := getenv("AT_COVE_WORKDIR")
	if workdir == "" {
		workdir = "/home/agent/workspace"
	}
	promptFile := getenv("AT_COVE_AGENT_PROMPT_FILE")
	if promptFile == "" {
		return agentrun.Config{}, fmt.Errorf("AT_COVE_AGENT_PROMPT_FILE is required")
	}
	prompt, err := os.ReadFile(promptFile)
	if err != nil {
		return agentrun.Config{}, fmt.Errorf("reading AT_COVE_AGENT_PROMPT_FILE: %w", err)
	}
	return agentrun.Config{WorkDir: workdir, Prompt: string(prompt)}, nil
}
```

Rewrite `run` to build both configs and use the real workload (delete `stubWorkload` and its methods):

```go
func run(getenv func(string) string, stderr *os.File) int {
	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg, err := buildConfig(getenv)
	if err != nil {
		fmt.Fprintln(stderr, "cove-master:", err)
		return 2
	}
	agentCfg, err := buildAgentConfig(getenv)
	if err != nil {
		fmt.Fprintln(stderr, "cove-master:", err)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := covemaster.New(cfg, log).Run(ctx, agentrun.New(agentCfg, log)); err != nil {
		fmt.Fprintln(stderr, "cove-master:", err)
		return 1
	}
	return 0
}
```

Update imports: add `github.com/aethons-tools/cove/internal/agentrun`; drop the now-unused `log/slog` import only if `stubWorkload` was its sole user (it is not — `run` still uses slog). Keep `grpc`/`insecure` (still used in `buildConfig`).

- [ ] **Step 4: Run the tests, verify they pass**

Run: `go test ./cmd/cove-master/ -v`
Expected: PASS.

- [ ] **Step 5: Verify the whole module builds + boundary gate**

Run:
```bash
go build ./...
go list -deps ./cmd/at-cove | grep -iE 'oidc|grpc' || echo "at-cove clean"
go list -deps ./internal/covemaster | grep internal/harbor || echo "covemaster clean"
```
Expected: builds; "at-cove clean"; "covemaster clean".

- [ ] **Step 6: gofmt + commit**

```bash
gofmt -w cmd/cove-master/
git add cmd/cove-master/main.go cmd/cove-master/main_test.go
git commit -m "cove-master: run the real agent wrapper, drop the stub workload (COV-156)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_018DRQWUqrvQMzYD8o9ZeLHv"
```

---

### Task 4: Docs — update the cove-master section of `coves.md`

**Files:**
- Modify: `docs/usage/harbor/coves.md` (the `## cove-master (the in-cove client)` section, lines ~86–112)

- [ ] **Step 1: Replace the stub note + env block**

Update the env block to add the two agent vars:

```
AT_HARBOR_RUNTIME_ADDR    harbor's runtime (Attach) listener, host:port
AT_HARBOR_IDENTITY_TOKEN  the cove's identity token
AT_HARBOR_LAUNCH_SECRET   the per-instance launch secret, minted at raise time
AT_COVE_WORKDIR           the agent's cwd + where .at-task/worker-result.json is read (default /home/agent/workspace)
AT_COVE_AGENT_PROMPT_FILE path to the file holding the agent's prompt (required)
```

Replace the blockquote (lines 105–108) with a description of the real wrapper:

```markdown
cove-master runs the agent as a **headless one-shot** (`internal/agentrun`):
it spawns `claude -p --dangerously-skip-permissions "<prompt>"` in `AT_COVE_WORKDIR`,
reports `running`, and when the agent exits reads `.at-task/worker-result.json`
(the same contract as the dispatch worker):

- `ok` → the client reports `done` and the supervisor tears the cove down.
- `needs-input` → a brief `waiting` is reported, then `done` (the dispatcher
  decides whether to re-dispatch; lingering-and-waking is a later slice).
- `error` / no result → `done` with the failure logged.

A harbor **teardown** cancels the run, which sends the agent `SIGTERM` and then
`SIGKILL` after a grace period. `wake` is a no-op for a one-shot agent.

> **Still deferred:** a persistent agent that lingers `waiting` and is woken with
> *new* input (needs the comms-hub input channel), `blocked`/escalation, and
> cove-master becoming the image entrypoint under its own non-root account
> (collapsing the SSH/systemd boot).
```

Keep the design-rationale link; update it to also reference the wrapper spec:

```markdown
Design rationale (the package boundary, the Workload seam, the reconnect model,
and the agent wrapper's lifecycle mapping) lives in
[`../../superpowers/specs/2026-09-13-cove-master-client.md`](../../superpowers/specs/2026-09-13-cove-master-client.md)
and [`../../superpowers/specs/2026-09-13-cove-agent-wrapper.md`](../../superpowers/specs/2026-09-13-cove-agent-wrapper.md).
```

- [ ] **Step 2: Bump `updated:` in the frontmatter to 2026-09-13** (already that date — confirm it reads `2026-09-13`).

- [ ] **Step 3: Docs audit (delta check)**

Run: `python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs --index OVERVIEW.md`
Expected: no *new* dangling links / orphans introduced by this change (the repo has a known pre-existing orphan baseline — compare the delta, not the absolute count). The new spec links resolve.

- [ ] **Step 4: Commit**

```bash
git add docs/usage/harbor/coves.md
git commit -m "docs: cove-master runs the real agent wrapper (COV-156)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_018DRQWUqrvQMzYD8o9ZeLHv"
```

---

## Self-Review

- **Spec coverage:** package+boundary (Tasks 1–3, gates in Task 3 step 5); command construction + fixed args (Task 2, asserted Task 2 test 7); env config (Task 3); lifecycle mapping ok/needs-input/error/missing (Task 2 tests); teardown via Cancel/WaitDelay (Task 1 tests 3–4) + ctx-return (Task 2 teardown test); Wake/Teardown Control no-ops (Task 2); docs (Task 4). All spec sections map to a task.
- **Type consistency:** `Spawner.Spawn(ctx, bin, args, dir)`/`Process.Wait()` identical across Tasks 1–2; `Config`/`New` signatures identical across Tasks 2–3; `covemaster.Handle.Report`, `covemaster.Control{Kind}`, `covemaster.Running/Waiting/Wake/Teardown` match `internal/covemaster/covemaster.go`; `worker.ReadWorkerResult` returns `(WorkerResult, any, bool, error)` and `WorkerResult.Status.Active()`/`.Error.Message` match `internal/dispatch/worker/resultv2.go`.
- **Placeholder scan:** none — every code step carries full code.
