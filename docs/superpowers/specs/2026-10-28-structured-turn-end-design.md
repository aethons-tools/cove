# jam: structured turn end — messages in the wake, the reply posted, the cursor committed

**Status:** design talked through with the user over the intercom (2026-10-28); the user asked for best-effort choices and an implementation PR (not merged) for review.
**Scope:** Jam-managed sessions (ticket, personal, standing): the wake-on engine and Wake wire, `agentrun`'s episode loop and Claude harness, the intercom MCP tools, the session context's agent guidance.
**Builds on:** the [turn-end lifecycle](2026-10-05-turn-end-lifecycle-design.md) (alarms, gates, idle timeout, `end`, `holding`), the intercom channel log and per-session inbox, the Compact session control.

## As built (2026-10-28)

The behavior below was built as written, with these changes of mechanism. The studio side was moved into the cove, where the code turned out to make it cheaper:

- **The studio does the turn-end work itself, over Jam's existing endpoints.** jam-studio-agent fetches the batch (`GET /squawks`), posts (`POST /squawks` with `auto`), and commits, ends, sets alarms and overrides idle. There is no TurnEnd or batch message on the attach proto, and no `PUT /turn-end` or `PendingTurnEnd` on Jam.
- **`turn_end` and `keep_*` reach the Workload over a local unix socket** (`/dev/shm/jam-agent.sock`). They don't go through Jam, and kept processes are not recorded on the Instance.
- **The capability is the context's turn protocol.** The cove asks for `GET /context?turn-protocol=2`, and the structured turn end turns on only when the bundle answers `turn_protocol: 2`. Guidance and behavior therefore always match.
- **Mute is cleared when the session next starts a turn,** and the cove unmutes at the next default turn end. It never blocks an owner's keep/release reply.
- **The auto-post marker is a part** (`application/vnd.jam.turn-end+json`), not a column. The "N more" count became "more are waiting".
- **A non-normal turn posts nothing and applies no directives** (§2 said its text was still posted).
- **Commit is tied to the prompt.** Each prompt has a generation, the turn answering it carries that generation, and only that turn commits the prompt's batch. Nothing is committed after a failed post, and a cut entry is never committed.
- **The duplicate check** compares only against `send`s since the session's previous auto-post.
- **A `process-exited` wake is raised locally** by the cove; it isn't sent by Jam.

The operator-facing description is [structured-turn-end.md](../../usage/jam/structured-turn-end.md).

## Why

The intercom asks an agent to do in tool calls what an agent loop does natively, and pays for it every turn:

- **Replies get lost.** Agents end a turn with a message for the person, as any agent does, and nobody sees it. The real reply has to go through `send`. An agent that forgets sends nothing, and one that remembers writes its reply twice.
- **Each wake costs extra round trips.** The wake says "a message may have arrived — use `read`". So the first model call of every turn exists only to fetch the message. Replying (`send`) and acknowledging (`commit`) cost more calls. A one-question, one-answer exchange takes about three model calls plus a duplicated reply, where one would do. Every call re-sends the whole context, which costs tokens and time.
- **Agents don't know how to end a turn in a studio.** The settings that govern waiting (alarms, `idle_timeout`, `end`) are scattered tools. The guidance says background processes die at turn end. So agents running a preview tell the user it will die when their turn ends, instead of keeping it up and waiting.

## Goals

1. **The wake brings the messages.** A woken session finds its pending messages in the prompt, up to a limit, so it can digest at its own pace and page the rest with `read`.
2. **The final text is the reply.** What the agent ends its turn with is posted for it, to its own channel by default.
3. **Handled is the default.** At turn end, the messages the agent was shown are committed for it, unless it says otherwise.
4. **Every turn end is structured** as three parts: the **message**, **commit or deny**, and **what happens next** (wait with its wake conditions, compact then wait, or end). Defaults fill whatever the agent doesn't say, so an ordinary turn costs no extra call.
5. **Processes can outlive a turn.** An agent can keep a process (a preview server) running while it waits, tend it, and be woken when it exits.

**Success:** a plain question-and-answer exchange costs one model call and posts exactly one reply. No agent tells its user that its preview will die at turn end.

## Non-goals

- Removing `send`, `read`, paging, `commit`, `alarm_*`, `idle_timeout` or `end`. They stay for when messaging is the task, or to set things mid-turn.
- A Jam-imposed lifetime for kept processes. The agent tends them; see §5.
- Harnesses other than Claude. The design stays harness-neutral at the seam (§2), but only `Claude{}` is built.
- Changing who a session hears, the loop breaker, or how `send` resolves addresses.

