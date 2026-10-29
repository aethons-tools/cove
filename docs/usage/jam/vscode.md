---
summary: The Jam VS Code extension (`aethons.jam`) — installing the .vsix, signing in with an app token, opening a session's window and connecting it to the studio over Remote-SSH on demand, the five views, board tabs and the focused session, notifications and the Needs You badge, what it writes to your machine, and fixing its errors.
read_when: You want to open an agent's session or studio in VS Code from the Jam app or a link, or connect/disconnect a window so the studio can idle, are installing or signing in the Jam extension, are using or changing a Jam view (Conversation, Thinking, Previews, Status, Artifacts) or a board tab, Jam: Focus Session or pinning, or want VS Code or OS notifications and the Jam badge (or none show on macOS), or it showed an error (host identification changed, participant sign-in missing, jam not found, can't SSH into, signed out).
owns: installing and using the Jam VS Code extension; its settings (`jam.url`, `jam.cliPath`); Sign In/Out; opening a session; Connect/Disconnect Studio and local session windows; the five views, board tabs, the focused session, pin/unpin, its empty states and link rules; VS Code and OS notifications, the Needs You view and badge, and what starts the extension; what the extension writes (ssh_config block, key, cert, token file); `remote.SSH.localServerDownload`; its troubleshooting
prereqs: collaboration.md for who may SSH and the ssh_config block the CLI writes; app-tokens.md for the sign-in token
tier: leaf
updated: 2026-10-29
---

# The Jam VS Code extension

The extension opens a session's window in VS Code with one click, and connects it to the studio
over Remote-SSH only when you want files or a terminal, with no `jam login --participant` or `jam ssh-config` step.
It signs in to your Jam with an app token and, on Connect, writes the SSH setup and opens `/home/agent/workspace`.
It also adds the Jam views and board tabs (below).
Its build and manual test are in [vscode-checklist.md](vscode-checklist.md).

## Install and requirements

Install the `.vsix` with *Extensions: Install from VSIX…*:
the CI artifact `jam-vscode`, or the release asset `jam-vscode_<version>.vsix`.

It needs:

- the `jam` CLI from the same release or newer (the window's `ssh-proxy` is `jam`'s),
- Microsoft's Remote - SSH extension (the extension offers to install it),
- macOS or Linux (Windows is refused).

## Settings

Both are **application-scoped**: only your user settings apply, never a workspace's.
A repository you open can't point the extension at its own Jam or make it run its own binary.

| Setting | Meaning |
|---------|---------|
| `jam.url` | Your Jam's address, `https:` only (`http:` for localhost). |
| `jam.cliPath` | Absolute path to `jam`; empty searches `PATH`. |

## Sign in and out

*Jam: Sign In* opens the browser to a consent page that names "VS Code on <host>".
On approval the extension keeps the app token in VS Code's SecretStorage
and in `~/.config/jam/default-app-token.json` (`$XDG_CONFIG_HOME/jam/…`; 0600), which the `jam` CLI reads.
Opening a session rewrites that file from the stored sign-in if it is missing or stale.
*Jam: Sign Out* clears both.
Revoke it from the web app's **Connected apps** ([app-tokens.md](app-tokens.md));
the next open then reports you signed out and offers Sign In.

## Opening a session

Three ways in, all the same flow:

- the **Open in VS Code** button in the session header ([session-view.md](session-view.md)),
  a `vscode://aethons.jam/session/<id>` link;
- the same link from anywhere (`open vscode://aethons.jam/session/<id>`);
- *Jam: Open Session…*, which lists your live sessions.

The extension signs you in if needed, checks the session exists (otherwise it says so and links the web app),
and opens the session's **local window**: a workspace file with no folders,
`<global storage>/sessions/<session>.code-workspace`, so a second open focuses the same window.
It holds no SSH connection, so the studio can idle; the Jam views work in it as usual.
A session you connected (below) opens, or focuses, its studio window instead.

### Connect and disconnect

- **Connect studio** (status bar, or *Jam: Connect to Studio*), in a local session window:
  checks you may SSH into the session (otherwise it says so and links the web app), writes the SSH setup below,
  and reloads the **same window** onto `vscode-remote://ssh-remote+jam-<studio>/home/agent/workspace`.
