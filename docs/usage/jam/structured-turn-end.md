---
summary: The structured turn end — a woken session finds its pending messages in its prompt, its final text is posted for it and what it was shown is committed for it; the optional `turn_end` tool changes those defaults (deny, recipient, quiet, compact, end, mute, wake time, idle timeout), and `keep_*` keeps processes running across turns. Covers the turn-protocol switch, `PUT /mute`, the auto-post marker and the agent socket.
read_when: You want to know what a woken agent is shown, why a session's reply was (or wasn't) posted or its messages committed, how an agent keeps a preview server running between turns, or you are changing the turn-end defaults, `turn_end`/`keep_*`, or how a studio and Jam agree on the turn protocol.
owns: the structured turn end — inbox batches in the wake prompt (caps, rendering, fallback), auto-posting the final text and auto-commit, the `turn_end` tool and its directives, `PUT /mute`, the auto-post marker part and wake-on's skip of it, the duplicate-reply skip, the turn-protocol switch (`GET /context?turn-protocol=2`, `Bundle.turn_protocol`), the agent socket, kept processes (`keep_start`/`keep_stop`/`keep_list`) and the `process-exited` wake. Does NOT own wake-on's other rules (intercom.md), alarms/idle/`end` (turn-end.md) or the context compile (session-context.md).
prereqs: intercom.md for the inbox, commit cursor and wake-on; turn-end.md for what a turn end is and the wake reasons
tier: leaf
updated: 2026-10-28
---

# The structured turn end

An agent loop ends each turn with a message. Under the structured turn end, Jam uses that instead of making the agent do its messaging in tool calls:

- **The wake brings the messages.** The agent's prompt carries its pending messages.
- **The final text is the reply.** It is posted for the agent.
- **Handled is the default.** What the agent was shown is committed for it.

A plain question and answer then costs one model call, not a `read` → answer → `send` → `commit` round. The design is in the [spec](../../superpowers/specs/2026-10-28-structured-turn-end-design.md).

## When it is on

It is on per studio, and only when both sides speak it:

- **The studio asks for it.** The studio's jam-studio-agent fetches its context with `GET /context?turn-protocol=2`.
- **Jam confirms it.** A Jam that knows the protocol compiles the structured guidance and answers with `"turn_protocol": 2` in the bundle. It also records the protocol on the Instance, so setup inspection recompiles the same context.
- **Otherwise it stays off.** A studio without a live context refresh, or a Jam that answers without `turn_protocol`, keeps the legacy behavior: the read prompt, explicit `send`/`commit`, and the legacy guidance. This prevents an old Jam pairing with a new studio from duplicating replies (or the reverse, losing them).

## Delivery: messages in the wake

