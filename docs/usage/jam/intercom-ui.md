---
summary: The participant intercom inbox — a two-pane, server-rendered (html/template + htmx) web UI under `/app/user` where a person reads and replies to their intercom channels (and their legacy History), grouped by attention (Needs you → Sessions → Channels), with a New Message picker, per-(participant,channel) unread, and a live status strip for the sessions in each conversation. It reads the channel read-model and writes through the `/app/user/send` endpoint; it refreshes live over an SSE push (`GET /app/user/events`), with a 30s poll as fallback.
read_when: You want a project member to read/reply to their studios and channels in a browser (not via the Discord/tracker relays), or you're operating/extending the `/app/user` inbox — its routes, the live push and fallback poll, the mark-read cursor, the session status strip, the composer and copy controls, or its wiring to the send path.
owns: the `/app/user` participant inbox UI — its two-pane rendering, the rail attention grouping, the conversation pane, the New Message picker, the session status strip (`GET /app/user/presence`) and how it maps session state to a status, the composer (keys, saved reply, draft stack) and copy controls, the unread mark-read (`POST /app/user/read`), the membership controls (`POST /app/user/join|leave|call-in`), the live push (`GET /app/user/events`) and fallback poll, and how it wires to the channel read-model and `/app/user/send`
prereqs: session-events.md for the event stream the status strip derives from; ui.md for the `/app` login and who counts as a user; comms-addressing.md for the target space; intercom.md for the squawk Log + wake-on; coves.md for the session phases and activity the "Needs you" treatment reflects; escalation.md for asking for a person
tier: leaf
updated: 2026-10-26
---

# The participant intercom inbox (`/app/user`)

