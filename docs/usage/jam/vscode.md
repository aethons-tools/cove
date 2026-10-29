---
summary: The Jam VS Code extension (`aethons.jam`) — installing the .vsix, signing in with an app token, opening a session's studio over Remote-SSH, what it writes to your machine, and fixing its errors.
read_when: You want to open an agent's studio in VS Code from the Jam app or a link, are installing or signing in the Jam extension, or it showed an error (host identification changed, participant sign-in missing, jam not found, can't SSH into, signed out).
owns: installing and using the Jam VS Code extension; its settings (`jam.url`, `jam.cliPath`); Sign In/Out; opening a session; what the extension writes (ssh_config block, key, cert, token file); `remote.SSH.localServerDownload`; its troubleshooting
prereqs: collaboration.md for who may SSH and the ssh_config block the CLI writes; app-tokens.md for the sign-in token
tier: leaf
updated: 2026-10-29
---

# The Jam VS Code extension

The extension opens a session's studio in a VS Code Remote-SSH window with one click,
with no `jam login --participant` or `jam ssh-config` step.
It signs in to your Jam with an app token, writes the SSH setup, and opens `/home/agent/workspace`.
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

The extension signs you in if needed, checks you may SSH into the session
(otherwise it says so and links the web app), writes the SSH setup below,
and opens `vscode-remote://ssh-remote+jam-<studio>/home/agent/workspace` in a new window.

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
| Signed out after a revoke | Run *Jam: Sign In*. A revoked token also makes `ssh-proxy` fail with a "VS Code sign-in … was revoked" message. |
