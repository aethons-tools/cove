---
summary: The session view in the `/app/user` inbox — a session's home channel adds a header (identity, state, cost, context use, uptime, quick actions) and a tabbed pane (Studio, Thinking, Previews, Artifacts, Status) that can sit right of or below the messages, with its layout remembered per session.
read_when: You're using or extending how a session's conversation looks in `/app/user` — its header and actions (Copy SSH, Interrupt, Compact, Clear, Release, Restart/End, Upgrade), the side pane and its tabs, the Studio tab (the in-pane IDE) and viewing a preview in the pane, the pane's layout memory and `&tab=` links, who sees the session's cost, or the Thinking tab (what the agent thinks and does: filters, formats, limits).
owns: the session header (`GET /app/user/session/{id}/head`) and its actions (`POST /app/user/session/{id}/release|restart|upgrade`), the session controls (Interrupt, Compact, Clear), the session pane (`GET /app/user/session/{id}/pane?tab=`) and its tabs, the Studio tab and a preview's View here (`POST /app/user/session/{id}/frame`), the Thinking tab (`GET /app/user/session/{id}/thinking/events|page|raw`) and its options, the pane layout and its localStorage memory, and the cost/count aggregates (`sessionevents.Totals`) shown to members
prereqs: intercom-ui.md for the inbox this extends; session-events.md for the events the aggregates come from; previews.md for exposed ports; collaboration.md for who may SSH or open a preview; previews.md#framing-the-jam-pane for the frame tickets and cookie
tier: leaf
updated: 2026-10-26
---

# The session view

When the open conversation is a live session's **home channel** (its session channel, or the
ticket channel of the ticket it works), the [inbox](intercom-ui.md) adds a header above
it and a pane beside it. The view is served by `internal/jam/userui` (`session.go`,
`sessionpane.go`). Anyone who can see the home channel sees both. Anyone else gets
404 from every route below.

## Header

One line, refreshed on push and every 30s (`GET /app/user/session/{id}/head`):

- **Identity:** the name (or actor id), kind (standing, personal, ticket, session), ticket,
  role, project, and owner. Under them, the session's [mission](intercom.md#what-the-tools-do) (one truncated line), when it has one.
