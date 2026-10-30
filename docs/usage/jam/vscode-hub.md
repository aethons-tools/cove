---
summary: The Jam VS Code extension's rail (the Conversations view, in every window) and hub window (conversations as editor tabs, the session views following the active tab), and how going to a conversation picks a studio window or the hub.
read_when: You are using or changing the VS Code rail or hub — a rail click or double-click, ▣, the needs-you badge, Jam: Open Hub, the Jam Hub launcher, conversation tabs, the hub's views, the pending handoff from another window — or a click went to the wrong window.
owns: the VS Code rail (jam.rail) and its clicks, ▣ and badge; the hub window (Jam Hub.code-workspace), Jam: Open Hub, the Jam Hub launcher and the …/hub and …/go links; going to a conversation and hub/pending.json; conversation tabs in the hub and the hub's views
prereqs: vscode.md for installing the extension, studio windows and the panes' shared rules
tier: leaf
updated: 2026-10-30
---

# The Jam rail and hub in VS Code

The rail shows every conversation in every window; the hub is the one window where conversations open as tabs.
A click goes to the session's studio window when one is open, otherwise to the conversation's hub tab.

## The rail

The **Conversations** view, first in the Jam container in **every** window, is the web app's rail:
Needs you, Sessions, Channels and History, with each row's mission and unread count.
It has no *New message* or *New session* buttons yet; use the web app for those.

- **Click** a row: [go to the conversation](#the-hub): its session's studio window if one is open, otherwise its tab in the hub.
- **Double-click** a session you may SSH into: its studio window opens, or comes to the front (see [Opening a session](vscode.md#opening-a-session)). Any other row does the same as a click.
- **▣** marks a session whose studio window is open (its connected mark is fresh). It refreshes on live changes and every 30 s.
- **The badge** on the Jam icon counts the Needs you rows. It's refreshed at once on start-up, sign-in and sign-out, and after a 3 s debounce on live changes.
- **Signed out:** "Sign in to Jam to see your conversations." with Sign in.

## The hub

The hub is a window with no studio connected: *Jam: Open Hub*, or a `vscode://aethons.jam/hub` link (`vscodium://…` in VSCodium), opens it, or brings it to the front.
It is the workspace `Jam Hub.code-workspace` (no folders) in the extension's global storage.

- **Conversations are editor tabs:** one per conversation, titled with its label. Keep several open, split them, close them; they come back after a reload. VS Code revives a background tab only when it is first shown, so a conversation opened again before then briefly has two tabs; showing the restored one closes it and reveals the other (as with board tabs).
- **The views** (Thinking, Status, Artifacts, Previews) show the session of the conversation tab you last made active; a chat or room tab, or none yet, shows "Open a session's conversation to see its …".
- **Previews** open in the browser: the hub has no studio connection to forward through.
- **Going to a conversation** (a rail click, a notification, a `…/go/<chn>` link) works the same in every window: its session's studio window if one is open (its connected mark is fresh), otherwise its tab in the hub.
  From another window, the extension writes `hub/pending.json` (the channel and the time) in global storage and opens the hub; the hub opens the tab when it starts or next gains focus, and deletes the file. A request older than 60 s is dropped.

## The launcher

`jam vscode install` adds a **Jam Hub (<host>)** launcher with the J logo (`Jam Hub (<host>) VSCodium` or `… Insiders` for those editors, picked from `--code`);
it opens `<scheme>://aethons.jam/hub?jam=<Jam URL>`, which starts the editor and opens or focuses the hub. A rerun replaces it; `--no-launcher` skips it.

- **macOS:** `~/Applications/Jam Hub (<host>).app`, a tiny unsigned app that runs `open` on the link. Drag it to the Dock to keep it there.
- **Linux:** `~/.local/share/applications/jam-hub-<host>.desktop` (icon in `~/.local/share/icons/hicolor/512x512/apps/jam-hub.png`), shown in the app menu; it runs `xdg-open` on the link.
- **Elsewhere:** no launcher; the install prints the link to open instead.
- A launcher for a Jam other than `jam.url` still opens the hub, with a warning naming both (one hub per Jam comes later).
- A launcher that can't be written (e.g. no writable home) is a warning; the extension is installed regardless.
