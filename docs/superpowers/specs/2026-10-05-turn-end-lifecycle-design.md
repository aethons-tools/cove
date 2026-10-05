# jam: turn-end lifecycle — wake conditions, alarms, gates, `end` and `report`

**Status:** design approved section-by-section over the intercom (2026-10-04 → 2026-10-05).
**Scope:** Jam-managed studios only (cove-master / `agentrun` — ticket, personal, standing sessions), the wake-on engine, and Jam's Requisitioner.
**Goal:** minimize agent turns. A session ends its turn and Jam wakes it only when something worth a turn has happened — a squawk, a due alarm whose gate passed, or an inactivity timeout — and a session can end itself.
**Builds on:** the wake-on engine (`internal/wakeon`), the Attach stream (`internal/jam/attach`), `agentrun`'s episode loop and idle tracker, the brokered-tool pattern of `escalate` (`cmd/cove-master/mcp.go` → `POST /escalate` → an Instance field).
**Does not change (hard constraint):** the non-Jam dispatch path — `at-cove work`, `at-task`, `internal/dispatchrun`, everything under `internal/dispatch/**`. It keeps `worker-result.json`, the harness commit/push/PR, and the scheduler's transitions, untouched; it is to be reworked separately. The Jam path may *read* `scheduler.Role` and the Linear client, never edit them.
**Not in this spec:** context lifecycle for long-running sessions (compaction, clearing, memory) — a follow-up spec, recorded in `docs/TODO.md`.

## Decisions

- **One turn-end model for every session kind.** At the end of a turn a session either **ends** (it called `end`) or **waits** for its wake conditions. There is no session-kind-specific return format: `needs-input` as a concept goes away on the Jam path. Session kinds differ only in role defaults.
- **Wake conditions** are: any inbound squawk (always on, as today), due **alarms** (optionally gated), and the **idle timeout**. The first to fire wakes the session; the wake says why.
- **Alarms are declared, not slept.** An agent sets any number of named alarms with a tool; they persist across turns. A normal turn end costs no tool call: the idle timeout comes from the role.
- **Jam owns the schedule**; the cove only runs gates. A paused cove can't keep time, and Jam already owns every other wake decision and the pause→resume→wake dance.
- **Gates run inside the cove**, run by cove-master (not claude), with the agent's tools, workspace, credentials and egress. Never in Jam (agent-written scripts in the broker), never a fixed declarative check set.
- **The agent owns its ticket work up to merge**: branch, push, PR, review comments, keeping the branch mergeable. It tells the allocator its state with `report`; Jam keeps owning the Linear transitions. Merge detection is the agent's job (typically a PR-watch alarm); there is no allocator backstop.
- **`end` takes effect at turn end** and is available to every session kind.

## 1. Tool surface

New cove-master MCP tools, each brokered to a Jam endpoint on the cove-facing mux, self-scoped by the caller's identity token (the `escalate` pattern):

| Tool | Endpoint | Effect |
|------|----------|--------|
| `alarm_set(name, schedule, note?, gate?)` | `PUT /alarms/{name}` | Create or replace the named alarm. |
| `alarm_clear(name)` | `DELETE /alarms/{name}` | Remove it (`404` if absent). |
| `alarm_list()` | `GET /alarms` | Each alarm with its next fire time and last gate result. |
| `idle_timeout(duration \| "off", scope)` | `PUT /idle` | Override the role's idle timeout; `scope` is `next` (the next turn end only) or `always` (until the session ends). |
| `report(state, summary, pr?)` | `POST /report` | Ticket status to the allocator; any number of times. |
| `end(reason)` | `POST /end` | Tear the session down when the current turn ends. Irrevocable. |

- **`schedule`** is an RFC 3339 time (one-shot) or a 5-field cron expression (recurring). Cron is evaluated in the role's time zone (`turn-end.time-zone`, default `UTC`).
- **`note`** becomes the wake prompt for that alarm ("check the nightly backup").
- **`gate`** is a shell command string run with `sh -c` in the workspace directory (§3).
- **`report.state`** is one of the scheduler's states: `in-progress`, `in-review` (with `pr`), `needs-input` (the question goes in `summary`), `blocked`, `done`. `done` and `blocked` are **terminal reports**. On a session with no ticket, `report` is a `400`.
- **What happens on idle stays role policy** (`turn-end.on-idle`); the agent only tunes *when*.