## Anti-goals (failure even if it works)

- **Noise.** Posting every turn's narration, or a "Done." after every tool-only turn, would make channels unreadable. Only the final text block is posted, and empty text posts nothing.
- **Lost messages.** A message must never be marked handled unless the agent was shown it in full, and that turn ended normally.
- **Chatter loops.** Auto-posting must not turn two sessions in one channel into an endless exchange beyond what the existing breaker already bounds.
- **A JSON-only agent.** The reply stays natural text. The structure rides beside it, not instead of it.

## Decisions (made with the user)

- **Messages are delivered in the wake prompt, with a limit.** Past the limit, the prompt says how many more are waiting and to `read` them.
- **Defaults plus an optional tool**, not a forced schema: the final text is the message, and a `turn_end` tool (§3) changes the rest only when the agent wants something non-default. The user rejected forcing every part every turn. Claude Code's `--json-schema` was considered and not chosen.
- **The reply goes to the session's own channel by default** (its home channel: the ticket channel for a ticket session). The agent *can* name another recipient.
- **Deny** means "I haven't handled these, give them to me again": the cursor does not move, so they are in the batch of the session's next wake, whatever wakes it. Deny does not by itself cause a wake. An agent that wants them back soon sets `wake_at` in the same `turn_end`. *(My call.)*
- **`compact` is a turn-end choice** alongside `wait` and `end`.
- **The agent tends a kept process.** It agrees with the user how long the process should run before asking to shut it down, and sets itself a wake for that. This is guidance, not machinery.

## 1. Delivery: the wake carries the messages

- **Every wake carries the batch**, whatever its reason (squawk, alarm, idle, process exit), whenever entries are pending past the session's commit cursor (`CommitSeq`). The batch is those entries, oldest first, capped at **20 entries or ~32 KiB of rendered text**, whichever comes first, plus the count beyond the cap and the batch's last id (`up_to`).
- **The batch is fetched at delivery, not at wake.** Just before the cove writes a wake prompt (into a live episode at a turn boundary, or as the first message of a new episode), it asks Jam for the batch over the attach stream (a new request/response pair). Because the batch is built from the commit cursor *at that moment*, a batch held while the agent was mid-turn never shows messages it already `read` and `commit`ted, and coalesced Wakes never carry stale or overlapping batches. If the fetch fails (Jam unreachable), the cove falls back to today's read-prompt for that wake.
- **Each entry renders** as a header line (sender, channel label and kind, time, id) and then its body. Attachments render as their refs with names, to `fetch` on demand. Ask answers and other `parts` render as one line each.
- **The prompt** = wake-reason lines (alarms, idle, as today) + the batch + one closing line:
  - nothing beyond the batch: "Reply by ending your turn; your final message is posted to your channel."
  - entries past the cap: "N more messages are waiting — use `read` to page them; only the ones above count as handled when you end your turn."
- The "a message may have arrived — use `read`" texts (`resumePrompt`, `residentResumePrompt`, `standingResumePrompt`) go away, except as the fallback for a Wake without a batch (§7).
- **What wakes a session doesn't change.** `WaitSeq` and the reply filter in `wakeon` still decide *whether* to wake. The batch decides *what is shown*, from the commit cursor. So notices and the session's own posts never wake it, but they can appear in a batch if they are past the cursor. *(Builder's call whether to filter the session's own posts out of the batch; recommended.)*
- The first prompt at raise is unchanged, except that a standing session's restart prompt also gets a batch if one is waiting.

## 2. The turn end: what the cove reports

- The harness gains a way to surface a turn's **final text**. For Claude it is the `result` event's `result` string (the last assistant text block of the turn). `Event` gets a `Text` field on `EventTurnEnd`. The interface shape is the builder's call, but it must stay harness-neutral.
- At each turn end, the cove sends Jam a **TurnEnd** message on the attach stream (new upstream proto message): the final text, the `up_to` of the batch shown in that turn (empty if none), and whether the turn ended normally.
  - **Normal** means a `result` with a success subtype.
  - An interrupted turn, an error result, or a process that died mid-turn is **not normal**. Its text, if any, is still posted, but nothing is committed.
- Maintenance turns (the idle-compaction episode, the Clear/Compact command turns) send no TurnEnd.
- Turns started by a background task's completion, or by an interrupt's acknowledgement, are ordinary turns: they post and commit like any other, with no batch, so nothing to commit.

## 3. The `turn_end` tool

