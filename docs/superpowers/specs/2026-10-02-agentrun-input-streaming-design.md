# agentrun: stream input into a live claude, close it only when truly idle

**Status:** design approved in conversation (2026-10-02); written spec awaiting review
**Scope:** cove-master's agent wrapper (`internal/agentrun`) plus the operator session-UI
totals (`internal/jam/adminui/session.go`). No proto, Jam supervisor, or covemaster change.
**Problem:** every agent turn is a fresh `claude -p … <prompt>` process. When it ends its
turn the process exits and **kills any background work** it started (`run_in_background`
Bash, background subagents, Monitors). The agent therefore cannot use Claude Code's
backgrounding features at all — a background task never completes and never wakes a
follow-up turn.

**Goal:** run claude with `--input-format stream-json`, deliver Attach Wakes as stdin
messages, and close stdin only when claude has ended its turn **and** all background
tasks are done. Transparent outside `agentrun`.

**Out of scope:** carrying message content in `Wake` (it stays payload-free; the agent
still reads the intercom with its MCP `read` tool); per-prompt connector refresh inside a
live process (see [Connector refresh](#connector-refresh)).

## Observed claude behaviour (Claude Code 2.1.284, verified in-sandbox)

These facts are the contract the design relies on:

1. **EOF ends the session.** Closing stdin lets claude finish queued turns, emit their
   `result`s, and exit 0. There is no "end" message.
2. **EOF kills outstanding background tasks.** They are reported as
   `{"type":"system","subtype":"task_notification","status":"stopped",…}` and no
   follow-up turn runs.
3. **Background state is reported in-stream.**
   `{"type":"system","subtype":"background_tasks_changed","tasks":[…]}` carries the
   **full current snapshot** of outstanding tasks on every change (`[]` = none).
   Per-task lifecycle: `task_started` (`is_backgrounded:true`) → `task_updated`
   (`patch.status`) → `task_notification` (`status`, `summary`, `output_file`).
4. **`result` does not mean idle.** A `result` (`terminal_reason:"completed"`,
   `queued_turn_count:0`) is emitted while background tasks are still running.
5. **A completing task self-starts a turn.** With stdin open, a task's
   `task_notification` is followed by `system/init` → `assistant` → a second `result`,
   with no user message written.
6. **`total_cost_usd` is cumulative per process; `usage` is per turn.** Two results in
   one process: cost 0.1364 → 0.1453; output tokens 116, then 21.
7. **`--continue` composes with `--input-format stream-json`** (a second process
   recalled context written by the first).

## Design

### 1. Spawner gains stdin (`spawner.go`)

`Process` gains `CloseInput() error` and `Input() io.Writer`; `execSpawner` wires
`cmd.StdinPipe()`. The fake spawner records written lines and lets a test script stdout
lines and the exit (including "exit on EOF"). A write error on stdin (EPIPE: the process
died) is treated as the process exiting — `Wait` reports the real outcome.

### 2. Argv (`claudeArgs`)

```
-p [--continue] --input-format stream-json --output-format stream-json --verbose
   --dangerously-skip-permissions --mcp-config <path> --strict-mcp-config
```

The prompt leaves argv and becomes the first stdin line:
`{"type":"user","message":{"role":"user","content":<prompt>}}` (built with
`encoding/json`, one line, `\n`-terminated).

### 3. Idle tracker (new `idle.go`; pure, table-tested)

Fed every stdout line (a third sink beside the stream log and the event `lineSplitter`,
with its own splitter capped at 64 MiB so a large `result` is never truncated out of
recognition; an unparseable line is ignored with a warning). State:

| field | set by |
|---|---|
| `tasks` | the latest `background_tasks_changed.tasks` snapshot (by `task_id`) |
| `awaiting` | tasks that left that snapshot but whose `task_notification` (by `task_id`) has not arrived — claude empties the list *before* notifying |
| `busy` | `true` on any stdin write, `system/init`, `assistant`, `user`, `task_notification`; `false` on `result` with `queued_turn_count == 0` (absent → 0) |
| `pendingWake` | a Wake arriving while `busy` |

Derived:

- **turn ended** = `!busy`
- **idle** = `!busy && len(tasks) == 0 && len(awaiting) == 0 && !pendingWake`

`awaiting` plus `task_notification` re-arming `busy` cover behaviour 5: claude emits
`background_tasks_changed:[]` *before* the completing task's `task_notification`, so
the task is held as awaiting until the notification starts the self-started turn,
and idle is not reached until that turn's `result`. A task that never gets a
notification keeps the episode on hold; the `BackgroundWait` cap is the backstop.

### 3a. Wakes are coalesced into one prompt

- **Busy:** a Wake only sets `pendingWake`; any number of Wakes collapse into one (Wake
  has no payload).
- **On turn end** (`busy` → `false`), *before* the idle check: if `pendingWake`, write
  **one** resume prompt (`resumePrompt`, or `residentResumePrompt` when `Resident`), clear
  the flag, mark `busy`.
- **Turn ended but tasks outstanding:** claude is between turns, so a Wake is written
  immediately.
- **Stdin already closed:** the Wake falls through to the existing buffered `w.wake`
  channel, and the next episode resumes with `--continue` exactly as today.

`Control` is unchanged: it still does a non-blocking send on `w.wake`. `Run`'s episode
loop selects on `w.wake` while the process is alive and hands it to the tracker.

### 4. One episode (inside `Run`'s existing loop)

An **episode** is one claude process. `Run`:

1. refreshes the connector (as today), spawns, writes the prompt, reports `Running`;
2. runs a single select loop over: tracker updates (from a channel fed by the stdout
   tap), `w.wake`, the background-wait timer, process exit, and `ctx.Done()`:
   - **idle** → `CloseInput()`, then wait for exit;
   - **Wake** → §3a;
   - **turn ended with tasks outstanding** → start the `BackgroundWait` timer (stopped
     again whenever `busy` re-arms or tasks empty);
   - **timer fires** → log loudly (task descriptions included), `CloseInput()`; claude
     stops the stragglers (behaviour 2);
   - **process exits on its own** (crash, auth failure) → fall through;
   - **ctx cancelled** → unchanged (SIGTERM, then SIGKILL after grace).
3. after exit, the **existing** worker-result / `Resident` / needs-input / `MaxWait` code
   runs unchanged. A later Wake respawns with `--continue` as today.

New `Config.BackgroundWait time.Duration`, default **30m** (same scale as `MaxWait`).

### 5. Session events: `turn` becomes the episode number

The cove-side counter is unchanged in code — it still increments once per spawn — but a
spawn now spans many prompts. Downstream impact was checked:

- **Unaffected:** storage, dedup, gap rows, acks, ordering, export — all key on
  `(stream_id, seq)`; `turn` is a plain stored column.
- **Admin session UI** (`internal/jam/adminui/session.go`) reads `turn` in two places,
  and its cost total is wrong under multi-result processes (behaviour 6) — see §5a.

### 5a. Session UI totals

- **Cost:** per `(stream, turn)` take the **last** `result`'s `total_cost_usd`; sum
  those. (Summing every result would double-count: the example above would show $0.28,
  not $0.145.) Episode = `turn` is exactly the key that makes this correct.
- **Tokens:** unchanged — summed over results (per-turn values).
- **"turns":** count `result` events (prompts answered), plus a new **"episodes"** figure
  = max(`turn`).
- **Per-result summary line:** label its `$` as cumulative for the episode.
- The `t{N}` per-event label stays (now groups events by process).

### Connector refresh

The connector is refreshed per **episode**, not per prompt: a Wake delivered into a live
process runs under the env that process started with. This is the one behavioural
regression against #304's per-turn refresh; it is bounded because every episode ends
once the agent is idle.

## Error handling

| situation | behaviour |
|---|---|
| process exits mid-turn | existing post-exit handling (resident: loud warn + wait; dispatched: worker-result logic) |
| stdin write fails | treated as process exit; `Wait` reports the cause |
| unparseable stdout line | ignored by the tracker (warn); still logged and forwarded as an event |
| background task never ends | `BackgroundWait` cap → close stdin → claude stops it |
| `result` never arrives (hang) | not detected here; Teardown/ctx still kills (unchanged) |

## Testing (hermetic, TDD)

- **Tracker table tests** built from the verified event sequences: plain turn → idle;
  `result` with tasks outstanding → not idle; tasks empty then self-started turn → idle
  only after the second `result`; `queued_turn_count > 0` → still busy; Wake while busy →
  one prompt on turn end; several Wakes → one prompt; Wake during the background hold →
  written immediately; unparseable line ignored.
- **Workload tests** (fake spawner): prompt arrives on stdin not argv; stdin closed only
  after tasks finish; `BackgroundWait` cap fires; Wake after exit → respawn with
  `--continue`; crash with no `result`; resident and needs-input paths unchanged.
- **adminui totals test:** two results in one turn using the real numbers (cost 0.1453,
  tokens summed), two episodes summed.

## Docs to update in the same change

- `internal/agentrun` package comment ("headless one-shot" → episode model).
- `docs/usage/jam/session-events.md`: `turn` = episode; `total_cost_usd` cumulative per
  episode; how the UI totals are computed.
- `docs/usage/jam/coves.md`, `intercom.md`, `personal-sessions.md` wherever they describe the per-turn
  loop, Wake delivery, or per-turn connector refresh (`connector.md`).
