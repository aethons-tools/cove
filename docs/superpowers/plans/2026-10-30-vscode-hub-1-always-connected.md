# VS Code hub, slice 1: session windows always connected, no pinning — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:**
- A session window is always the connected studio window.
- Every Jam session view shows that window's session.
- Pinning, *Focus Session*, the local session window and *Connect/Disconnect Studio* are gone.

**Architecture:** This removes code from `web/vscode` and the pane bundle in `web/app`.
- `openSession` (in `open.ts`) becomes the single path into a studio window. It checks the session, prepares SSH, asks about `localServerDownload` once, then calls `openFolder(remoteUri)`. That opens a new window, or focuses the window already on that URI.
- `FocusModel` shrinks to "the window's studio session, resolved and cached".
- The five session views only show in a studio window (`when: jam.window == session`).
- The Needs You view stays everywhere; slice 2 replaces it with the rail.
- The connected-mark heartbeat stays, because slices 2 and 3 use it for ▣ and click routing.

**Tech Stack:**
- TypeScript, VS Code API 1.105, vitest (`web/vscode`);
- React and Mantine, vitest (`web/app`).

**Spec:** `docs/superpowers/specs/2026-10-30-vscode-hub-design.md`. This plan is slice 1 of its build order: "remove pinning and make session windows always connected". Read the spec's *Session window*, *When a session can't have a window* and *Removed* sections.

## Global Constraints