`turn_end(...)` is an intercom MCP tool, brokered to `PUT /turn-end`. It sets directives for **the current turn's end**. Called twice in one turn, the last call wins. Every field is optional, and anything unset takes the default.

| Field | Default | Meaning |
|-------|---------|---------|
| `to` | home channel | where the final text is posted; same address grammar as `send` |
| `post` | `true` | `false` = post nothing this turn, even if there is final text |
| `commit` | `true` | `false` = deny: leave the cursor where it is |
| `next` | `wait` | `wait`, `compact` (then wait), or `end` (with `reason`) |
| `wake_on_message` | `true` | `false` = a new message doesn't wake me (alarms, idle and process exits still do) |
| `wake_at` | — | shorthand for a one-shot alarm: `{name?, at, note?}` |
| `idle_timeout` | role's | as `idle_timeout(…, scope: next)` |

- Jam stores the directives on the Instance as `PendingTurnEnd` and applies them when the TurnEnd arrives, then clears them.
- **Order of application:** post the text, then commit, then apply `next`/wake settings, then arm the idle deadline.
- `next: end` is `end(reason)`: the existing `SetEndRequested`. `next: compact` asks the cove, through the existing Compact control, to compact after this turn. The cove runs it as the idle compaction it already knows, before waiting.
- **`wake_on_message: false` is a scope cut.** It holds until the next turn starts. The builder places it in the wake-on engine's reply check. The user asked for "wake conditions" as a part; mute-until-alarm is the one new condition beyond today's (alarms, idle, and process exit, §5).
- **Keeping processes** is its own tool (§5), not a `turn_end` field. A kept process outlives many turns, so it isn't a per-turn setting.
- The standalone `alarm_set`, `idle_timeout` and `end` tools stay and keep working. `turn_end` is the one place guidance points to.

## 4. Jam: posting and committing

On a TurnEnd for session S:

1. **Post.** If `post` is on and the text is non-empty after trimming, Jam posts it as S, to `to` or the home channel.
   - It goes through the same path as `send` (`Intercom.Plan`/`Post`), so addressing, membership, relays, loop-breaker accounting and notifications behave exactly as if S had called `send`.
   - A refused `to` (not reachable) falls back to the home channel, with a one-line Jam notice to S in that channel saying why.
2. **Commit.** If the turn was normal, `commit` is on and `up_to` is set, Jam advances S's commit cursor to `up_to`, forward only (`AdvanceCommitCursor`). Entries after `up_to` stay pending.
3. Jam applies `next`, then the wake settings, then clears `PendingTurnEnd`.

**Duplicate replies.** An agent that also `send`s its reply to the same place, then ends with the same text, posts twice. Guidance (§6) says not to. As a backstop, Jam skips the auto-post when S's last post in that channel during this turn has the same trimmed body.

**Chatter loops.** A session woken by another session's post will usually reply now, and that reply wakes the first one. The existing breaker (`session-wake-limit`, default 8 per channel) still bounds this. **Auto-posts don't wake sessions.** A post made from a TurnEnd is marked as such (`auto` on the squawk). The wake-on reply filter skips auto-posts by other *sessions*, the way it already skips notices. People are notified as usual. A session that wants another session to act uses `send`, which wakes it as today. So a reply never bounces between sessions, and the breaker stays the guard for explicit `send` loops. *(My call.)*

**Ticket sessions** post by default, like any session: their home channel is the ticket's conversation, so the final text becomes a tracker comment. Guidance for a ticket session adds: "your final text is a comment on your ticket — end with no text when your `report` already says it". *(My call.)*

## 5. Keeping processes alive

- New intercom tools: `keep_start(name, command, workdir?)`, `keep_stop(name)`, `keep_list()`.
- **The cove's agent process (jam-studio-agent, which outlives every claude run) starts and owns the process**: `sh -c` in its own process group, stdout and stderr to `/agent-data/keep/<name>.log` (rotated, size-capped), up to 8 per session.
  - It is **not** a child of claude, so it survives turn ends and episode exits.
  - It stops with the session (teardown, `end`) or on `keep_stop`. Stopping is SIGTERM to the group, then SIGKILL after 10s.
