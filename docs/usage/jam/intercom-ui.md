---
summary: The participant intercom inbox — a two-pane, server-rendered (html/template + htmx) web UI under `/me` where a person reads and replies to their intercom channels (and their legacy History), grouped by attention (Needs you → Sessions → Channels), with a New Message picker, per-(participant,channel) unread, and a live status strip for the sessions in each conversation. It reads the channel read-model and writes through the `/me/send` endpoint; it refreshes live over an SSE push (`GET /me/events`), with a 30s poll as fallback.
read_when: You want a project member to read/reply to their studios and channels in a browser (not via Discord/Linear relays), or you're operating/extending the `/me` inbox — its routes, the live push and fallback poll, the mark-read cursor, the session status strip, the composer and copy controls, or its wiring to the send path.
owns: the `/me` participant inbox UI — its two-pane rendering, the rail attention grouping, the conversation pane, the New Message picker, the session status strip (`GET /me/presence`) and how it maps session state to a status, the composer (keys, saved reply, draft stack) and copy controls, the unread mark-read (`POST /me/read`), the membership controls (`POST /me/join|leave|call-in`), the live push (`GET /me/events`) and fallback poll, and how it wires to the channel read-model and `/me/send`
prereqs: session-events.md for the event stream the status strip derives from; ui.md for the `/me` participant gate (OIDC-always, no loopback trust) and the operator/participant boundary; comms-addressing.md for the target space; intercom.md for the squawk Log + wake-on; coves.md for the session phases and activity the "Needs you" treatment reflects; escalation.md for asking for a person
tier: leaf
updated: 2026-10-08
---

# The participant intercom inbox (`/me`)

