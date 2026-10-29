---
summary: Building the Jam VS Code extension (`just vscode`, the `.vsix`, its version, the vendored macOS notifier, tests) and the by-hand end-to-end checklist, since VS Code can't run in CI.
read_when: You changed `web/vscode` or are cutting a release with it, and must build it or verify it by hand.
owns: the extension's build (`just vscode`/`vscode-check`, scripts/vscode.sh, the 0.<N>.0 version, the CI artifact and release asset), its unit-test layout, the ssh-block golden, and the manual checklist
prereqs: vscode.md for what the extension does
tier: leaf
updated: 2026-10-29
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
- **Version:** `package.json` stays `0.0.0`; `VERSION=<N>-<MMDD>` stamps `0.<N>.0` (vsce needs SemVer). `VSIX_OUT` names the output.
- **CI:** the gate builds it and uploads the artifact `jam-vscode` (`jam.vsix`). A release attaches `jam-vscode_<version>.vsix` beside the binaries.

## Tests

`web/vscode/test/`: the logic is in pure modules (`open`, `auth`, `ssh/*`, `uri`, …) tested directly with injected dependencies.
`src/extension.ts` is the only module importing `vscode`; `test/vscode.ts` is the mock for it.
`test/fixtures/ssh-block.txt` is the golden for the `Host` block: the TypeScript test and Go's `cmd/jam/ssh_apptoken_test.go` both compare against it, so the extension and `jam ssh-config` stay byte-compatible. Change both sides together.

## Verify first (spec assumptions)