- **Every Jam view shows the window's current conversation.** In this slice, that's the studio window's session. There is no pin and no override.
- **Opening a session never opens a second window for it:** `openFolder` of `vscode-remote://ssh-remote+jam-<id>/home/agent/workspace` with `forceNewWindow` focuses the existing window (phase 1, #567).
- **No SSH access:** the error is "You can't SSH into <label>'s studio (ask an operator to share it).", with "Open in the web app". The hub doesn't exist yet, so the web app stands in for it until slice 3.
- **Studio not running:** show the existing cert error (`certErrorMessage`, e.g. "studio <id> is not running"). No window opens, and opening never starts the studio.
- **Remote-SSH missing:** offer Install, as today (`editor.remoteSsh.name`).
- **`remote.SSH.localServerDownload` prompt:** only when `editor.localServerDownload` is set (VS Code, not VSCodium). Ask once (`jam.askedLocalServerDownload`), before the first studio open.
- **Old state is ignored, not migrated:** workspaceState `jam.focus` and `jam.pins`, and globalStorage `sessions/*.code-workspace`. Nothing reads them. A window still open on an old local workspace file becomes an ordinary window.
- **Run checks with** `npm run check` in `web/vscode` and in `web/app` (typecheck, plus lint in app, plus tests).
- **Docs change in the same PR** (AGENTS.md): `docs/usage/jam/vscode.md` and `docs/usage/jam/vscode-checklist.md`.

## Review Focus

1. **A window still open on an old local session workspace after the upgrade:** `jam.window` is `""`, no session views or status-bar item show, and nothing throws. *(Test in Task 3.)*
2. **Stale `jam.focus` or `jam.pins` in workspaceState** naming another session: the views still show the window's own session. *(Test in Task 2.)*
3. **Open a link for a session whose window is already open:** exactly one `openFolder(remoteUri(id))` with no reuse option, and no `setConnected` call from `openSession`. *(Test in Task 1.)*
4. **A link to a session the user can't SSH into:** error plus "Open in the web app", no `ssh` call, no `openFolder`. *(Test in Task 1.)*
5. **A board tab, which can open in a studio window, keeps working** now that `PaneState` lost `pinned` and `elsewhere`. *(Existing `board.test.ts` and `BoardTab.test.tsx` must pass after the type change in Task 2.)*

---

### Task 1: `openSession` always opens the studio window

**Files:**
- Modify: `web/vscode/src/open.ts`. Remove:
  - from `Ui`: `localWorkspace`, `isConnected`, `setConnected`, and the `reuse` option of `openFolder`;
  - `connectStudio`, `disconnectStudio`, `statusBarItem`, `SESSION_WORKSPACE`, `WindowKind` and `windowKind`.
- Test: `web/vscode/test/open.test.ts`. Delete the `connectStudio`, `disconnectStudio`, `windowKind` and `statusBarItem` describes. Move the still-relevant `connectStudio` cases (Remote-SSH missing, ssh error, 409 cert message, localServerDownload ask / Not now / VSCodium) to `openSession`.

**Interfaces:**
- Produces:
  - `openSession(d: OpenDeps, id: string): Promise<void>` (unchanged signature);
  - `Ui.openFolder(uri: string): Promise<void>`;
  - `connectedSet(st, now?)` stays exported and unchanged, for Task 3's heartbeat;
  - `studioFromAuthority` and `windowAuthority` stay exported.

- [ ] **Step 1: Rewrite the `openSession` tests (failing)**

In `describe("openSession")`, replace the local-window cases with:

```ts
it("checks the session, sets up SSH, then opens its studio window in a new window", async () => {
  const f = fake({ global: "always" });
  await openSession(f.d, ID);
  expect(f.titles).toEqual([`Jam: opening ${ID}…`]);
  expect(f.steps).toEqual(["checking the session", "setting up SSH for Ada", "opening the window"]);
  expect(f.order).toEqual(["ssh", `open ${remoteUri(ID)}`]);
  expect(f.ui.openFolder).toHaveBeenCalledTimes(1);
  expect(f.ui.openFolder).toHaveBeenCalledWith(remoteUri(ID));
  expect(f.ssh).toHaveBeenCalledWith(f.api, ID);
});

it("a session whose window is open: still one openFolder (VS Code focuses it), no connected-mark writes", async () => {
  const f = fake({ global: "always", connected: [ID] });
  await openSession(f.d, ID);
  expect(f.ui.openFolder).toHaveBeenCalledTimes(1);
  expect(f.ui.openFolder).toHaveBeenCalledWith(remoteUri(ID));
});

it("can't SSH: error with the web app, no ssh, no window", async () => {
  const f = fake({ head: { id: ID, label: "Ada", channel: "chn_1", ssh: "" } });
  f.answers.error = "Open in the web app";
  await openSession(f.d, ID);
  expect(f.ui.error).toHaveBeenCalledWith("You can't SSH into Ada's studio (ask an operator to share it).", "Open in the web app");
  expect(f.ui.openExternal).toHaveBeenCalledWith(`${URL_}/app/user/c/chn_1`);
  expect(f.ssh).not.toHaveBeenCalled();
  expect(f.ui.openFolder).not.toHaveBeenCalled();
});

it("a studio that isn't running (409) shows the cert message and opens nothing", async () => {
  const f = fake({ sshErr: new ApiError("x", 409, "") });
  await openSession(f.d, ID);
  expect(f.ui.error).toHaveBeenCalledWith(`studio ${ID} is not running`);
  expect(f.ui.openFolder).not.toHaveBeenCalled();
});
```

Then:
- move the "missing Remote-SSH offers Install", "in VSCodium, missing Open Remote - SSH", "ssh error is shown and logged", "asks about localServerDownload once", "Not now still connects" and "in VSCodium, never asks" cases from `connectStudio` to `openSession`, changing `connectStudio(` to `openSession(` and `{ reuse: true }` expectations to no second argument;
- delete the `isConnected`/`setConnected`/`localWorkspace` members from the `fake()` ui.
- `fake()` keeps a `connected` option only so the "window is open" test can assert nothing reads or writes it; drop `setConnected` from `order`.

- [ ] **Step 2: Run, and see it fail**

Run: `cd web/vscode && npx vitest run test/open.test.ts`
Expected: FAIL. `openFolder` is called with `file:///gs/sessions/…`, and the `localWorkspace` member is missing.

- [ ] **Step 3: Implement**

- In `open.ts`, `openSession` becomes `checkSession`, then `prepareSsh(d, s, id, step, log, fail)`, then the localServerDownload ask (moved verbatim from `connectStudio`), then `step("opening the window")`, `log("openFolder <uri>")`, `ui.openFolder(remoteUri(id))`, `log("openFolder returned")`.
- Remove the dead code listed under **Files**, and update the file header comment ("Opening a session's studio window over Remote-SSH.").

- [ ] **Step 4: Run, and see it pass**

Run: `cd web/vscode && npx vitest run test/open.test.ts`
Expected: PASS. Typecheck fails in `extension.ts` until Task 3; that's expected.

- [ ] **Step 5: Commit**

```bash
git add web/vscode/src/open.ts web/vscode/test/open.test.ts
git commit -m "feat(vscode): opening a session always opens its studio window"
```

### Task 2: one session per window: drop pins, focus and `elsewhere`

**Files:**
- Modify: `web/vscode/src/focus.ts`
  - remove `FOCUS`, `PINS`, `focus()`, `isPinned()`, `togglePin()` and `focusSessionCommand`;
  - `stateOf(view)` becomes `target()`;
  - rename the constructor dep `windowDefault` to `windowSession`.
- Modify: `web/vscode/src/panes/session.ts`
  - `sessionPaneState(d)` no longer takes `view`;
  - remove the `jam.<name>.pinned` and `jam.<name>.hasSession` context keys and the `view.description` "elsewhere" label.
- Modify: `web/app/src/bridge/protocol.ts`
  - `PaneState = { signedIn: boolean; target: SessionTarget | null; error?: string }`;
  - remove the `{ kind: "focusSession" }` intent.
- Modify: `web/vscode/src/bridge/intents.ts`: remove the `focusSession` case and the `{ command: "jam.focusSession" }` action.
- Modify: `web/app/src/vscode/Pane.tsx` and `web/app/src/vscode/panes.tsx`
  - `Copy`'s `action` and `onClick` become optional (no button when absent);
  - the `none` copy becomes "This window isn't a Jam studio window.";
  - the `none` and `gone` states have no action;
  - `SessionGate` loses `onFocus`.
- Tests:
  - `web/vscode/test/focus.test.ts`: rewrite.
  - `web/vscode/test/session.test.ts`, `test/intents.test.ts`, `test/board.test.ts`, `test/port.test.ts`: remove `pinned`/`elsewhere`/`focusSession`.
  - `web/app/src/vscode/Pane.test.tsx`, `panes.test.tsx`, `BoardTab.test.tsx`, `bridge.test.ts`: likewise.

**Interfaces:**
- Produces:
  - `new FocusModel({ windowSession: string | undefined; resolve: (session: string) => Promise<SessionTarget> })`, with:
    - `windowSession(): string | undefined`;
    - `target(): Promise<SessionTarget | null>` (null when there's no window session; cached per session, and a rejected lookup isn't cached);
    - `invalidate(): void`;
    - `onChange(cb): () => void`.
  - `sessionResolver(api)` is unchanged.
  - `sessionPaneState(d: SessionPaneDeps): Promise<PaneState>`, where `SessionPaneDeps.model` is `Pick<FocusModel, "target" | "onChange">`.

- [ ] **Step 1: Write the failing tests**

`focus.test.ts` keeps the `sessionResolver` cases, and adds:

```ts
it("target() is the window's session, resolved once and cached", async () => {
  const resolve = vi.fn(async (s: string) => ({ session: s, label: "Ada", channel: "chn_1" }));
  const m = new FocusModel({ windowSession: SES, resolve });
  expect(await m.target()).toEqual({ session: SES, label: "Ada", channel: "chn_1" });
  await m.target();
  expect(resolve).toHaveBeenCalledTimes(1);
});
it("no window session: target() is null and resolves nothing", async () => {
  const resolve = vi.fn();
  expect(await new FocusModel({ windowSession: undefined, resolve }).target()).toBeNull();
  expect(resolve).not.toHaveBeenCalled();
});
it("a failed lookup isn't cached; invalidate() clears and fires onChange", async () => { /* reject once, then resolve: second target() resolves; invalidate → cb called once */ });
```

Review Focus 2, in `extension.test.ts`'s activate block (Task 3 owns the file, so add it there): with `workspaceState` pre-seeded `{"jam.focus": OTHER_SES, "jam.pins": {"jam.status": OTHER_SES}}` in a `jam-${SES}` window, the Status view's first posted state has `target.session === SES`.

`session.test.ts`: every expected `PaneState` loses `pinned` and `elsewhere`. Delete the context-key and description tests, and add:

```ts
it("sets no jam.*.pinned or jam.*.hasSession context keys", () => {
  /* resolve a view; expect(executeCommand).not.toHaveBeenCalledWith("setContext", expect.stringMatching(/\.(pinned|hasSession)$/), expect.anything()) */
});
```

`Pane.test.tsx`:
- the "no target" and "gone" cases expect the copy text, and `screen.queryByRole("button")` is null;
- delete the `focusSession` post assertions.

`intents.test.ts`: `intentAction({ kind: "focusSession" } as never, JAM)` is an `Error("Unknown intent.")`.

- [ ] **Step 2: Run, and see them fail**

Run: `cd web/vscode && npx vitest run test/focus.test.ts test/session.test.ts test/intents.test.ts` and `cd web/app && npx vitest run src/vscode`
Expected: FAIL (no `target()`, extra `pinned` fields, a button still rendered).

- [ ] **Step 3: Implement** the interface changes listed under **Files**.

- [ ] **Step 4: Run, and see them pass**

Run: `cd web/app && npm run check`, then `cd web/vscode && npx vitest run test/focus.test.ts test/session.test.ts test/intents.test.ts test/board.test.ts test/port.test.ts`
Expected: PASS. `web/vscode` typecheck still fails only in `extension.ts`, until Task 3.

- [ ] **Step 5: Commit**

```bash
git add web/vscode/src/focus.ts web/vscode/src/panes/session.ts web/vscode/src/bridge/intents.ts web/app/src web/vscode/test
git commit -m "feat(vscode): Jam views always show the window's session; pinning removed"
```

### Task 3: wire the extension and manifest

**Files:**
- Modify: `web/vscode/src/extension.ts`. Remove:
  - `sessionsDir` and `localWorkspace`;
  - the status-bar item;
  - the `jam.connectStudio`, `jam.disconnectStudio`, `jam.focusSession` and `jam.<pane>.pin/unpin` registrations;
  - the `localWorkspace`, `isConnected` and `setConnected` Ui members, and the `forceLocalWindow` logic in `openFolder` (always `{ forceNewWindow: true }`).
- Modify: `web/vscode/package.json`. Remove:
  - those commands, all of `menus.view/title`, and their `commandPalette` entries;
  - in their place, add `"when": "jam.window == session"` to the five session views in `views.jam` and `views.jamPanel`. Needs You keeps no `when`.
- Tests: `web/vscode/test/extension.test.ts` (rewrite the `local session windows (connect on demand)` describe as `session windows`), `web/vscode/test/manifest.test.ts`.

**Interfaces:**
- Consumes:
  - Task 1's `openSession`, `connectedSet`, `studioFromAuthority`, `windowAuthority`;
  - Task 2's `FocusModel({ windowSession, resolve })` and `SessionPaneProvider` deps (`model` without pin methods).
- Produces:
  - the context key `jam.window` = `"session"` in a `jam-<id>` remote window, else `""`;
  - the activation log line `window: studio session <id>` or `window: no session`.

- [ ] **Step 1: Write the failing tests**

`manifest.test.ts`:
- the command list is exactly `["jam.forgetHostKey", "jam.needsYou.open", "jam.openSession", "jam.signIn", "jam.signOut"]` (sorted), each registered;
- `contributes.menus` has no `view/title`, and `commandPalette` is `[{ command: "jam.needsYou.open", when: "false" }]`;
- each of `jam.conversation`, `jam.status`, `jam.artifacts`, `jam.previews`, `jam.thinking` has `when: "jam.window == session"`, and `jam.needsYou` has no `when`.

`extension.test.ts` `describe("session windows")`:
- **a `jam-${SES}` remote window** sets `setContext("jam.window", "session")`, marks `jam.connected.${SES}` in globalState on activate, refreshes it every `CONNECTED_BEAT_MS`, and clears it on `deactivate()`. *(Keep the existing heartbeat tests.)*
- **a link** (`handleUri /session/${SES}`) runs the ssh setup and calls `vscode.openFolder` with authority `ssh-remote+jam-${SES}` and `{ forceNewWindow: true }`. Nothing is written under globalStorage `sessions/`.
- **Review Focus 1:** a window whose `workspaceFile` is `<globalStorage>/sessions/${SES}.code-workspace` sets `jam.window` to `""`, creates no status-bar item (`createStatusBarItem` not called), and activates without error.
- **Review Focus 2** (from Task 2).
- **removed commands:** `vscode.commands.executeCommand("jam.connectStudio")` isn't registered (the mock's registry has no such id).