A **project member** reads and replies to their intercom channels in a two-pane web
inbox served under `/me`, rather than through the Discord/Linear relays. It is the
human analog of a studio agent's `/squawks` tools: the same durable squawk Log,
seen from the human's side. It is served by `internal/jam/meui` (mirroring
`adminui`'s embedded-`html/template` + htmx mechanism) and mounted behind the
**participant gate** — see [ui.md](ui.md#the-participant-intercom-me) for the gate
(OIDC-always, no loopback trust) and the operator/participant boundary.

## What it shows

- **Left rail — channels grouped by attention:** `Needs you` (a conversation
  with a session that **asked for a person** — a `needs-input` report or
  the `escalate` tool, see [escalation.md](escalation.md) — or is blocked — live, or
  paused while it waits: the attention signal), then `Sessions` (conversations with a live, starting or
  paused session, including one that is merely idle), then `Channels`: every [channel](intercom.md#enabling-it) the person
  is in or has messages in, that they may see — chats (labelled with the others
  in them), tickets and rooms — across their projects. Leaving a project takes
  its channels away at once. Each row shows the label, a presence pin, and an
  **unread** badge. Last, **History (before the upgrade)**: the conversations of
  the frozen legacy log, read-only (no composer, all read).
- **Right pane — the open conversation:** the selected channel's messages (the
  viewer's own on one side), a **"Needs you"** chip when a session in it
  asked for a person or is blocked, and a composer. Each message renders per its
  [content type](intercom.md#content-type-markdown-or-plain-text): markdown as
  sanitized HTML, plain text as-is with its whitespace. A wide code block
  scrolls sideways inside its bubble.
- **Copy controls:** hovering a message (or tabbing to its controls) shows
  **Text** and **Markdown** beside the sender. Text copies what the bubble shows;
  Markdown copies the body exactly as sent. Each code block has a copy icon in
  its corner that copies just the code. On touch screens they're always shown.
- **Session status strip:** under the messages, one line per session taking part
  in the conversation, like a typing indicator — see
  [Session status](#session-status).
- **View selector (top bar):** **Rendered** (the default) or **Raw**, which shows
  every message as its body text as sent, in monospace. Raw is handy for copying
  markdown. The choice is remembered per browser. The composers always use a
  monospace font.
- **New Message:** a picker of who to start a conversation with in the person's
  projects: other members (a chat with them), the live sessions they may reach
  (a standing or ticket session; a personal session only if they're already in
  its channel), live sessions' tickets, and rooms. A message to a session goes to
  its home channel, which the person joins.

**Writes are same-origin only.** Every `/me` POST (send, mark-read, join,
leave, call-in) is refused (`403`) unless its `Origin` (or `Referer`) is the
page's own host or one of [`ui-origins`](serve.md) — the same check as `/ui`
writes — so another site, a sibling subdomain included, can't post as you.

## Membership controls

The conversation header offers **Join** (a channel the person can see but isn't
in), **Leave** (one they're in; never a chat) and **Call in…** (a picker of the
project's people, and on ticket and session channels its live sessions, who
aren't in it yet). They post to `POST /me/join`, `/me/leave` and `/me/call-in`
(`channel`, and `who` = `user:<id>`/`session:<id>`); on success (`204`) the page
reloads. The rules are the intercom's ([membership](intercom.md#channel-membership)).

## Sending, unread, and refresh

- **Sending** posts to [`POST /me/send`](intercom.md) (the participant send path):
  the composer sends `{to, body}` — `to` is the open channel's id, or for a new
  conversation `user:<id>` / `session:<id>` — and, on success, refreshes the pane
  and rail. The intercom decides whether the person may post there (a chat
  they're in; a ticket, room or session channel of a project they're a member
  of — a personal session's only if they're in it: they join it)
  and who hears it.
  Messages are markdown by default. Tick the composer's **Plain text** box to
  send one as `text/plain`, shown literally; it applies to that message only. A
  reply addressed to a waiting studio **wakes it** exactly as a relayed reply does
  (see [intercom.md](intercom.md#waiting-for-a-reply-wake-on)).
- **Composer keys:** **⇧↵** (Shift+Enter) sends — the Send button shows it —
  and inserts nothing; plain Enter always inserts a newline. A message
  is trimmed at both ends before it is sent.
- **Saved reply:** what you type is kept per conversation in the tab's session
  storage, so switching conversations (a full page load) and coming back
  restores it. Emptying the box or a successful send clears it. It never leaves
  the browser.
  **Cmd-Shift-V** (Ctrl-Shift-V off the Mac) pastes as a fenced code block at
  the cursor, on its own lines. The fence is longer than any backtick run in
  the pasted text, and Cmd-Z undoes the paste. It takes over the browser's own
  "paste as plain text" shortcut, which is a real paste, so no browser asks for
  clipboard permission. On a Mac, Cmd-Alt-Shift-V (Safari's "Paste and Match
  Style") works too, if a browser doesn't bind Cmd-Shift-V.
- **Draft stack:**
  - **Cmd-Down** (Ctrl-Down off the Mac) pushes the draft you're writing onto a
    per-conversation stack and clears the box, so you can write and send another
    message first.
  - **Sending pops the top draft back**, with the cursor where you left it.
  - **Cmd-Up**, or the "↩ N stacked drafts" chip above the box, pops by hand.
  - A pop never overwrites text already in the box.
  - Stacks live in the browser tab's session storage: they survive switching
    conversations and reloads, and are never sent to Jam.
- **Unread** is a per-(participant, channel) cursor. Opening a channel marks it
  read via `POST /me/read`, which advances the person's read cursor on it
  (forward only) to its latest append sequence; the badge clears on the next
  refresh. Unread is the person's deliveries above it.
- **Refresh is live:** the page holds an SSE stream (`GET /me/events`, behind
  the same gate). Each squawk Log append sends a payload-free `changed` event,
  and the rail and the open conversation's message list re-fetch their fragments
  (`GET /me/rail`, `GET /me/stream`). Those fragment routes are the only place
  message content is served. A **30s poll** is the fallback, for state that
  appends nothing (a session going Waiting) and for a dropped stream. A reconnect
  also refreshes, to catch up on anything missed. Fragments follow the same
  partial/full convention `adminui` uses (branch on the `HX-Request` header).
  The pane header and composer never refresh, so a half-typed reply survives.
  The message list **doesn't refresh while text in it is selected**, so a squawk
  can be selected and copied; a push held by a selection runs once it clears.
  The push is in-process: an `intercom.Notifier` wraps serve's one shared
  channel-log handle, so every writer (agent send, relay ingress, `/me/send`,
  Jam's notices) fires it. That's correct while serve is the Log's sole writer. Without a
  configured Log, `/me/events` answers `204` and the page just polls.
  A second payload-free event, `presence`, fires when a session's status
  changes (at most one per 250 ms per stream; a change inside that gap is sent
  when it ends) and refetches just the status strip (`GET /me/presence`).
  `/me/events` answers `204` only when neither source is configured.
  A conversation **opens scrolled to its latest message**. If it is scrolled to
  the bottom when a refresh brings a new squawk, it scrolls so the new message
  is fully in view; scrolled up into history, the view stays put. **Send always
  scrolls to the bottom**, wherever you were.

## Session status

Each session that is a member of the open conversation gets one line:
**`<name> is <status>`** (or **`<name> needs you`**). The name is the session's declared name, else its
actor id, as in the New Message picker. Busy states show animated dots; idle,
paused, and done are dimmed; needs you and blocked use the attention colour. A
session whose turn ended is **idle**, not waiting on anyone: it needs you only
once it asks.

| Status | When |
|--------|------|
| starting | the session is raising |
| paused | it is idled |
| needs you | it is waiting (live or paused) and asked for a person since it was last woken (`needs-input` report or `escalate`); this wins over paused |
| idle | it is waiting without having asked (its turn ended) |
| blocked / done | its reported activity says so (this and the two above win over its events) |
| thinking | its latest event began a turn, was a thinking block, or was a tool result |
| running **\<Tool\>** | its latest event was a tool call |
| writing | its latest event was assistant text |
| idle | its turn finished (`result`) |
| working | live, but no event seen since Jam started |

Terminating, lost, and gone sessions are left out. The status comes from the
session's phase and activity plus its [session events](session-events.md),
summarized server-side by an in-memory tracker that every published event
passes through (`sessionevents.Presence`, fed via `Hub.Observe`). Only the
status and a **tool name** ever reach `/me` — never event content (see
[Sensitivity](session-events.md#sensitivity)). The tracker is in memory, so after
a restart a running session reads "working" until its next event.

## Enabling it

The inbox is mounted whenever the participant gate is (browser OIDC login
configured — see [ui.md](ui.md#the-participant-intercom-me)). Its conversations
come from the channel log, which is always available.

## Not yet

- **Per-participant push filtering:** today every `changed` and `presence` ping
  reaches every connected participant, who then refetch only what they may see.
- **Multi-process Jam:** the push is in-process; several serve processes over one
  Postgres log would need `LISTEN/NOTIFY`.
- **A project-grouping toggle** (the read-model already carries the tags).