- **Wire path.** The MCP server has no channel to the Workload today. The tools broker through Jam (`PUT/DELETE /keeps/{name}`) to a new attach control, the same way Compact reaches the cove. Jam records the names on the Instance so `keep_list` and the session view can show them. *(Builder's call: a local socket from the MCP server to jam-studio-agent is acceptable instead, if it keeps the Instance record.)*
- **A kept process exiting** wakes the session with a new reason, `process-exited`, rendered as "Kept process `<name>` exited (status N); last lines: …" with the last 20 log lines. This is the "a process finishing" wake condition.
- **Pausing.** A waiting session with kept processes is still paused at `warm-timeout`, as today, since a preview request resumes a paused studio. A kept process that must not be frozen is the agent's problem to mention. *(Builder's call; record it in the docs.)*
- **Restart.** Kept processes do not survive a cove rebuild or re-raise. After a restart, the restart prompt lists the kept processes that were lost.
- The "Background processes you start die when the turn ends" line in the boilerplate is replaced (§6).

## 6. Agent guidance

Rewrite the turn and intercom lines of the session-context boilerplate (`internal/jam/sessionctx/boilerplate.go`) and the matching docs:

- "Messages to you arrive in your prompt. **End your turn with your reply**: it is posted to your channel. End with no text if there's nothing to say. Don't also `send` it."
- "Handled is automatic: ending your turn marks the messages you were shown as handled. Use `turn_end(commit: false)` to see them again. Use `turn_end` to post elsewhere, stay quiet, compact, end, or change what wakes you."
- "Turns: each turn is one `claude -p` run; ending your turn is how you wait. Shell background jobs die with the turn. To keep something running (a preview), use `keep_start`: it survives your turn ends and wakes you if it exits. Agree with the person how long it should run, and set a `wake_at` to ask them then."
- MCP tool descriptions for `read`, `commit` and `send` say what they are now for: paging past the batch, catching up mid-turn, and messaging elsewhere.

## 7. Compatibility and rollout

- **Version skew.** A cove older than this change ignores the Wake's batch field and sends no TurnEnd. So it keeps today's behavior: read-prompt, explicit `send`/`commit`. Jam must not depend on TurnEnd arriving.
- The cove advertises the capability on attach. *(The proto already carries a hello/status; builder's call where.)* Jam posts TurnEnds and serves batches only for capable coves.
- **The guidance follows the capability.** Old guidance on a new cove means duplicate replies; new guidance on an old cove means lost replies. So `SessionFacts` gains the capability, and the boilerplate picks the old or new turn and intercom lines from it. A cove that attaches with a different capability than its context was built for gets a context refresh (the existing context-changed path). *(My call.)*
- `PendingTurnEnd` on an incapable cove: `turn_end` returns an error saying the studio needs upgrading, rather than silently doing nothing.
- Docs owned elsewhere get updated in the same change: `docs/usage/jam/turn-end.md` (wake reasons table, the new tools, the TurnEnd), `intercom.md` (delivery and commit), `session-context.md`, `previews.md` (keep).

## 8. Suggested slicing (for the plan)

1. TurnEnd plumbing: the harness surfaces the final text, the cove sends TurnEnd, Jam can post it. **Dark**: Jam ignores TurnEnd (logs it) until slice 2, since today's guidance still tells agents to `send`, and posting would duplicate every reply.
2. The batch fetched at delivery, its rendering, posting turned on, auto-commit from `up_to`, the capability-gated guidance. **This slice delivers the token saving.**
3. The `turn_end` tool: deny, `to`, `post`, `next`, `wake_on_message`, `wake_at`, `idle_timeout`.
4. Keep: tools, cove ownership, the `process-exited` wake.

## Left to the builder

- Exact proto field and message names. Where the capability flag rides.
- The render format of a batch entry, within §1's constraints.
- The duplicate-reply check's exact window.
- Test strategy. A fake harness exists for `agentrun`, so the TurnEnd path and coalescing are unit-testable. Jam's post/commit is testable against the in-memory store.

## My calls (not discussed with the user)

- The batch cap: 20 entries / about 32 KiB.
- Only the `result` text is posted. Interrupted or errored turns post their text but never commit.
- `turn_end` is per turn, last call wins. Keep is separate tools, not a `turn_end` field.
- Kept processes are owned by jam-studio-agent, capped at 8, with `process-exited` wakes and no Jam lifetime limit.
- `wake_on_message: false` lasts only until the next turn.
- Deny never wakes by itself; denied messages ride the next wake of any kind.
- Every wake carries the batch, fetched by the cove at delivery time, not built at wake time.
- Slice 1 ships dark; the guidance switches per cove capability.
- Auto-posts never wake other sessions; `send` still does.
- Ticket sessions post by default, with a guidance line against restating `report`.
- A refused `to` falls back to the home channel with a notice.
- The duplicate-reply backstop.
- Waiting sessions with kept processes still pause at warm-timeout.
