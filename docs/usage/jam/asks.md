---
summary: Structured asks — a session asks people a one-of-N question (plus Other with a comment) with the `ask` tool; a person answers it; the answer reaches the session through `read`. Asks stay open until answered or withdrawn, can pile up, and put the session under Needs you.
read_when: You want an agent to ask people for a decision with answers to pick from (approve, choose A/B/C) instead of free text, you're answering or debugging an ask, or you're wiring the `/squawks/ask*` endpoints.
owns: the ask model (open → answered | withdrawn, versions), the `ask`/`amend`/`withdraw`/`asks` tools and their `/squawks` routes, who may ask and answer, the answer as a read entry's part, limits, and how open asks count as asking for a person. Does NOT own the `/me` rendering — see intercom-ui.md.
prereqs: intercom.md for channels, read and wake-on; comms-addressing.md for `to`; escalation.md for what asking for a person sets off
tier: leaf
updated: 2026-10-21
---

# Asks

An **ask** is a question with 2–8 answers to pick from, plus **Other…** with a
comment, that a session posts into a channel. It is an **open item**: it stays
open until a person answers it or the session withdraws it. A session can have
several open at once, people can discuss one in the channel before answering,
and they can answer them in any order. The session never blocks on one.

## The tools

- **`ask(question, options, to?, of?)`** — posts the ask into `to` (any
  target `send` takes, under the same [addressing](comms-addressing.md); omitted:
  the session's own channel) and returns `{ask_id, version: 1, channel}` at once.
  `options` are `{id, label, detail?}`; `of` (`user:<name>`) limits who may answer,
  else anyone in the channel. `POST /squawks/ask`.
- **`amend(ask_id, question?, options?)`** — rewords an open ask and bumps its
  version (`POST /squawks/ask/amend` → `{version}`). An answer to an older version
  is refused, so nobody approves wording they didn't see.
- **`withdraw(ask_id, reason?)`** — closes an open ask unanswered
  (`POST /squawks/ask/withdraw` → `204`).
- **`asks(state?)`** — the session's asks and their state, with the answer
  (`GET /squawks/asks?state=open|answered|withdrawn`) — for picking up after a
  restart.

Refusals: `400` a malformed ask; `403` not the asker, or a `to` beyond its
addressing; `404` no such ask, or an `of` person who isn't in the project;
`409` the ask is closed.

## The answer

An answer is a post in the ask's channel **by the person**, so it reaches the
session through `read` and wakes it like any reply. Its entry carries the
answer in `parts`: `{type: "application/vnd.jam.ask+json", data: {event:
"answer", ask_id, version, choice, comment}}`; `choice` is an option id or
`other`. Its `body` is a one-line rendering ("alice chose Yes on: Merge PR
#12? — after CI"), as is every ask event's, so an older reader still follows.

## Rules

- Only the asking session amends or withdraws. Only a **person** answers — a
  current member of the ask's live channel, and one of `of` if it names anyone.
  Sessions never answer.
- **First answer wins**; a later one is refused (`409`, "already answered").
- When the asking session ends, its open asks close as withdrawn ("session
  ended"); an ask whose channel is archived under it (a room removed) closes
  as "channel archived" within a minute. A restart keeps them.
- Limits: question ≤ 2 KiB; option id `[a-z0-9_-]{1,32}` (not `other`), label ≤
  200 B, detail ≤ 1 KiB; comment ≤ 4 KiB; reason ≤ 1 KiB.
- The Discord and Linear relays skip asks: they are answered in `/me` — see
  [intercom-ui.md](intercom-ui.md#asks) for the card, Discuss and the Open
  asks panel.

## Asking for a person

While a session has an open ask in a live channel it counts as **asking for a
person**, like `needs-input` or `escalate`: `/me` shows it under **Needs you**,
and if it waits, [escalation](escalation.md) calls people in. That lasts until
its last open ask closes, not until it is next woken.
