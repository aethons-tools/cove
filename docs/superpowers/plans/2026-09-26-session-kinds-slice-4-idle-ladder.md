# Session kinds, Slice 4: the personal-session idle ladder

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development (recommended) or superpowers:executing-plans. Steps use checkbox (`- [ ]`) syntax.

**Goal:** A personal session its owner has forgotten doesn't sit forever. Once it has waited on its owner for `idle-after`, harbor nags the owner over the intercom every `nag-every`; if `reclaim-after` is set and passes, harbor tells the owner and reclaims the session. Pausing an idle session already happens (wake-on's warm-timeout), so it isn't part of this slice.

**What "idle" means** (resolves the spec's open question): a personal session is idle while it is Waiting on its owner — measured from `Instance.WaitingSince`, which resets each time the owner replies and the cove runs another turn.

**Architecture:**
- **Settings on the Role.** `RoleAllocation` gains `IdleAfter`, `NagEvery`, `ReclaimAfter` (durations) for that role's personal sessions, administered with `role add`. Defaults when unset: idle-after **4h**, nag-every **24h**, reclaim-after **never**.
- **Nags are sent as the cove.** A nag is a squawk `From: actor:<cove id>`, `To: human:<owner>`, appended to the intercom log and delivered by the Discord relay like any cove message. So the owner replying to a nag is routed to the cove and wakes it — replying is "keep going".
- **The ladder runs in wake-on.** The wake-on engine already walks Waiting coves every tick and already special-cases personal ones; it gains the nag cadence and the optional reclaim, through a new `Nagger` dependency.
- **Nag state on the Instance.** `Instance.LastNagAt` and `Instance.Nags`, reset whenever the cove enters a new Waiting period.

**Tech Stack:** Go 1.26, `slog`, `just`. Spec: [`../specs/2026-09-26-session-kinds-standing-personal.md`](../specs/2026-09-26-session-kinds-standing-personal.md). Builds on session-kinds Slice 3 (merged: resident loop, wake-on exemption for personal coves, intercom without a dispatcher).

## Global Constraints

- **Only personal sessions.** Ephemeral/dispatcher coves are untouched (they keep wait-max teardown and escalation).
- **Nag timing.** First nag when `now − WaitingSince ≥ idle-after`; each later nag when `now − LastNagAt ≥ nag-every`. A reply resets the clock (new `WaitingSince`) and the nag state.
- **Reclaim.** Only when the Role sets `reclaim-after > 0` and `now − WaitingSince ≥ reclaim-after`: append a final notice to the owner, then tear the session down through `Supervisor.Teardown` (which records the release).
- **The final notice must still reach the owner after the cove is gone.** Verify how the Discord relay resolves a `human:` delivery; if it needs the sending Instance to still exist, make human delivery resolve from the squawk's `Project` instead (the notice carries it).
- **Best-effort.** A failed nag append is logged and retried next tick; it never tears the session down.
- **Validation.** Negative durations → 400. `nag-every` must be > 0 when nagging is on.
- **Docs in the same change.** TDD. Stage files by path (untracked `.switchboard/`). Each commit builds. End each commit with:
  ```
  Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_01BtTj5yZ4qoJzyK8o6625mv
  ```

---

## Task 1: Idle settings on the Role

**Files:** `internal/harbor/identity.go` (`RoleAllocation`), `internal/harbor/admin.go` (`RoleBody`, `RoleSummary`, handlers), `internal/harbor/adminclient/adminclient.go`, `cmd/at-harbor/main.go` (`role add` flags, `role list`), tests. The admin UI role form already keeps the whole `Allocation`; extend its test to cover the new fields.

```go
type RoleAllocation struct {
	MaxEphemeral        int           `json:"max_ephemeral,omitempty"`
	MaxPersonal         int           `json:"max_personal,omitempty"`
	MaxPersonalPerOwner int           `json:"max_personal_per_owner,omitempty"`
	IdleAfter           time.Duration `json:"idle_after,omitempty"`    // personal: nag after this long waiting; 0 = default (4h)
	NagEvery            time.Duration `json:"nag_every,omitempty"`     // personal: then nag this often; 0 = default (24h)
	ReclaimAfter        time.Duration `json:"reclaim_after,omitempty"` // personal: reclaim after this long; 0 = never
}
```

API/CLI use seconds on the wire (`idle_after_seconds`, …, following `ttl_seconds`) and durations on the CLI: `role add --idle-after 4h --nag-every 24h --reclaim-after 72h`. Add a helper that applies the defaults: `func (a RoleAllocation) PersonalIdle() (idleAfter, nagEvery, reclaimAfter time.Duration)`.

- [ ] **Tests (fail first):** admin/client/CLI round-trip; negative → 400; `PersonalIdle` defaults; UI edit keeps them.
- [ ] **Commit** — `harbor: personal-session idle settings on the role (session-kinds slice 4)`

---

## Task 2: Nag state on the Instance

**Files:** `internal/harbor/instance.go`, `internal/harbor/supervisor.go` (+tests).

```go
// Instance gains:
LastNagAt time.Time `json:"last_nag_at,omitempty"`
Nags      int       `json:"nags,omitempty"`

// Supervisor gains:
func (s *Supervisor) RecordNag(actorID string, at time.Time) error // sets LastNagAt, Nags++
```

`Supervisor.Report`: when the cove enters a new Waiting period (where `WaitingSince`/`WaitSeq` are set today), also clear `LastNagAt` and `Nags`.

- [ ] **Tests (fail first):** `RecordNag` persists; entering Waiting after a reply resets both.
- [ ] **Commit** — `harbor: track idle nags on personal coves (session-kinds slice 4)`

---

## Task 3: The idle ladder in wake-on

**Files:** `internal/wakeon/wakeon.go` (+tests).

```go
// Nagger tells a personal session's owner their session is idle, or that it was reclaimed.
type Nagger interface {
	Nag(ctx context.Context, inst harbor.Instance, idle time.Duration) error
	NotifyReclaimed(ctx context.Context, inst harbor.Instance, idle time.Duration) error
}
```

The engine also needs the Role's idle settings (read the Role from the store it already has, or add a small `RoleLookup` dependency) and a way to record nags (`RecordNag`). `nagger` may be nil (no intercom log) → no nags, no reclaim notices, but reclaim still happens if configured.

In the tick, for a Waiting personal instance **that hasn't just been woken this tick** (keep the existing replied/wake logic first):
1. `idle := now − WaitingSince`; `idleAfter, nagEvery, reclaimAfter := role.Allocation.PersonalIdle()`.
2. If `reclaimAfter > 0 && idle ≥ reclaimAfter`: `NotifyReclaimed`, then `reap.Teardown`; continue.
3. If `idle ≥ idleAfter && (LastNagAt.IsZero() || now − LastNagAt ≥ nagEvery)`: `Nag`; on success `RecordNag(now)`; on failure log and move on.
4. The existing warm-timeout pause still applies.

- [ ] **Tests (fail first, fake clock + fakes):** no nag before idle-after; first nag at idle-after; no repeat before nag-every; repeat after; a reply resets (new WaitingSince, nags cleared → no immediate nag); reclaim at reclaim-after with a notice before teardown; reclaim-after unset → never reclaimed; ephemeral instances unaffected; nil nagger → no panic.
- [ ] **Commit** — `wakeon: nag idle personal sessions and optionally reclaim them (session-kinds slice 4)`

---

## Task 4: Send the nags

**Files:** a `Nagger` implementation (e.g. `internal/harbor/nag.go`, or in `cmd/at-harbor` if it needs relay/directory pieces) (+tests), `cmd/at-harbor/main.go` wiring (pass it and the settings source to `wakeon.New` when `intercomLog != nil`), and the relay's human-delivery resolution if the reclaim notice needs it (see Global Constraints).

- `Nag` appends a squawk `From: actor:<inst.ActorID>`, `To: [human:<inst.Owner>]`, `Project: inst.Project`, body:
  `Your personal session <id> (<role>) has been waiting on you for <idle, rounded>. Reply to this message to pick it back up, or release it with: at-harbor session release <id>`
- `NotifyReclaimed` appends the same shape with body:
  `Reclaimed your personal session <id> (<role>) after <idle, rounded> without a reply.`
- Use the intercom's normal append path (`Prepare`/`Append`), so ids, timestamps and `Seq` are assigned like any squawk.

- [ ] **Tests (fail first):** the appended squawk's from/to/project/body; a reply to a nag routed via Discord receipts lands in the cove's inbox (reuse the existing routing test harness); the reclaim notice still resolves to the owner's Discord inbox after the Instance is removed.
- [ ] **Commit** — `harbor: deliver idle nags and reclaim notices to the owner (session-kinds slice 4)`

---

## Task 5: Docs + verification

- [ ] **Docs.** `personal-sessions.md`: the idle ladder (pause, nags, optional reclaim), the three settings and their defaults, "reply to a nag to keep going". `roster.md`: the new `role add` flags. Update the spec's open question as resolved.
- [ ] **Commit** — `docs: personal-session idle ladder (session-kinds slice 4)`
- [ ] **Verification gate:** `just test`; `go build ./...`; `go build -tags integration ./...`; `just integration-harbor` if Postgres starts, else CI; `just lint`. **Behavior check:** a waiting personal session gets its first nag after idle-after and repeats every nag-every; a reply wakes it and resets the ladder; with reclaim-after set it is reclaimed with a notice and its slot freed; ephemeral coves unchanged.

## Self-review

- **Spec coverage:** Slice 4 of the revised sequence — the pester step and optional reclaim; pause already exists. The spec's "reuse the escalation engine" is replaced by wake-on, because escalation delivers through tracker tickets and personal sessions have none.
- **Types/names:** `RoleAllocation.{IdleAfter,NagEvery,ReclaimAfter}`, `PersonalIdle()`, `Instance.{LastNagAt,Nags}`, `Supervisor.RecordNag`, `wakeon.Nagger{Nag,NotifyReclaimed}`.
- **Out of scope:** acting on a reply's text ("release"), standing sessions (Slice 5).