- **State:** the same status as the [status strip](intercom-ui.md#session-status), how
  long it has been in that state, and open asks.
- **Cost:** the total and the latest turn's cost. The tooltip shows input and output tokens.
- **Context:** how full the agent's context is, e.g. `ctx 84k / 200k · 42%`; the tooltip
  gives exact tokens and the model. The size comes from the main thread's latest model call
  (its input, cache and output tokens; subagents' calls don't count). The window comes from the
  latest result's `modelUsage`. After a compaction, or a Clear or Compact press cove-master
  carried out, it shows `ctx —` until the next model call.
- **Uptime:** time since the session was raised.
- **Tabs:** Studio (only with SSH and framing on), Thinking, Previews, Artifacts, Status. These open the pane.
- **Copy SSH:** copies `at-jam ssh <id>`. It appears only if [collab](collaboration.md) would allow you to SSH.
- **Interrupt:** a visible button beside Copy SSH, enabled while the session is running. See [Session controls](#session-controls).
- **⋯ menu:** Compact and Clear (see [Session controls](#session-controls); only Clear asks for confirmation), then the release, restart, end and upgrade actions, each of which asks for confirmation, then `POST /app/user/session/{id}/<action>`.
  The server re-checks permission (403 otherwise). Ending a session reloads the page.
  The header doesn't refresh while the menu is open.

| Action | Offered to | Does |
|--------|------------|------|
| Release | a personal session's owner, or an operator | tears it down |
| End / Restart | operators, for non-personal sessions (Restart for standing) | tears it down; a standing session is raised again by its reconciler |
| Upgrade | operators, standing sessions | queues an upgrade to the current image; the pending state shows beside it |

### Session controls

Interrupt, Compact and Clear act on the session's agent; cove-master's handling is in
[coves.md](coves.md#session-controls-interrupt-compact-clear). Anyone who can see the
session may press them, on any session kind. Clear asks for confirmation.

| Control | Enabled when | Does |
|---------|--------------|------|
| Interrupt | running | stops the current turn; the agent then acknowledges in its channel and waits |
| Compact | running, holding (a turn ended with background tasks outstanding) or waiting | shrinks the conversation with Claude's `/compact` |
| Clear | waiting, including paused | starts the next turn as a blank conversation, and resets the session's mission (a failed reset is logged and doesn't fail the Clear) |

Each press is `POST /app/user/session/{id}/interrupt|compact|clear`. The server answers 409
when the control isn't available in the session's state, and 502 when the cove can't be reached.
A paused studio is resumed first, so a press may take up to about 30s. The Thinking tab shows
each press as "<Command> — pressed by <name>", and a press cove-master dropped with its reason,
e.g. "Clear — pressed by Bob (ignored: live episode)".

## Pane

The pane is closed until a tab opens it. Clicking the open tab again, or **×**, closes it.
**⇄** moves it between the right of the messages and below them. Below 900px wide it always
sits below. Drag the divider to resize it, or focus the divider and use the arrow keys. The
composer stays under the messages.

Each tab is an htmx fragment (`GET /app/user/session/{id}/pane?tab=`). The open tab reloads
on push and every 30s.

- **Studio:** the session's IDE, in the pane. See [Studio](#studio).
- **Previews:** the session's exposed ports ([previews.md](previews.md)). Each link opens in a
  new tab. With framing on, **View here** shows one preview in the pane (see
  [View here](#view-here)). The `ide` exposure is left out of the list: SSH holders reach it through
Studio. The tab says
  so when previews are off, or when collab doesn't allow you to open them.
- **Artifacts:** what the home channel holds, newest first:
  - whiteboards, with thumbnails;
  - attached files;
  - asks, with their state;
  - links from message bodies, deduplicated, up to 50. GitHub PRs and issues are labelled
    `<repo> PR #n` or `<repo> issue #n`.
- **Status:** state and time in it, uptime, model spec, kit and image, alarms, then from events:
  turns and episodes, tool calls and errors, tokens, context use, cost (total and this turn),
  and the last event.
- **Thinking:** the agent's events, live. See [Thinking](#thinking).

**Layout memory:** the open tab, placement and size are saved in `localStorage` under
`jam-pane:<session id>`. A session you have never opened starts from the last layout you
used (`jam-pane:last`). `&tab=studio|previews|artifacts|status|thinking` on a conversation URL opens that
tab. A remembered or linked Studio tab stays closed when the header has no Studio (no SSH, or
framing off). A tab clicked while a whiteboard is open goes back to the chat with that tab open.
A page restored with the browser's Back or Forward lays the pane out again and reloads its tab.

## Studio

The **Studio** tab is a web IDE (code-server) on the session's workspace, framed in the pane.
It is a shell, so it shows only if you may SSH ([collab](collaboration.md#who-may-connect)) and
framing is on: previews are on and [`ui-origins`](serve.md) is set, since previews refuse every
other framer ([previews.md](previews.md#framing-the-jam-pane)). There is no browse-only mode.
A kit's own `ide` exposure replaces the built-in IDE ([previews.md](previews.md#a-web-ide)).

- **First start:** opening the tab asks `POST /app/user/session/{id}/frame` (form field `name`;
  `ide` is checked against the ssh right, any other name against the preview right) for a
  one-time frame URL, and loads it. It reads "Starting the IDE…" until the frame loads; the
  first start can take several seconds while code-server starts.
- **Kept across tabs:** switching to another pane tab hides the IDE without unloading it. Going
  back shows it as it was. Closing the pane (**×**, or clicking Studio again) unloads the IDE.
- **Conversation switch:** a different conversation is a full page load, so the IDE reloads
  (1–3 s). code-server and VS Code state (open files, terminals) live in the studio and survive.
- **Width:** the first open takes 60% of the split, unless you have a saved size for that placement.
- **Reload** mints a fresh URL and reloads; the old frame is removed while the new URL is minted.
  **Open in new tab** opens the IDE on its own host, where you sign in normally. It works even
  when a mint is refused.
- **Rate limit:** 30 mints per user per minute, then 429 ("Too many views opened; wait a minute.").

**Errors** show in place of "Starting the IDE…":

| Cause | Shown |
|-------|-------|
| `signin` (the frame login lapsed) | re-mints once; if it recurs, a **Reopen this view** button |
| `cookies` (an expired login, or the browser refused the frame cookie) | retries once, then "This browser can't sign in inside the pane — use Open in new tab." (Safari does this) |
| 403 (refused mint, or from the frame) | No longer shared with you. |
| 404, refused mint (not exposed, or framing off) | Nothing to show. |
| 404, from the frame | Nothing to show: the studio isn't running. |
| a frame URL that isn't `http(s)`, or another refused mint | Couldn't open this view. |
| 502, 503, 504 | The IDE didn't start: the studio's image may predate it, so upgrade the studio or expose an IDE as `ide`. |

### View here

With framing on, each row in Previews has **View here**. It replaces the list with that one
preview framed, under a bar with **← All previews**, the name, a status and **Open in new tab**. One preview
is framed at a time. It loads and fails like the Studio, but a 502, 503 or 504 reads "The preview isn't answering."
Going back reloads the list.

## Cost and counts

`sessionevents.Totals` folds a session's events across all its streams into turns,
episodes, tool calls, errors, tokens, and cost. A turn's cost is its result's increase
over the previous result of the same claude session ([why](session-events.md)), and the
cost is the sum of those. `sessionevents.TotalsCache` keeps the totals per
session and reads only events newer than those it has seen. Event content is shown in the Thinking tab
([Sensitivity](session-events.md#sensitivity)).

## Thinking

The **Thinking** tab shows what the session's agent thinks and does, live, beside the chat.
Anyone who can see the home channel sees it ([Sensitivity](session-events.md#sensitivity)).
The server renders each event as a fragment (`internal/jam/userui/thinking.go`, using
`internal/jam/sessionview`); the browser filters and formats them. Without recorded events
the tab says so.

| Route (`/app/user/session/{id}/thinking/`) | Returns |
|-------|---------|
| `events` | A live SSE stream of new event fragments; 429 beyond 4 streams per viewer |
| `page` | An older page of 300 events (Load earlier) |
| `raw` | One event's raw JSON, capped at 1 MB |

All three give 404 to anyone who can't see the home channel (as for every route above), and 204 when events aren't recorded.
An open `events` stream re-checks access every 25s (its keep-alive) and ends once the viewer loses it.
When the stream is refused, the tab checks the pane: a 404 shows "This session is no longer available
to you"; anything else (such as the 429) retries after 2s, 5s, and 15s, then shows "Too many Thinking
views open".

- **Filters:** Thinking, Text, Tools, Errors only, Progress. A tool dropdown narrows Tools to
  one tool, and search matches each line's text, including its details.
- **Formats:** Narrative (one line each, the default), Detailed (full inputs and outputs),
  Raw (the JSON). Every line shows its local time beneath it.
- **Lines:** 💭 thinking, 💬 text, → the prompt that started the turn (filtered with Text), ▶ a
  tool call. Thinking with no visible text (only its signature came back) is a muted
  "thinking" line, and a run of them collapses ("thinking ×3"). Anything else (system events
  such as hooks, rate-limit notices) shows only its type, muted, with no detail.
- **Pairing:** a tool call and its result are joined, with ✓ or ✗ and the duration under the call's line, even when
  Load earlier brings the call in after its result.
- **Dividers:** each result is a divider: its episode, its cost since the previous loaded result
  of that episode (or, with none loaded before it, the episode's total so far), and its duration.
- **Following:** while scrolled to the bottom, new lines keep it there. Scrolling up pauses
  following and shows "↓ N new". **Load earlier** fetches the previous page.
- **Streams:** the picker offers Current and past streams. A new stream (the studio
  restarted) starts with a "session restarted" divider.
- **Memory:** filters, tool and format are saved per browser in
  `localStorage['jam-thinking']`. The search isn't saved.

**Limits:** the view opens on the newest 300 events; page size is 300; a detail is clipped at
64 KB (the raw route reaches 1 MB); 4 streams per viewer; 2000 lines in the list, and
once older pages are loaded past that, following pauses.
