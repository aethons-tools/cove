---
summary: The session status card — one compact summary of a session (state, mission, focus, counts, context, a state sparkline) that is a session channel's row in the Jam app's rail and heads the VS Code extension's Session Status pane; what each part shows and where its data comes from.
read_when: You are reading or changing a session's status card — its state and pill, count icons, context bar, mission/focus lines or sparkline — or its data (`SessionCard` on `GET /app/user/api/rail` and `GET /app/user/api/sessions/{id}`) and the live `presence` push that refreshes it.
owns: the status card's contents, its state mapping, how its counts (subagents included) are derived, the client-built sparkline, and where the card shows
prereqs: intercom-ui.md for the rail and the live refresh; session-events.md for the event stream Presence derives from; coves.md for session phases and activity
tier: leaf
updated: 2026-11-01
---

# The session status card

A session's **status card** sums it up at a glance. It is a **session
channel's row in the rail** (the channel's own session, or a ticket channel's
most recently raised active session; chats and rooms keep a plain row) and the
top of the extension's **Session Status** pane ([vscode.md](vscode.md)); the
extension's rail (in every window, and the hub) shows the same rows
([vscode-hub.md](vscode-hub.md#the-rail)). The
design: [session status card](../../superpowers/specs/2026-10-30-session-status-card-design.md).

## What it shows

- **Left border** — the session's color ([session-colors.md](session-colors.md); none without one).
- **Icon** — a grey box (VS Code's 2px corners, everywhere) with a robot silhouette until real icons arrive. The **context bar** sits just right of it: how full the main thread's context window is, green through
  yellow to red, pulsing from 85%. No bar until the window is reported.
- **Name** and the **state pill** (the tool in progress in its tooltip).
- **Count icons**, each hidden at 0: open asks (orange, pulsing), alarms,
  previews, kept processes, running subagents.
- **Mission** (the end goal or standing order) and **focus** (what the
  session is on now, in green) — both set by the agent
  ([intercom.md](intercom.md#what-the-tools-do)).
- **Sparkline** of the last 10 minutes of states, high to low: thinking,
  working, waiting, idle, sleeping; a dashed mark at each `system/init`.

## Data

`SessionCard` (`internal/jam/userui/card.go`) is on each card-bearing rail row
(`card`) and on the session header (`card`). Its **state**, with the
Instance's lifecycle winning over the event stream (as for the status strip):

| Session | State | Pill |
|---|---|---|
| paused | sleeping | paused (or needs you) |
| starting | thinking | starting |
| asked for a person | idle | needs you |
| blocked | idle | blocked |
| turn over, background tasks out — or kept processes or alarms | waiting | waiting |
| turn over, nothing local | idle | idle (done when done) |
| in a tool | working | working |
| in a model call, or writing | thinking | thinking |

Ending and gone sessions have no card.

**Counts** come from the Instance (open asks, alarms, exposures, kept
processes), except **subagents**: `sessionevents.Presence` derives them from
the event stream, reading only ids, tool names and task types — `Agent`/`Task`
calls awaiting their `tool_result` (cleared at each turn's `system/init` and
`result`) plus the `*_agent` tasks in claude's latest `background_tasks_changed`
list. Presence also counts `system/init` events (`inits`). Both are in memory:
after a Jam restart they start from 0.

## Freshness

A card-visible Instance change (`jam.InstanceChanges` wraps the store: phase,
activity, mission, focus, open asks, alarms, previews, kept processes) and a
Presence change (status, init, subagent count) push `presence` on the live
stream ([intercom-ui.md](intercom-ui.md#unread-and-refresh)); the app refetches
the rail and the header.

## The sparkline

The page builds it from the cards it fetches
(`web/app/src/session/card/history.ts`): a step at each state change, a mark
where `inits` went up. Nothing is stored, so a fresh page starts flat at the
current state.
