---
summary: The Jam VS Code extension (`aethons.jam`), in VS Code or VSCodium — installing it with `jam vscode install` and its update offers, signing in with an app token, opening a session's studio window over Remote-SSH, the hub, the rail, conversation and board tabs and the four session views, notifications and the needs-you badge, what it writes to your machine, and fixing its errors.
read_when: You want to open an agent's session or studio in VS Code or VSCodium from the Jam app or a link, are installing, updating or signing in the Jam extension (`jam vscode install`, the update offer), are using or changing a Jam view (Thinking, Previews, Status, Artifacts), a conversation tab or a board tab, or want VS Code or OS notifications and the Jam badge (or none show on macOS), or it showed an error (host identification changed, participant sign-in missing, jam not found, can't SSH into, signed out).
owns: installing and using the Jam VS Code extension (`jam vscode install`, the `/me/vscode` routes, the update check); its settings (`jam.url`, `jam.cliPath`); Sign In/Out; opening a session's studio window and the connected mark; conversation tabs, the four views, board tabs, its empty states and link rules; VS Code and OS notifications; and what starts the extension; what the extension writes (ssh_config block, key, cert, token file); `remote.SSH.localServerDownload`; VSCodium and Open Remote - SSH; its troubleshooting
prereqs: collaboration.md for who may SSH and the ssh_config block the CLI writes; app-tokens.md for the sign-in token
tier: leaf
updated: 2026-10-31
---

# The Jam VS Code extension

The extension opens a session's studio window in VS Code with one click, over Remote-SSH,
with no `jam login --participant` or `jam ssh-config` step.
It signs in to your Jam with an app token, writes the SSH setup, and opens `/home/agent/workspace`.
It also adds the Jam views and board tabs (below).
Its build and manual test are in [vscode-checklist.md](vscode-checklist.md).

## Install and requirements

Your Jam serves the extension it was released with. Install it with:

```
jam vscode install            # or: --code code-insiders, --code /path/to/code
```

