# Session controls: interrupt, compact, clear — design

**Status:** built — PR #490.
**Context:** Sessions today can only be released (or restarted/upgraded by operators). Three gaps hurt cost and quality ([TODO.md](../../TODO.md) "Context lifecycle for long-running sessions"):

1. **Interrupt.** A mistyped message or a wrong paste starts a turn, and the agent acts on it. People need to stop the turn *now* without losing the session.
2. **Compact.** Long tasks grow the context: cost goes up and quality goes down. People need to shrink it and carry on with the same task.
3. **Clear.** A finished task leaves its whole conversation behind. People need a blank conversation for the next task without releasing the session and starting a new one.

## Planning amendments

Five decisions made while planning changed the spec: the commands bypass `ControlSink`; the docs live in session-view.md; a clear-pending file beats the standing conversation marker; Interrupt is a visible button while Compact and Clear sit in the `⋯` menu; command records are synthetic `cove`/`command` event lines. See the [plan](../plans/2026-10-25-session-interrupt-compact-clear.md#plan-amendments-to-the-spec-decided-while-planning).

## Decisions (2026-10-25)

- **Buttons, not chat commands.** Three buttons in the session header (the [session view](../../usage/jam/intercom-ui.md)), beside Release: **Interrupt**, **Compact**, **Clear**. Behind them are new control messages from Jam to cove-master. Jam never parses chat text for them.
- **Who and where:** "for now", **anyone who can see the session**, on **any session kind** (personal, ticket, standing).
- **Native Claude mechanisms only.** No homegrown summarizer or kill switch. The mechanisms were verified against claude 2.1.287 `-p --input-format stream-json` (see [Verified behaviour](#verified-behaviour)).
- **Interrupt** stops the in-flight turn, including running **foreground** tool calls. Background tasks the turn started are **left running**: foreground only. The agent then gets **one** turn to **acknowledge the interrupt and ask for guidance** in its channel, and then waits.
- **Compact** uses `/compact`. **No automated prep turn**: the model gets no chance to act before the summary is written. Steering comes only from a fixed instruction text after `/compact`.
- **Clear** waits for a turn boundary: the button is **disabled unless the session is idle**. The next turn re-sends the session's starting prompt.

## Verified behaviour

These were checked against the real CLI on 2026-10-25, with stdin/stdout in stream-json, as cove-master runs it.

- **Interrupt:** `{"type":"control_request","request_id":"<id>","request":{"subtype":"interrupt"}}` written to stdin.
  - It is answered at once by `control_response` (`subtype: success`).
  - A running Bash tool is killed: `sleep 25; touch marker` interrupted at 3s left no marker and no process.
  - The model gets a rejected `tool_result` ("…STOP what you are doing and wait for the user…") plus `[Request interrupted by user for tool use]`.
  - The turn ends with `result` / `error_during_execution`.
  - The process stays alive. The next stdin user message continues the same conversation.
- **Compact:** a user message whose content is `/compact <instructions>`.
  - It emits `system/status` `compacting`, then `status` with `compact_result: success`, `system/init`, `system/compact_boundary` (with `pre_tokens`/`post_tokens`), two synthetic `user` messages (the summary and `<local-command-stdout>Compacted …`), and finally `result` / `success`. So turn-end tracking on `result` still holds.
  - The instructions reach `PreCompact` hooks as `custom_instructions`. A `SessionStart` hook with `source: compact` fires afterwards.
  - A fact the instructions said to keep survived (31.6k → 1.1k tokens).
- **Clear:** `/clear` over stdin emits `conversation_reset` and resets in-process. *Not used by this design* (see Clear below), but recorded.

## 1. Jam side

### Wire (`internal/jam/attach/proto/attach.proto`)

Add three `ControlDown` messages, one per command, matching the existing one-message-per-control style:

```proto
Interrupt interrupt = 9;
Compact   compact   = 10;
Clear     clear     = 11;

message Interrupt { string by = 1; }               // display name of who pressed it
message Compact   { string by = 1; string instructions = 2; }
message Clear     { string by = 1; }
```

The instruction text is sent from Jam, not hard-coded in the cove, so it can change without rebuilding studio images. For now it's a Go constant in Jam, not configurable:

> Keep: the current task and its goal; decisions made and why; work done and work still to do; open questions; the people involved and their latest requests; anything you were told to remember.

### Plumbing

- **`ControlSink`** (`internal/jam/supervisor.go`) and **`attach.Server`** gain `Interrupt`, `Compact` and `Clear`. They are best-effort and non-blocking, like `Wake`.
- **`userui.SessionOps`** gains one method, `Command(ctx, inst, kind, by)`, implemented in `cmd/at-jam/session_ops.go`.
  - If the studio is paused (supervisor `Idle`), it **resumes it first**, the way wake-on does before waking.
  - It then waits, bounded (≈30s, *my call*), for the stream to reconnect before sending. Wake-on deliberately sends on a later tick after `Resume`, because the old registration may be stale.
  - With no live Attach stream by then, it returns an error, and the handler answers 502 like the other actions.
- **`sessionAction`** (`internal/jam/userui/session.go`) gains the cases `interrupt`, `compact` and `clear`.
  - **Permission:** anyone `sessionFor` lets see the session. No owner or operator check.
  - **Activity check:** re-checked server-side against `inst.Activity` at press time. A command that's not allowed in the current activity gets **409**.
  - **Response:** `HX-Trigger: meChanged` (a header refresh). Not `HX-Refresh`: the session isn't going anywhere.
  - **Logging:** each press is logged, with the action and who pressed it, as today (`session action`).

### Buttons (`internal/jam/userui/templates/inbox.html`, session head)

| Button | Enabled when `Activity` is | Confirm |
|---|---|---|
| Interrupt | `running` | none (speed matters) |
| Compact | `running`, `holding` or `waiting` | none |
| Clear | `waiting` only | "Clear this session's conversation? The agent keeps its files and memory but forgets the conversation." |

Disabled buttons are rendered `disabled`, not hidden, so people learn they exist. The head already refreshes on activity changes; if it doesn't refresh often enough to keep the enabled state honest, the server-side 409 is the backstop.

## 2. cove-master side (`internal/covemaster`, `internal/agentrun`)

- **`covemaster.ControlKind`** gains `Interrupt`, `Compact` and `Clear`. `Control` gains `By` and `Instructions`, decoded in `controlFromPB`.
- **`Workload.Control`** hands them to the episode loop, the same way `Wake` uses the wake box. They must never block the Attach receive loop.

### Interrupt

- **Live turn in flight.** Write the interrupt `control_request` to the live episode's stdin. When that turn's `result` arrives, write one user message as the next turn:
  > *<by>* pressed **Interrupt** in the session header, which stopped your turn. Don't resume what you were doing. In your channel, acknowledge the interrupt with `send`, ask what they want instead, then end your turn.
- **No turn in flight** (a race with turn end). Log it and drop it. Don't wake the agent.
- **Wakes coalesced into that turn are kept, not dropped** *(my call, from review)*. Wake-on advances `WaitSeq` past messages it woke a running cove for, so a dropped wake could strand someone else's message. Their reasons are folded into the acknowledge turn, whose prompt also says: "New messages may be waiting. Read them, but don't act on any request until the person who interrupted you answers."
- **During a compaction** (a pending `/compact` running at the turn boundary), Interrupt is logged and ignored *(my call)*. The compaction finishes and the turn ends anyway.

### Compact

- **Live episode, turn in flight.** Mark compact pending. At the turn boundary, after `result` and before the existing "deliver coalesced wakes / close stdin" logic, write `/compact <instructions>` and wait for its `result`. Then carry on with the normal turn-end logic.
- **Live episode, between turns** (`holding`). Write it at once and wait for its `result`.
- **No live episode** (`waiting`, process exited).
  - Spawn a maintenance episode: the same argv with `--continue`, stdin `/compact <instructions>`. Close stdin after its `result`.
  - It does not report `Running` to Jam: the session stays `waiting`, so the idle ladder isn't reset. *Builder's call* if that turns out to fight the idle ladder.
- **Nothing to compact.** If no conversation exists yet (first episode never ran, or a Clear is pending), log it and do nothing.
- **Two compacts pending** collapse into one.

### Clear

- **Only honoured when no episode is live** (`waiting`). Otherwise log it and drop it; Jam's 409 should have stopped it. The one exception is a compact-only maintenance episode, which Jam can't see: a Clear arriving during one is queued until that episode exits *(my call)*.
- **Mechanism:** the next episode spawns **without `--continue`**, which gives a new conversation. Later episodes `--continue` that newest conversation as today. This is equivalent to `/clear` and needs no process to be running. The session context (CORE.md, role, kit), the workspace and memory files are untouched.
- **Durable across a cove-master restart:** record it as a file, `/agent-data/.cove-clear-pending`. It is consumed on the next episode's first agent reply, the same way the conversation marker is written, so a fresh episode that dies early doesn't quietly undo the Clear *(my call)*. Consuming it also removes the standing-session marker `/agent-data/.cove-conversation`.
- **The first episode after a Clear re-sends the session's starting prompt** (`cfg.Prompt`: the ticket, a standing session's job, a personal session's opener), then a line saying the conversation was cleared at *<by>*'s request, then the wake text. This mirrors the existing fresh-start fallback after a failed resume. Without it, a ticket session would forget its ticket and a standing session would lose its instructions.

### Events and record

- **Who pressed it.** Each command emits one cove event into the session event stream naming the command and `by`, so the Thinking tab and the admin timeline show it. *Builder's call* on the event's shape: reuse an existing synthetic/cove event type if one fits, else add one and render it in `sessionview`.
- **What Claude reports.** Claude's own events (`compact_boundary`, the interrupt `tool_result`) pass through as today.

## 3. Non-goals

- Chat slash commands (`/stop`, `/clear` typed into the channel).
- An automated "prepare to compact" turn.
- Configurable compact instructions (per role or per session).
- Permission tiers (owner only, operator only). This is "anyone, for now".
- Retracting or editing the mistyped message itself.
- Stopping background tasks on Interrupt.
- Auto-compact or auto-clear policies (timers, thresholds).

## 4. Anti-goals

These would make it a failure even if it technically works.

- **Interrupt that doesn't stop the foreground tool call already running.** A command left running is the harm this button exists to prevent.
- **Interrupt that ends the session, or loses the conversation.**
- **Compact or Clear that cuts off a turn halfway.**
- **Any of the three blocking the Attach stream, or wedging the episode loop.** For example, waiting forever for a `result` that never comes. Bound every wait.

## 5. Docs and tests

- **Docs:**
  - [intercom-ui.md](../../usage/jam/intercom-ui.md) (session view): the three buttons and when they're enabled.
  - [coves.md](../../usage/jam/coves.md) (cove-master): the new controls and the clear-pending file.
  - Remove the TODO.md item.
- **Tests:**
  - **Fake-claude tests in `agentrun`** for each command in each state:
    - interrupt mid-turn, then the acknowledge turn;
    - interrupt with no turn in flight;
    - compact mid-turn waits for `result`;
    - compact when idle spawns `--continue` with `/compact`;
    - clear, then the next spawn has no `--continue`;
    - clear-pending survives a Workload restart.
  - **`userui` handler tests:** the 409 matrix; anyone-who-can-see may press; 404 for unseen sessions.
  - **`controlFromPB`** decode.

## 6. Left to the builder

- The PR split. *Suggested:* (1) proto + cove-master + agentrun, (2) Jam plumbing + buttons + docs.
- The exact cove event shape for the record.
- A new `Harness` method to encode the interrupt `control_request`. `EncodeInput` only does user messages.
- The timeouts on each wait for a `result` (interrupt, compact).
- How the head keeps the enabled state fresh.