A **project member** reads and replies to their intercom channels in a two-pane web
inbox served under `/app/user`, rather than through the Discord/tracker relays. It is the
human analog of a studio agent's `/squawks` tools: the same durable squawk Log,
seen from the human's side. It is served by `internal/jam/userui` (mirroring
`adminui`'s embedded-`html/template` + htmx mechanism) and mounted behind the
`/app` gate, which admits a roster member — see
[ui.md](ui.md#signing-in-one-login-one-principal) for the login and who counts as a user.

## Preview: the Jam app at `/app/next/`

A new single-page version of this inbox is being built at **`/app/next/`**, behind the same sign-in
([design](../../superpowers/specs/2026-10-26-jam-spa-design.md), [dev doc](../../web-app.md)).
So far it shows the title bar and the rail; opening a conversation links back here. Its JSON API lives
under `/app/user/api/`. This page stays the inbox until the cutover.

## What it shows

The page carries the shared app title bar (logo, `{display name} Jam`, tabs, principal
menu; see [ui.md](ui.md)) with **Inbox** current. New Message lives in the header of the conversation rail, above the channel list; the
header stays put when the list refreshes.

- **Left rail — channels grouped by attention:** `Needs you` (a conversation
  with a session that **asked for a person** — a `needs-input` report or
  the `escalate` tool, see [escalation.md](escalation.md) — or is blocked — live, or
  paused while it waits: the attention signal), then `Sessions` (conversations with a live, starting or
  paused session, including one that is merely idle), then `Channels`: every [channel](intercom.md#enabling-it) the person
  is in or has messages in, that they may see — chats (labelled with the others
  in them), tickets and rooms — across their projects. Leaving a project takes
  its channels away at once. Each row shows the label, a presence pin, and an
  **unread** badge.
  Last, **History (before the upgrade)**: the conversations of
  the frozen legacy log, read-only (no composer, all read).
  - **Mission:** A row may show the session's [mission](intercom.md#what-the-tools-do) as a muted second line under the label, truncated with an ellipsis. A session channel shows its own active session's mission; a ticket channel the most recently raised active session's; a chat only when exactly one session in it is active; rooms never. A session that is no longer active (ended or gone) shows none; an idle one still counts as active. It updates on the rail's next refresh (up to 30s).
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
- **Composers** use a monospace font. To get a message's markdown as sent,
  use its **Markdown** copy button.
- **New Message (rail header):** a picker of who to start a conversation with in the person's
  projects: other members (a chat with them), the live sessions they may reach
  (a standing or ticket session; a personal session only if they're already in
  its channel), live sessions' tickets, and rooms. A message to a session goes to
  its home channel, which the person joins.

**Writes are same-origin only.** Every `/app/user` POST (send, mark-read, join,
leave, call-in, answering an ask) is refused (`403`) unless its `Origin` (or `Referer`) is the
page's own host or one of [`ui-origins`](serve.md) — the same check as `/app/admin`
writes — so another site, a sibling subdomain included, can't post as you.

## Asks

A session's [ask](asks.md) renders as a **card** where it was asked: the
question, a button per option (with its detail), **Other…**, and a comment
box. Answering posts `POST /app/user/asks/{id}/answer` (`version`, `choice`,
`comment`): `204`; `409` "this ask changed — review the new version" (it was
amended since the card loaded) or "already answered by alice: …"; `403` if
it isn't yours to answer. An answered card collapses to "✓ alice chose …", a
withdrawn one says so, an amended one is marked *edited*, and an ask for
someone else (`of`) is read-only, "for carol". Its later events are one-line
system notes, not bubbles.

**Discuss** on a card starts a reply about it: the composer sends
`reply_to` (an ask in the same conversation; `/app/user/send` refuses any other with
`400`), and that reply shows a **re:** chip linking the card. An **Open asks**
panel above the stream lists the conversation's open asks
(`GET /app/user/asks?c=`, refreshed with the stream); each rail row shows its count,
and a conversation with an open ask you may answer is under **Needs you**.

## Attachments

With [attachments](attachments.md) on, the composer takes files three ways:
**📎** (a file picker), **paste** (a screenshot on the clipboard), and
**drag-and-drop** onto the composer. Each file uploads at once
(`POST /app/user/uploads`) as a chip with a progress bar and ✕; Send refuses
("Still uploading…") until they finish, then sends their refs (the text may
be empty). A paste with text on the clipboard stays a text paste. Only a
conversation's composer attaches, not New message's. A refused
upload shows why on its chip ("too large (max …)"). In the stream, images
show as inline thumbnails that open full size; other files as a download
chip with name and size. With attachments off there is no 📎.

## Whiteboards

A conversation you can post in has **New whiteboard** in its header (asks a
title, then opens the board). A [whiteboard](whiteboards.md)'s card shows its
title, a live thumbnail and **Open**; opening (`?c=<channel>&board=<id>`)
replaces the stream with the Excalidraw editor and **← Back to chat**. Edits
save as you draw (changed elements, ~300 ms after the last change and at
least every second during a long drag; a failed save retries, and leaving
with unsaved edits warns); others' and agents'
edits appear live (the `board` event, with a 30 s poll as fallback). Where you
can see but not post, the board opens view-only. The image tool is off for
now. A Jam built without the editor bundle shows cards ("whiteboard not
built") but no New whiteboard.

## Membership controls

The conversation header offers **Join** (a channel the person can see but isn't
in), **Leave** (one they're in; never a chat) and **Call in…** (a picker of the
project's people, and on ticket and session channels its live sessions, who
aren't in it yet). They post to `POST /app/user/join`, `/app/user/leave` and `/app/user/call-in`
(`channel`, and `who` = `user:<id>`/`session:<id>`); on success (`204`) the page
reloads. The rules are the intercom's ([membership](intercom.md#channel-membership)).

## Sending, unread, and refresh

- **Sending** posts to [`POST /app/user/send`](intercom.md) (the participant send path):
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
- **Composer keys:** **⌘↵** (Ctrl+Enter off a Mac) sends — the Send button
  shows it — and inserts nothing; plain Enter and Shift+Enter insert a
  newline. A message
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
  read via `POST /app/user/read`, which advances the person's read cursor on it
  (forward only) to its latest append sequence. Unread is the person's deliveries
  above it. The rail (and the app badge) counts the open conversation as read
  from the moment it's shown.
- **Refresh is live:** the page holds an SSE stream (`GET /app/user/events`, behind
  the same gate). Each squawk Log append sends a payload-free `changed` event,
  and the rail and the open conversation's message list re-fetch their fragments
  (`GET /app/user/rail`, `GET /app/user/stream`). Those fragment routes are the only place
  message content is served. A **30s poll** is the fallback, for state that
  appends nothing (a session going Waiting) and for a dropped stream. A reconnect
  also refreshes, to catch up on anything missed. Fragments follow the same
  partial/full convention `adminui` uses (branch on the `HX-Request` header).
  The pane header and composer never refresh, so a half-typed reply survives.
  The message list **doesn't refresh while text in it is selected**, so a squawk
  can be selected and copied; a push held by a selection runs once it clears.
  The push is in-process: an `intercom.Notifier` wraps serve's one shared
  channel-log handle, so every writer (agent send, relay ingress, `/app/user/send`,
  Jam's notices) fires it. That's correct while serve is the Log's sole writer. Without a
  configured Log, `/app/user/events` answers `204` and the page just polls.
  A second payload-free event, `presence`, fires when a session's status
  changes (at most one per 250 ms per stream; a change inside that gap is sent
  when it ends) and refetches just the status strip (`GET /app/user/presence`).
  `/app/user/events` answers `204` only when neither source is configured.
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
passes through (`sessionevents.Presence`, fed via `Hub.Observe`). The strip shows
only the status and a **tool name**; event content is for members of the session's
home channel, in its Thinking tab (see [Sensitivity](session-events.md#sensitivity)). The tracker is in memory, so after
a restart a running session reads "working" until its next event.

## Enabling it

The inbox is always mounted; reaching it takes a login that names a roster
member (or a loopback [`dev-identity`](serve.md) — see
[ui.md](ui.md#signing-in-one-login-one-principal)). Its conversations
come from the channel log, which is always available. Phone and desktop notifications, and the app badge: [push.md](push.md).

A session's home channel also gets a header and a tabbed side pane: [session-view.md](session-view.md).

## Not yet

- **Per-participant push filtering:** today every `changed` and `presence` ping
  reaches every connected participant, who then refetch only what they may see.
- **Multi-process Jam:** the push is in-process; several serve processes over one
  Postgres log would need `LISTEN/NOTIFY`.
- **A project-grouping toggle** (the read-model already carries the tags).