- [ ] With `remote.SSH.localServerDownload: always`, the studio makes no download attempt (the studio's squid log shows no `update.code.visualstudio.com`).
- [ ] Whether VS Code asks for confirmation when a `vscode://aethons.jam/…` link opens a remote.
- [ ] `vscode.openFolder` with `forceNewWindow` focuses an existing window on that folder rather than opening a second — for a remote folder and for a session's local `.code-workspace` file.
- [ ] `vscode.openFolder` with `forceReuseWindow` from a local session window reloads it onto the `jam-` remote, and back; `workspace.workspaceFile` in the local window is the `file:` URI under global storage.
- [ ] Disconnect: after the window reloads locally, Remote-SSH drops the SSH connection and the studio idles.
- [ ] `vscode.env.asExternalUri(http://localhost:<port>/)` from the (UI) Jam extension in a `jam-` remote window adds a forward in the Ports view and returns a working local URL (if not, Open falls back to the preview's web URL in the browser).
- [ ] Excalidraw in a board tab: CJK text (Xiaolai isn't shipped) and image export may log CSP violations from the font fallback; record what is seen and accept or document it — the CSP doesn't change.
- [ ] A restored board panel (after a window reload) keeps `retainContextWhenHidden`: hide and show it and the drawing stays.
- [ ] Upload a file once from the Conversation view (the `ArrayBuffer` check from slice 3).
- [ ] Installed from the .vsix on macOS, `dist/vendor/mac.noindex/terminal-notifier.app` runs: it keeps its exec bit (or the chmod restores it), Gatekeeper doesn't block it, and the first notification asks for permission. Clicking a notification opens `vscode://aethons.jam/session/<id>`.
- [ ] A `TreeView.badge` on the collapsed `jam.needsYou` view shows on the Jam activity-bar icon while the Jam container is closed.
- [ ] `notify-send --action … --wait` on the target Linux desktops (GNOME, KDE) prints `default` on a click.
- [ ] `osascript -e 'on run argv' … -- <title> <body>` passes `--` through or not: if `item 1 of argv` comes out as `--`, drop the `--` from `osascriptArgs`.
- [ ] terminal-notifier 3.0 (UNUserNotificationCenter; its first run waits on the permission prompt, and output saying "declined" is a quiet stop, never an osascript fallback) still displays on the current macOS: a silent non-display exits 0, so no fallback triggers.
- [ ] `go test -race ./internal/jam/notify/ ./internal/jam/userui/` passes on a host with gcc.

## Steps

- [ ] Install the `.vsix` (*Install from VSIX…*).
- [ ] Set `jam.url`, run Sign In; the consent page names "VS Code on <host>"; Connected apps lists it.
- [ ] Open in VS Code from the session header: a local window opens (no Remote-SSH), its views show the session, and the status bar shows *Connect studio*.
- [ ] The same link again focuses that window.
- [ ] *Connect studio*: the same window reloads on `/home/agent/workspace`, the terminal works, and the status bar shows *Disconnect studio*. The link now focuses this window.
- [ ] *Disconnect studio*: the window reloads locally, and the studio can idle. The link opens the local window again.
- [ ] `open vscode://aethons.jam/session/<id>` from a terminal opens it.
- [ ] *Jam: Open Session…* lists live sessions.
- [ ] A member without SSH can open the local window; *Connect studio* shows the error and the web app link.
- [ ] After an existing `jam ssh-config --write`, the block is unchanged.
- [ ] `jam ssh-config --prune --dry-run` handles an extension block.
- [ ] Revoke in Connected apps: the next open says signed out, and `ssh-proxy` fails with the VS Code message.
- [ ] Sign Out deletes the token file.
- [ ] Upgrade the studio: host-key error, *Jam: Forget Host Key*, reconnect works.
- [ ] VS Code Insiders sign-in works.

Conversation view:

- [ ] The Jam icon shows, and the Conversation renders for the window's session in the light, dark and high-contrast themes.
- [ ] Send a message with an attachment and watch progress, then send an image and see the thumbnail.
- [ ] Answer an ask inline.
- [ ] A message from the agent appears without a reload (live).
- [ ] *Jam: Focus Session…* to another session: the view follows. Pin, refocus: the pinned view stays, with its label shown. Unpin.
- [ ] Drag the view to the panel and to the secondary sidebar.
- [ ] A window on a local folder shows "Focus a session".
- [ ] Open a board card and a message link: both open in the browser.
- [ ] Revoke the token while the view is open: it shows Sign in. Sign In again: the view recovers.
- [ ] Sleep the laptop for 2 minutes, wake it: the messages catch up.
- [ ] With Web Push on, the phone doesn't buzz for the visible conversation, and does once VS Code is unfocused.
- [ ] *Developer: Open Webview Developer Tools* shows no CSP violations, and `fetch("https://example.com")` in its console is blocked.

Panes:

- [ ] Each view (Thinking, Previews, Status, Artifacts) renders for the window's session in the light, dark and high-contrast themes.
- [ ] Thinking follows a busy session live, with Load earlier and the stream select.
- [ ] Two Thinking views on one session, then *Developer: Show Running Extensions* plus Jam's server log, show one feed.
- [ ] Pin Thinking to another session, and its label shows.
- [ ] Connected, Previews Open shows the app in the Simple Browser and a Ports-view entry; in a local session window it opens the preview URL in the browser. Browser opens the system browser, and a pinned other session's preview opens its preview URL.
- [ ] Status Interrupt and Compact work, and Release flips every pane to the ended copy.
- [ ] An Artifacts ask scrolls the Conversation view, a board thumb opens a tab, and a file opens in the browser.
- [ ] Open two boards side by side and draw in one: the other client sees it live. Hide and show a tab and the drawing is still there. Reload the window and the boards come back.
- [ ] Revoke the token: every view and board shows Sign in, and recovers after Sign In.
- [ ] DevTools on the Thinking view and a board tab shows no CSP violations.

Notifications:

- [ ] An agent message in another session gives one toast (in the focused window) and no OS notification; with VS Code unfocused, one OS notification.
- [ ] With three code windows open, exactly one toast.
- [ ] Clicking an OS notification for session X opens or focuses X's window.
- [ ] An ask shows a warning toast and bumps the badge; answering it lowers the badge.
- [ ] Reading the conversation in the Conversation view, or in a focused web-app tab, gives no notification, on a Jam with and without `runtime.push`.
- [ ] A burst of messages gives at most one notification per 5 s.
- [ ] Signing out stops notifications and clears the badge; signing in restores them.
- [ ] The phone's Web Push still arrives.
- [ ] Linux without `notify-send`: toasts only, one line in the Jam output channel.