It signs in as `jam ssh-config` does (the extension's token file, else `jam login --participant`, else the operator login),
downloads the `.vsix` to `~/.config/jam/vscode/`, and runs `code --install-extension <file> --force`.
Without `code` on `PATH` (on macOS it also tries the one inside *Visual Studio Code.app*) it keeps the file and says so:
install it with *Extensions: Install from VSIX…*.
It also adds a **Jam Hub launcher** for that Jam and editor (`--no-launcher` skips it): see [vscode-hub.md](vscode-hub.md#the-launcher).
A Jam built without the extension (a dev build), or one older than this, says so; use the release asset
`jam-vscode_<version>.vsix` (or the CI artifact `jam-vscode`) instead.

It needs:

- the `jam` CLI from the same release or newer (the window's `ssh-proxy` is `jam`'s),
- Microsoft's Remote - SSH extension (the extension offers to install it);
  in VSCodium, Open Remote - SSH instead (see [VSCodium](#vscodium)),
- macOS or Linux (Windows is refused).

## Updates

A signed-in window asks your Jam for its extension at start-up and every 6 h (`GET /me/vscode`).
When Jam's is **newer** than the installed one it offers *Update* or *Later*:

- **Update** downloads `/me/vscode/jam.vsix`, installs it, and offers *Reload Window* (reload other VS Code windows too).
- **Later** holds that version back for a day in every window; a newer one is offered at once.
- One window offers each version per 6 h period (the notification lock, below).
- A Jam rolled back to an older extension never downgrades you, a dev build (`0.0.0`) never checks,
  and a failed check only writes a line to the Jam output channel.

The routes: `GET /me/vscode` answers `{"available":true,"version":"0.<N>.0"}` (`{"available":false}` when the Jam has none)
and `GET /me/vscode/jam.vsix` the file. They take an app token or a `jam login --participant` ID token;
operators also have `/admin/vscode` and `/admin/vscode/jam.vsix`. How the `.vsix` gets into `jam`: [vscode-checklist.md](vscode-checklist.md#build).

## Settings

All are **application-scoped**: only your user settings apply, never a workspace's.
A repository you open can't point the extension at its own Jam or make it run its own binary.

| Setting | Meaning |
|---------|---------|
| `jam.url` | Your Jam's address, `https:` only (`http:` for localhost): the default Jam. |
| `jam.urls` | More Jams' addresses (same rules); each gets its own sign-in and hub ([vscode-hub.md](vscode-hub.md#several-jams)). *Jam: Add a Jam…* appends here. |
| `jam.cliPath` | Absolute path to `jam`; empty searches `PATH`. |

## Sign in and out

*Jam: Sign In* opens the browser to a consent page that names "VS Code on <host>".
On approval the extension keeps the app token in VS Code's SecretStorage
and in `~/.config/jam/default-app-token.json` (`$XDG_CONFIG_HOME/jam/…`; 0600), which the `jam` CLI reads.
Opening a session rewrites that file from the stored sign-in if it is missing or stale.
*Jam: Sign Out* clears both.
Each Jam has its own sign-in: *Jam: Sign In* and *Sign Out* act on the window's Jam ([vscode-hub.md](vscode-hub.md#several-jams));
a Jam other than `jam.url` keeps its token under its own secret and in `vscode-<host>-app-token.json` (`<host>` with `:` as `-`).
Revoke it from the web app's **Connected apps** ([app-tokens.md](app-tokens.md));
the next open then reports you signed out and offers Sign In.

## Opening a session

Three ways in, all the same flow:

- the **Open in VS Code** or **Open in VSCodium** button in the session header ([session-view.md](session-view.md)),
  a `vscode://aethons.jam/session/<id>` (or `vscodium://…`) link;
- the same link from anywhere (`open vscode://aethons.jam/session/<id>`);
- *Jam: Open Session…*, which lists your live sessions.

Each one signs you in if needed and checks the session exists. If it doesn't, it says so and links the web app. Then it opens the session's **studio window**, `vscode-remote://ssh-remote+jam-<studio>/home/agent/workspace`, over Remote-SSH, in a new window, from a local workspace file whose title bar is the session's color ([session-colors.md](session-colors.md)). If that window is already open, VS Code brings it to the front instead.

Before opening, it:
- checks you may SSH into the session. If you can't, it says "You can't SSH into …" and offers the web app;
- checks Remote-SSH is installed, and offers to install it if not;
- writes the SSH setup (below) and renews the cert. The cert is read before `ssh-proxy` can renew it, so this runs on every open.

If the studio isn't running, the cert step says so and nothing opens. Opening never starts a studio.

**Closing the studio window** closes the SSH connection, whose heartbeats would otherwise keep the studio from idling.

**The connected mark.** A studio window marks its session in VS Code's global state (`jam.connected.<session>`: "a studio window for this session is open"). The mark is set when the window starts, refreshed every 30 s, and cleared when it closes. A mark not refreshed for 90 s counts as gone, e.g. after a crash or when VS Code quit before clearing it.

## The rail and the hub

Every window has the **rail** (the Conversations view), and one **hub** window holds conversations as tabs;
a click opens the conversation's hub tab, a double-click the session's studio window: [vscode-hub.md](vscode-hub.md).

## Panes

A **conversation tab** is the web app's conversation pane (composer, attachments and asks inline) running in a webview;
the four session views and the board tabs follow the same rules.
It asks the extension for everything; the webview never holds your token, the Jam URL or a cookie.

- **Views:** **Thinking** (the panel's Jam Thinking container; it wants width), **Previews**, **Session Status** and **Artifacts** (Previews and Artifacts start collapsed), in the Jam container under the rail. All are draggable anywhere, to the panel or a sidebar.
- **One session per window:** the views show only in a studio window, where they always show its session, and in the hub ([vscode-hub.md](vscode-hub.md#the-hub)). The window's remote comes from its workspace folder URI. Other windows show only the rail. A board tab shows its own board.
- **The studio window's conversation tab** opens the first time the window opens; after that it comes back with the window, or stays closed if you closed it; a rail click on the window's own session opens it again. A studio window opens no other conversation's tab: those go to the hub.
- **Thinking:** the web app's Thinking log. The window's Thinking views share one connection per (session, stream), at most 2 feeds per pane; a view opened later gets up to 2,000 replayed events; it resumes after a network drop. It counts toward your four server feeds, and a hidden view holds none.
- **Previews:** Open shows the app in the Simple Browser and Browser opens the system browser. Only the window's own studio is forwarded through Remote-SSH (`asExternalUri`); an unchanged `localhost` URI counts as forwarded, and only a throw falls back to the preview's web URL in the browser. Another session's preview opens its preview URL in the browser.
- **Session Status:** the session's [status card](session-card.md) (as on its rail row, full size), then the session header's facts and actions, then the Status rows. *Open in VS Code* there focuses this window.
- **Artifacts:** a file opens in the browser, a board thumb opens a board tab, and an ask reveals it in the session's conversation tab (opening the tab if needed).
- **Board tabs:** one per board, several side by side, kept while hidden (`retainContextWhenHidden`) and restored after a reload. VS Code restores a background tab only when it is first shown, so a board opened again before then briefly has two tabs; showing the restored one closes it and reveals the other. The title is the board's, capped at 60 characters, "Whiteboard" if empty. *Open in the web app* is in the tab. A stroke made in the last 300 ms before closing a tab may not be saved.
- **Empty states:** "Sign in to Jam to see this conversation." (with Sign in); "Open a session's conversation to see its …" (a view in the hub with no session); "That conversation is gone, or you can't see it." (a tab); "That session has ended, or you can't see it.". Only Sign in (and Try again, below) has an action.
  A failed lookup shows "Couldn't load the focused session: …" with Try again.
- **Links:** the pane never navigates. `https:`, `http:` and `mailto:` links open in the browser; boards open as board tabs. `/app/user/c/<other>` opens the web app; `#ask-…` and own-conversation links scroll in place. `vscode://aethons.jam/session/<id>` (or `vscode-insiders:`) opens that session's window; `javascript:`, `command:`, `file:` and any other `vscode:` link do nothing.
- **Push focus:** while a conversation tab is the active editor in a focused window, the extension reports its conversation as focused, so Web Push stays quiet for it. The suppression also applies to VS Code notifications, and works without Web Push configured.
- **Live updates:** one stream per window, held from startup while signed in (no pane needs to be open), feeds the views and notifications; it restarts on sign-in, sign-out and when `jam.url` changes. Where Jam offers no live updates, the view polls every 30 s.

## Notifications and the badge

The extension starts with VS Code (`onStartupFinished`), in every window, and every signed-in window holds one live stream.
What notifies is the phone's rule ([push.md](push.md#what-notifies)), not restated here.

- **One window per event, usually the focused one.** The focused window gets a 250 ms head start, so a lagging stream can let an unfocused window win; it then also shows an OS banner while VS Code is focused. Windows agree through a lock file in the extension's global storage, so a second VS Code profile, and VS Code Insiders, each notify separately. If the lock can't be written (e.g. read-only storage), every window notifies and the Jam output channel says why, once per window.
- **Rate:** at most one message notification per conversation per 5 s; asks, call-ins and escalations always notify. An ask shows as a warning, a message as information. Link syntax in a message (`[text](command:…)`, `https:`, `file:`) shows as text, never as a clickable link.
- **Open**, and the OS notification (`vscode://aethons.jam/go/<chn>?session=<id>`), [go to the conversation](vscode-hub.md#the-hub): the session's studio window if open, else its hub tab.
- **OS notifications** show only while the winning window isn't focused:
  - macOS uses the bundled `terminal-notifier` 3.0.0 (vendored from node-notifier, ad-hoc signed, not notarized, shipped whole with Apple's `Terminal.icns`; the .vsix is private). It is listed as *terminal-notifier* in System Settings → Notifications, where you allow or mute it. The first notification waits up to 120 s for the permission prompt; if it times out, only that note falls back to `osascript`. A declined permission shows nothing and does not fall back. If the notifier can't run it falls back to `osascript`, whose banners can't open a session.
  - Linux uses `notify-send`, with a click only when it supports `--action` (libnotify 0.7.10+); at most one waits per conversation and 8 overall. Without `notify-send` you get toasts only, and one line in the Jam output channel.
  - Other platforms get VS Code notifications only.
- **Closed VS Code:** nothing is queued (the phone still gets Web Push); a user with both gets both.
- **Lock screen:** body visibility follows [`preview`](push.md#privacy-and-previews) and the OS's settings.
- **The badge:** the rail's needs-you count ([vscode-hub.md](vscode-hub.md#the-rail)).
- **Signed out:** no notifications, no badge. A revoked token shows "sign-in expired" at startup.

## What it writes

- **`~/.ssh/config`:** a `Host jam-<studio>` block between `# >>> jam <studio> >>>` and `# <<< jam <studio> <<<`,
  with the marker line `# managed by the Jam VS Code extension (aethons.jam)` right after the begin marker.
  The block is the one `jam ssh-config` writes ([collaboration.md](collaboration.md#the-ssh-commands)), plus the marker; a rerun replaces it.
  A block `jam ssh-config` wrote (no marker), or a hand-written `Host jam-<studio>`, is **left alone**.
- **`~/.config/jam/ssh/id_ed25519`** (the CLI's key, shared) and **`<studio>-cert.pub`** (an 8 h cert, renewed on open;
  the CLI's `ssh-proxy` renews it when a connection starts).
- **The token file** above.
- **`<globalStorage>/studios/jam-<studio>.code-workspace`:** a studio window's workspace file (its remote folder and title-bar colors), rewritten on each open ([session-colors.md](session-colors.md)).

The ProxyCommand pins `--participant` (and, for a Jam other than `jam.url`, `--app vscode-<host>`, its token profile); `jam` uses the token file when it names the same Jam
(see [collaboration.md](collaboration.md)).

## `remote.SSH.localServerDownload`

A studio's egress is locked down, so it can't fetch VS Code Server itself.
The first time, the extension offers to set Remote-SSH's `remote.SSH.localServerDownload` to `always`
(VS Code downloads it locally and uploads it). It asks once. VSCodium never asks: see below.

## VSCodium

Microsoft's Remote - SSH is licensed to VS Code only, so in VSCodium the extension uses
[Open Remote - SSH](https://github.com/jeanp413/open-remote-ssh) (`jeanp413.open-remote-ssh`, 0.5.1 or newer) and offers to install it.
Install it with `jam vscode install --code codium`. Sign-in, links and notifications use `vscodium://`; the token file is shared with VS Code.

- **Server download in the studio.** Open Remote - SSH has no local download: the studio fetches
  `vscodium-reh-…` from GitHub releases (a redirect to `release-assets.githubusercontent.com`).
  A studio whose egress doesn't allow that can't install the server.
- **Certificate login.** Its built-in ssh2 client can't present `CertificateFile`. The block's
  `PreferredAuthentications publickey,gssapi-with-mic` makes it fall back to your `ssh`, which can;
  OpenSSH still tries the cert first. Each connect costs one failed ssh2 attempt first, and the first
  connect to a studio asks once to trust its host key.
  A block written by an older `jam ssh-config` lacks the line: rerun `jam ssh-config --write <studio>`.

## Troubleshooting

| Symptom | Fix |
|---------|-----|
| Remote-SSH: host identification changed (after a studio upgrade) | Run *Jam: Forget Host Key* (in the window, or pick the studio), then open the session again. |
| "your participant sign-in is missing" from `ssh-proxy` | The `jam` CLI predates the extension. Install `jam` from the same release or newer. |
| "The jam CLI was not found" | Install `jam` or set `jam.cliPath` to its absolute path. |
| "You can't SSH into …" | The session isn't shared with you; ask an operator ([collaboration.md](collaboration.md#who-may-connect)). The button opens the web app. |
| No OS banners on macOS | System Settings → Notifications → *terminal-notifier* must allow them. If the Jam output channel says the notifier was blocked (Gatekeeper, on a quarantined copy), run `xattr -dr com.apple.quarantine` on the extension's `dist/vendor`, or rely on the `osascript` fallback. |
| `jam vscode install`: "this Jam doesn't serve the VS Code extension" or "built without" | The Jam predates serving it, or is a dev build. Install the release asset with *Extensions: Install from VSIX…*. |
| Signed out after a revoke | Run *Jam: Sign In*. A revoked token also makes `ssh-proxy` fail with a "VS Code sign-in … was revoked" message. |
