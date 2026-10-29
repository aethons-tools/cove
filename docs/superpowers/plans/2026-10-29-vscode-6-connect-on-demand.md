# VS Code slice 6 (connect on demand) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** *Open in VS Code* opens a **local session window** (no Remote-SSH, so the studio can idle). *Jam: Connect to Studio* reloads that same window onto the studio over Remote-SSH when files or a terminal are needed; *Jam: Disconnect Studio* reloads it back to local.

**Architecture:**
- **The local session window is a workspace file.** `<globalStorageUri>/sessions/<ses_id>.code-workspace`, content `{"folders": []}`, written on demand (no secret in it). Opening a session opens it with `vscode.openFolder(…, {forceNewWindow: true})`, which focuses a window already on it.
- **Window → session.** `windowKind(workspace, sessionsDir, platform)` in `src/open.ts`: a `jam-<id>` remote (as today, `studioFromAuthority(windowAuthority(…))`), or a `workspaceFile` whose directory is `sessionsDir` (case-insensitive on darwin/win32) and whose name is `<id>.code-workspace` with `SESSION_ID_RE`. `FocusModel`'s `windowDefault` is that session. `windowStudio` (preview forwarding) stays the remote one only: a local window forwards nothing.
- **Connect / disconnect.** `connectStudio` in `src/open.ts` runs today's SSH steps (Remote-SSH check, `d.ssh`, the localServerDownload ask) and `openFolder(remoteUri(id), {reuse: true})`. `disconnectStudio` opens the local workspace with `{reuse: true}`. Status-bar items show per window kind.
- **Connected flag.** `globalState["jam.connected.<id>"]`, "a studio window for this session is open" (best effort): a remote session window sets it on activation and clears it on deactivate. Opening a flagged session runs the shared `prepareSsh` (SSH access, Remote-SSH, `d.ssh`, renewing the cert) and opens its remote URI (focusing that window); on failure it clears the flag and opens the local workspace.
- **Status-bar items** show only while signed in.

**Spec:** the slice-2 open flow (`docs/superpowers/plans/2026-10-29-vscode-2-extension.md`) and the window session of slice 3/4 (`FocusModel`, `windowStudio`).

## Global Constraints

- Only `src/extension.ts` imports `vscode`; the rest is pure with injected deps, tested with `test/vscode.ts`.
- No token in any file, argv, URL or log. Never `vscode.env.remoteAuthority` (proposed API).
- A window already on a `jam-` remote behaves as before (its session, forwarding, Forget Host Key).
- Session-id validation, "No such session" (404) and the signed-out path stay in the open flow.

## Tasks

- [ ] **1. Window session.** Tests first: a session workspace file → its id; the same name in another directory, a non-session workspace and a bad id → none; a remote window as before. Implement `sessionsDir`/`localWorkspaceUri`/`windowSession`, wire `windowDefault` from it and keep `windowStudio` remote-only.
- [ ] **2. Open → local.** Tests first: open checks the session (404, no SSH needed) then writes and opens the local workspace (new window); a connected session opens the remote URI. `Ui.openFolder(uri, {reuse})`, `Ui.ensureLocalWorkspace(id)`, `Ui.connected()/setConnected()`.
- [ ] **3. Connect / Disconnect.** Tests first: connect → ssh then `openFolder(remote, reuse)` and marks connected; disconnect → `openFolder(local, reuse)` and unmarks; status-bar items visible per window kind; deactivate of a remote window unmarks. Register `jam.connectStudio` / `jam.disconnectStudio` in the manifest.
- [ ] **4. Docs.** `docs/usage/jam/vscode.md` (getting in; "Connect and disconnect") and `vscode-checklist.md` (verify-first + steps); docs-audit adds no findings.
- [ ] **5. Verify.** `just app-check`; `cd web/vscode && npx vitest run && npm run check`; `just vscode`; `just lint`.
