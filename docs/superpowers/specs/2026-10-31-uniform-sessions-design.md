# Uniform sessions — design

**Status:** agreed with the user 2026-10-31; not built.
**Context:** Jam boxed in worker (ticket) sessions to keep them on task: their home is the ticket, they are told to `end` when done and to "send and end your turn" when stuck, and they are reaped after 30 minutes of waiting. The thinking has since moved to *sessions are sessions*. The boxing limited workers without speeding anything up, and it cost real work:

1. A worker hit a problem, posted it on the ticket, and exited. A Linear comment is far less visible than a squawk, and the context was lost, so another session had to start over.
2. A worker finished a task but its commit or report failed. It ended and the studio was torn down, so the work was lost — while the studio was still viable and help could have saved it.

The full inventory of today's differences, with `file:line`, is the catalog this design was built from (attached to the 2026-10-31 jam-go-01 conversation; its tables are summarised below where needed).

## Intent

Every session behaves the same way; only **how it was started** differs (Requisitioner, `jam studio raise`, standing config, personal). A worker can do everything a personal session can: talk in its own channel, call people in, wait as long as it needs, and be found by escalation when stuck.

## Goals

- **One home for every session: its own session channel.** A ticket session's home is its session channel, not the ticket channel.
- **People are reached the same way from any session.** `send to=user:X` or `call_in(user:X)` brings X into the session channel, as from a personal session.
- **Sessions end themselves only when sure their work has landed** (committed, pushed, PR open or merged, ticket reported). When they cannot land it, they stay alive, report `needs-input`/`blocked`, and wait.
- **Escalation draws attention to stuck sessions** — into the session channel. (Escalation eligibility is already kind-blind; only where the notice lands changes.)
- **No kind is torn down for waiting.**
- **State survives a restart for every kind** (persisted volumes + conversation resume), and a session that dies is raised again, so a ticket is never left stranded In Progress with no session.
- **`board-execute` is rewritten to match** (it lives in this repo's studio base image).

## Non-goals

- **Personal-session privacy rules ride for now:** owner-only addressing (`supervisor.go:413-421`) and invite-only channels stay. The user can't recall their reason; revisit separately.
- Outcome → tracker writeback beyond what `report` already does.
- Multi-instance Jam, webhook intake, per-class caps.

## Anti-goals

- A worker that can no longer move its ticket or open its PR — the ticket stays the work item.
- Conversation silently disappearing from Linear people rely on: a person who comments on the issue must still reach the session, and the session's `report` must still show on the issue.
- Unbounded studios: the concurrency cap must still hold, and an operator must still be able to end any session.

## Decisions

### 1. Home channel (the user's call)

- `HomeChannel` (`internal/jam/intercom_post.go:222`) returns the session channel for every session.
- A ticket session **also joins its ticket channel** at raise (it stays a member, as today). The ticket channel keeps its issue binding, so:
  - issue comments still arrive there and, because the session is a member, reach its inbox and wake it (*my call*: the simplest way to keep Linear commenters connected without moving the binding);
  - `report` keeps moving the tracker state and commenting (unchanged);
  - the session posts to the issue only deliberately (`send to=ticket:<key>`); its final turn text goes to its home, not the issue.
- `send to=user:X` from a ticket session follows the same rule as everyone else: into the home channel if X is a member, else a chat (`intercom_post.go:657-670` loses its ticket special case).
- Escalation notices for a ticket session call the tiers into the **session channel**; the notice text drops "replies go to <unit>'s conversation, its issue included" (`intercom_escalate.go:88-92`).
- Mission card on the rail: the session's own card (drop the ticket-channel "latest raised session" lookup, `channel_views.go:124-133`), *my call*.

### 2. Addressing

- Remove the extra `ticket:` glob requirement for reaching or calling in a ticket session (`sessionTicketAllowed`, `decide.go:265-282`, `intercom_post.go:828-834`, `intercom_membership.go:150`). `session:` addressing is enough, as for any session.
- A ticket session keeps the implicit right to post to its own ticket and keeps `ticket:<key>` in `list_targets`.

### 3. Prompts and wording

Remove every "end when finished / end your turn when you need a person" instruction aimed at workers, and replace with the shared rules:

- `sessionctx/boilerplate.go`: workers get the same lifetime and comms lines as other sessions ("send without `to` posts to your own channel"; "A message to you wakes you"). The ticket line becomes: keep the ticket current with `report`; call `end` only once the work has landed (merged, or PR handed off) and the ticket is reported; if you can't land it, report `needs-input`/`blocked`, say why in your channel, and wait.
- `dispatcher.go:66-73` `turnEndProtocol`: same rewrite; "if you need a person, `call_in` them or `send` to them, then end your turn — their reply wakes you."
- `agentrun/workload.go:33-46,240-248` `resumePrompt`: ticket wording only for sessions that have a ticket (fixes the manual-raise bug); otherwise the shared resident wording.
- `board-execute` skill (`images/jam-studio-base/.../skills/board-execute/SKILL.md`): rewritten to the same rules — talk in the session channel, call people in, never end with unlanded work.

### 4. Waiting and teardown

- Drop the wait-max reaper's kind check: no session is torn down for waiting (`wakeon.go:376-389`). Remove `wait-max` from config (`cmd/jam/config.go:210`, `runtime.wake`) or make it a no-op with a deprecation warning — *builder's call*.
- What frees a studio afterwards is the same for every kind: `end`, operator End/Release, and the role's idle timeout + on-idle action (`--idle-timeout`/`--on-idle`).
- Teardowns are classified (*my call*):
  - **Final** — `end`, operator End/Release, idle-timeout teardown, re-raise give-up (§5), manager removal (its volumes go with it). Volumes deleted; no re-raise.
  - **Recoverable** — Lost, or the agent dying without `end` (Done). Re-raised per §5; the ticket is untouched.
- `blockUnfinished` (mark the ticket blocked when a session ends without a terminal report) runs on **every final** teardown of a ticket session, not just wakeon's (`internal/jam/supervisor.go` Teardown/Lost paths, `cmd/jam/ticket.go:50`), and never on a recoverable one.
- **No default idle timeout for worker roles (the user's call).** A waiting worker holds its slot until answered or ended; escalation and operators handle stuck sessions. Automate once real usage shows how it behaves.

### 5. State and re-raise

- Persisted agent-data + workspace volumes and conversation resume (today standing-only: `launcher.go:273-296`, `workload.go:330-341`, manager pinning `supervisor.go:797-815`) apply to every kind for the session's lifetime. Volumes are deleted when the session **ends** (end, release, operator End), kept across Lost/crash.
- A session that is Lost or whose agent dies (Done without `end`) is **re-raised under the same id** onto its pinned manager, resuming its conversation, with the standing reconciler's backoff (30s doubling to 30m), applied to post-raise crashes too (fixes the standing crash loop).
- Re-raise holds its allocator slot across the gap (no new grant).
- *My calls:*
  - The **Supervisor** owns re-raise for every kind via a new **recovering** phase (not Gone), so the Requisitioner's dedup still sees the ticket as worked and the slot stays held; the standing reconciler's backoff logic is generalised into it.
  - **Give up** after 5 failed re-raises or 2 h, whichever first → a final teardown (ticket blocked, slot released, escalation raised).
  - Personal sessions are re-raised the same way.
  - Whether backoff state survives a Jam restart: builder's call.
  - **Rollout:** sessions raised before the change keep today's behaviour (no re-raise) until they end; only sessions raised after it get persisted volumes and re-raise.

### 6. UI

- User UI: every kind gets the same controls — End (operator, or the person who started it), Restart (operator). Personal keeps Release as its End. Kind label stays as an informational "started by" tag (ticket / manual / standing / personal).

### 7. Bugs fixed alongside

- Admin raises take an allocator grant like every other raise (today they bypass it yet release on teardown, drifting the cap: `standing_sessions.go:156`, `allocpg.go:113-125`).
- Admin raise `--unit` claims the ticket (In Progress) like the Requisitioner (`dispatcher.go:166`).
- `report` checks `TicketUnit`, not `Unit != ""` → a clean 400 for a free-text unit (`report.go:120`).
- `MayReach` checks personal before `Unit != ""` (`intercom_post.go:531`).
- Ticket-channel asks are answerable by any project member who can see them (`intercom_asks.go:165`).

## Constraints

- Turn protocol: both legacy and structured paths change together.
- Docs: `docs/usage/jam/` owners updated in the same PRs (intercom.md, comms-addressing.md, turn-end.md, requisitioner.md, dispatch-runbook.md, coves.md, standing-sessions.md) per the repo's docs-author rules.
- Studio image and host `jam` must ship from the same commit (wording and the MCP surface change together).

## Left to the builder

Slicing into PRs (suggested order: 3 prompts → 1+2 comms → 4 waiting → 7 bugs → 5 state/re-raise → 6 UI), test strategy, the wait-max config deprecation path, and naming.
