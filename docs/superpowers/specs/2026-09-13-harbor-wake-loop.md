# harbor: comms B1 — turn loop + wake-on-reply (COV-160)

**Status:** design approved, pre-plan
**Issue:** COV-160 (comms hub slice B1; B split B1 loop+wake → B2 container pause/unpause = COV-162)
**Foundation:** COV-145 (messaging MCP `read`/`send`), COV-149 (supervisor + Attach `Wake`/`Waiting` + `ControlSink`), COV-155 (covemaster `Control`/reconnect), COV-156 (agent wrapper). **Design history:** `docs/superpowers/specs/2026-09-10-harbor-design.md` (§wake-on exit).

## Summary

Make a managed cove **suspend and resume**: it runs a turn, and on `needs-input` reports **Waiting** and blocks until harbor **wakes** it — over the live Attach stream — when a reply lands on its ticket, then runs the next turn (`claude --continue`). This closes the **ask → wait → resume** loop on top of COV-145's messaging MCP. **No container pause this slice** (that's B2): an idle cove stays up (claude not running between turns) and cove-master's heartbeats keep its lease, so there's no lease/reap conflict.

**Decided in brainstorming:** cove-master owns the turn loop + blocks on `Wake`; harbor owns the wake *decision*. `needs-input` **implicitly** means "suspend + wake me on a ticket reply" (messages is B1's only trigger — no new agent-facing syntax; this flips COV-156's terminal `needs-input` into suspend-until-reply, bounded by a max-wait).

## 1. Agent wrapper turn loop (`internal/agentrun`)

`Run` stops being one-shot. New shape:

```
loop:
  args := ["-p", "--dangerously-skip-permissions", "--mcp-config", …, prompt]   // turn 1
        | ["-p", "--continue", "--dangerously-skip-permissions", "--mcp-config", …, resumePrompt]  // turn N>1
  spawn + Report(Running); Wait
  if ctx cancelled → return ctx.Err()
  read worker-result:
    ok        → return nil                       (Done)
    error     → return err                       (Done, logged)
    missing   → return err
    needs-input → Report(Waiting); wait for one of:
                    - Wake        → prompt = resumePrompt; continue loop
                    - ctx/Teardown→ return ctx.Err()
                    - max-wait    → return nil    (give up waiting → Done → teardown)
```

