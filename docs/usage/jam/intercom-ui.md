---
summary: The participant intercom inbox — a two-pane, server-rendered (html/template + htmx) web UI under `/me` where a roster human reads and replies to their intercom channels, grouped by attention (Waiting on you → Active → Channels), with a New Message picker and per-(participant,channel) unread. It reads the channel read-model and writes through the `/me/send` endpoint; refresh is a 3s htmx poll this slice.
read_when: You want a roster human to read/reply to their studios and channels in a browser (not via Discord/Linear relays), or you're operating/extending the `/me` inbox — its routes, the 3s poll, the mark-read cursor, or its wiring to the send path.
owns: the `/me` participant inbox UI — its two-pane rendering, the rail attention grouping, the conversation pane, the New Message picker, the unread mark-read (`POST /me/read`), the 3s htmx poll, and how it wires to the channel read-model and `/me/send`
prereqs: ui.md for the `/me` participant gate (OIDC-always, no loopback trust) and the operator/participant boundary; comms-addressing.md for the target space; intercom.md for the squawk Log + wake-on; coves.md for the studio phases the "Waiting on you" treatment reflects
tier: leaf
updated: 2026-10-01
---

# The participant intercom inbox (`/me`)

A **roster human** reads and replies to their intercom channels in a two-pane web
inbox served under `/me`, rather than through the Discord/Linear relays. It is the
human analog of a studio agent's `/squawks` tools: the same durable squawk Log,
seen from the human's side. It is served by `internal/jam/meui` (mirroring
`adminui`'s embedded-`html/template` + htmx mechanism) and mounted behind the
**participant gate** — see [ui.md](ui.md#the-participant-intercom-me) for the gate
(OIDC-always, no loopback trust) and the operator/participant boundary.

## What it shows

- **Left rail — channels grouped by attention:** `Waiting on you` (a studio
  waiting/idled on you — the attention signal), then `Active`, then `Channels`
  (named channels and human DMs), across all the projects the participant belongs
  to. Each row shows the label, a presence pin, and an **unread** badge. The
  grouping and rows come from the channel read-model
  ([comms-addressing.md](comms-addressing.md) for the target space); the same data
  also supports a project grouping (a later toggle).
- **Right pane — the open conversation:** the selected channel's messages (the
  viewer's own on one side), a **"Waiting on you"** chip when a studio is
  soliciting a reply, and a composer.
- **New Message:** a picker of the currently-active recipients (humans, sessions,
  studios, named channels) to start a conversation with.

## Sending, unread, and refresh

- **Sending** posts to [`POST /me/send`](intercom.md) (the participant send path):
  the composer sends `{to, body}` and, on success, refreshes the pane and rail. A
  reply addressed to a waiting studio **wakes it** exactly as a relayed reply does
  (see [intercom.md](intercom.md#waiting-for-a-reply-wake-on)).
- **Composer keys:** Enter inserts a newline; a second consecutive Enter sends
  (the extra newline is dropped). Shift+Enter always inserts a newline and never
  arms a send, so deliberate blank lines are possible.
- **Unread** is a per-(participant, channel) cursor. Opening a channel marks it
  read via `POST /me/read`, which advances the cursor to the channel's latest
  append sequence; the badge clears on the next refresh.
- **Refresh is a 3s htmx poll** this slice: the rail and the open conversation's
  message list each re-fetch their fragment (`GET /me/rail`, `GET /me/stream`)
  every 3 seconds — the same partial/full convention `adminui` uses (branch on the
  `HX-Request` header). The pane header and composer never poll, so a half-typed
  reply survives. The message-list poll **pauses while text in it is selected**,
  so a squawk can be selected and copied; it resumes once the selection clears.
  A live push (SSE) is a later slice.

## Enabling it

The inbox is mounted whenever the participant gate is (browser OIDC login
configured — see [ui.md](ui.md#the-participant-intercom-me)). Its conversations
come from the intercom Log: with no `intercom-log:` configured the inbox renders
empty (there is nothing to read), and sending returns `503`.

## Not yet

- **Live push (SSE)** instead of the 3s poll.
- **A project-grouping toggle** (the read-model already carries the tags).
- **Access-graph gating of participant sends** (open addressing to start; the
  graph still gates agent `/squawks` sends).
