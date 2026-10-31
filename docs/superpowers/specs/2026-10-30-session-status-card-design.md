# Session status card — design

**Status:** agreed with the user 2026-10-30; built (PRs #582–#586). Docs: `docs/usage/jam/session-card.md`.
**Context:** a rail row for a session shows only the session's name and its [mission](2026-10-26-session-mission-design.md). It doesn't show what the session is doing now, how full its context is, or what it has running. The extension's Status pane (`jam.status`) has the same gap. The user designed a compact **session status card** on a whiteboard and in a live mockup ([assets/2026-10-30-session-status-card-mockup.html](assets/2026-10-30-session-status-card-mockup.html)). It replaces the session's rail row in the web app and becomes the extension's **Session Status** pane.

## Decisions (2026-10-30)

### Card layout

The card has three parts, top to bottom:

1. **Icon block.** A square **agent icon** on the left. For now it is a plain box: real agent icons come later.
   - A thin vertical **context bar** is overlaid inside the icon's right edge. It shows how full the context window is: green when low, shading through yellow to red, and pulsing red at 85% or more.
   - To the right of the icon, stacked:
     - The **session name**.
     - The **state pill**.
     - A row of **count icons**. Each icon is hidden when its count is 0.

     | Count | Icon | Notes |
     |---|---|---|
     | Open asks | speech bubble with "!" | Orange and pulsing: it needs you |
     | Alarms | clock | |
     | Previews | eye | |
     | Kept processes | terminal | |
     | Subagents | small square, like a mini agent icon | |

2. **Text lines**, full width, each one line long and truncated with an ellipsis:
   - The **mission**.
   - The **focus**, in green.

3. **Sparkline**, full width: the session's recent states over time.
   - Its y-axis runs from highest to lowest:

     | Level | Meaning |
     |---|---|
     | thinking | The turn is in an API call |
     | working | The turn is in a tool call |
     | waiting | The turn ended with local waits |
     | idle | The turn ended and the session waits only on messages |
     | sleeping | The studio is paused |

   - Each system/init event shows as a dashed vertical mark.

**Left out on purpose:**
- Uptime.
- The status text line. The pill already shows the status.
- The per-agent tool list and the event feed. These may come back in a separate detail view.

### Where it shows

- **Web app rail:** the card replaces the row for **session channels only** (`ChannelStudio`, which includes ticket channels). Rooms (`ChannelNamed`) and DMs keep today's row. The unread badge stays on the card.
- **Extension:** the `jam.status` view is retitled **Session Status**. It shows the card for the focused session, in place of `SessionHeader`. The pane's existing Status rows stay below it.
- **Hub:** the hub is being built on another branch. The card is built so the hub's rail can reuse it once both branches land. Wiring it into the hub is out of scope here.

### Mission vs. focus

- **Focus** is new. It is what agents use `mission` for today: one line saying what the session is on right now, changing with each step.
- **Mission** narrows to the **end goal or standing order**. It changes rarely: a ticket's title, a standing session's order, or a personal session's task.
- **Tools:**
  - A new `focus` tool is added. It is modelled on `mission` in every respect: the same normalisation, `PUT /focus`, and self-scoping.
  - The `mission` tool's description is reworded to "your end goal or standing order — what you were started for; set it when you get a new task".
  - The boilerplate line becomes: set `mission` to the end goal when you get a task, and `focus` to what you are on now whenever that changes. It must fit `BudgetBoilerplate`.
- **Clear** clears both mission and focus, since Clear means a new task.
- **Naming in code:** the extension already uses "focus" to mean the focused session (`FocusModel`, *Jam: Focus Session*). Inside the extension and the bridge, name this field `sessionFocus` or similar so the two never meet.

## 1. Data: one `SessionCard` type

Add `userui.SessionCard` to `apitypes.go` (tygo generates the matching TypeScript). It is built by a single function, used both by the rail and by the session header:

```go
type SessionCard struct {
    Session  string        `json:"session"`  // actor id
    Name     string        `json:"name"`
    State    CardState     `json:"state"`    // machine-readable, drives the sparkline level and pill colour
    Pill     string        `json:"pill"`     // the pill's text: "working", "needs you", "blocked", "starting", "paused"…
    Tool     string        `json:"tool"`     // the tool in progress when State is working; "" otherwise
    Mission  string        `json:"mission"`
    Focus    string        `json:"focus"`
    Counts   CardCounts    `json:"counts"`   // asks, alarms, previews, processes, subagents
    Context  CardContext   `json:"context"`  // used and window, in tokens; window 0 = unknown, so no bar
    Inits    int           `json:"inits"`    // system/init events seen so far; an increase is a sparkline mark
}
```

### Card state

`CardState` is one of: `thinking`, `working`, `waiting`, `idle`, `sleeping`. It is derived the same way as `sessionRow` (`userui/presence.go`), with the Instance's lifecycle taking precedence:

| Instance / event | State | Pill |
|---|---|---|
| Phase `Idled` | sleeping | paused |
| Phase `Raising` | thinking | starting |
| `NeedsPerson` (not blocked) | idle | needs you |
| Activity `Blocked` | idle | blocked |
| Activity `Holding` | waiting | waiting |
| Activity `Waiting` with kept processes or armed alarms | waiting | waiting |
| Activity `Waiting`, nothing local | idle | idle |
| Activity `Done` | idle | done |
| Running, presence `running` | working | working (+ tool) |
| Running, presence `thinking`, `writing` or unknown | thinking | thinking |

Terminating, lost and gone sessions get no card. Their row falls back to today's row.

### Counts

- **asks:** `Instance.OpenAsks`
- **alarms:** `len(Instance.Alarms)`
- **previews:** `len(Instance.Exposures)`
- **processes:** `Instance.KeptProcesses`
- **subagents:** how many subagents are running now. Derived in `sessionevents.Presence` from the event stream:
  - Agent/Task tool calls with no result yet, plus outstanding background tasks.
  - Events carrying `parent_tool_use_id` are the subagents' own events.
  - It is kept in memory beside each session's status. Like status, it is lost on restart, which is acceptable.

### Context

`sessionevents.Totals.Context` and `ContextWindow` go out as raw numbers. `SessionHead.Context` and `ContextTip` stay as they are.

### Inits

`Presence` counts the `system`/`init` events it has seen for each session. The client marks the sparkline when the count goes up.

### Where the card is attached

- **`ChannelRow`** gains `Card *SessionCard`. It is set only for `ChannelStudio` rows with an active session. Use the same session `userChannel` already picks for phase and mission: the latest-raised active session for a ticket.
- **`SessionHead`** gains `Card SessionCard`.

### Freshness: one more live signal

Today, Instance-only changes reach the page only on the 30 s poll. These are mission, focus, phase, activity, alarms, exposures, kept processes and open asks.

- The Supervisor publishes a coalescing, payload-free signal whenever it writes one of those fields. Model it on `Presence.Subscribe`.
- `userui/events.go` turns that signal into the existing `presence` SSE event, under the same 250 ms throttle. No new event name is needed, so the extension's `PaneHub` forwarding is unchanged.
- In `useLiveEvents`, `presence` now also invalidates `rail`.

## 2. The sparkline is client-built

The sparkline is not a source of truth, so the server keeps no history.

- **Store:** a module-level store in the app (`useStateHistory`), keyed by session id.
- **Recording:** each time a card arrives, from the rail or the header, the store records `(time, state)` if the state changed, and a mark if `inits` went up.
- **Window:** it keeps about the last 10 minutes or 120 points.
- **Rendering:** it draws a step line coloured by state, like the mockup.
- **Persistence:** it may also persist to `sessionStorage` *(builder's call)*.
- **Gaps:** a freshly opened page starts with a flat line at the current state. That is fine.

## 3. Components

- **`web/app/src/session/SessionCard.tsx`:** the card, matching the mockup. It takes a `SessionCard` and a `compact` flag for the rail, which gives a smaller icon and a narrower sparkline *(builder's call on the exact sizes)*.
  - Clicking the subagent count is a no-op for now. It will later open the detail view.
- **Web rail (`rail/Rail.tsx`):** a row with a `card` renders `SessionCard compact` inside the `NavLink` instead of `label`/`description`. Other rows are unchanged.
- **Extension:**
  - The `jam.status` pane (`vscode/panes.tsx`) renders `SessionCard` from `SessionHead.card` above `PaneBody tab="status"`.
  - Its title in `package.json` and `Pane.tsx` becomes "Session Status".

## 4. Slices

| # | Slice | Depends on |
|---|---|---|
| 1 | Backend card: the `SessionCard` type and builder, state mapping, counts without subagents, raw context, inits, attached to `ChannelRow` and `SessionHead`, the instance-change live signal, and `presence` invalidating `rail` | — |
| 2 | `focus` field and tool, `mission` re-description, boilerplate line, Clear clears both, `Card.Focus` filled | 1 |
| 3 | Subagent count in `Presence`, filled into `Card.Counts.Subagents` | 1 |
| 4 | `SessionCard` component, `useStateHistory`, and the web rail | 1 |
| 5 | The extension's Session Status pane | 4 |

Slices 2, 3 and 4 can run in parallel once slice 1 lands. The UI shows focus and subagents as soon as their slices fill the fields.

## 5. Non-goals

- Real agent icons.
- The detail view: the per-agent tool trace and the event feed.
- Hub integration.
- Server-side state history.
- Changing rooms' or DMs' rows.

## 6. Docs and tests

- **Docs:**
  - `docs/usage/jam/intercom.md`: the `focus` tool and the reworded `mission`.
  - `intercom-ui.md`: the rail card.
  - `session-context.md`: the boilerplate.
  - `docs/usage/jam/vscode.md`: the Session Status pane.
  - `docs/web-app.md`: if the live-event table changes.
- **Tests:**
  - The card state mapping: one case per table row.
  - Counts, and context as raw numbers.
  - The card appears only on studio rows with an active session.
  - The instance-change signal fires `presence`.
  - The `focus` handler: auth, self-scope, normalisation. Clear clears both.
  - Subagent counting: foreground, background and finished.
  - `useStateHistory` records transitions and marks.
  - The `SessionCard` component: counts hidden at 0, the context bar's colour bands.
  - The rail renders cards only for session rows.
  - The extension pane renders the card.