- **Every wake carries a batch.** Whatever woke the session (a squawk, an alarm, the idle timeout, a kept process), its prompt carries its pending inbox entries:
  - the entries are past the commit cursor ([intercom.md](intercom.md#the-inbox-as-a-durable-queue)), oldest first;
  - the batch is capped at **20 entries or 32 KiB** of rendered text.
- **What an entry looks like.** Each entry is a header line, `From <author> in <channel> (<kind>) at <time> — id <id>`, followed by its body. Each attachment adds an `[attachment <name> ref <ref>]` line.
- **Oversized entries.** An entry alone over the cap is cut, with a marker telling the agent to `read` that id for the rest. A cut entry isn't committed for the agent: it commits it itself once handled, and until then auto-commit doesn't pass it.
- **The closing line** says either "Reply by ending your turn…" or, when entries were left out, "More messages are waiting — use `read`…".
- **Fetched just before writing.** The batch is fetched (`GET /squawks`) just before the prompt is written into the episode. Because it starts from the commit cursor at that moment, it never re-shows messages the agent already read and committed itself.
- **Fallback.** If the fetch fails, that wake gets the legacy read prompt.
- **Interrupts and restarts.** The interrupt acknowledgement and a standing session's restart prompt carry the batch too.

## At turn end: post, commit, apply

The studio reads each turn's `result` event and then acts in this order. It finishes before it writes anything else into the episode.

1. **Post.** It posts the trimmed final text as the session, marked as a turn end's (`POST /squawks` with `auto: true`).
   - **Where:** to the home channel unless `turn_end` named a recipient. If that recipient is refused, the text goes to the home channel and the next prompt says why.
   - **When nothing is posted:** empty text posts nothing.
2. **Commit.** It commits the batch the turn's prompt showed (`POST /squawks/commit`), up to the last entry shown in full.
   - **Only that turn's batch.** Each prompt is numbered, and only the turn that answers it commits its batch. A turn a background task started commits nothing.
   - **Not after a failed post.** If the post failed, nothing is committed: the messages come back with a note saying why.
3. **Apply the turn's directives** (below).

A turn that didn't end normally posts, commits and applies nothing: an interrupt, an error result, or a process that died. Its directives are dropped, as are those of a turn that never reached its end. A `/compact` turn (a press or `turn_end`) posts and commits nothing either.

**Auto-posts don't wake sessions.** An auto-post carries the part `application/vnd.jam.turn-end+json`. Wake-on skips it when its author is a session ([intercom.md](intercom.md#waiting-for-a-reply-wake-on)), and it never counts toward the loop breaker. People are notified as usual. A session that wants another session to act uses `send`.

**Duplicate replies.** Jam skips an auto-post whose trimmed body equals a `send` by the same session in the same channel since its previous auto-post, within 10 minutes. It answers `{"skipped": true}`, which covers an agent that also `send`s its reply. The same short answer at two turn ends is two replies, and both are posted.

## `turn_end`

`turn_end` is an intercom tool, called before the final message, only when the defaults don't fit. The last call in a turn wins, and it applies to that turn only.

| Field | Default | Effect |
|-------|---------|--------|
| `to` | home channel | where the final text is posted (`send`'s targets) |
| `post` | true | false: post nothing |
| `commit` | true | false (deny): the shown messages stay pending and come back with the next wake. Deny alone doesn't wake. |
| `next` | `wait` | `compact` (compact, then wait) or `end` (with `reason`: the `end` tool's effect) |
| `wake_on_message` | true | false: `PUT /mute`. New messages don't wake the session until it next runs. Alarms, the idle timeout and kept processes still do, and an owner's keep/release reply to a nag still acts. The next turn end with the default unmutes. |
| `wake_at` | — | `{at, note?, name?}`: a one-shot alarm (`PUT /alarms/<name>`, default name `turn-end`) |
| `idle_timeout` | role's | as `idle_timeout(…, scope: next)` |

`alarm_set`, `idle_timeout` and `end` still work mid-turn ([turn-end.md](turn-end.md)).

## Kept processes

Shell background jobs die with the agent's turn. `keep_start(name, command, workdir?)` starts a process that keeps running instead, owned by jam-studio-agent itself (the long-lived Workload), not by the agent CLI.

- **How it runs.** It runs as `sh -c` in its own process group, with the agent's environment, and starts in the workspace unless `workdir` says otherwise.
- **Output** goes to `/agent-data/keep/<name>.log`. The log is capped at 1 MiB, with one `.1` rotation.
- **Limits.** At most 8 at a time. A name is `[a-z0-9][a-z0-9_-]{0,31}`.
- **Stopping one.** `keep_stop(name)` sends SIGTERM to the group, then SIGKILL after 10 s. `keep_list()` lists them.
- **When it exits on its own,** the session is woken with the reason `process-exited`, rendered as ``Kept process `<name>` exited (status N); last lines:`` plus up to 20 log lines.
- **They stop with the session** (teardown, `end`).
- **When the shell exits,** whatever it left in its process group is killed with it.
- **After a jam-studio-agent restart,** whatever its predecessor kept is stopped (found through `/agent-data/keep/registry.json`), and the first prompt names those processes. A process is stopped only when its boot id and start time match the registry, so a reused pid is never signalled.
- **Pausing.** A waiting session with kept processes is still paused at `warm-timeout`, which freezes them. A preview request resumes the studio ([previews.md](previews.md)).

The agent tends what it keeps: its guidance tells it to agree with the person how long a preview should run, and to set a `wake_at` to ask them then. Jam imposes no lifetime.

## The agent socket

`turn_end` and `keep_*` reach the Workload over a unix socket at `/dev/shm/jam-agent.sock` (mode 0600). It is served by jam-studio-agent and used by its MCP subcommand (`jam-studio-agent mcp`). Its routes are:

- `PUT /turn-end`;
- `POST /keeps`, `DELETE /keeps/{name}` and `GET /keeps`.

`turn_end` answers 409 while the structured turn end is off. Against a studio with no socket, either tool fails with "upgrade the studio".