- **Disconnect studio** (status bar, or *Jam: Disconnect Studio*), in a studio window: reloads it back to the local window;
  Remote-SSH closes the connection, whose heartbeats would otherwise keep the studio from idling.
- Connected sessions are remembered in VS Code's global state (set on Connect, cleared on Disconnect and when the studio window closes),
  so a link opens the studio window rather than a second, local one. A stale entry just opens the studio window.

## Panes and focus

The **Jam** icon in the activity bar holds the **Conversation** view: the web app's conversation pane
(composer, attachments and asks inline) running in a webview. Four more views and the board tabs follow the same rules.
It asks the extension for everything; the webview never holds your token, the Jam URL or a cookie.

- **Views:** Conversation (activity bar), **Thinking** (the panel's Jam Thinking container; it wants width), **Previews**, **Status** and **Artifacts** (Previews and Artifacts start collapsed). All are draggable anywhere, to the panel or a sidebar.
- **Focused session:** by default the window's session: a local session window's, or the studio's (the window's remote comes from its workspace folder URI).
  *Jam: Focus Session…* picks another session, or *This window's studio* to go back; signed out, it starts sign-in first.
  Every unpinned view follows it (a board tab shows its own board), and the choice is kept per workspace, so it survives reopening the folder.
- **Pin:** every view's title-bar pin button fixes it to the session it shows; the pinned button unpins it.
  When a view's session isn't the window's, its label shows beside the view's name.
- **Thinking:** the web app's Thinking log. The window's Thinking views share one connection per (session, stream), at most 2 feeds per pane; a view opened later gets up to 2,000 replayed events; it resumes after a network drop. It counts toward your four server feeds, and a hidden view holds none.
- **Previews:** Open shows the app in the Simple Browser and Browser opens the system browser. Only the window's own studio, when connected, is forwarded through Remote-SSH (`asExternalUri`); an unchanged `localhost` URI counts as forwarded, and only a throw falls back to the preview's web URL in the browser. Another session's preview, or any in a local session window, opens its preview URL in the browser.
- **Status:** the session header's facts and actions, then the Status rows. *Open in VS Code* on a pinned session opens that session's window.
- **Artifacts:** a file opens in the browser, a board thumb opens a board tab, and an ask reveals it in the Conversation view; an ask from a session the Conversation view doesn't show doesn't scroll.
- **Board tabs:** one per board, several side by side, kept while hidden (`retainContextWhenHidden`) and restored after a reload. VS Code restores a background tab only when it is first shown, so a board opened again before then briefly has two tabs; showing the restored one closes it and reveals the other. The title is the board's, capped at 60 characters, "Whiteboard" if empty. *Open in the web app* is in the tab. A stroke made in the last 300 ms before closing a tab may not be saved.
- **Empty states:** "Sign in to Jam to see this conversation." (Sign in); "This window isn't on a Jam studio. Focus a session to see its conversation." (a local folder; Focus a session…); "That session has ended, or you can't see it." (Focus a session…).
  A failed lookup shows "Couldn't load the focused session: …" with Try again.
- **Links:** the pane never navigates. `https:`, `http:` and `mailto:` links open in the browser; boards open as board tabs. `/app/user/c/<other>` opens the web app; `#ask-…` and own-conversation links scroll in place. `vscode://aethons.jam/session/<id>` (or `vscode-insiders:`) opens that session's window; `javascript:`, `command:`, `file:` and any other `vscode:` link do nothing.
- **Push focus:** while the view is visible in a focused window, the extension reports its conversation as focused, so Web Push stays quiet for it. The suppression also applies to VS Code notifications, and works without Web Push configured.
- **Live updates:** one stream per window, held from startup while signed in (no pane needs to be open), feeds the views and notifications; it restarts on sign-in, sign-out and when `jam.url` changes. Where Jam offers no live updates, the view polls every 30 s.

## Notifications and the badge

The extension starts with VS Code (`onStartupFinished`), in every window, and every signed-in window holds one live stream.
What notifies is the phone's rule ([push.md](push.md#what-notifies)), not restated here.

