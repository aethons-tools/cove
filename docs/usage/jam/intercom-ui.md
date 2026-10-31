---
summary: The user area under `/app/user` — the Jam app, a single-page web app where a person reads and replies to their intercom channels (and their legacy History), grouped by attention (Needs you → Sessions → Channels), with New message, asks, attachments, whiteboards, per-(participant,channel) unread and a live status strip for the sessions in each conversation. It reads the user area's JSON API (`/app/user/api/`) and refreshes live over an SSE signal stream (`GET /app/user/events`), with a 30 s poll as fallback.
read_when: You want a project member to read/reply to their studios and channels in a browser (not via the Discord/tracker relays), or you're operating the `/app/user` area — what it shows, its URLs, the composer and copy controls, unread, membership, the live refresh, or the session status strip.
owns: what the `/app/user` user area shows and does — the rail attention grouping, the conversation view, New message, New session, asks in a conversation, the composer (keys, saved reply, draft stack, attachments) and copy controls, membership controls, unread and mark-read, the live refresh and fallback poll, its URLs and old-link redirects, and the session status strip and how it maps session state to a status
prereqs: web-app.md for how the app is built and served; session-events.md for the event stream the status strip derives from; ui.md for the `/app` login and who counts as a user; intercom.md for the squawk Log + wake-on; coves.md for the session phases and activity the "Needs you" treatment reflects; escalation.md for asking for a person
tier: leaf
updated: 2026-10-30
---

# The user area (`/app/user`)

