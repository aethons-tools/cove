---
summary: Building the Jam VS Code extension (`just vscode`, the `.vsix`, its version, the vendored macOS notifier, tests) and the by-hand end-to-end checklist, since VS Code can't run in CI.
read_when: You changed `web/vscode` or are cutting a release with it, and must build it or verify it by hand.
owns: the extension's build (`just vscode`/`vscode-check`, scripts/vscode.sh, the 0.<N>.0 version, the CI artifact and release asset, the `.vsix` jam embeds via scripts/stage-vscode.sh), its unit-test layout, the ssh-block golden, and the manual checklist
prereqs: vscode.md for what the extension does
tier: leaf
updated: 2026-10-30
---

# Building and verifying the VS Code extension

`web/vscode` is a separate npm package (esbuild bundle, vitest) built into a `.vsix`, with the panes' webview bundle from `web/app`.
The unit tests run in CI; VS Code itself can't, so the checklist below is run by hand.
What the extension does: [vscode.md](vscode.md).

## Build

- `just vscode` runs `scripts/vscode.sh`: `npm ci`, `npm run check` (typecheck and tests), the esbuild bundle, then `vsce package` into `web/vscode/dist/jam.vsix`. It needs Node and npm.
- The webview bundle is `web/app/vite.vscode.config.ts`, which `scripts/vscode.sh` runs before the esbuild step; its output is `web/vscode/dist/webview/` (`pane.js`, `pane.css`), and the script fails without them. It also copies Excalidraw's fonts (all but Xiaolai) to `dist/webview/excalidraw/fonts/` for the board tabs, and fails if no `.woff2` lands.
- The macOS notifier: `scripts/vscode.sh` vendors `terminal-notifier.app` (from `node-notifier@12.0.0`, into `dist/vendor/mac.noindex/`, with its MIT licence as `dist/vendor/terminal-notifier-LICENSE`.
- `just vscode-check` runs only `npm ci` and `npm run check`.
- `VSCODE_SKIP_CHECK=1` makes `scripts/vscode.sh` skip `npm run check`; the release leg sets it, as the gate already ran the checks on that commit.
- **Version:** `package.json` stays `0.0.0`; `VERSION=<N>-<MMDD>` stamps `0.<N>.0` (vsce needs SemVer). `VSIX_OUT` names the output.
- **CI:** the gate builds it and uploads the artifact `jam-vscode` (`jam.vsix`). A release attaches `jam-vscode_<version>.vsix` beside the binaries.
- **Embedded in `jam`:** a goreleaser before-hook runs `scripts/stage-vscode.sh`, which builds it into `internal/jam/vscodeext/vsix/jam.vsix` (git-ignored; only `.keep` is committed) for `jam`'s `//go:embed`. The release asset is that same file, so Jam serves exactly what the release ships ([vscode.md](vscode.md#updates)). A plain `go build` embeds none, and Jam answers `available: false`. The extension's version is read from the `package.json` inside the `.vsix`.

## Tests

`web/vscode/test/`: the logic is in pure modules (`open`, `auth`, `ssh/*`, `uri`, …) tested directly with injected dependencies.
`src/extension.ts` is the only module importing `vscode`; `test/vscode.ts` is the mock for it.
`test/fixtures/ssh-block.txt` is the golden for the `Host` block: the TypeScript test and Go's `cmd/jam/ssh_apptoken_test.go` both compare against it, so the extension and `jam ssh-config` stay byte-compatible. Change both sides together.

## Verify first (spec assumptions)

- [ ] With `remote.SSH.localServerDownload: always`, the studio makes no download attempt (the studio's squid log shows no `update.code.visualstudio.com`).
- [ ] Whether VS Code asks for confirmation when a `vscode://aethons.jam/…` link opens a remote.
- [ ] `vscode.openFolder` with `forceNewWindow` focuses an existing window on a `jam-` remote folder rather than opening a second.
- [ ] Closing the studio window drops the SSH connection, and the studio idles.
- [ ] `vscode.env.asExternalUri(http://localhost:<port>/)` from the (UI) Jam extension in a `jam-` remote window adds a forward in the Ports view and returns a working local URL (if not, Open falls back to the preview's web URL in the browser).
- [ ] Excalidraw in a board tab: CJK text (Xiaolai isn't shipped) and image export may log CSP violations from the font fallback; record what is seen and accept or document it — the CSP doesn't change.
- [ ] A restored board panel (after a window reload) keeps `retainContextWhenHidden`: hide and show it and the drawing stays.
- [ ] `jam vscode install` against a released Jam installs the extension with `code`, and, with `code` off `PATH` on macOS, with the app bundle's CLI.
- [ ] With an older extension installed, a signed-in window offers the update; *Update* installs it through `workbench.extensions.installExtension` with a `file:` URI under global storage, and *Reload Window* comes up on the new version. With two windows open, only one offers.
- [ ] Upload a file once from a conversation tab (the `ArrayBuffer` check from slice 3).
- [ ] Installed from the .vsix on macOS, `dist/vendor/mac.noindex/terminal-notifier.app` runs: it keeps its exec bit (or the chmod restores it), Gatekeeper doesn't block it, and the first notification asks for permission. Clicking a notification opens `vscode://aethons.jam/session/<id>`.
- [ ] A `WebviewView.badge` on the `jam.rail` view shows on the Jam activity-bar icon while the Jam container is closed.
- [ ] `notify-send --action … --wait` on the target Linux desktops (GNOME, KDE) prints `default` on a click.
- [ ] `osascript -e 'on run argv' … -- <title> <body>` passes `--` through or not: if `item 1 of argv` comes out as `--`, drop the `--` from `osascriptArgs`.
- [ ] terminal-notifier 3.0 (UNUserNotificationCenter; its first run waits on the permission prompt, and output saying "declined" is a quiet stop, never an osascript fallback) still displays on the current macOS: a silent non-display exits 0, so no fallback triggers.
- [ ] `go test -race ./internal/jam/notify/ ./internal/jam/userui/` passes on a host with gcc.

## Steps

- [ ] Install the `.vsix` (*Install from VSIX…*).
- [ ] Set `jam.url`, run Sign In; the consent page names "VS Code on <host>"; Connected apps lists it.
- [ ] Open in VS Code from the session header: the studio window opens on `/home/agent/workspace`, the terminal works, and its Jam views show the session. There's no status-bar item.
- [ ] The same link again focuses that window.
- [ ] Close the studio window: the studio can idle. The link opens it again.
- [ ] `open vscode://aethons.jam/session/<id>` from a terminal opens it.
- [ ] *Jam: Open Session…* lists live sessions.
- [ ] A member without SSH gets "You can't SSH into …" with the web app link, and no window opens.
- [ ] A window left open on an old local session workspace (`…/sessions/<id>.code-workspace`) shows no Jam views after upgrading, only the rail.
- [ ] After an existing `jam ssh-config --write`, the block is unchanged.
- [ ] `jam ssh-config --prune --dry-run` handles an extension block.
- [ ] Revoke in Connected apps: the next open says signed out, and `ssh-proxy` fails with the VS Code message.
- [ ] Sign Out deletes the token file.
- [ ] Upgrade the studio: host-key error, *Jam: Forget Host Key*, reconnect works.
- [ ] VS Code Insiders sign-in works.

VSCodium (with the `.vsix` installed in VSCodium):

- [ ] *Open in VSCodium* opens VSCodium; sign-in returns through `vscodium://` and the consent page names "VSCodium on <host>".
- [ ] *Open in VSCodium* without Open Remote - SSH offers to install `jeanp413.open-remote-ssh`, and never asks about localServerDownload.
- [ ] With it installed, *Open in VSCodium* asks once to trust the host key, then the studio window opens on `/home/agent/workspace` (the Remote - SSH log shows the local `ssh` fallback) and the terminal works.
- [ ] The studio installed `vscodium-reh-…` itself (`~/.vscodium-server`).
- [ ] Closing the window and the conversation tab work as in VS Code.
- [ ] VS Code still connects with the new `PreferredAuthentications` line.

Conversation tabs and views:

- [ ] The J mark shows in the Extensions view (colour), the activity bar and the Thinking panel (tinted), on board tabs (colour) and in Linux notifications.
- [ ] The Jam icon shows, and the conversation tab renders for the window's session in the light, dark and high-contrast themes.
- [ ] Send a message with an attachment and watch progress, then send an image and see the thumbnail.
- [ ] Answer an ask inline.
- [ ] A message from the agent appears without a reload (live).
- [ ] The views have no pin buttons, and there is no *Jam: Focus Session…*.
- [ ] Drag the Status view to the panel and to the secondary sidebar.
- [ ] In a studio window, a rail click on its own session reopens its closed conversation tab.
- [ ] Reload the hub with a background conversation tab, then click its rail row: a second tab opens; showing the restored one closes it (as with boards).
- [ ] A window on a local folder shows no Jam views except the rail (Conversations).
- [ ] Rail: the groups, missions and unread counts match the web app's rail.
- [ ] Rail: the hub's active conversation tab is the highlighted row, and moves with the tab; in a studio window, its own conversation is.
- [ ] Jam's views take the VS Code theme's font, buttons, list selection, badges and inputs; switching the theme restyles them.
- [ ] Rail *New message*: pick a person, send; the conversation opens as a hub tab (from an ordinary window too).
- [ ] Rail *New session*: start one (with no studio access needed); its conversation opens in the hub.
- [ ] Rail click on a session with no studio window opens its tab in the hub (opening the hub, whether it was closed or open); with one open, it brings that window to the front.
- [ ] *Jam: Open Hub* opens the hub; again, it focuses it. `open vscode://aethons.jam/hub` does the same.
- [ ] macOS: `jam vscode install` adds `~/Applications/Jam Hub (<host>).app` with the J icon; it opens VS Code on the hub (VS Code closed and open) and stays in the Dock when kept there.
- [ ] Linux: the *Jam Hub (<host>)* entry shows in the app menu with the J icon and opens the hub.
- [ ] `jam vscode install --code codium` adds a `… VSCodium` launcher that opens VSCodium.
- [ ] A launcher for an unlisted Jam warns with *Add it* and opens nothing.
- [ ] Several Jams: *Jam: Add a Jam…* adds a second Jam and signs in to it; *Open its hub* opens `Jam Hub (<host>)`.
- [ ] *Jam: Switch Jam…* in an ordinary window reloads it onto the other Jam (rail, badge); a hub says it belongs to its Jam.
- [ ] A studio window opened from the second Jam reconnects to it after a reload (`~/.ssh/config` names `--app vscode-<host>`).
- [ ] A notification from the second Jam in a first-Jam window opens the second Jam's hub on that conversation.
- [ ] Removing the second Jam from `jam.urls`: a window switched to it falls back to `jam.url`.
- [ ] In the hub, a rail click opens a conversation tab; a second click reveals it. Tabs split side by side and come back after a reload.
- [ ] In the hub, Thinking/Status/Artifacts/Previews follow the active conversation tab; a chat tab empties them.
- [ ] A notification's Open (and the OS click) with the session's studio window open focuses it; without, it opens the hub tab.
- [ ] A new studio window opens its conversation tab once; close it and reload: it stays closed.
- [ ] An ask in Artifacts reveals and scrolls the conversation tab.
- [ ] Rail double-click on a session you can SSH into opens its studio window; on a chat it acts as a click.
- [ ] ▣ appears on the row within 30 s of opening the studio window, and goes once it's closed (within 90 s).
- [ ] The Jam icon's badge counts the Needs you rows and clears on sign-out.
- [ ] Open a board card and a message link: both open in the browser.
- [ ] Revoke the token while the view is open: it shows Sign in. Sign In again: the view recovers.
- [ ] Sleep the laptop for 2 minutes, wake it: the messages catch up.
- [ ] With Web Push on, the phone doesn't buzz for the visible conversation, and does once VS Code is unfocused.
- [ ] *Developer: Open Webview Developer Tools* shows no CSP violations, and `fetch("https://example.com")` in its console is blocked.

Panes:

- [ ] Each view (Thinking, Previews, Status, Artifacts) renders for the window's session in the light, dark and high-contrast themes.
- [ ] Thinking follows a busy session live, with Load earlier and the stream select.
- [ ] Two Thinking views on one session, then *Developer: Show Running Extensions* plus Jam's server log, show one feed.
- [ ] Thinking shows the window's session.
- [ ] Previews Open shows the app in the Simple Browser and a Ports-view entry. Browser opens the system browser.
- [ ] Status Interrupt and Compact work, and Release flips every pane to the ended copy.
- [ ] A board thumb in Artifacts opens a tab, and a file opens in the browser.
- [ ] Open two boards side by side and draw in one: the other client sees it live. Hide and show a tab and the drawing is still there. Reload the window and the boards come back.
- [ ] Revoke the token: every view and board shows Sign in, and recovers after Sign In.
- [ ] DevTools on the Thinking view and a board tab shows no CSP violations.

Notifications:

- [ ] An agent message in another session gives one toast (in the focused window) and no OS notification; with VS Code unfocused, one OS notification.
- [ ] With three code windows open, exactly one toast.
- [ ] Clicking an OS notification for session X opens or focuses X's window.
- [ ] An ask shows a warning toast and bumps the badge; answering it lowers the badge.
- [ ] Reading the conversation in a conversation tab, or in a focused web-app tab, gives no notification, on a Jam with and without `runtime.push`.
- [ ] A burst of messages gives at most one notification per 5 s.
- [ ] Signing out stops notifications and clears the badge; signing in restores them.
- [ ] The phone's Web Push still arrives.
- [ ] Linux without `notify-send`: toasts only, one line in the Jam output channel.