- **One window per event, usually the focused one.** The focused window gets a 250 ms head start, so a lagging stream can let an unfocused window win; it then also shows an OS banner while VS Code is focused. Windows agree through a lock file in the extension's global storage, so a second VS Code profile, and VS Code Insiders, each notify separately. If the lock can't be written (e.g. read-only storage), every window notifies and the Jam output channel says why, once per window.
- **Rate:** at most one message notification per conversation per 5 s; asks, call-ins and escalations always notify. An ask shows as a warning, a message as information. Link syntax in a message (`[text](command:…)`, `https:`, `file:`) shows as text, never as a clickable link.
- **Open**, and the OS notification, open the session's code window (`vscode://aethons.jam/session/<id>`), or the web app for a chat.
- **OS notifications** show only while the winning window isn't focused:
  - macOS uses the bundled `terminal-notifier` 3.0.0 (vendored from node-notifier, ad-hoc signed, not notarized, shipped whole with Apple's `Terminal.icns`; the .vsix is private). It is listed as *terminal-notifier* in System Settings → Notifications, where you allow or mute it. The first notification waits up to 120 s for the permission prompt; if it times out, only that note falls back to `osascript`. A declined permission shows nothing and does not fall back. If the notifier can't run it falls back to `osascript`, whose banners can't open a session.
  - Linux uses `notify-send`, with a click only when it supports `--action` (libnotify 0.7.10+); at most one waits per conversation and 8 overall. Without `notify-send` you get toasts only, and one line in the Jam output channel.
  - Other platforms get VS Code notifications only.
- **Closed VS Code:** nothing is queued (the phone still gets Web Push); a user with both gets both.
- **Lock screen:** body visibility follows [`preview`](push.md#privacy-and-previews) and the OS's settings.
- **Needs You view** (Jam container, collapsed) lists what needs you, from the rail's needs-you group, refreshed at once on start-up, sign-in and sign-out, and with a 3 s debounce on live changes; its count is the Jam icon's badge. Selecting a row opens that conversation.
- **Signed out:** no notifications, no badge. A revoked token shows "sign-in expired" at startup.

## What it writes

- **`~/.ssh/config`:** a `Host jam-<studio>` block between `# >>> jam <studio> >>>` and `# <<< jam <studio> <<<`,
  with the marker line `# managed by the Jam VS Code extension (aethons.jam)` right after the begin marker.
  The block is the one `jam ssh-config` writes ([collaboration.md](collaboration.md#the-ssh-commands)), plus the marker; a rerun replaces it.
  A block `jam ssh-config` wrote (no marker), or a hand-written `Host jam-<studio>`, is **left alone**.
- **`~/.config/jam/ssh/id_ed25519`** (the CLI's key, shared) and **`<studio>-cert.pub`** (an 8 h cert, renewed on open;
  the CLI's `ssh-proxy` renews it when a connection starts).
- **The token file** above.

The ProxyCommand pins `--participant`; `jam` uses the token file when it names the same Jam
(see [collaboration.md](collaboration.md)).

## `remote.SSH.localServerDownload`

A studio's egress is locked down, so it can't fetch VS Code Server itself.
The first time, the extension offers to set Remote-SSH's `remote.SSH.localServerDownload` to `always`
(VS Code downloads it locally and uploads it). It asks once.

## Troubleshooting

| Symptom | Fix |
|---------|-----|
| Remote-SSH: host identification changed (after a studio upgrade) | Run *Jam: Forget Host Key* (in the window, or pick the studio), then reconnect. |
| "your participant sign-in is missing" from `ssh-proxy` | The `jam` CLI predates the extension. Install `jam` from the same release or newer. |
| "The jam CLI was not found" | Install `jam` or set `jam.cliPath` to its absolute path. |
| "You can't SSH into …" | The session isn't shared with you; ask an operator ([collaboration.md](collaboration.md#who-may-connect)). The button opens the web app. |
| No OS banners on macOS | System Settings → Notifications → *terminal-notifier* must allow them. If the Jam output channel says the notifier was blocked (Gatekeeper, on a quarantined copy), run `xattr -dr com.apple.quarantine` on the extension's `dist/vendor`, or rely on the `osascript` fallback. |
| Signed out after a revoke | Run *Jam: Sign In*. A revoked token also makes `ssh-proxy` fail with a "VS Code sign-in … was revoked" message. |
