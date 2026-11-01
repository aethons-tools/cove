# Always a repo, slice 4: a read-only console outside a branch — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The admin console never writes config live.
- Outside a branch, every config form and button is hidden, and the server refuses such writes on any Jam, repo or not.
- The managed badges and drift notes go.

**Architecture:**
- **Server (`internal/jam/adminui`):** a refusal wrapper is always installed around the console mux. It is the branch router when a repo is set, and a plain refusal otherwise.
- **Web (`web/app/src/admin`):** a `useConfigEditable()` hook gates config controls. `EditForm` and `ConfirmAction` hide by default outside a branch, and live-state uses opt in with `live`.

**Tech Stack:** Go; React 19 + Mantine + TanStack Query, Vitest; tygo generates `web/app/src/admin/api/types.ts` from `adminui/apitypes.go` (run `just gen-types`, or whatever `tygo.yaml` and the justfile name).

**Spec:** `docs/superpowers/specs/2026-10-31-always-a-repo-design.md`, §"No live edits" and §"The console, read-only".

## Global Constraints

- **The server refusal.** A console write on a config path (`onBranchPath`) without `X-Jam-Config-Branch` answers 409 with code `conflict`.
  - With a repo, the message is `the config is in git: open a branch to edit it` (today's text).
  - Without one, it is `this Jam has no config repo, so its config is read-only: set config.github, config.gitlab or config.git in the serve config`.
- **Paths that stay live** are exactly `branchLive`'s (`adminui/branches.go:45`), plus every path outside `branchPaths`: agents, enrollments, grants, shares, tracking/diff and tracking/apply (the first-apply confirm), and config/branches.
- **The branch bar without a repo** (`GET config/branches` → 404) renders an Alert with the title `Read-only` and the text `This Jam has no config repo. Set config.github, config.gitlab or config.git in the serve config to edit its config on branches.` It no longer renders nothing.
- **The hook.** `useConfigEditable(): boolean` in `web/app/src/admin/branch/branch.ts` is `useBranch() !== ""`.
- **`EditForm` and `ConfirmAction`** get a `live?: boolean` prop. Without it, they render nothing when `!useConfigEditable()`.
- **Live-state uses must pass `live`:**
  - Request agent;
  - standing reset and upgrade;
  - accounts link and unlink, OIDC, and app tokens revoke;
  - kit import, source track and untrack, and sync;
  - enrollments, grants and shares;
  - agents and studio delete;
  - Tracking's Confirm and apply;
  - the branch bar's own Merge.
- **Every other config control** (Mantine `Button`s that open create forms, `details` edit sections) is wrapped in `{editable && …}`.
- **Removed:** `ui/ManagedBadge.tsx` (`ManagedBadge`, `driftNote`) and every use, with their `save`/`confirm` drift strings; Go `adminui/managed.go`; the `Managed` type and every `managed` field in `apitypes.go`; `ManagedRow` if only the Tracking page's managed list uses it.
  - Keep the Tracking page's list of what the repo manages if it exists, without the drift state. It is how an operator sees what a confirm will own.

## Review Focus

- A page left open on a branch, when the tab switches back to the followed branch through the bar, must hide its forms at once. The hook re-renders on `setBranch`: test in Task 2.
- Live-state controls on a config page must stay visible on the followed branch: Request agent on a role, standing reset and upgrade, kit sync. Test in Task 2.
- The Tracking page's first-apply Confirm and apply must keep working with no branch open (`POST tracking/apply` is not a branch path). Test in Task 1 (server) and Task 2 (web).
- A Jam with no repo must refuse a forged console write: `curl POST /app/admin/api/users` gets 409. Test in Task 1.
- The draft editor on a branch keeps its Save and Publish, and shows no drift checkbox. That checkbox is removed in slice 2; check it still holds.

---

### Task 1: The server refuses console config writes on every Jam

**Files:**
- Modify: `internal/jam/adminui/adminui.go:142-145`. When `o.branches == nil`, return a `readOnlyRouter{live: mux}` whose `ServeHTTP` gives the no-repo 409 for a write on `onBranchPath` and otherwise serves `mux`.
- Modify: `internal/jam/adminui/branches.go:108-111`. The text stays as Global Constraints says; also check it after slice 2 removed the `tracking/managed/` clause.
- Test: `internal/jam/adminui/branches_test.go`, plus new cases in `internal/jam/adminui/readonly_test.go`.
- Migrate: every `adminui/*_test.go` that drives a live config write on a handler without branches (the inventory lists 17 files). Do one of two things:
  - Route the write through a branch, as `branches_test.go` sets one up, keeping the assertions on the workspace store.
  - When the test checks the write handler's own validation, build the mux directly with an internal helper `registerAppForTest` that skips the wrapper. Prefer the first.

**Interfaces:**
- Produces: `type readOnlyRouter struct{ live http.Handler }`.

- [ ] **Step 1: Write the failing tests** in `readonly_test.go`:
  - `TestNoRepoRefusesConfigWrites`: a `Handler(...)` with no `WithConfigBranches`. `POST /app/admin/api/users` with a JSON body gets 409, with the exact no-repo message. `POST …/projects/p/roles/r/request` does not get 409 (it may be 404 for a missing role).
  - `TestNoRepoServesReads`: `GET /app/admin/api/tree` gets 200.
  - `TestNoRepoTrackingApplyLive`: `POST /app/admin/api/tracking/apply` does not get the read-only 409.
- [ ] **Step 2: Run them and see them fail.** Run: `go test ./internal/jam/adminui -run NoRepo -v`. Expected: FAIL (the write succeeds).
- [ ] **Step 3: Implement `readOnlyRouter`, and migrate the tests** per the Files block.
- [ ] **Step 4: Run the package.** Run: `go test ./internal/jam/adminui/... 2>&1 | tail -30`. Expected: `ok`.
- [ ] **Step 5: Commit:** `git commit -am "admin console: config writes only on a branch, on every Jam"`

### Task 2: The web console is read-only outside a branch

**Files:**
- Modify:
  - `web/app/src/admin/branch/branch.ts` (`useConfigEditable`);
  - `branch/BranchBar.tsx` (the no-repo Alert);
  - `ui/EditForm.tsx`, `ui/ConfirmAction.tsx` (`live`).
- Modify every page in the inventory's console-UI list (`/home/agent/workspace/notes/aar-inventory.md` §Console UI): gate the config controls and pass `live` on the live ones.
- Delete: `ui/ManagedBadge.tsx` and its uses. Go: `adminui/managed.go`, the `managed` fields in `apitypes.go`, their fill sites (grep `managedOf(`), then regenerate `api/types.ts`.
- Test: `branch/branch.test.tsx`, `ui/ui.test.tsx`, `roles/role.test.tsx`, `kits/kit.test.tsx`, `projects/projects.test.tsx`, `tracking/tracking.test.tsx`; update the other page tests the inventory lists. Their write-form tests set the branch (`setBranch("jam/test")`) in `beforeEach`.

**Interfaces:**
- Produces: `export function useConfigEditable(): boolean`; `EditForm` and `ConfirmAction` props `live?: boolean`.

- [ ] **Step 1: Write the failing tests.**
  - `branch.test.tsx`, `BranchBar shows Read-only without a repo`: mock `GET config/branches` → 404, and the Alert with the exact title and text renders.
  - `ui.test.tsx`:
    - an `EditForm` renders nothing with no branch, and its submit with `setBranch("b")`;
    - an `EditForm live` renders with no branch;
    - the same for `ConfirmAction`.
  - `role.test.tsx`:
    - with no branch, a role page shows "Request agent" and the standing "Reset", but not "Delete" or the scope Save;
    - after `setBranch("jam/me")`, "Delete" is present;
    - no "managed ·" text in either case.
  - `tracking.test.tsx`: with no branch, "Confirm and apply" is present while following awaits a confirm.
- [ ] **Step 2: Run them and see them fail.** Run: `cd web/app && npx vitest run src/admin/branch src/admin/ui src/admin/roles src/admin/tracking`. Expected: FAIL.
- [ ] **Step 3: Implement the hook and props, gate the pages, and remove the badges** (Go and web). Regenerate the types.
- [ ] **Step 4: Run everything.** Run: `cd web/app && npx tsc --noEmit && npx vitest run 2>&1 | tail -15; cd ../.. && go build ./... && go test ./internal/jam/adminui/...`. Expected: all pass.
- [ ] **Step 5: Commit:** `git commit -am "admin console: read-only outside a branch; managed badges and drift notes removed"`
