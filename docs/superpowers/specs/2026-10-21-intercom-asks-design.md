# intercom: structured asks — design

**Status:** approved in brainstorming (2026-10-21); amended 2026-10-21: ask events ride in a general message **parts** envelope (shared with the coming attachments). Built: #421 (store, intercom, agent tools), #422 (/me).
**Builds on:** the channel log ([`2026-10-06-intercom-identity-and-channels-design.md`](2026-10-06-intercom-identity-and-channels-design.md) slice 2), session channels and wake-on (slice 3), escalation as call-in (slice 4, [`2026-10-07-intercom-slice4-escalation-call-in-design.md`](2026-10-07-intercom-slice4-escalation-call-in-design.md)).
**Scope decisions (2026-10-21):** relays are paused, so this targets `/me` only; collaboration roadmap order is asks → attachments → whiteboard.

## 1. What and why

Today an agent that needs a decision posts free text ("merge PR #12? reply yes or no") and calls `escalate`; the answer is free text too. An **ask** is a structured question: one of N options, an optional comment, and "Other…". It is an **open item** that stays open until answered or withdrawn, so asks can pile up, be discussed first, and be answered in any order. The agent never blocks on one.

## 2. Model

An ask belongs to one channel and one asking session. It has:

| Field | Meaning |
|---|---|
| `id` | the id of the squawk that posted it |
| `channel`, `asker` | where it lives; the session that asked |
| `question` | text (markdown) |
| `options` | 2–8 of `{id, label, detail?}`; ids unique, short (`[a-z0-9_-]{1,32}`) |
| `of` | optional user ids who may answer; empty = any person in the channel |
| `version` | starts at 1; each amend bumps it |
| `state` | `open` → `answered` or `withdrawn` (terminal) |
| `answer` | `{version, choice, comment, by, at}` once answered; `choice` is an option id or `other` (comment required) |

**Every change is a squawk in the channel log**, so ordering, inboxes, `read`, wake-on, SSE refresh, History and export all apply unchanged:

- A squawk gains **parts**: a list of typed parts, each either **inline** (`{type, data}` — small JSON or text carried in the message) or a **reference** (`{type, ref, name, size}` — a stored blob; reserved for attachments, refused until that spec lands). An ask event is one inline part of type `application/vnd.jam.ask+json` whose data is `{event: ask|amend|withdraw|answer, ask_id, …}`; the store recognizes that type and folds it. At most one ask part per squawk; unknown part types are refused for now. The envelope is general so attachments add blob storage and reference parts without another wire or schema change, and one message can carry an image and an ask. The squawk's `body` is a plain one-line rendering ("asked: merge PR #12? (a) yes (b) no", "alice chose **b** — ship it after lunch", "withdrawn: found it"), so an older agent, an export or History still reads sensibly. Content type stays as today.
- An `asks` read-model (one row per ask: `ask_id, channel, asker, of, version, state, answer, last_seq`) is written **in the same transaction** as the event's append. It is where the atomic checks run (Postgres: row `FOR UPDATE`; MemStore: its mutex) and where open asks are listed. It is rebuildable from the log; the log is the source of truth.
- A chat reply about an ask carries `re: ask_id` (`Squawk.ReplyTo`), rendered as a link chip.

## 3. Agent side (MCP tools, beside `send`)

- `ask(question, options, to?, of?)` → `{ask_id, version: 1, channel}` immediately. `to` defaults to the home channel, exactly like `send`, and obeys the same addressing rules. `of` takes `user:<name|id>` targets resolved to members of the channel's project.
- `amend(ask_id, question?, options?)` → `{version}`; only the asker, only while open.
- `withdraw(ask_id, reason?)`; only the asker, only while open.
- `asks(state?)` → the session's own asks with their state; for picking up after a restart.

An **answer reaches the agent through `read`** as an inbox entry carrying the answer as its ask part (`{event: answer, ask_id, version, choice, comment}`); the read wire lists a squawk's `parts` and whose `from` is the person; it wakes the session like any reply. Amend/withdraw events by the asker are not delivered back to it.

**Needs you / escalation:** `AskedForPerson` also holds while the session has an open ask a person may answer. That flag clears when its last open ask closes (not on wake). So the `/me` "Needs you" group and the escalation engine pick up open asks with no further change.

## 4. Person side (`/me`)

- **Card.** An ask renders in the timeline as a card: question, asker, one button per option (with detail), **Other…** (opens a required comment), and an optional comment field. Answered: collapses to "✓ alice chose **B** — comment". Withdrawn: "withdrawn: reason". Amended: the latest version with an "edited" mark; earlier versions viewable. Restricted (`of`): others see it read-only, "for brent".
- **Discuss.** Each card has **Discuss**: the composer starts a reply with `re:` that ask; linked replies show a chip that scrolls to the card.
- **Open asks.** A collapsible panel at the top of the conversation lists the channel's open asks. Each rail row shows its open-asks count; a channel with an open ask the viewer may answer is in **Needs you**.
- **Answering:** `POST /me/asks/{id}/answer` with `{version, choice, comment}`. **409** "this ask changed — review the new version" (stale version; the card refreshes) or "already answered by alice: B"; **403** for anyone not allowed. Everything refreshes over the existing SSE stream.

## 5. Rules

- Only the asking session amends or withdraws; only a **person** who is a current member of the channel (and in `of`, if set) answers, on a live channel. Sessions never answer in v1.
- **First valid answer wins.** An answer must name the current version.
- When the asking session ends (Gone), or the channel is archived, its open asks close as withdrawn ("session ended"). A restart keeps them (same session id).
- Limits: question ≤ 2 KiB, option label ≤ 200 B, detail ≤ 1 KiB, comment ≤ 4 KiB; all within the squawk body cap.

## 6. Data, upgrade, compatibility

- Postgres: `intercompg` migration adds a `parts` jsonb column on the channel log and the `asks` table. MemStore mirrors it.
- `at-jam export` carries no intercom state (backup.md), so nothing changes there.
- An older Jam ignores `parts` and shows the body text; rolling back loses only the structure.
- **Relays** (paused) skip ask events entirely.

## 7. Testing

- Store conformance (Mem + Postgres): conditional append — open-only, version match, first answer wins under concurrency — and the read-model fold/rebuild.
- Intercom rules: who may ask/amend/withdraw/answer, `of`, archived channels, session end closing asks.
- MCP tools round-trip through `/squawks/ask*`.
- `/me`: card states, the 409 paths, the open-asks panel and rail count, Discuss linking.
- "Needs you" and escalation with open asks; clearing on last close.

## 8. Docs

New leaf `docs/usage/jam/asks.md` (+ INDEX row); `intercom.md` (tools), `intercom-ui.md` (cards, panel), `escalation.md` (open asks count as asking).

## 9. Later (to ticket, not in v1)

Multi-select; short forms; tracked free-text asks; asks between agents (a session answering); relay rendering (Discord buttons, Linear) when relays resume; ask templates.