- **`resumePrompt`** is a fixed generic string, e.g. *"New input may have arrived on your ticket — use the messaging `read` tool to fetch it, then continue. When finished, write .at-task/worker-result.json as before."* The agent fetches the reply via the COV-145 `read` tool; the wrapper does not shuttle it.
- **Wake delivery:** `Control(Wake)` (today a logging no-op) does a non-blocking send on a `wake chan struct{}`; `Run`'s wait selects on `wake`, `ctx.Done()`, and a `time.After(maxWait)` timer. covemaster already calls `w.Control(Control{Kind: Wake})` on a `ControlDown_Wake` (client.go) — **no covemaster change**. `Config` gains `MaxWait time.Duration` (default e.g. 30m; 0 → a sane default).
- Turn 1 vs N differ only by `--continue` + the prompt; keep the fixed flags (incl. COV-145's `--mcp-config`/`--strict-mcp-config`) on every turn.

## 2. Supervisor (`internal/harbor`, stays kit/dispatch-free)

- `Instance` gains additive fields: `WaitingSince time.Time` + `WaitCursor string` (both `omitempty`; additive JSON — no store migration, old records default to zero).
- `Report`: when Activity **transitions into** `Waiting` (was not already Waiting), set `inst.WaitingSince = now` and clear `inst.WaitCursor` (so the wake-on engine re-baselines each wait). Leaving/other activities are unchanged. `ActivityDone⇒Terminating⇒Teardown` unchanged. A Waiting cove keeps its lease via heartbeats → not reaped.
- New method `SetWaitCursor(actorID, cursor string) error` — read-modify-write the Instance's `WaitCursor` under the supervisor (so the engine persists the baseline without racing the lease writer). Opaque string (no messaging concept in core).

## 3. Wake-on engine (`internal/wakeon`, new; harbor-resident, wired from `cmd/at-harbor`)

Like the dispatcher: imports may include `internal/dispatch/*`/`internal/kit` (it's wired from cmd, not harbor core). Depends via narrow interfaces:

```go
type Registry interface { ListInstances() []harbor.Instance }
type Cursors  interface { SetWaitCursor(actorID, cursor string) error }
type Waker    interface { Wake(actorID string) }                       // the Attach ControlSink
type Reaper   interface { Teardown(ctx, actorID string) error }        // the supervisor
type Commenter interface {                                             // reuse the COV-145 harbor.Commenter (linearCommenter adapter)
    IssueByIdentifier(ctx, identifier string) (string, error)
    Comments(ctx, issueID string) ([]harbor.Comment, error)
}
type Config struct { PollInterval, MaxWait time.Duration }
func New(reg Registry, cur Cursors, wake Waker, reap Reaper, cmt Commenter, cfg Config, log) *Engine
func (e *Engine) Run(ctx)   // immediate tick + ticker
func (e *Engine) tick(ctx)
```

Per tick, for each instance with `Activity == Waiting`:
1. **max-wait:** if `now - WaitingSince > MaxWait` → `reap.Teardown(actorID)`; continue. (No zombies.)
2. resolve `issueID = IssueByIdentifier(inst.Unit)` (skip/log on error).
3. `n = len(Comments(issueID))`.
4. if `inst.WaitCursor == ""` → `cur.SetWaitCursor(actorID, itoa(n))` (baseline — captured *after* the cove's own question is posted, since the cove posts before it reports Waiting); continue.
5. if `n > atoi(inst.WaitCursor)` → a new comment (the reply) arrived → `wake.Wake(actorID)`. (The cove then runs `--continue`, reports `Running`, so it drops out of the Waiting set until it waits again — which resets `WaitingSince`+`WaitCursor` per §2.)

`scheduler.Comment` carries no id/timestamp, so the baseline is a **count**; "a new comment" = count increased. Restart-safe: the baseline lives in the persisted `WaitCursor`.

## 4. Wiring + config (`cmd/at-harbor`)

- `runtime.dispatcher` (or a small shared block) gains `wake-poll-interval` (default e.g. 15s) + `wait-max` (default 30m). Reuse the dispatcher's Linear client via the same `linearCommenter` adapter (COV-145). Gate the wake-on engine on a tracker being configured (same as the dispatcher/messaging).
- Build `wakeon.New(store, supervisor, attachServer /*ControlSink*/, supervisor, linearCommenter{client}, cfg, log)` and `go eng.Run(ctx)` alongside the dispatcher + supervisor loops. (`store` = Registry; `supervisor` = Cursors + Reaper; `attachServer` = Waker.)
- Pass `MaxWait` into `agentrun.Config` too (cove-master builds it — plumb via the launcher's env or a fixed default; simplest for B1: a fixed default in agentrun, tunable later).

## Tests (hermetic)

- **agentrun turn loop:** fake Spawner scripted per turn + a fake Handle/wake. Assert: `ok` first turn → one turn, Done; `needs-input` then a Wake → a second `--continue` turn runs (spawner sees `--continue` + resumePrompt); `needs-input` then max-wait (injected clock) → returns Done without a second turn; ctx cancel while waiting → returns ctx.Err(); `Control(Wake)` unblocks the wait. Assert Report saw `[Running, Waiting, Running, …]`.
- **supervisor:** `Report(Waiting)` sets `WaitingSince` + clears `WaitCursor` on transition-in; a second `Report(Waiting)` while already Waiting doesn't reset; `SetWaitCursor` persists.
- **wakeon engine:** fake Registry (canned Waiting instances) + fake Commenter (canned comment counts) + fake Waker/Reaper/Cursors. Assert: first tick baselines (SetWaitCursor called, no Wake); count unchanged → no Wake; count increased → Wake(actorID); WaitingSince older than MaxWait → Teardown (no Wake); non-Waiting instances ignored.
- config parse/validation.

## Docs

`docs/usage/harbor/messaging.md` (extend): a cove can now **wait** for a reply — `needs-input` suspends it, a new ticket comment wakes it, `wait-max` bounds it. Note B2 (container pause) is the cost optimization to come. `coves.md`: one line on the wait/resume lifecycle.

## Deferred

- **B2 (COV-162):** `docker pause` after a warm timeout + unpause-then-Wake + an `Idled` phase suspending lease-reaping.
- Explicit `wake-on { messages | timer | ticket-event }` selector; `timer(n)` + ticket-event triggers; a Wake payload/reason (bare `Wake` today).
- Comment id/timestamp cursor (needs `scheduler.Comment` to carry them) instead of a count baseline.
