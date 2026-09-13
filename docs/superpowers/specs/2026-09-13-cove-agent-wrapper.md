# cove-master: the agent wrapper (COV-156)

**Status:** design approved, pre-plan
**Issue:** COV-156 (slice 4 of the cove-management tree)
**Foundation:** COV-155 (Attach client + `Workload` seam), COV-153 (Attach server), COV-149 (supervisor). **Design history:** `docs/superpowers/specs/2026-09-10-harbor-design.md`; `docs/superpowers/specs/2026-09-13-cove-master-client.md`.

## Summary

Replace cove-master's `stubWorkload` with the **real agent wrapper**: a `covemaster.Workload` that runs the `claude` agent as a **headless one-shot** (the dispatch-style invocation) and maps its lifecycle onto the Attach `Activity` stream. A raised cove now actually does a unit of work and reports it — Running while the agent runs, then Done (or a brief Waiting) when it finishes — and a harbor Teardown kills the agent. No SSH, no host orchestration: the wrapper needs only its environment.

This is deliberately the **one-shot** shape (chosen over a persistent/waiting agent): it is the immediate consumer of the cove-management spine (COV-146's dispatcher raises a cove to do a job and tears it down), and it is the tightest honest slice. The richer persistent-agent behavior — a cove that lingers Waiting and is Woken with *new* input — needs an input channel (COV-145) and is deferred.

## Package layout & boundary

- **`internal/agentrun`** (new) — the wrapper. Implements `covemaster.Workload`. Imports `internal/covemaster` (the seam types), `internal/dispatch/worker` (the `worker-result.json` schema + `ReadWorkerResult` — reused, not re-invented; verified to pull no oidc/ssh/grpc), and stdlib (`os/exec`). Spawning is behind an injectable `Spawner` seam so tests need no real `claude`.
- **`cmd/cove-master`** — swaps `stubWorkload` for an `agentrun` workload built from env. No other change to the client wiring.
- **Boundaries preserved.** `internal/covemaster` stays lean (attachpb + grpc only) — `agentrun` is a *consumer* of it, nothing leaks back. `go list -deps ./cmd/at-cove | grep -iE 'oidc|grpc'` stays **empty** (cove-master remains a separate, non-embedded binary). `go list -deps ./internal/covemaster | grep internal/harbor` stays empty.

## What it runs

Matches the existing dispatch invocation (`internal/dispatchrun/dispatchrun.go`) exactly:

```
claude -p --dangerously-skip-permissions "<prompt>"
```

run with the working directory set to the configured workdir. The binary (`claude`) and the `-p --dangerously-skip-permissions` flags are **fixed internally** (not configurable) so the wrapper stays faithful to the dispatch contract. The prompt is the only variable input.

### Configuration (env-driven, on `cmd/cove-master`)

Consistent with cove-master's existing three env vars:

| Env | Meaning | Default |
|-----|---------|---------|
| `AT_COVE_WORKDIR` | cwd for the agent **and** the directory whose `.at-task/worker-result.json` is read after exit | `/home/agent/workspace` |
| `AT_COVE_AGENT_PROMPT_FILE` | path to a file containing the full prompt (read off disk, keeping large text off the environment — mirrors dispatch's tmpfs prompt file) | **required** |

`cmd/cove-master` reads these (erroring clearly if `AT_COVE_AGENT_PROMPT_FILE` is missing/empty or unreadable), reads the prompt file, and constructs the typed `agentrun.Config`. The future backend Launcher is what will drop `task.json` + the prompt file into place; this slice just consumes them.

## The Workload

```go
package agentrun

// Spawner launches the agent process. Production uses execSpawner (os/exec);
// tests inject a fake. Start returns once the process has started (or fails to).
type Spawner interface {
    Spawn(ctx context.Context, bin string, args []string, dir string) (Process, error)
}

// Process is a started agent process. Wait blocks until it exits, returning the
// process's exit error (nil on exit 0, ctx.Err()-derived if killed by ctx cancel).
type Process interface { Wait() error }

type Config struct {
    WorkDir string        // cwd + where worker-result.json is read
    Prompt  string        // the full prompt (read from AT_COVE_AGENT_PROMPT_FILE by cmd/cove-master)
    Grace   time.Duration // SIGTERM→SIGKILL grace on teardown; default 10s
    Spawner Spawner       // nil → the real execSpawner
}

// Workload implements covemaster.Workload.
type Workload struct { /* cfg, log, spawner */ }

func New(cfg Config, log *slog.Logger) *Workload
func (w *Workload) Run(ctx context.Context, h covemaster.Handle) error
func (w *Workload) Control(c covemaster.Control)
```

### `Run` — lifecycle mapping (one-shot, terminal)

1. Build args `["-p", "--dangerously-skip-permissions", cfg.Prompt]`; `Spawn(ctx, "claude", args, cfg.WorkDir)`.
   - Spawn error → return the error (client reports Done; unit over).
2. `h.Report(Running)`.
3. `proc.Wait()`.
4. If `ctx.Err() != nil` (Teardown / parent shutdown cancelled us) → return `ctx.Err()`; **do not** read the result file (the run was interrupted, not completed).
5. Otherwise the agent exited on its own → `worker.ReadWorkerResult(cfg.WorkDir)`:
   - **file absent or parse error, or `Status.Active()` errors** → return an error (`"agent wrote no usable result: …"`). The agent didn't respond or wrote garbage.
   - **`ok`** → return `nil`. (Client reports `Done` → supervisor tears the cove down.)
   - **`needs-input`** → `h.Report(Waiting)`, then return `nil`. The supervisor sees a brief `Waiting` before `Done` — an honest signal that the unit ended wanting input. (The dispatcher/operator decides whether to re-dispatch; lingering-and-waking is a later slice.)
   - **`error`** → return an error carrying `WorkerError.Message` (logged). (→ Done.)

`Run` reports **Running** (always, on start) and **Waiting** (only on `needs-input`). It never returns without the client subsequently reporting `Done` — consistent with the seam. **`Blocked` is reserved and unused this slice** (a one-shot has no escalation state; it re-enters the seam when escalation/COV-145 lands).

### Teardown & Wake delivery

The real `execSpawner` builds the command with graceful cancellation:

```go
cmd := exec.CommandContext(ctx, bin, args...)
cmd.Dir = dir
cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
cmd.WaitDelay = grace // SIGKILL if still alive after this
```

So when a `Teardown` arrives, the covemaster client cancels `Run`'s ctx (per the seam), which trips `cmd.Cancel` → **SIGTERM**, and after `grace` the runtime sends **SIGKILL**. `Wait()` then returns a ctx-derived error and `Run` returns `ctx.Err()` (step 4). No custom signal plumbing.

`Workload.Control`:
- `Teardown` → **log** only. The client has already cancelled `Run`'s ctx; that cancel (not `Control`) does the kill. `Control(Teardown)` is the cooperative hook and stays a no-op beyond logging this slice.
- `Wake` → **log** and no-op. A one-shot has no suspended state to wake.

## Tests (hermetic, TDD)

All in `internal/agentrun` using a **fake `Spawner`** — no real `claude`, no network. The fake's `Process.Wait` is scripted per case: write a `worker-result.json` into the workdir then return nil; return a non-nil exit error; or block on `ctx.Done()` and return `ctx.Err()` (teardown).

1. **ok** → `Run` returns nil; `Report` saw `[Running]` only (no `Waiting`).
2. **needs-input** → `Run` returns nil; `Report` saw `[Running, Waiting]` in order.
3. **error result** → `Run` returns a non-nil error containing the worker message; `[Running]` reported.
4. **missing result file** → `Run` returns a non-nil error ("no usable result"); `[Running]` reported.
5. **unparseable / empty-status result** → `Run` returns a non-nil error.
6. **teardown** → fake blocks on ctx; cancel the ctx → `Run` returns `ctx.Err()` (Canceled); the result file (even if present) is **not** consulted. `Control(Teardown)` does not panic.
7. **spawn args** → the fake records bin/args/dir; assert `bin=="claude"`, `args==["-p","--dangerously-skip-permissions", prompt]`, `dir==cfg.WorkDir`.
8. **spawn failure** → fake `Spawn` returns an error → `Run` returns it; nothing reported (or reported nothing after the failure).
9. **`Control(Wake)`** → no panic, no-op.
10. **`cmd/cove-master` env-config** (`buildAgentConfig(getenv)` or equivalent): missing `AT_COVE_AGENT_PROMPT_FILE` → clear error; unreadable prompt file → clear error; present → `Config` with the file's contents as `Prompt` and the workdir default applied.

No `integration`-tagged test is required this slice (the real path is a trivial `os/exec` wrapper; the COV-155 integration test already covers the live client). If a cheap smoke of `execSpawner` against `/bin/echo`-style binaries is useful it may be added, but it is not load-bearing.

## Docs

Update the **cove-master** section of `docs/usage/harbor/coves.md`: cove-master now runs `claude -p` as a one-shot via the agent wrapper (replacing the stub); document `AT_COVE_WORKDIR` + `AT_COVE_AGENT_PROMPT_FILE`; state the Activity mapping (Running → Done, with `needs-input` → a brief Waiting before Done) and that Teardown SIGTERM/SIGKILLs the agent. Keep the harbor design as the rationale source; note the persistent/waiting-agent behavior is deferred.

## Deferred (unchanged)

- The **persistent/waiting agent** + real **Wake-with-input** (needs the COV-145 input channel); `Blocked`/escalation.
- cove-master as the image **entrypoint** in its own **non-root account**; collapsing the systemd + sshd boot; binary delivery into the image.
- Real backend **Launcher** (drops `task.json`/prompt into place; sets the env).
- `:443`/TLS **mux**; token rotation.