- [ ] **Step 2: Run, and see them fail**

Run: `cd web/vscode && npx vitest run test/manifest.test.ts test/extension.test.ts`
Expected: FAIL (old commands present, `jam.window` is `"remote"`).

- [ ] **Step 3: Implement** the changes listed under **Files**.
- `windowStudio = studioFromAuthority(windowAuthority(vscode.workspace))` replaces `windowKind`, and feeds `FocusModel({ windowSession: windowStudio, … })`, `portDeps.windowStudio`, `jam.window` and the heartbeat.
- `needsYou.open` and the URI handler keep calling `openSession`.

- [ ] **Step 4: Run, and see the whole package pass**

Run: `cd web/vscode && npm run check && npm run build`
Expected: typecheck clean, all tests pass, build succeeds.

- [ ] **Step 5: Commit**

```bash
git add web/vscode
git commit -m "feat(vscode): session windows are studio windows; drop Connect/Disconnect and the local window"
```

### Task 4: docs

**Files:**
- Modify: `docs/usage/jam/vscode.md`:
  - frontmatter `summary`/`read_when`/`owns`: drop "connect/disconnect", "local session windows", "pin/unpin" and "Focus Session";
  - `## Opening a session`: every way in opens the studio window, or focuses it. Cover the no-SSH error, the not-running error, and the localServerDownload ask on the first open;
  - delete `### Connect and disconnect`. Closing the studio window lets the studio idle;
  - `## Panes and focus`: rename it to `## Panes`. Views show only in a studio window and always show its session. No pinning;
  - Troubleshooting: drop rows that mention Connect.
- Modify: `docs/usage/jam/vscode-checklist.md`: replace the local-window and Connect/Disconnect steps with:
  - "the link opens (or focuses) the studio window";
  - "a no-SSH member gets the error with the web app";
  - "an old local session window shows no Jam views after upgrading".
- Modify: `docs/usage/jam/INDEX.md`: the `vscode.md` row's `read_when` mirrors the new frontmatter.

- [ ] **Step 1:** Make the edits.
- [ ] **Step 2:** Run `python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs | grep -E "usage/jam/(vscode|INDEX)"`. Expected: no new error or warning beyond the existing INDEX budget warning.
- [ ] **Step 3:** Confirm `grep -rn "Connect studio\|Disconnect studio\|Focus Session\|jam.focusSession\|pin" docs/usage/jam/vscode*.md` finds nothing stale.
- [ ] **Step 4: Commit**

```bash
git add docs/usage/jam
git commit -m "docs(vscode): studio windows always connected; no pinning"
```
