# Jam in VS Code, phase 2: the hub

Status: draft for review (2026-10-30). Builds on [the code window design](2026-10-28-vscode-code-window-design.md) (phase 1, shipped in #567, #573, #575).

## Why

The web app's goal is "everything visible in one place", and its rail delivers that: at a glance you see which agents have something to say, and one click takes you there. Its weak spot is code work. A connected IDE is squeezed into a pane inside the page, and a session's views are tabs in one area that can only be laid out as a whole.

Phase 1's session window in VS Code fixes the second part for free. VS Code lets you place every view where it suits, and we maintain no layout code. But it lost the one-look attention view. **The hub brings the rail into VS Code**, so that VS Code can be where you live:
- some sessions never leave the hub, because the conversation is the whole job;
- others graduate to their own session window when you need the studio's files, and can still be handled in the hub when you happen to be there.

## Goals

- A **hub window** per Jam: the rail plus conversations, with the session views following the active conversation.
- **Session windows are always connected** to the studio. Opening one is a deliberate act from the hub, the web app, a link or a notification. Closing it lets the studio idle.
- **Ordinary VS Code windows** (your own projects) show the rail only, so attention is visible everywhere.
- **One rule for every Jam view:** it shows the window's current conversation. Pinning goes away.
- **A Dock (or desktop) launcher** that opens the hub.
- **More than one Jam** works: one hub per Jam. Mostly a single Jam is used; several are an admin case that must work but needn't be polished.

## Non-goals

- The config tree and admin screens in VS Code (they stay in the web app). *(My call: the original phase 2 sketch listed a config tree; nothing in this conversation asked for it.)*
- One hub that merges several Jams' rails.
- Replacing the web app. It stays complete, including on the phone.
- Per-window layout. VS Code stores view locations per profile, and we don't manage layout.

## Anti-goals (failure even if it "works")

- Any situation where you can't tell which session a view is showing. That is why pinning is removed.
- A click that opens a second copy of something already open: a second window for a session, or a duplicate conversation tab in the same window. *(My call:)* a hub tab and the session window's own tab for the same session may both exist. If the hub already had the tab when the window opened, it stays and stays usable, and nothing gets closed behind your back. Clicks still go to the window.
- The hub needing a studio connection, or keeping a studio awake.

## The windows

### Hub (one per Jam)

A VS Code window with no remote, for one Jam (named in its title, e.g. "Jam Hub — jam.example.com").

- **Rail** (left, the Jam view container): the same groups as the web app's rail (Needs you, Sessions, Channels, History), with the same cues as the web rail, no more (its label, mission and unread badge; "needs you" is a group). A session that has a window open is marked (e.g. ▣).
- **Click a row:**
  - if the row's session has a window open, that window comes to the front;
  - otherwise the row's conversation opens as an **editor tab**, or that tab is brought forward if it's already open.
- **Double-click a row**, or *Open session* in a session's conversation: open the session window, or bring it to the front. Rows without a session (chats, rooms, tickets) have no window; double-click is just a click.
- **Conversation tabs** behave like files. They can be kept open, split side by side and closed, and they survive a reload.
- **The other views** show the active conversation tab's session: Thinking (bottom panel), Status, Artifacts and Previews (right, the secondary sidebar, by default). *(My call:)* when the active tab isn't a conversation (a board, say), they keep showing the last conversation's session. When that conversation has no session (a chat or room), they show an empty state.
- **Previews in the hub** open in the browser (the preview's web URL), since there's no connection to forward a port through.
- **Boards** open as editor tabs, as today.

Mockup B from the session (tab per conversation) is the agreed layout:

```
┌ Jam Hub — jam.example.com ─────────────────────────────────────────┐
│J│ Needs you   │ 💬 ux-bot │ 💬 #design │ 💬 Sam │      │ Status     │
│ │ ● ux-bot    │                                │      │ Artifacts  │
│ │ ● Ada ▣     │   conversation                 │      │ Previews   │
│ │ Sessions    │                                │      │            │
│ │ Channels    ├────────────────────────────────┴──────┤            │
│ │ History     │ Thinking · ux-bot                     │            │
└────────────────────────────────────────────────────────────────────┘
```

### Session window (always connected)

`vscode-remote://ssh-remote+jam-<id>/home/agent/workspace`, over Remote-SSH (VS Code) or Open Remote - SSH (VSCodium), as in phase 1.
- **The conversation is an editor tab** among your files, showing only this window's session. It opens when the window first opens and comes back after a reload.
- Every other Jam view shows this window's session, with no way to change it.
- **The rail is there too**, so attention stays visible while coding. A click or double-click follows the same rules as in an ordinary window (below).
- Closing the window ends the SSH connection, so the studio can idle.

### Ordinary window

Any other window, such as your own project or an empty window.
- **The rail only.** No other Jam views show.
- **Click** a row: the session's window if one is open, otherwise the hub (opened if needed) with that conversation's tab.
- **Double-click:** open or focus the session window.
- **With several Jams signed in,** the rail shows one Jam at a time and has a switcher. *(My call: the window remembers its choice.)*

### When a session can't have a window

- **No SSH access** (a member's default, `--collaborate-ssh none`):
  - the rail doesn't mark the row as openable;
  - double-click and *Open session* just open its conversation tab in the hub, or bring it forward.
  - The rail API doesn't carry SSH access today; the builder adds what's needed, for example a per-row flag next to `phase`.
- **Studio not running:** show an error saying so, and keep or open the conversation in the hub. Opening a window doesn't start the studio.
- **Remote-SSH missing:** offer to install it, as phase 1 does.

### Removed

- **Pinning:** the pin/unpin buttons and their context keys, and the "elsewhere" state.
- ***Jam: Focus Session…***
- **The local session window:** the `sessions/*.code-workspace` files.
- ***Connect/Disconnect Studio*** and its status-bar item.
- **The Needs You tree view.** The rail replaces it; the activity-bar badge stays, on the rail's container.

**Existing users:** stale local session workspace files and pin state should be cleaned up or simply ignored. *(Builder's call.)*

## Getting in

- **Dock launcher:** "Jam Hub" with the J logo, one per Jam. It opens `vscode://aethons.jam/hub?jam=<url>` (or `vscodium://…`), which starts VS Code if needed and opens or focuses that Jam's hub.
  - `jam vscode install` creates it: a small `.app` on macOS, a `.desktop` entry on Linux. A VSCodium install gets its own launcher.
  - *(The exact launcher mechanics are the builder's call.)*
- ***Jam: Open Hub*** opens the hub from the command palette, in any window.
- **Web app links:**
  - *Open in VS Code / VSCodium* (`…/session/<id>`) opens the **connected** session window, or focuses it.
  - Today's link opens the local window first; that step goes away.
  - A new `…/channel/<chn>` link, or similar, opening a conversation in the hub is optional. *(Builder's call.)*
- **Notifications:** a click goes to the session's window if one is open, otherwise to that conversation's tab in the hub (opening the hub if needed).

**One rule for "go to conversation X"** (rail click, notification click): X's session window if it's open, otherwise X's tab in the hub. Rows without a session always go to the hub.

## Several Jams

- **Hubs:** one per Jam. A hub window knows its Jam, e.g. from its own workspace file in global storage, one per Jam. *(My call; the builder may choose another marker.)*
- **Sign-in per Jam.** Today the extension has one `jam.url` setting and one stored session (`jam.session`), and writes `default-app-token.json`. It needs:
  - a session per Jam URL;
  - a way to add a Jam, such as Sign In asking for or offering a URL, with `jam.url` kept as the first or default one;
  - per-Jam token files the `jam` CLI's `ssh-proxy` can use. The CLI already has `--app <name>` profiles and `<app>-app-token.json`, and checks the token's Jam against `--admin-url`, so the ssh block's ProxyCommand could pass `--app`.
  - *(The exact shape is the builder's call. Constraint: a single-Jam user must see no extra steps.)*
- **Session windows know their Jam.** The remote authority only carries `jam-<studio>`, so the extension records the studio's Jam when it opens the window. It could also read the ssh block's `--admin-url`. *(Builder's call.)*
- **Ordinary windows:** one Jam's rail at a time, with a switcher, as above.
- **Notifications and badge** *(my call):* every window holds a live stream for each signed-in Jam, so no Jam's notifications are missed. The cross-window lock is per Jam, so each event still notifies once. The activity-bar badge counts the Jam whose rail the window shows.

## Starting conversations

The web rail's *New message* and *New session* buttons are wanted in the VS Code rail too, but **not in the first slices**. Build the rail without them, then add them as a later slice:
- a new chat opens as a hub tab;
- a new session opens its conversation in the hub.

## What it reuses

- **Rail:** `GET /app/user/api/rail` plus the live `/app/user/events` stream, which the extension host already holds (one per window). The rail view should reuse the web app's rail component (`web/app/src/rail/`) in a webview, as the session views already reuse web/app.
- **Webviews:** conversation tabs are webview panels, like board tabs (`panes/board.ts`), restored by a serializer. The views are phase 1's webview views, minus pinning.
- **Knowing a window is open:** phase 1's connected marks (`connectedSet`, with a heartbeat from the studio window). Focusing a window: `vscode.openFolder` on the remote URI, which focuses the existing window (phase 1, #567).
- **Notifications:** phase 1's notifier and cross-window lock. Only the click target changes.

## Left to the builder

- How the rail webview tells the host about clicks and double-clicks (bridge intents).
- Keyboard access for the rail.
- How conversation tabs are keyed so they're never duplicated, and how they're titled.
- How the hub opens a conversation when asked from another window: a URI route, cross-window state, or similar.
- The details of the per-Jam sign-in and token files, within the constraints above.
- Slicing into PRs. A natural order is:
  1. remove pinning and make session windows always connected;
  2. add the rail view;
  3. add the hub and conversation tabs;
  4. add the launcher;
  5. support several Jams;
  6. add *New message* and *New session*.
