# harbor: comms B2 — container pause/unpause for cheap idle coves (COV-162)

**Status:** design approved, pre-plan
**Issue:** COV-162 (comms hub slice B2 — the cost optimization on B1's loop)
**Foundation:** COV-160 (B1 wake-on turn loop + wake-on engine + `WaitingSince`/`WaitCursor`), COV-158 (Colima launcher/backend), COV-149 (supervisor Phase/Lease/Reconcile). **Design history:** `docs/superpowers/specs/2026-09-10-harbor-design.md`.

## Summary

On top of B1's proven suspend/resume loop, actually **freeze** an idle cove so it burns ~0 CPU: `raise → Create` (done); **`idle → after a warm timeout, docker pause`**; **`wake → docker unpause` then re-Wake**; `teardown → rm` (done — `docker rm -f` works on a paused container). A paused (cgroup-frozen) cove-master can't heartbeat and can't receive `Wake`, so B2 adds a first-class **`Idled`** phase (lease-reaping suspended) and a wake path that unpauses first.

**Decided in brainstorming:**
- Ownership: **Backend + Launcher gain `Pause`/`Unpause`; the supervisor owns Phase via `Idle`/`Resume`; the wake-on engine drives the timing** (warm-timeout → Idle; reply on Idled → Resume). The engine goes through the supervisor, never the Launcher directly.
- Wake after unpause: **retry `Wake` each tick until the cove resumes** (Wake is non-blocking + drops if no stream — early wakes before reconnect are harmless; a later one lands once cove-master's COV-155 reconnect re-establishes the stream). No explicit reconnect detection.
- agentrun is **unchanged** — it's frozen mid-`select` and thawed still blocked; harbor re-Wakes it. Its in-cove `max-wait` is a pure backstop; harbor owns the real bounds.

## 1. Backend — `Pause`/`Unpause` (`internal/backend`)

- Add to `DispatchOps`: `Pause(name string) error` (`docker pause <name>`) + `Unpause(name string) error` (`docker unpause <name>`). Implement on Colima (`internal/backend/colima`), mirroring `RemoveContainer`'s shape. Add to the `launcher.Backend` composite interface too (it embeds `DispatchOps`, so it inherits them — no separate change if they go on `DispatchOps`).
- `--rm` is unaffected by pause (rm triggers on exit/stop, not pause); `docker rm -f` removes a paused container, so teardown of an Idled cove already works via the existing `RemoveContainer`.
- Test: fake `runner.Runner` asserts `docker pause <name>` / `docker unpause <name>` argv (like `TestRunEphemeralArgs`).

## 2. Supervisor — `Idled` phase + `Pause`/`Unpause` seam + `Idle`/`Resume` + Reconcile (`internal/harbor`)

- Add `PhaseIdled Phase = "idled"` (paused; intentionally idle, not dead).
- `Launcher` interface gains `Pause(ctx, inst Instance) error` + `Unpause(ctx, inst Instance) error`. Update `placeholderLauncher` (no-op) and **every fake launcher** in tests (supervisor/admin/attach/adminclient/covemaster/cmd-at-harbor — ~7, like the COV-158 seam change) with no-op implementations. The real launcher (`internal/harbor/launcher`) implements them via `l.cfg.Ops.Pause(inst.Location)` / `Unpause(inst.Location)`.
- Supervisor methods:
```go
func (s *Supervisor) Idle(ctx context.Context, actorID string) error {
	inst, ok := s.store.GetInstance(actorID)
	if !ok || inst.Phase == PhaseGone { return fmt.Errorf("idle: no live instance for %q", actorID) }
	if err := s.launcher.Pause(ctx, inst); err != nil { return err }
	inst.Phase = PhaseIdled
	return s.store.PutInstance(inst)
}
func (s *Supervisor) Resume(ctx context.Context, actorID string) error {
	inst, ok := s.store.GetInstance(actorID)
	if !ok || inst.Phase == PhaseGone { return fmt.Errorf("resume: no live instance for %q", actorID) }
	if err := s.launcher.Unpause(ctx, inst); err != nil { return err }
	inst.Phase = PhaseLive
	inst.WaitingSince = s.now() // restart the warm/wait clock for the resume window
	return s.store.PutInstance(inst)
}
```
- **Reconcile:** at the top of the per-instance loop, `if inst.Phase == PhaseIdled { continue }` — a paused cove can't heartbeat, so its lease *will* expire; the reconciler must NOT probe/reap/adopt it (a paused container's `GetStatus` reports Running, so without this guard the "expired+alive→adopt" branch would keep renewing its lease for the wrong reason). The wake-on engine owns the Idled lifecycle (resume on reply, teardown past the bound).

## 3. Wake-on engine — pause/resume logic (`internal/wakeon`)

Add an `Idler` dep + `WarmTimeout` config; extend `tick` to the full state machine. Instances with `Activity == ActivityWaiting` include both `Live+Waiting` (warm window) and `Idled+Waiting` (paused).

```go
type Idler interface {
	Idle(ctx context.Context, actorID string) error
	Resume(ctx context.Context, actorID string) error
}
// Config gains WarmTimeout time.Duration (default e.g. 60s; must be < MaxWait).
// New gains the Idler param.
```

Per tick, for each `Activity == Waiting` instance:
1. `wait := now - WaitingSince`. If `wait > MaxWait` → `Teardown` (bounds paused + unpaused total wait; rm works on Idled); continue.
2. resolve `issueID`; `n = len(Comments)`. If `WaitCursor == ""` → `SetWaitCursor(n)` (baseline; only a fresh Live+Waiting cove — an Idled one was baselined before pause); continue.
3. `reply := n > atoi(WaitCursor)`.
   - if `reply` and `Phase == PhaseIdled` → `idler.Resume(actorID)` (unpause + Phase=Live + reset WaitingSince); continue. (Wake is sent on a subsequent tick once it's Live+Waiting-with-reply and the stream has reconnected.)
   - if `reply` (Phase == Live) → `wake.Wake(actorID)` (direct; retried each tick until the agent reports Running and drops out of Waiting); continue.
4. no reply: if `Phase != PhaseIdled` (Live+Waiting) and `wait > WarmTimeout` → `idler.Idle(actorID)` (pause). (An Idled cove with no reply and `wait <= MaxWait` → nothing; stays paused.)

Notes: the reply branch precedes the pause branch, so a cove being woken is never re-paused; `Resume` resets `WaitingSince`, giving the resumed cove a fresh window to reconnect + run before it could be re-paused or torn down; `WaitCursor` is NOT cleared on Resume (reply stays pending so the retry-Wake keeps firing until the agent reports Running; the next fresh Waiting-entry re-baselines per B1's `Report`).

## 4. Config + wiring (`cmd/at-harbor`)

- `dispatcherConfig` gains `WarmTimeout string \`yaml:"warm-timeout"\`` (optional; default engine value; must be < `wait-max`).
- In `cmdServe`, pass the supervisor as the engine's `Idler` (it already passes `sup` as Cursors/Reaper): `wakeon.New(st, sup, rsrv, sup, sup /*Idler*/, linearCommenter{tracker}, wakeon.Config{PollInterval, MaxWait, WarmTimeout}, log)` — adapt the arg list to the engine's `New` signature. The Launcher's new `Pause`/`Unpause` flow through the already-wired supervisor→launcher path (no new wiring — the real launcher is built in COV-158's `runtime.launcher` block).
- agentrun's `max-wait` default should be **≥ harbor's `wait-max`** so harbor's warm-timeout→pause / wait-max→rm always acts first (the in-cove timer is only a backstop for a dead harbor). Set/keep the agentrun default accordingly (or leave the fixed 30m default and document that harbor's `wait-max` should be ≤ it).

## Tests (hermetic)

- **backend:** `docker pause`/`unpause` argv (fake runner).
- **supervisor:** `Idle` → launcher.Pause called + Phase=Idled; `Resume` → launcher.Unpause + Phase=Live + WaitingSince reset; Reconcile **skips** an Idled instance (no probe/reap/adopt even with an expired lease). Fake launcher records Pause/Unpause.
- **wakeon engine:** extend the B1 tests — Live+Waiting past WarmTimeout, no reply → `Idle` called; Idled + reply → `Resume` called (no direct Wake that tick); Live + reply → `Wake`; Idled + no reply + wait ≤ MaxWait → nothing; wait > MaxWait (Idled or Live) → `Teardown`. Clock-injected.
- **config:** `warm-timeout` parse.

## Docs

`docs/usage/harbor/messaging.md` — update the "Waiting for a reply" section: an idle cove is now **paused** (`docker pause`, ~0 CPU) after `warm-timeout` and **unpaused** to resume when a reply arrives; document `warm-timeout` (+ that `wait-max` bounds total wait, paused or not). `coves.md` — note the `idled` phase.

## Deferred

- **Fly memory-snapshot suspend** (frees RAM, not just CPU) — a later backend/infra optimization; the `Idle`/`Resume` seam + `Idled` phase are the hook.
- The `Idled`-lease-expiry interaction with true multi-instance harbor (COV-150) — single-instance now, so an expired Idled lease is harmless (nothing else steals it).
