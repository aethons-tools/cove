# Session view, part 1: the shell — design

**Status:** approved in brainstorming (2026-10-24); built (header and pane shipped in one PR). User doc: [session-view.md](../../usage/jam/session-view.md).
**Context:** the session view (the user's "Session layout" board): a session's conversation gains a header and a splittable, tabbed pane of other views. Sub-projects, in order: **1 the shell (this)**, 2 the thinking view, 3 the studio view (code-server, inline previews), 4 process tails.

## Decisions (2026-10-24)

- The header shows identity, state, cost and uptime, with quick actions.
- Actions: Copy SSH (visible) and a "⋯" menu with Release / Restart / Upgrade, each shown only to who may use it; Share later.
- The pane is closed until a tab opens; right or below; resizable; **remembered per session**, a never-opened session starting from the last layout used; `&tab=` deep links.
- Artifacts: whiteboards, files, asks and links from the session's **home channel**.
- Previews: links that open in a new tab (inline framing comes with sub-project 3).
- Cost and status: **aggregates for anyone who can see the session**; session events' content stays operator-only.

## 1. Where

A conversation that is a session's home channel: a session channel (`Kind == session`, `Key ==` the session's actor id) or a ticket channel with a live session working it (that session's `Unit` is the ticket key in `Key`). DMs, rooms and History are unchanged.

## 2. Header

Server-rendered (`GET /app/user/session/{id}/head`), refreshed on the live `presence` and `changed` pushes:

- **Identity:** label (declared name, else id), kind (standing / personal / ticket, the ticket key linking to the issue when known), role, project, and for a personal session its owner.
- **State:** the presence line (phase/activity and live tool, as the presence strip), time in that state (`WaitingSince`/`TurnEndedAt`/`RaisedAt`), open asks.
- **Cost:** this turn and total (USD), tokens in the title. **Uptime** since `RaisedAt`.
- **Actions**, each shown only to who may use it:
  - **Copy SSH** — `at-jam ssh <id>`, when `collab.Decide(…, collab.SSH)` allows;
  - **Release** — a personal session's owner, or an operator → teardown;
  - **Restart** — operators; a standing session is torn down and its reconciler raises it again, state kept; any other session is ended (teardown), labelled **End**;
  - **Upgrade** — operators, standing sessions → `jam.UpgradeStanding` (shows its state).
  - Routes: `POST /app/user/session/{id}/release|restart|upgrade`, each re-checking the permission (403), behind the app's same-origin check; confirmation in the page.
- **Tabs:** Previews · Artifacts · Status.

## 3. Pane

- Closed until a tab is opened; clicking the open tab closes it. A corner toggle: right / below (always below under ~900px). A draggable divider, clamped.
- Layout `{open, tab, placement, size}` in localStorage per session id; a session with none starts from the last layout saved. `?c=…&tab=previews|artifacts|status` opens a tab.
- Each tab is an htmx fragment, `GET /app/user/session/{id}/pane?tab=…`, refreshed on the live pushes. The composer stays under the messages.
- **Previews:** the session's exposures — name, port, URL (opens in a new tab); "no previews" otherwise; hidden when previews aren't configured.
- **Artifacts** (home channel, newest first): whiteboards (title, thumbnail, Open), files (thumbnail or name/size), asks (open first; answered/withdrawn with the answer), links (URLs in messages, deduplicated; GitHub PR/issue URLs labelled `PR #n` / `#n`).
- **Status:** state and time in it, uptime, role/model spec, kit and image, background work (Holding), alarms, turns, episodes, tool calls, errors, tokens, cost (total and this turn), last activity.

## 4. Stats

`sessionevents.Totals` (moved from adminui's `totals`, which reuses it): turns, episodes, tool calls, errors, input/output tokens, total cost (cumulative per episode: the latest per turn counts), this turn's cost, last activity. `sessionevents.TotalsOf(store, actorID)` folds every stream of the actor (`Streams` + `List`), cached per actor and refreshed incrementally from each stream's last seq. Only numbers leave it — never event content.

## 5. Access

Everything in the header and pane is shown to whoever can see the conversation (as the conversation itself). Actions are additionally checked per §2. The operator test is `appui.PrincipalFrom(r).Admin`.

## 6. Testing

Totals folding (episodes, deltas, errors, this turn); home-channel resolution (session and ticket channels; DM/room none); each action's permission matrix (owner, member, operator, kinds) and its effect via fakes; head/pane fragments; a headless-Chrome test of the split (open/close, right/below, resize, per-session memory, last-layout default, `&tab=`).

## 7. Docs

`intercom-ui.md` (the session view); `session-events.md` (aggregates shown to members).

---

# Plan

- **PR A — stats and header.** (1) `sessionevents.Totals` + `TotalsOf` with a cache; adminui switches to it. (2) `userui` home-channel resolution → session; `Deps` gains `Events`, `Ops` (`Teardown`, `UpgradeStanding`, `UpgradeState`), `PreviewURL`. (3) The head fragment and the three action routes with permissions. (4) Wiring in `cmd/at-jam/main.go`. Tests per §6; docs.
- **PR B — the pane.** (1) Pane fragment and the three tabs. (2) Layout JS (split, placement, resize, per-session memory, `&tab=`). (3) Browser test; docs.
