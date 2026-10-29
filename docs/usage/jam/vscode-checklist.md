---
summary: Building the Jam VS Code extension (`just vscode`, the `.vsix`, its version, tests) and the by-hand end-to-end checklist, since VS Code can't run in CI.
read_when: You changed `web/vscode` or are cutting a release with it, and must build it or verify it by hand.
owns: the extension's build (`just vscode`/`vscode-check`, scripts/vscode.sh, the 0.<N>.0 version, the CI artifact and release asset), its unit-test layout, the ssh-block golden, and the manual checklist
prereqs: vscode.md for what the extension does
tier: leaf
updated: 2026-10-29
---

# Building and verifying the VS Code extension

`web/vscode` is a separate npm package (esbuild bundle, vitest) built into a `.vsix`.
The unit tests run in CI; VS Code itself can't, so the checklist below is run by hand.
What the extension does: [vscode.md](vscode.md).

## Build

- `just vscode` runs `scripts/vscode.sh`: `npm ci`, `npm run check` (typecheck and tests), the esbuild bundle, then `vsce package` into `web/vscode/dist/jam.vsix`. It needs Node and npm.
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
- [ ] `vscode.openFolder` with `forceNewWindow` focuses an existing window on that folder rather than opening a second.

## Steps

- [ ] Install the `.vsix` (*Install from VSIX…*).
- [ ] Set `jam.url`, run Sign In; the consent page names "VS Code on <host>"; Connected apps lists it.
- [ ] Open in VS Code from the session header: a window opens on `/home/agent/workspace` and the terminal works.
- [ ] The same link again focuses that window.
- [ ] `open vscode://aethons.jam/session/<id>` from a terminal opens it.
- [ ] *Jam: Open Session…* lists live sessions.
- [ ] A member without SSH sees the error and the web app link.
- [ ] After an existing `jam ssh-config --write`, the block is unchanged.
- [ ] `jam ssh-config --prune --dry-run` handles an extension block.
- [ ] Revoke in Connected apps: the next open says signed out, and `ssh-proxy` fails with the VS Code message.
- [ ] Sign Out deletes the token file.
- [ ] Upgrade the studio: host-key error, *Jam: Forget Host Key*, reconnect works.
- [ ] VS Code Insiders sign-in works.
