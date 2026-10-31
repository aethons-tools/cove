---
summary: The Jam VS Code extension's rail (the Conversations view, in every window) and hub window (conversations as editor tabs, the session views following the active tab), and how going to a conversation picks a studio window or the hub.
read_when: You are using or changing the VS Code rail or hub, or using several Jams — a rail click or double-click, ▣, the needs-you badge, Jam: Open Hub, the Jam Hub launcher, conversation tabs, the hub's views, the pending handoff from another window — or a click went to the wrong window.
owns: the VS Code rail (jam.rail) and its clicks, ▣ and badge; the hub windows (Jam Hub (<host>).code-workspace), Jam: Open Hub, the Jam Hub launcher and the …/hub and …/go links; several Jams (each window's Jam, Switch Jam, Add a Jam, notifications from every Jam); going to a conversation and hub/pending-<host>.json; conversation tabs in the hub and the hub's views
prereqs: vscode.md for installing the extension, studio windows and the panes' shared rules
tier: leaf
updated: 2026-10-31
---

# The Jam rail and hub in VS Code

The rail shows every conversation in every window; the hub is the one window where conversations open as tabs.
A click goes to the session's studio window when one is open, otherwise to the conversation's hub tab.

## The rail

The **Conversations** view, first in the Jam container in **every** window, is the web app's rail:
Needs you, Sessions, Channels and History, with each row's unread count; a session channel's row is its session's [status card](session-card.md), any other row its label (and mission).
Above it, **New message** picks whom to write to and opens a small composer; once sent, the new conversation opens in its hub tab. **New session** (when you may start one) starts it and opens its conversation in the hub.

- **Click** a row: [go to the conversation](#the-hub): its session's studio window if one is open, otherwise its tab in the hub.
- **Double-click** a session you may SSH into: its studio window opens, or comes to the front (see [Opening a session](vscode.md#opening-a-session)). Any other row does the same as a click.
- **▣** marks a session whose studio window is open (its connected mark is fresh). It refreshes on live changes and every 30 s.
- **The badge** on the Jam icon counts the Needs you rows. It's refreshed at once on start-up, sign-in and sign-out, and after a 3 s debounce on live changes.
- **Signed out:** "Sign in to Jam to see your conversations." with Sign in.

## The hub

The hub is a window with no studio connected: *Jam: Open Hub*, or a `vscode://aethons.jam/hub` link (`vscodium://…` in VSCodium), opens it, or brings it to the front.
Each Jam has its own hub: the workspace `Jam Hub (<host>).code-workspace` (no folders) in the extension's global storage.

- **Conversations are editor tabs:** one per conversation, titled with its label. Keep several open, split them, close them; they come back after a reload. VS Code revives a background tab only when it is first shown, so a conversation opened again before then briefly has two tabs; showing the restored one closes it and reveals the other (as with board tabs).
- **The views** (Thinking, Status, Artifacts, Previews) show the session of the conversation tab you last made active; a chat or room tab, or none yet, shows "Open a session's conversation to see its …".
- **Previews** open in the browser: the hub has no studio connection to forward through.
- **Going to a conversation** (a rail click, a notification, a `…/go/<chn>` link) works the same in every window: its session's studio window if one is open (its connected mark is fresh), otherwise its tab in the hub.
  From another window, the extension writes `hub/pending-<host>.json` (the channel and the time) in global storage and opens that Jam's hub; the hub opens the tab when it starts or next gains focus, and deletes the file. A request older than 60 s is dropped.

## The launcher

`jam vscode install` adds a **Jam Hub (<host>)** launcher with the J logo (`Jam Hub (<host>) VSCodium` or `… Insiders` for those editors, picked from `--code`);
it opens `<scheme>://aethons.jam/hub?jam=<Jam URL>`, which starts the editor and opens or focuses the hub. A rerun replaces it; `--no-launcher` skips it.

- **macOS:** `~/Applications/Jam Hub (<host>).app`, a tiny unsigned app that runs `open` on the link. Drag it to the Dock to keep it there.
- **Linux:** `~/.local/share/applications/jam-hub-<host>.desktop` (icon in `~/.local/share/icons/hicolor/512x512/apps/jam-hub.png`), shown in the app menu; it runs `xdg-open` on the link.
- **Elsewhere:** no launcher; the install prints the link to open instead.
- A launcher opens its own Jam's hub. One for a Jam that isn't in `jam.url`/`jam.urls` opens nothing; it warns, with *Add it*.
- A launcher that can't be written (e.g. no writable home) is a warning; the extension is installed regardless.

## Several Jams

`jam.url` is the default Jam; `jam.urls` lists more (*Jam: Add a Jam…* adds one and signs in to it). Each Jam has its own sign-in, hub and launcher.

- **Each window shows one Jam:** a hub its own; a studio window the Jam it was opened from (kept across reloads); any other window the one picked with *Jam: Switch Jam…* (the ⇄ button on the rail, shown only with several Jams, which also names the window's Jam; the window reloads onto it), else `jam.url`. The rail, badge, Sign In/Out and the views all follow it. A Jam later removed from the list falls back to `jam.url`; a hub for a removed Jam shows the sign-in copy.
- **File names** use the Jam's host with `:` and other unsafe characters as `-` (e.g. `h-8443` for `https://h:8443`).
- **Notifications come from every Jam you're signed in to**, each event once across windows (one lock per Jam). Open goes to that Jam's conversation: its studio window if open (focused without a new sign-in), else its tab in that Jam's hub. OS notification links for a non-default Jam carry `&jam=<url>`.
- **Links:** `…/go/<chn>?jam=<url>` and `…/hub?jam=<url>` name the Jam; one for a Jam not in the list warns and opens nothing.

