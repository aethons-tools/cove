---
summary: The session view in the `/app/user` inbox — a session's home channel adds a header (identity, state, cost, uptime, quick actions) and a tabbed pane (Previews, Artifacts, Status) that can sit right of or below the messages, with its layout remembered per session.
read_when: You're using or extending how a session's conversation looks in `/app/user` — its header and actions (Copy SSH, Release, Restart/End, Upgrade), the side pane and its tabs, the pane's layout memory and `&tab=` links, or who sees the session's cost.
owns: the session header (`GET /app/user/session/{id}/head`) and its actions (`POST /app/user/session/{id}/release|restart|upgrade`), the session pane (`GET /app/user/session/{id}/pane?tab=`) and its tabs, the pane layout and its localStorage memory, and the cost/count aggregates (`sessionevents.Totals`) shown to members
prereqs: intercom-ui.md for the inbox this extends; session-events.md for the events the aggregates come from; previews.md for exposed ports; collaboration.md for who may SSH or open a preview
tier: leaf
updated: 2026-10-24
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
  role, project, and owner.
- **State:** the same status as the [status strip](intercom-ui.md#session-status), how
  long it has been in that state, and open asks.
- **Cost:** the total and the latest turn's cost. The tooltip shows input and output tokens.
- **Uptime:** time since the session was raised.
- **Tabs:** Previews, Artifacts, Status. These open the pane.
- **Copy SSH:** copies `at-jam ssh <id>`. It appears only if [collab](collaboration.md) would allow you to SSH.
- **⋯ menu:** each action asks for confirmation, then `POST /app/user/session/{id}/<action>`.
  The server re-checks permission (403 otherwise). Ending a session reloads the page.
  The header doesn't refresh while the menu is open.

| Action | Offered to | Does |
|--------|------------|------|
| Release | a personal session's owner, or an operator | tears it down |
| End / Restart | operators, for non-personal sessions (Restart for standing) | tears it down; a standing session is raised again by its reconciler |
| Upgrade | operators, standing sessions | queues an upgrade to the current image; the pending state shows beside it |

## Pane

The pane is closed until a tab opens it. Clicking the open tab again, or **×**, closes it.
**⇄** moves it between the right of the messages and below them. Below 900px wide it always
sits below. Drag the divider to resize it, or focus the divider and use the arrow keys. The
composer stays under the messages.

Each tab is an htmx fragment (`GET /app/user/session/{id}/pane?tab=`). The open tab reloads
on push and every 30s.

- **Previews:** the session's exposed ports ([previews.md](previews.md)). Each link opens in a
  new tab. The tab says so when previews are off, or when collab doesn't allow you to open them.
- **Artifacts:** what the home channel holds, newest first:
  - whiteboards, with thumbnails;
  - attached files;
  - asks, with their state;
  - links from message bodies, deduplicated, up to 50. GitHub PRs and issues are labelled
    `<repo> PR #n` or `<repo> issue #n`.
- **Status:** state and time in it, uptime, model spec, kit and image, alarms, then from events:
  turns and episodes, tool calls and errors, tokens, cost (total and this turn), and the last
  event.

**Layout memory:** the open tab, placement and size are saved in `localStorage` under
`jam-pane:<session id>`. A session you have never opened starts from the last layout you
used (`jam-pane:last`). `&tab=previews|artifacts|status` on a conversation URL opens that
tab. A tab clicked while a whiteboard is open goes back to the chat with that tab open.

## Cost and counts

`sessionevents.Totals` folds a session's events across all its streams into turns,
episodes, tool calls, errors, tokens, and cost. Cost is summed per episode from each
result's cumulative `total_cost_usd`; a turn's cost is its result's increase over the previous one. `sessionevents.TotalsCache` keeps the totals per
session and reads only events newer than those it has seen. These aggregates are all
that members see. Event content stays operator-only
([Sensitivity](session-events.md#sensitivity)).
