# VS Code hub, slice 6: New message and New session in the rail — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The VS Code rail gets the web rail's *New message* and *New session* buttons:
- a new message's conversation opens as a hub tab once it's sent;
- a new session's conversation opens in the hub.

**Architecture:** This is all in the pane bundle (`web/app`).
- The web app's `RecipientPicker` and `NewSession` gain an optional completion callback. Their default still navigates the web app's router, so the web app doesn't change.
- `RailPane` shows both buttons above the rail rows. Picking a recipient opens a small *New message* modal holding the shared `Composer`.
- Completion posts the existing `go` intent, so the extension's `goTo` opens the conversation in the hub (from any window). No extension or server change is needed.

**Tech Stack:** React, Mantine, TanStack Query, vitest (`web/app`).

**Spec:** `docs/superpowers/specs/2026-10-30-vscode-hub-design.md`, *Starting conversations*. Slices 1–5 are on this branch.

## Global Constraints

- **Buttons:** *New message* always; *New session* only when the bootstrap's `newSession` is true. They're placed as in `Rail.tsx`: `Button size="xs" variant="light"` in a `Group gap="xs" m="xs"`.
- **New message:**
  - `RecipientPicker` with `onPick(o: { to: string; label: string })`;
  - the rail pane then opens a `Modal` titled `New message`, with the line `to <label>` and a `Composer` (`to`, `label`, `attachMax` from the bootstrap);
  - `onSent(s)` closes the modal and posts `{ type: "open", intent: { kind: "go", channel: s.channel, session: "" } }`.
- **New session:** `NewSession` with `onStarted(s: SessionStarted)`. The rail posts `{ kind: "go", channel: s.channel, session: s.id }`.
  - A brand-new session has no studio window, so `goTo` opens the hub tab.
- **Web app defaults:** without the callbacks, both components navigate exactly as today, and their existing tests pass unchanged.
- **Bootstrap:** the rail pane reads it with `useQuery(["bootstrap"], apiGet("bootstrap"), { staleTime: Infinity })`, as `Boot` does, and provides `BootContext` to the modals.
- **Run checks with:** `cd web/app && npm run check`.

## Review Focus

1. **Cancelling the picker or the modal** posts nothing. *(Test in Task 1.)*
2. **No `newSession` in the bootstrap:** no *New session* button. *(Test in Task 1.)*
3. **The web app's `RecipientPicker` and `NewSession`** still navigate (their existing tests are unchanged and pass). *(Task 1, Step 4.)*
4. **A failed send** (the composer's error path) leaves the modal open and posts nothing. *(Test in Task 1.)*
5. **Signed out:** the buttons don't show; the rail's sign-in copy does. *(Covered by slice 2's test; re-run it.)*

---

### Task 1: the buttons in the VS Code rail

**Files:**
- Modify: `web/app/src/compose/RecipientPicker.tsx`: add the prop `onPick?: (o: { to: string; label: string }) => void`. When it's set, call it after `onClose()` instead of `nav(...)`.
- Modify: `web/app/src/compose/NewSession.tsx`: add the prop `onStarted?: (s: SessionStarted) => void`. When it's set, call it after `close()` instead of `go(...)`.
- Modify: `web/app/src/vscode/RailPane.tsx`:
  - read the bootstrap;
  - render the button group, `RecipientPicker` with `onPick`, `NewSession` with `onStarted`, and a `NewMessageModal` (local component: `Modal` + `Composer`), all wrapped in `BootContext.Provider`;
  - the buttons show in every state of the rail: loading, error, empty and rows.
- Test: `web/app/src/vscode/RailPane.test.tsx`.

**Interfaces:**
- Consumes: slice 2's `go` intent and `RailPane`.
- Produces: `RecipientPicker.onPick`, `NewSession.onStarted`.

- [ ] **Step 1: Write the failing tests** in `RailPane.test.tsx`, with routes for `GET /app/user/api/recipients`, `GET /app/user/api/session-roles`, `POST /app/user/api/messages` and `POST /app/user/api/sessions`:
  - `"New message: pick, write, send → go to the new conversation"`: click *New message*, pick `Ann`, type into the composer, and send. `h.posted` contains `{ type: "open", intent: { kind: "go", channel: "chn_new…", session: "" } }`, and the modal closes.
  - `"New session: start → go to its conversation"`: the posted intent is `{ kind: "go", channel: s.channel, session: s.id }`.
  - `"no newSession in the bootstrap: no New session button"` *(Review Focus 2)*.
  - `"cancelling posts nothing"` *(Review Focus 1)*: open the picker, then close it; and open the modal, then close it. No `open` messages are posted.
  - `"a failed send keeps the modal open and posts nothing"` *(Review Focus 4)*: the `POST messages` route answers 500.

  Adapt the request bodies and routes to what `Composer` and `NewSession` actually call (see `compose/NewMessage.test.tsx` and `compose/NewSession.test.tsx`).
- [ ] **Step 2: Run, and see them fail.** Run: `cd web/app && npx vitest run src/vscode/RailPane.test.tsx`. Expected: FAIL (no buttons).
- [ ] **Step 3: Implement** as listed under **Files**.
- [ ] **Step 4: Run, and see them pass.** Run: `cd web/app && npm run check`. Expected: PASS, including the unchanged `compose/*` and `rail/*` tests *(Review Focus 3)*.
- [ ] **Step 5: Commit** `feat(app): New message and New session in the VS Code rail`.

### Task 2: docs

- `docs/usage/jam/vscode-hub.md`, *The rail*: replace "It has no *New message* or *New session* buttons yet; use the web app for those." with what the two buttons do: the picker, then a modal composer, then the hub tab; and a new session lands in its hub tab.
- `docs/usage/jam/vscode-checklist.md`: add the steps "New message from the rail sends and opens the conversation's hub tab" and "New session from the rail starts it and opens its hub tab", the latter without studio access.

- [ ] **Step 1:** Make the edits.
- [ ] **Step 2:** Run `python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs | grep ERROR`. Expected: no new errors.
- [ ] **Step 3: Commit** `docs(vscode): New message and New session in the rail`.
