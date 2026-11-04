# VS Code kit editing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Operators edit a kit's draft on their config branch as a folder in VS Code (served by the Jam), see its problems, build it, publish and discard it.

**Architecture:** Operator app tokens minted from now on carry an admin flag and may call `/app/admin/api/` (bearer chain, no cookie, no same-origin check). The admin API gains a per-file draft file API (each write conditional on that file's sha256) and draft-build routes that build a branch workspace's draft through the existing `KitDraftBuilds`. The extension adds an admin API client, a `jamkit:` `FileSystemProvider`, a native Kits tree view in the hub, Problems diagnostics and a Build output channel.

**Tech Stack:** Go (net/http, existing jam/adminui/appui/apptoken packages, Postgres migration), TypeScript VS Code extension (vitest with `test/vscode.ts` mock), tygo for API types.

**Spec:** `docs/superpowers/specs/2026-11-03-vscode-kit-editing-design.md`

## Global Constraints

- Only tokens with operator standing **and** minted after this change (`admin` flag) reach `/app/admin/api/`; the HTML console (`/app/admin/` outside `/api/`) keeps refusing every bearer (401).
- Bearer + session cookie on the same request stays a 400. Operator standing is not re-checked; the 30-day fixed lifetime is unchanged (user's choice).
- A draft save is never a commit. Publish commits. Builds never commit.
- Draft file writes are conditional on the file's own content (sha256), not the whole-draft ETag; draft file writes do not send `X-Jam-Config-Head`; publish does.
- Draft image tag: `jam-kit-draft:<kit>-<12 hex of the definition's chain digest>`; builds of the same tag on the same manager are never concurrent.
- `TestNoRaiseSetsKitDraft` stays unchanged and passing.
- Docs change in the same task as the behavior (AGENTS.md); `docs-audit` passes at the end.
- Go: `GOPROXY=https://proxy.golang.org,direct GOPRIVATE=github.com/aethons-tools`. Checks: `scripts/lint.sh`, `scripts/app-types-check.sh`, `go test ./...`, `cd web/vscode && npm run check`, `cd web/app && npm run check`.

## Review Focus

1. An operator's **old** token (no admin flag) hitting the admin API → a clear 403 `no_admin` that the extension turns into "sign in again", never a sign-out loop. (Task 2 test, Task 5 test.)
2. A save to file A while file B changed in the console → A's save succeeds; a save to A while A changed → visible save error, nothing overwritten. (Task 3 test, Task 6 test.)
3. The draft vanished (discarded elsewhere, Jam restarted) while files are open → saves fail with a `FileSystemError` naming the cause, the Kits view offers reopen. (Task 3 `no_draft` test, Task 6 test.)
4. Two branches build the same kit with identical inputs at once → one build, both see it; different parents → different tags. (Task 4 tests.)
5. A branch name with `/` (`jam/alice`) in `jamkit:` URIs and in headers round-trips. (Task 6 test.)

---

### Task 1: App tokens carry an admin flag

**Files:**
- Create: `internal/jam/migrations/0025_app_token_admin.sql`
- Modify: `internal/jam/app_tokens.go` (AppToken, NewAppToken), `internal/jam/pgstore_app_tokens.go` (insert/select the column), `internal/jam/apptoken/service.go` (exchangeResponse), `internal/jam/apptoken/consent.html`
- Test: `internal/jam/apptoken/service_test.go`, `internal/jam/storetest/` (app-token case), `internal/jam/app_tokens_test.go`
- Docs: `docs/usage/jam/app-tokens.md` (Operator standing, exchange answer, bearer scope table, the "demoting an operator means revoking their tokens" note)

**Interfaces:**
- Produces: `AppToken.Admin bool` (true iff minted by `NewAppToken` with a non-empty operatorID); exchange JSON gains `"admin": bool`.

- [ ] **Step 1: Failing tests.** `TestNewAppTokenAdmin`: `NewAppToken(u, "", "op1", now)` → `Admin == true`; `NewAppToken(u, "", "", now)` → `false`. In `service_test.go`, `TestExchangeReportsAdmin`: operator consent → exchange body has `"admin":true`; member → `"admin":false`. `TestConsentPageOperatorAdmin`: operator page contains `the admin console's API`; member page still contains `It can't use the admin console`. Storetest: a token put with `Admin: true` reads back `Admin: true` via `AppTokenByHash` and `AppTokenByID`.
- [ ] **Step 2: Run** `go test ./internal/jam/ ./internal/jam/apptoken/ -run 'AppToken|Exchange|Consent'` → FAIL.
- [ ] **Step 3: Implement.** Migration: `ALTER TABLE app_tokens ADD COLUMN admin boolean NOT NULL DEFAULT false;` (existing rows: false, so no old token widens). Consent copy for operators: "It will see and do what you can in the Inbox, open SSH connections to the studios you may reach, and use the admin console's API with your operator rights (change config on your branches). You can revoke it under Connected apps in your account menu."; the member sentence is unchanged.
- [ ] **Step 4: Run** the same tests → PASS; `go test ./internal/jam/...` → PASS.
- [ ] **Step 5: Commit** `feat(apptoken): operator tokens carry an admin flag`.

### Task 2: Admin-flagged bearers reach /app/admin/api/

**Files:**
- Modify: `internal/jam/appui/principal.go` (Principal), `internal/jam/appui/mux.go` (mount), `internal/jam/appui/bearer.go` (requireAdminAPI), `internal/jam/apptoken/bearer.go`, `internal/jam/adminui/api.go` (`sameOriginAPI`)
- Test: `internal/jam/appui/mux_test.go` (or the existing bearer test file), `internal/jam/apptoken/bearer_test.go`
- Docs: `docs/usage/jam/app-tokens.md` bearer scope table; `docs/web-app.md` line on app tokens and the admin area

**Interfaces:**
- Consumes: `AppToken.Admin` (Task 1).
- Produces: `Principal.Bearer bool` (request authenticated by app token), `Principal.AdminAPI bool` (may use `/app/admin/api/`). Refusal: 403 JSON `{"error":"this app token has no admin access: sign in again","code":"no_admin"}`.

- [ ] **Step 1: Failing tests** (appui, with a fake `BearerFunc`):
  - `TestAdminAPIBearer`: bearer principal `{Admin:true, AdminAPI:true, Bearer:true}` → `GET /app/admin/api/x` reaches the admin handler; a cross-origin `POST` with that bearer also reaches it.
  - `TestAdminAPIBearerNoAdminFlag`: `{Admin:true, AdminAPI:false}` → 403, body code `no_admin`; `{Admin:false}` → 403 `no_admin`.
  - `TestAdminHTMLStillRefusesBearer`: `GET /app/admin/config` with any bearer → 401.
  - `TestAdminAPIBearerAndCookie` → 400.
  - apptoken `TestBearerAdminAPI`: a token with `Admin:true` and an operator id → principal `AdminAPI && Bearer`; `Admin:false` → `!AdminAPI`.
- [ ] **Step 2: Run** `go test ./internal/jam/appui/ ./internal/jam/apptoken/` → FAIL.
- [ ] **Step 3: Implement.** In `Handler`, mount `"/app/admin/api/"` before `"/app/admin/"`: `withBearer(d.Bearer, d.SessionCookies, requireAdminAPI(WithShell(d.Admin, d.Shell)), <the existing cookie chain>)`; `requireAdminAPI(next)` answers the 403 above unless `p.Admin && p.AdminAPI`. `apptoken.Bearer` sets `Bearer: true, AdminAPI: t.Admin && t.Operator()`. `sameOriginAPI` skips its check when the principal has `Bearer`.
- [ ] **Step 4: Run** tests → PASS; `go test ./internal/jam/...` → PASS.
- [ ] **Step 5: Commit** `feat(appui): admin app tokens may use the admin API`.

### Task 3: Per-file draft file API

**Files:**
- Create: `internal/jam/adminui/api_kit_draft_files.go`, `internal/jam/adminui/api_kit_draft_files_test.go`
- Modify: `internal/jam/adminui/apitypes.go` (new types; `KitRow.Managed`), `internal/jam/adminui/api_kit_draft.go` (register), `internal/jam/adminui/api_kits.go` (fill `Managed` from `jam.KitManaged`), `web/app/src/admin/api/types.ts` (regenerate: `go tool tygo generate`)
- Docs: `docs/usage/jam/kits-drafts.md` (new section "File API", owns the routes below)

**Interfaces:**
- Produces (all under `/app/admin/api/kits/{name}/draft/`, branch-routed like the rest):
  - `GET files` → `DraftFiles{etag string; base int; current int; problems []string; maxFile int64; files []DraftFile}`; `DraftFile{path string; exec bool; size int64; sha256 string}`. 404 code `no_draft` when the kit has no draft.
  - `GET files/{path...}` → raw bytes, `ETag: "<sha256>"`, `Content-Type: application/octet-stream`.
  - `PUT files/{path...}` raw body; exactly one of `If-Match: "<sha256>"` (replace) or `If-None-Match: *` (create); optional `?exec=1|0` (absent: keep the mode; new file: not executable). Answers `DraftFiles`. 412 code `changed` when the precondition fails; 428 when neither header is sent.
  - `DELETE files/{path...}` with `If-Match` → `DraftFiles`, 412 `changed`.
  - `POST files-rename` JSON `{from, to, sha256}` → `DraftFiles`; 412 `changed` when `from` no longer has `sha256`; 409 when `to` exists.
  - `KitRow.Managed bool json:"managed"` — the kit comes from its own git source.

- [ ] **Step 1: Failing tests** (use `editOn` from the operator-notes tests for a branch setup; open a draft with `POST kits/{name}/draft {from}`):
  - `TestDraftFilesListAndRead`: list contains `kit.yml` with its sha256 == sha256 of `GET files/kit.yml` bytes and that response's `ETag`.
  - `TestDraftFilePutConditionalOnFile`: change `b.sh` through the console route `/save`; then `PUT files/kit.yml` with `kit.yml`'s old sha → 200 and the new content reads back; `PUT files/b.sh` with its old sha → 412 code `changed`, content unchanged.
  - `TestDraftFileCreateDeleteRename`: `If-None-Match: *` on an existing path → 412; on a new path → 200, not exec; `?exec=1` → `exec:true`; DELETE with a wrong sha → 412; rename onto an existing file → 409.
  - `TestDraftFilesNoDraft`: after `/discard`, `GET files` → 404 `no_draft`; `PUT` → 404 `no_draft`.
  - `TestDraftFileWriteIsNotACommit`: after a PUT, the branch head (`X-Jam-Config-Head` on the answer) is unchanged.
  - `TestKitRowManaged`: a kit with a `.source.yml` lists `managed:true`.
- [ ] **Step 2: Run** `go test ./internal/jam/adminui/ -run 'DraftFile|KitRowManaged'` → FAIL.
- [ ] **Step 3: Implement.** Each write: read the draft (`store.GetKitDraft`) and the file (`jam.KitDraftFile`), check the precondition against the file's sha256, then call `jam.PutKitDraftFile` / `AddKitDraftFile` / `RemoveKitDraftFile` / `RenameKitDraftFile` with `jam.DraftETag(d.Tree)` as `ifMatch`; on a 412 from those (another file changed between read and write) retry the read-check-write, at most 3 times. Body cap `draftFormLimit()` (413 `too_large`). Actor `jam.OperatorID(r)`.
- [ ] **Step 4: Run** tests → PASS; `scripts/app-types-check.sh` → clean.
- [ ] **Step 5: Commit** `feat(adminui): per-file kit draft file API`.

### Task 4: Draft builds on a branch

**Files:**
- Modify: `internal/jam/kit_draft_build.go`, `internal/jam/supervisor.go` (`raiseDraft` call sites only to pass the live store / `""` branch), `internal/jam/adminui/api_kit_draft.go` (routes), `internal/jam/adminui/adminui.go` or wherever `Option`s live (`WithKitDraftBuilds`), `internal/jam/adminui/branches.go` (`branchOf(r) string` from `workspaceKey`), `cmd/jam/main.go` (pass `WithKitDraftBuilds(draftBuilds)` into `uiOpts` before `branchOpts` is cloned)
- Test: `internal/jam/kit_draft_build_test.go`, `internal/jam/adminui/api_kit_draft_build_test.go` (new)
- Docs: `docs/usage/jam/kits-drafts.md` — replace "no test build" with a "Test build" section (anchor `#test-build`, routes below, the tag rule, 503 without a draft-capable manager); `docs/usage/jam/config-branches.md` one line: builds run on a branch and never commit.

**Interfaces:**
- Produces in `jam`: `type DraftSource struct{ Branch string; Store Store }`. `KitDraftBuilds.Start(src DraftSource, name, ifMatch, by, manager string) (KitDraftBuild, error)`, `Status(src DraftSource, name, manager string, since int64)`, `Cancel(ctx, src DraftSource, name, manager string)`. `Ensure`/`DraftKitDefinition` keep their behavior with `DraftSource{Store: <live>}`. Job key `branch\x00kit\x00manager`. `KitDraftBuild` gains `Branch string json:"branch,omitempty"`. `KitDraftTag(kit, digest string)` takes the definition's chain digest (`def.Ref.Digest`).
- Produces in adminui: `WithKitDraftBuilds(*jam.KitDraftBuilds) Option`; routes `POST kits/{name}/draft/build` JSON `{etag, manager?}` → `KitDraftBuild`; `GET kits/{name}/draft/build?since=N` → `KitDraftBuild`; `POST kits/{name}/draft/build/cancel` → `KitDraftBuild`. Without the option every route answers 503 with `kitDraftBuildsOff`'s text.

- [ ] **Step 1: Failing tests** (fake `ManagedDraftBuilder` as the existing tests use):
  - `TestDraftBuildPerBranch`: two MemStores with the same kit draft but different parent versions, `Start` on branches `a` and `b` → two jobs, different `Tag`s; `Status(DraftSource{"a",…})` sees only `a`'s.
  - `TestDraftBuildSameInputsShareOneBuild`: identical inputs on `a` and `b` while the first runs → the second `Start` returns the running build (same `ID`), the builder ran once.
  - `TestDraftBuildReusesSucceeded`: `Start` again after success with nothing changed → same `ID`, state `succeeded`, builder not called again (image present).
  - Update `TestKitDraftTag` for the digest argument.
  - adminui `TestDraftBuildRoutes`: on a branch, POST build → 200 `running`; GET with `since` returns the log tail; the branch head is unchanged; without `WithKitDraftBuilds` → 503.
  - `go test ./internal/jam -run TestNoRaiseSetsKitDraft` still PASS.
- [ ] **Step 2: Run** `go test ./internal/jam/ ./internal/jam/adminui/ -run 'DraftBuild|KitDraftTag'` → FAIL.
- [ ] **Step 3: Implement.** Every store read in `KitDraftBuilds` goes through the call's `src.Store`; the live-store `store` field stays only for `Ensure`'s path. Before starting, a running or succeeded (image still present) job with the same tag on that manager, under any branch, is returned instead of starting another.
- [ ] **Step 4: Run** `go test ./internal/jam/...` → PASS.
- [ ] **Step 5: Commit** `feat(kits): build a branch's kit draft`.

### Task 5: Extension admin client and session admin flag

**Files:**
- Create: `web/vscode/src/admin/api.ts`, `web/vscode/src/admin/branch.ts`, `web/vscode/test/adminapi.test.ts`
- Modify: `web/vscode/src/auth.ts` (`Session.admin`), `web/vscode/test/auth.test.ts`

**Interfaces:**
- Consumes: Task 1 exchange `admin`; Tasks 3–4 routes and `web/app/src/admin/api/types.ts` (`DraftFiles`, `DraftFile`, `KitDraftBuild`, `KitsResponse`, `ConfigBranches`, `DraftResult`).
- Produces: `Session.admin?: boolean` (absent on sessions saved before this change). `class NoAdminError extends Error` (403 `no_admin`). `class AdminApi { constructor(base, token, doFetch, branch: () => string) }` with `kits()`, `branches()`, `createBranch(name)`, `openDraft(kit, from)`, `files(kit)`, `readFile(kit, path): Promise<{bytes: Uint8Array; sha256: string}>`, `writeFile(kit, path, bytes, cond: {sha256: string} | "create", exec?: boolean)`, `deleteFile(kit, path, sha256)`, `renameFile(kit, from, to, sha256)`, `publish(kit, etag, head)`, `discard(kit, etag)`, `startBuild(kit, etag)`, `buildStatus(kit, since)`, `cancelBuild(kit)`; each sends `X-Jam-Config-Branch: branch()`; only `publish` sends `X-Jam-Config-Head`. Errors reuse `ApiError`/`SignedOutError` from `src/api.ts`. `BranchState` (`branch.ts`): `get(jamUrl): string`, `set(jamUrl, name)` in `globalState`, default `""` meaning "use `ConfigBranches.defaultName`".

- [ ] **Step 1: Failing tests:** `writeFile` with `{sha256}` sends `If-Match: "<sha>"`, with `"create"` sends `If-None-Match: *`, `exec` adds `?exec=1`; a path `a b/c.sh` is encoded per segment; a 403 `no_admin` throws `NoAdminError`; a 412 throws `ApiError` with code `changed`; every call carries the branch header; `publish` alone carries the head. Auth: an exchange answer with `admin:true` stores `admin: true`.
- [ ] **Step 2: Run** `cd web/vscode && npx vitest run test/adminapi.test.ts test/auth.test.ts` → FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `npm run check` → PASS.
- [ ] **Step 5: Commit** `feat(vscode): admin API client`.

### Task 6: The jamkit: file system

**Files:**
- Create: `web/vscode/src/kits/fs.ts`, `web/vscode/src/kits/uri.ts`, `web/vscode/test/kitfs.test.ts`
- Modify: `web/vscode/test/vscode.ts` (add `FileSystemError`, `FileType`, `FileChangeType`, `workspace.registerFileSystemProvider`, `workspace.updateWorkspaceFolders` mocks as needed)

**Interfaces:**
- Consumes: `AdminApi` (Task 5).
- Produces: `kitUri(jamSlug, branch, kit, path = "")` / `parseKitUri(uri) → {jam, branch, kit, path}`: `jamkit://<jam slug>/<encodeURIComponent(branch)>/<kit>/<path>`. `class KitFileSystem implements vscode.FileSystemProvider` constructed with `(apiFor: (jamSlug) => AdminApi | undefined)`; `onProblems: Event<{uri: Uri (kit root); files: DraftFiles}>` fired after every listing (Task 7 consumes it); `refresh(kitRoot)`.

- [ ] **Step 1: Failing tests:**
  - `kitUri("jam-x", "jam/alice", "web", "bin/run.sh")` round-trips through `parseKitUri`.
  - `readDirectory` of the kit root lists files and implicit directories from `files`; `stat` of a file gives its size; of an unknown path throws `FileNotFound`.
  - `writeFile` of a known file sends the sha256 last read (from `readFile` or the listing); `create` of a new file sends `"create"`; after the write the cached sha is the answer's.
  - A 412 `changed` → throws a `FileSystemError` whose message says the file changed on the Jam, and fires `onDidChangeFile` (Changed) for it so VS Code offers its compare.
  - A 404 `no_draft` → throws `FileSystemError.Unavailable` with "the draft is gone (discarded or the Jam restarted): reopen the kit"; unsaved buffers are not touched (no `onDidChangeFile` Deleted events).
  - `createDirectory` keeps an empty directory in memory only; `rename` maps to `renameFile`; `delete` of a directory deletes each file under it.
- [ ] **Step 2: Run** `npx vitest run test/kitfs.test.ts` → FAIL.
- [ ] **Step 3: Implement.** Mark executable files' mode by keeping the server's `exec`; new files are created non-executable (Task 7 toggles it).
- [ ] **Step 4: Run** `npm run check` → PASS.
- [ ] **Step 5: Commit** `feat(vscode): jamkit file system over kit drafts`.

### Task 7: Kits view, branch, open/publish/discard, Problems

**Files:**
- Create: `web/vscode/src/kits/view.ts` (TreeDataProvider), `web/vscode/src/kits/commands.ts`, `web/vscode/src/kits/problems.ts`, `web/vscode/test/kitsview.test.ts`
- Modify: `web/vscode/src/extension.ts` (register provider, file system, commands in the hub), `web/vscode/package.json` (view, commands, menus), `web/vscode/test/manifest.test.ts`
- Docs: `docs/usage/jam/vscode.md` — new section "Editing kits" (owner of the extension side); link from `kits-drafts.md`.

**Interfaces:**
- Consumes: Tasks 5–6.
- Produces: view `jam.kits` ("Kits", in the `jam` container, `when: jam.window == hub`); commands `jam.kits.open`, `jam.kits.build` (Task 8 implements), `jam.kits.publish`, `jam.kits.discard`, `jam.kits.toggleExec`, `jam.kits.switchBranch`, `jam.kits.openInConsole`, `jam.kits.refresh`.

- [ ] **Step 1: Failing tests:**
  - With `session.admin` not `true`, or after any `NoAdminError` → the view shows one item "Kit editing needs an operator sign-in — sign in again" whose command is `jam.signIn`.
  - After a `no_draft` failure from the file system for a kit, its item reads "draft gone — reopen" and its command reopens it (`openDraft(kit, current)`, then `refresh`).
  - With admin: the first item is `Branch: <name>` (command `jam.kits.switchBranch`); kits follow, `draft` as description when `draft`; `managed` kits show "from its git repo" and have no open command.
  - Branch default: no stored branch → `ConfigBranches.defaultName`; when it isn't in `branches`, the item reads "Create branch <name>" and `createBranch` runs on click.
  - `jam.kits.open` on a kit without a draft calls `openDraft(kit, current)` then adds the folder `kitUri(...)` named `<kit> (<branch>)` via `updateWorkspaceFolders`; a kit already open is just revealed.
  - `jam.kits.publish` asks for confirmation, calls `publish(kit, etag, head)`, shows the Jam's message; a 400 shows the problems; `jam.kits.discard` asks first, then removes the folder.
  - Problems: each `DraftFiles.problems` entry goes on the file whose path it starts with (`<path>:`), else on `kit.yml`; an empty list clears the collection for that kit.
- [ ] **Step 2: Run** `npx vitest run test/kitsview.test.ts test/manifest.test.ts` → FAIL.
- [ ] **Step 3: Implement.** `openInConsole` opens `<jam>/app/admin/config/kits/<kit>` with the branch selected the way the console reads it (check `web/app/src/admin/branch/branch.ts` for the query or storage it uses; if it can't be selected by URL, open the page and say which branch to pick).
- [ ] **Step 4: Run** `npm run check` → PASS.
- [ ] **Step 5: Commit** `feat(vscode): Kits view in the hub`.

### Task 8: Build from VS Code

**Files:**
- Create: `web/vscode/src/kits/build.ts`, `web/vscode/test/kitbuild.test.ts`
- Modify: `web/vscode/src/kits/commands.ts` (wire `jam.kits.build`)
- Docs: `docs/usage/jam/vscode.md` "Editing kits" — the Build paragraph.

**Interfaces:**
- Consumes: `AdminApi.startBuild/buildStatus/cancelBuild` (Task 5), `KitDraftBuild`.
- Produces: `runBuild(api, kit, etag, out: OutputChannel, progress, token: CancellationToken, sleep = 1000ms): Promise<KitDraftBuild>`.

- [ ] **Step 1: Failing tests:** status polls append each `log` chunk once (using `log_end` as the next `since`) to the output channel; `log_skipped` writes a "… earlier output dropped" line; `succeeded` ends with an info message "Kit <kit> builds"; `failed` ends with an error message carrying `error`'s first line and a "Show log" action; cancelling the progress calls `cancelBuild`; a 503 shows the Jam's message (no manager can build); unsaved editors of the kit trigger "Save all and build?" first.
- [ ] **Step 2: Run** `npx vitest run test/kitbuild.test.ts` → FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `npm run check` → PASS.
- [ ] **Step 5: Commit** `feat(vscode): build a kit draft`.

### Task 9: Whole-branch verification

- [ ] **Step 1:** `scripts/lint.sh && scripts/app-types-check.sh && go test ./... && (cd web/app && npm run check) && (cd web/vscode && npm run check)` → all PASS.
- [ ] **Step 2:** docs-audit skill on `docs/` → no findings for touched docs.
- [ ] **Step 3:** Commit any fixes; open the draft PR.