**Limits** (so alarms can't become a busy loop): ≤ 20 alarms per session; cron no more often than once a minute; a gate runs ≤ 60s and keeps ≤ 4 KiB of output (truncated with a marker).

## 2. Data model

All persisted on the **Instance** (already a JSON document in the Postgres store — no new table):

```go
Alarms       []Alarm        `json:"alarms,omitempty"`
IdleOverride *IdleOverride  `json:"idle_override,omitempty"` // {Duration (0 = off), Scope: next|always}
EndRequested *EndRequest    `json:"end_requested,omitempty"` // {Reason, At}
TurnEndedAt  time.Time      `json:"turn_ended_at,omitempty"` // left running for holding/waiting; anchors the idle deadline
Report       *TicketReport  `json:"report,omitempty"`        // last successful report {State, Summary, PR, At}

type Alarm struct {
    Name, Schedule, Note, Gate string
    NextAt   time.Time
    GateRun  *GateRun   // in-flight gate: {RunID, StartedAt}; nil = none
    LastGate *GateResult // {At, Exit, TimedOut, Output}
}
```

The **role** gains a `turn-end` block on `RoleAllocation` (named apart from the personal idle ladder's `idle-after`, which nags an owner and never wakes the agent):

```yaml
turn-end:
  idle-timeout: 30m     # 0/unset = no idle timeout
  on-idle: teardown     # wake | teardown
  time-zone: UTC        # cron evaluation
```

**Fallback:** a role with no `turn-end` keeps today's behavior — a ticket session tears down after `runtime.wake.wait-max`; a resident session has no idle timeout. So existing configs are unaffected.

## 3. Engine

### Activity: `holding`

A new Activity, **`holding`**: the turn has ended but background tasks are outstanding (`agentrun`'s `actHold`; the episode is held open up to `BackgroundWait`, default 30m). Today cove-master reports `running` there, deliberately, so Jam never pauses a cove whose background job is working — but then alarms and the idle timeout could not fire until the episode ends.

Jam treats `holding` **like `waiting` for every wake trigger** (squawks, alarms and gates, the idle timeout) and **like `running` for pause and teardown** — never paused, never torn down for idleness; an idle deadline that passes while holding with `on-idle: teardown` waits for `waiting`. Admin UI and the `/me` status strip show it as its own state.

### At turn end (entering `holding` or `waiting`)

Jam stamps **`TurnEndedAt`** (a new Instance field) when a session leaves `running` for `holding` or `waiting` — not `WaitingSince`, which `holding` never sets.

1. If `EndRequested` is set: tear the session down when it enters `waiting` (a `holding` session's background tasks finish first, bounded by `BackgroundWait`). A session with `EndRequested` is never woken again — not by squawks, alarms or idle. Notify the owner — or, for a ticket session, the Requisitioner (§4) — `session X ended itself: <reason>`. A standing session has no owner: the notice is a session event only.
2. Otherwise arm the **idle deadline**: `TurnEndedAt + (IdleOverride ?: role idle-timeout)`. A `next`-scoped override is consumed here.

### Each tick, for a `holding` or `waiting` session, in order

1. **Squawks** — as today (`WaitSeq` baseline, external-origin only, the personal `keep`/`release` commands).
2. **Due alarms** (`NextAt ≤ now`):
   - **No gate:** fire.
   - **Gate:** if the cove is `idled`, `Resume` it and continue on a later tick (the existing pattern). Once Live, send **`RunGate{run_id, alarm, command}`**; record `GateRun`. Never start a second run while one is in flight.
   - **`GateResult`:**
     - exit 0 → **fire**, carrying the gate's stdout;
     - any other exit → not yet: no wake; re-`Idle` the cove if it was idled for the gate;
     - **the gate couldn't answer** — timed out, exit 126/127 (missing or not executable), or no result within timeout + 30s grace (cove disconnected/restarted) → **fire** as `gate-failed` with what went wrong and the output. A broken gate must never silently never fire.
   - **After evaluating:** a one-shot alarm is deleted — including when its gate said "not yet" (recorded as a session event, no wake). A cron alarm advances `NextAt` to the next match after `now`; **no catch-up** (missed matches during a Jam outage fire once).
3. **Idle deadline passed:** `on-idle: wake` → wake with reason `idle` (and re-arm from that wake's turn end); `on-idle: teardown` → tear down (ticket sessions are reported `blocked: idle`, §4). The personal idle ladder is unchanged and runs alongside.

**While `running`** (mid-turn), due alarms are **held** and fire as soon as the session holds or waits; a cron alarm due several times fires once. Squawks keep waking `running` sessions as today (`SetRunningWake`).

### Wake carries its reasons

`Wake{}` becomes `Wake{repeated WakeReason reasons}`, each `{kind, alarm, note, detail}`; kinds `squawk`, `alarm`, `gate-failed`, `idle`, `context-changed`. Everything firing on one tick goes in one Wake. `agentrun` renders the reasons into the resume prompt (replacing today's per-kind resume prompts' fixed text).

**`agentrun` must merge, not drop:** the wake channel is a `chan struct{}` of capacity 1, which coalesces safely only because a Wake carries nothing. It becomes a mutex-guarded pending-reasons list plus the existing signal, drained into one resume prompt (mid-turn coalescing, between-turns delivery and post-exit `--continue` all unchanged in timing).

### Wire (attach.proto, additive)

```proto
enum Activity { ... HOLDING = 5; }
message ControlDown { oneof msg { ... RunGate gate = 6; } }   // Wake gains `repeated WakeReason reasons = 1;`
message StatusUp    { oneof msg { ... GateResult gate = 5; } }
message RunGate    { string run_id = 1; string alarm = 2; string command = 3; uint32 timeout_s = 4; }
message GateResult { string run_id = 1; int32 exit = 2; bool timed_out = 3; bytes output = 4; bool truncated = 5; }
message WakeReason { string kind = 1; string alarm = 2; string note = 3; string detail = 4; }
```

cove-master runs a gate as the agent user, with the agent's env (connector-owned keys included), in the workspace, `sh -c`, killed at `timeout_s`, stdout+stderr captured to 4 KiB. A gate never starts a claude turn.

## 4. Ticket studios on the Jam path

- **Requisitioner prompt:** drops the `worker-result` protocol. It tells the agent it owns the branch, push and PR through merge, and to use `report` and `end`. The role grants `git` / `github-api` as today.
- **`POST /report`:** Jam calls the Requisitioner's tracker `Transition(issue, state)` and posts a comment (summary, PR link), then stamps `Report`. Synchronous: a tracker failure is a `502` to the agent (retry), and `Report` is stamped only on success. Repeating the same state is a no-op transition. The comment formatting is new Jam-side code, not the scheduler's.
- **`agentrun`:** stops reading `worker-result` entirely. At the end of every episode it reports `waiting` (and `holding` while an episode is held open for background tasks); `end` is enforced by Jam (§3), so `agentrun` needs no knowledge of it. A crashed agent process still reports `blocked`. The `ok` / `needs-input` / `error` branches go.
- **`end` without a terminal report** (`done`/`blocked`): Jam transitions the ticket to `blocked` with the end reason before teardown. Idle teardown does the same with `blocked: idle`. Both are **best-effort**: the teardown always proceeds; a failed transition is logged and recorded as a session event, and the ticket stays IN PROGRESS for a human to see.

A typical ticket life: work → `report(in-review, pr=…)` → `alarm_set("pr-watch", "*/5 * * * *", gate="<new comment | CI red | behind main | merged>")` → end turn → woken by the gate → address it → … → merged → `report(done)` → `end("merged")`.

## 5. Errors

- **Tool input:** bad cron, an `at` time in the past, an exceeded limit, an unknown `report.state` → `400` with a reason; nothing stored.
- **Jam restart:** alarms survive (on the Instance). An in-flight `GateRun` is cleared on load; its alarm is still due, so the gate re-runs (gates should be idempotent checks).
- **Version skew** (a new Jam, older studios still running): all proto changes are additive. An old cove-master ignores `RunGate` (→ the grace timeout → a `gate-failed` wake: degraded but correct), ignores wake reasons (generic resume prompt), never reports `holding`. Studios raised under the `worker-result` prompt finish as today.

## 6. Testing

- **`wakeon`:** table tests on a fake clock, extending the existing suite: alarm due vs held while running; cron no catch-up; every gate outcome including the grace timeout; idle deadline per `on-idle` and per override scope; `holding` never paused/torn down; several reasons on one tick → one Wake.
- **`agentrun`:** reason merging under coalescing; `holding` reported on `actHold`; every episode end reports `waiting`; no `worker-result` read.
- **Endpoints:** handler tests in the `escalate_handler` style (scoping, validation, limits, `report` without a ticket).
- **Attach:** `RunGate`/`GateResult` round-trip; cove-master gate runner (timeout, 126/127, output cap).
- **Requisitioner:** fake tracker — `report` → transition + comment; `end` without a terminal report → blocked; idle teardown → `blocked: idle`.
- **Store:** the new Instance fields round-trip through the Postgres conformance suite.
- **New dependency:** `github.com/robfig/cron/v3` for cron parsing and time-zone/DST-correct next-match.

## 7. Docs

- New leaf `docs/usage/jam/turn-end.md` owns alarms, gates, the idle timeout, `holding`, `end` and `report`.
- `intercom.md`'s wake-on section links to it instead of restating; its tool list gains the new tools by link.
- `requisitioner.md` replaces the `worker-result` result protocol with `report`/`end`.
- `coves.md` gains `holding` in the activity list; `roster.md` the role's `turn-end` block.
- `docs/TODO.md`: the context-lifecycle follow-up spec; the timed self-wake item is removed.

## 8. Rollout

Five slices, one PR each, each useful alone:

1. **Wake reasons + `holding`** — proto, `agentrun` reason merging and `actHold` reporting, `wakeon` treats `holding` as waiting for squawks.
2. **`end` + the role's `turn-end` idle settings** — `POST /end`, `PUT /idle`, idle deadline in `wakeon` (with the `wait-max` fallback).
3. **Alarms without gates** — `alarm_*` tools and endpoints, cron, `NextAt`, held-while-running.
4. **Gates** — `RunGate`/`GateResult`, the cove-master runner, resume/re-idle, the failure-wakes rule.
5. **`report` + the Requisitioner switch** — `POST /report`, transitions and comments, `end`/idle → blocked, `agentrun` stops reading `worker-result`.