A **project member** reads and replies to their intercom channels in the **Jam
app**, a single-page web app served under `/app/user`, rather than through the
Discord/tracker relays. It is the human analog of a studio agent's `/squawks`
tools: the same durable squawk Log, seen from the human's side. It sits behind
the `/app` gate, which admits a roster member — see
[ui.md](ui.md#signing-in-one-login-one-principal). How it's built, served and
tested: [web-app.md](../../web-app.md).

## URLs

| URL | Shows |
|-----|-------|
| `/app/user/` | the rail, no conversation open |
| `/app/user/c/<channel>` | a conversation (a session's home channel adds its header and pane, [session-view.md](session-view.md)) |
| `/app/user/new` | New message |
| `/app/user/boards/<id>` | a [whiteboard](whiteboards.md) |

Old links still work: the old Inbox's `/app/user/?c=<channel>` (with `&board=`
or `&tab=`) redirects to the new form, and `/app/next/…` (the app's preview
address) redirects to `/app/user/…`. Any other path says "Nothing here." with a
link back.

## What it shows

The page carries the shared app title bar (logo, `{display name} Jam`, tabs,
principal menu; see [ui.md](ui.md)) with **Inbox** current.

- **Left rail — channels grouped by attention:** `Needs you` (a conversation
  with a session that **asked for a person** — a `needs-input` report or the
  `escalate` tool, see [escalation.md](escalation.md) — or is blocked, live or
  paused while it waits, or with an open ask you may answer), then `Sessions`
  (a live, starting or paused session, idle included), then `Channels`: every
  [channel](intercom.md#enabling-it) the person is in or has messages in, that
  they may see — chats (labelled with the others in them), tickets and rooms —
  across their projects. Leaving a project takes its channels away at once.
  Each row shows the label, a presence pin, its open asks and an **unread**
  badge. Last, **History (before the upgrade)**: the conversations of the
  frozen legacy log, read-only. **New message** (and **New session**) sit in the rail's header.
  - **Mission:** a row may show a session's [mission](intercom.md#what-the-tools-do)
    as a muted second line. A session channel shows its own active session's; a
    ticket channel the most recently raised active session's; a chat only when
    exactly one session in it is active; rooms never.
  - **Status card:** a session channel's row (its own session, or a ticket's
    most recently raised active one; never chats or rooms) carries that
    session's status card in `GET /app/user/api/rail` (`card`), as does its
    header (`GET /app/user/api/sessions/{id}`). The card holds a state for the
    sparkline and pill — `thinking` (a model call), `working` (a tool, named),
    `waiting` (turn over, background tasks, kept processes or alarms still
    out), `idle` (waits only on messages, or on a person: pill `needs you` or
    `blocked`), `sleeping` (paused) — plus the mission, the [focus](intercom.md#what-the-tools-do) (what the session is on now), counts (open asks,
    alarms, previews, kept processes), the context window's use in tokens, and
    how many `system/init` events Jam has seen since it started. The design:
    [session status card](../../superpowers/specs/2026-10-30-session-status-card-design.md).
    The rail shows such a row *as* the card (`SessionCard`, compact): a plain
    icon box with the context bar overlaid on its right edge (green, through
    yellow, to red; pulsing from 85%), the name, the state pill (its tool in
    the tooltip), count icons (each hidden at 0; open asks orange and
    pulsing), the mission, and a sparkline of the last 10 minutes. The page
    builds the sparkline from the cards it fetches (`session/card/history.ts`):
    a step per state change, a dashed mark where `inits` went up; it isn't
    stored, so a fresh page starts flat.
- **The open conversation:** its messages (the viewer's own on one side) with
  day dividers and **Load earlier** for older pages, a **"Needs you"** chip
  when a session in it asked for a person or is blocked, the **Open asks**
  panel, the session status strip, and a composer. Each message renders per
  its [content type](intercom.md#content-type-markdown-or-plain-text): markdown
  as sanitized HTML, plain text as-is with its whitespace. A wide code block
  scrolls sideways inside its bubble. It opens at the latest message; a new
  message scrolls into view only if you were already at the bottom, and Send
  always scrolls there.
- **Copy controls:** hovering a message (or tabbing to it) shows **Text** and
  **Markdown** beside the sender: what the bubble shows, or the body exactly as
  sent. Each code block has a copy icon for just the code.
- **New message:** a picker of who to start a conversation with in the
  person's projects: other members (a chat with them), the live sessions they
  may reach (a standing or ticket session; a personal session only if they're
  already in its channel), live sessions' tickets, and rooms. A message to a
  session goes to its home channel, which the person joins.
- **New session:** start a [personal session](personal-sessions.md#starting-one-from-the-inbox)
  of a role, with an optional first message, and land in its conversation.

**Writes are same-origin only.** Every `/app/user` write is refused (`403`)
unless its `Origin` (or `Referer`) is the page's own host or one of
[`ui-origins`](serve.md) — the same check as `/app/admin` writes — so another
site, a sibling subdomain included, can't post as you.

## Asks

A session's [ask](asks.md) renders as a **card** where it was asked: the
question, a button per option (with its detail), **Other…** and a comment box.
An answer is `409` if the ask changed since the card loaded ("review the new
version") or was already answered ("already answered by alice: …"), `403` if
it isn't yours to answer. An answered card collapses to "✓ alice chose …", a
withdrawn one says so, an amended one is marked *edited*, and an ask for
someone else (`of`) is read-only, "for carol". Its later events are one-line
system notes. **Discuss** starts a reply about the ask: the reply carries
`reply_to` and shows a **re:** chip linking the card. The **Open asks** panel
above the messages lists the conversation's open asks.

## The composer

- **Sending** posts `{to, body}` through the participant send path
  ([intercom.md](intercom.md)): `to` is the open channel, or for New message
  `user:<id>` / `session:<id>`. The intercom decides whether the person may
  post there and who hears it. A reply to a waiting studio **wakes it** exactly
  as a relayed reply does ([wake-on](intercom.md#waiting-for-a-reply-wake-on)).
  Messages are markdown; tick **Plain text** to send one as `text/plain`. A
  message is trimmed at both ends.
- **Keys:** **⌘↵** (Ctrl+Enter off a Mac) sends; Enter and Shift+Enter insert
  a newline. **⌘⇧V** (Ctrl+Shift+V) pastes as a fenced code block on its own
  lines, its fence longer than any backtick run in the text; it rides the
  browser's own paste-as-plain-text shortcut, so no permission prompt.
- **Saved reply:** what you type is kept per conversation in the tab's session
  storage, so it survives switching conversations; a send or emptying the box
  clears it. It never leaves the browser.
- **Draft stack:** **⌘↓** (Ctrl+↓) pushes the draft onto a per-conversation
  stack and clears the box; sending pops the top draft back, cursor and all;
  **⌘↑** or the "↩ N stacked drafts" chip pops by hand. A pop never overwrites
  text in the box. Stacks live in session storage, never sent to Jam.
- **Attachments** (with [attachments](attachments.md) on): **📎**, **paste**
  (a screenshot) or **drag-and-drop**. Each file uploads at once as a chip with
  progress and ✕; Send waits until they finish, then sends their refs (the text
  may be empty). A refused upload says why on its chip. In the stream, images
  show as thumbnails that open full size; other files as a download chip.
  New message doesn't attach.

## Membership controls

The conversation header offers **Join** (a channel the person can see but
isn't in), **Leave** (one they're in; never a chat) and **Call in…** (the
project's people, and on ticket and session channels its live sessions, not
yet in it). The rules are the intercom's
([membership](intercom.md#channel-membership)).

## Unread and refresh

- **Unread** is a per-(participant, channel) cursor. Showing a conversation
  marks it read up to its latest message (forward only); unread is the
  person's deliveries above it. The rail and the app badge count the open
  conversation as read from the moment it's shown.
- **Refresh is live:** the app holds an SSE stream (`GET /app/user/events`).
  It carries payload-free signals — `changed` (the squawk Log grew),
  `presence` (a session's status changed, or what its status card shows:
  phase, activity, mission, focus, open asks, alarms, previews, kept processes; at
  most one per 250 ms, the last always sent; it refetches the rail too) and `board` (a whiteboard changed) — and one event with
  content, `event: notify`: the viewer's own notification, as a push would
  show it ([VS Code](vscode.md#notifications-and-the-badge)); the web app
  ignores it. The app refetches what each names through the API, which checks access again. A
  reconnect refetches too, and a **30 s poll** covers state that appends
  nothing and a dropped stream. A half-typed reply is never touched.
- The push is in-process: an `intercom.Notifier` wraps serve's one shared
  channel-log handle, so every writer (agent send, relay ingress, the user
  area, Jam's notices) fires it — correct while serve is the Log's sole writer.
  With neither the Log nor presence configured, `/app/user/events` answers
  `204` and the app just polls.

## Session status

Each session that is a member of the open conversation gets one line:
**`<name> is <status>`** (or **`<name> needs you`**). The name is the session's
declared name, else its actor id. Busy states show animated dots; idle, paused
and done are dimmed; needs you and blocked use the attention colour. A session
whose turn ended is **idle**: it needs you only once it asks.

| Status | When |
|--------|------|
| starting | the session is raising |
| paused | it is idled |
| needs you | it is waiting (live or paused) and asked for a person since it was last woken; this wins over paused |
| idle | it is waiting without having asked (its turn ended) |
| blocked / done | its reported activity says so (this and the two above win over its events) |
| thinking | its latest event began a turn, was a thinking block, or was a tool result |
| running **\<Tool\>** | its latest event was a tool call |
| writing | its latest event was assistant text |
| idle | its turn finished (`result`) |
| working | live, but no event seen since Jam started |

Terminating, lost and gone sessions are left out. The status comes from the
session's phase and activity plus its [session events](session-events.md),
summarized by an in-memory tracker every published event passes through
(`sessionevents.Presence`, fed via `Hub.Observe`). The strip shows only the
status and a **tool name**; event content is for members of the session's home
channel, in its Thinking tab ([Sensitivity](session-events.md#sensitivity)).
After a restart a running session reads "working" until its next event.

## Enabling it

The user area is always mounted; reaching it takes a login that names a roster
member (or a loopback [`dev-identity`](serve.md) — see
[ui.md](ui.md#signing-in-one-login-one-principal)). Its conversations come from
the channel log, which is always available. A Jam built without the app bundle
answers "not built" ([web-app.md](../../web-app.md)). Phone and desktop
notifications, and the app badge: [push.md](push.md).

## Not yet

- **Per-participant push filtering:** every signal reaches every connected
  participant, who then refetch only what they may see.
- **Multi-process Jam:** the push is in-process; several serve processes over
  one Postgres log would need `LISTEN/NOTIFY`.
- **A project-grouping toggle** (the read-model already carries the tags).
