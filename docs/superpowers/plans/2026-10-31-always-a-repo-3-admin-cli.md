# Always a repo, slice 3: no config writes on /admin or the CLI — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove every `/admin` route that writes config, and its adminclient method and `jam` CLI verb. Config then changes only through git: the followed branch or the branch editor.

**Architecture:**
- Pure removal, area by area.
- The store functions behind the routes stay: the console's branch workspaces call them in-process.
- With every config write gone, the in-git guard (`config_ingit.go`) has nothing left to refuse, and goes in the last task.

**Tech Stack:** Go.

**Spec:** `docs/superpowers/specs/2026-10-31-always-a-repo-design.md`, §"No live edits".

**Inventory:** `/home/agent/workspace/notes/aar-inventory.md` lists every route, method and verb, with file:line at b5654adc. Re-find the lines before editing; they drift.

## Global Constraints

- **Kept live** (never removed):
  - **/admin:** enrollments, actors' grants, coves, personal sessions, standing reset and upgrade, users' OIDC, accounts, kits import, source (track and untrack) and sync, managers, collab `/admin/studios/*`, `POST /admin/config` (the snapshot import that `jam import` uses), and `/admin/config/{diff,sync,follow}`.
  - **CLI:** `user oidc`, `account add|link|unlink`, `kit track|import|untrack|sync`, `standing reset|upgrade`, `grant`/`ungrant`, `config write|diff|sync|status`, `export`/`import`, and every read verb (`list`, `show` and so on).
- **The store-level functions stay**, because the console's workspace writes call them. Examples: `PutRoleKeeping`, `SetRoleContextChecked`, `PutRoom`, `PushKitFiles`, `PublishKitDraft`, `CreateConnection`. Delete a function only when `go build ./... && go vet ./...` shows no non-test caller is left after the route goes, and no `internal/jam/adminui` caller either.
- **Tests:**
  - A test that only drove a removed route or verb is deleted.
  - When it asserted the behavior of a function that stays (a guard, a validation, an error text), move the assertion onto a direct call of that function. Keep the same inputs and the same expected error text.
  - When a test of a kept feature used a removed route only to **seed** state, seed the store directly (`st.CreateProject`, `st.PutRole`, `PushKitFiles` and so on) and keep the test.
- **CLI usage:**
  - A removed verb disappears from its group's usage text and from the command table's brief (`cmd/jam/main.go:125-165`).
  - Running a removed verb gets the group's normal unknown-subcommand error. There's no special "use git" message.
  - A whole group whose verbs are all gone (`destination`, `egress`, `room`, `connection` and `context` if nothing read-only is left; check for `list`/`show` first) is removed from the table.
- Every task ends with `go build ./... && go vet ./internal/... ./cmd/... && go test ./internal/jam/... ./cmd/jam/... 2>&1 | tail -40`, all `ok`.

## Review Focus

- A kept verb that shares a file or handler with a removed one: `standing reset` and `upgrade` next to `standing add` and `rm`; `kit track|sync|import` next to `kit push|pin|rm`; `user oidc` next to `user add`. It must still work. Each task keeps or adds one test per kept neighbour.
- `jam import` (`POST /admin/config`) still restores a snapshot. A test exists (`backup_test.go`); it must stay passing, unmodified apart from seeding.
- Room connections: `PutRoom` creating a connection must still work through the console. `adminui/api_projects_test.go` covers it; keep that test.
- The kit draft **console** editor, on a branch, keeps working after the `/admin` draft routes go. `adminui/api_kit_draft_test.go` must stay passing.
- `--admin-url` read verbs in the same groups (`role list`, `project list`, `kit list`) still work. Each task runs its group's remaining tests.

---

### Task 1: Users, members and connections

**Remove:**
- **/admin routes** in `internal/jam/users_admin.go`:
  - `POST /admin/users`;
  - `PUT /admin/users/{user}/name` and `/logins`;
  - `DELETE /admin/users/{user}`;
  - `PUT|DELETE /admin/projects/{project}/members/{user}`;
  - `POST /admin/connections`;
  - `PUT /admin/connections/{c}/name` and `/cred`;
  - `DELETE /admin/connections/{c}`.
- **adminclient:** `CreateUser`, `RenameUser`, `SetUserLogins`, `RemoveUser`, `PutMember`, `RemoveMember`, `CreateConnection`, `RenameConnection`, `SetConnectionCred`, `RemoveConnection`.
- **CLI** (`cmd/jam/users.go`): `user add|rename|rm|login`, `project member add|rm`, `connection add|rename|cred|rm`.

**Tests:** `internal/jam/users_admin_test.go`, `internal/jam/adminclient/adminclient_test.go`, `cmd/jam/main_test.go:247` and the user/connection cases, `cmd/jam/rename_test.go`.

- [ ] **Step 1: Write the failing test** `TestUserConfigRoutesGone` in `users_admin_test.go`. Each route above, called on `newTestAdmin`, gets 404 or 405. In the same test, `PUT /admin/users/{u}/oidc` and `POST /admin/accounts` still answer non-404.
- [ ] **Step 2: Run it and see it fail.** Run: `go test ./internal/jam -run TestUserConfigRoutesGone -v`. Expected: FAIL (the routes exist).
- [ ] **Step 3: Remove the routes, methods and verbs, and migrate the tests** per Global Constraints.
- [ ] **Step 4: Run Global Constraints' verification.** Expected: all `ok`.
- [ ] **Step 5: Commit:** `git commit -am "jam: users, members and connections change only in git"`

### Task 2: Projects, rooms, escalation, chat service, context

**Remove:**
- **/admin routes:**
  - `admin.go`: `POST /admin/projects`, `PUT …/name`, `DELETE /admin/projects/{p}`, `PUT …/escalation`, `PUT …/chat-service`;
  - `rooms_admin.go`: `POST …/rooms`, `PUT …/rooms/{room}/name`, `DELETE …/rooms/{room}`;
  - `context_admin.go`: all six `PUT|DELETE` context routes, for role, project and `jam/context`. Keep any `GET`.
- **adminclient:** `CreateProject`, `RenameProject`, `RemoveProject`, `PutRoom`, `RenameRoom`, `RemoveRoom`, `SetEscalationPolicy`, `SetChatService`, `SetContext`, `ClearContext`.
- **CLI:**
  - `project create|rm|rename|escalation set|clear|chat-service set|clear` (main.go);
  - `room add|rename|rm` (users.go);
  - `context set|clear` (context.go). Keep `context show` or `get`, if present.

**Tests:** `projects_test.go`, `rooms_admin_test.go`, `context_admin_test.go`, `adminclient_projects_test.go`, the cmd/jam cases.

- [ ] **Step 1: Write the failing test** `TestProjectConfigRoutesGone`. Every route above gets 404 or 405; `GET /admin/projects` still gets 200.
- [ ] **Step 2: Run it and see it fail.**
- [ ] **Step 3: Remove, and migrate.**
- [ ] **Step 4: Run the verification.**
- [ ] **Step 5: Commit:** `git commit -am "jam: projects, rooms and context change only in git"`

### Task 3: Roles, standing, egress, destinations, model specs

**Remove:**
- **/admin routes:**
  - `admin.go`: `POST /admin/roles`, `DELETE /admin/roles/{p}/{n}`, `POST /admin/destinations`, `DELETE /admin/destinations/{name}`;
  - `standing.go`: `POST …/standing`, `DELETE …/standing/{name}`. **Keep** reset and upgrade;
  - `egress.go`: `PUT|DELETE …/egress`;
  - `modelspec_admin.go`: `POST /admin/model-specs`, `PUT|DELETE /admin/model-specs/{name}`.
- **adminclient:** `PutRole`, `RemoveRole`, `AddStanding`, `RemoveStanding`, `SetEgress`, `ClearEgress`, `AddDestination`, `RemoveDestination`, `CreateModelSpec`, `UpdateModelSpec`, `DeleteModelSpec`.
- **CLI:**
  - `role add|rm`, `standing add|rm`, `egress set|clear` and `destination add|rm|import` (main.go);
  - `model-spec add|update|delete` (modelspec.go).

**Tests:** `standing_test.go`, `standing_upgrade_test.go` (keep the upgrade parts), `egress_test.go`, `modelspec_admin_test.go`, `modelspec_binding_test.go`, `admin_test.go` role and destination cases, `image_per_manager_test.go`, `connector_status_test.go`, `oauthbeta_migration_test.go`, `adminclient/modelspec_test.go`, `cmd/jam/main_test.go:155,184,295`, `cmd/jam/modelspec_test.go`, `cmd/jam/role_images_test.go`, `cmd/jam/inspect_test.go`.

- [ ] **Step 1: Write the failing test** `TestRoleConfigRoutesGone`. Every route above gets 404 or 405. `POST /admin/roles/{p}/{r}/standing/{name}/reset` on a declared standing still answers non-404.
- [ ] **Step 2: Run it and see it fail.**
- [ ] **Step 3: Remove, and migrate.**
- [ ] **Step 4: Run the verification.**
- [ ] **Step 5: Commit:** `git commit -am "jam: roles, standing, egress, destinations and model specs change only in git"`

### Task 4: Kits and kit drafts

**Remove:**
- **/admin routes:**
  - `admin.go`: `POST /admin/kits`, `POST /admin/kits/{n}/versions`, `POST /admin/kits/{n}/pin`, `DELETE /admin/kits/{n}`;
  - **all** of `kit_draft_admin.go` (open, discard, put, files, publish);
  - **all** of `kit_draft_build_admin.go` (build and cancel).
- Delete the live draft-studio support only if nothing in `adminui` uses it: `kit_draft_studio*.go`. Check with `go build` after removal. A draft studio raised from a live draft has no live draft left to raise from.
- **adminclient:** `PushKit`, `PushKitTree`, `PinKit`, `RemoveKit`, and all of `kitdraft.go`'s methods (open, discard, put file, remove file, replace, publish, build and cancel).
- **CLI:** `kit push|pin|rm` (main.go), and the whole `kit draft` group (`cmd/jam/kitdraft.go`).
- **Keep:** `kit track|import|untrack|sync|list|show` and their routes.

**Tests:** `admin_kitpush_test.go`, `admin_kitchain_test.go`, `kit_draft_admin_test.go`, `kit_draft_studio_admin_test.go`, `kit_draft_studio_test.go`, `kit_draft_publish_test.go` (keep the function-level publish tests; the console uses `PublishKitDraft`), `kit_diff_test.go`, `adminclient/kitdraft_test.go`, `cmd/jam/kitpush_test.go`, `cmd/jam/kitdraft_test.go`, `cmd/jam/kit_chain_test.go`. `admin_kitsource_test.go` stays green.

- [ ] **Step 1: Write the failing test** `TestKitConfigRoutesGone`. Every route above gets 404 or 405; `POST /admin/kits/import` still answers non-404.
- [ ] **Step 2: Run it and see it fail.**
- [ ] **Step 3: Remove, and migrate.**
- [ ] **Step 4: Run the verification**, plus `go test ./internal/jam/adminui/... -run 'KitDraft|Kits'`.
- [ ] **Step 5: Commit:** `git commit -am "jam: kits change only in git; live kit drafts removed"`

### Task 5: `config apply` and the in-git guard

**Remove:**
- `POST /admin/config/apply` (`config_admin.go:185`), along with the `--dir` gzip body handling, if nothing else uses it. `config/diff` still takes a `--dir` upload, so keep the shared decoding.
- **adminclient:** `ConfigApply` and `ConfigApplySource`.
- **CLI:** `config apply`. Fix the `config` brief at `cmd/jam/main.go:141`, which still lists `track/untrack`. It should read `write|diff|sync|status`.
- **The guard:** all of `internal/jam/config_ingit.go`:
  - `configWrite`, `liveWrite`, `configInGitGuard`, `refusedApply`, `ErrConfigInGit`, `WithConfigInGit`;
  - its install at `admin.go:1107-1109`;
  - `cmd/jam/main.go:3075`;
  - its uses at `config_admin.go:149` and `:162`.
- `config/diff` of a `--dir` tree stays allowed (a dry run). `config/diff` of another git source stays as it is today. If `refusedApply` refused it, keep that refusal inline in the diff handler with the same text.
- **Delete:** `config_ingit_test.go`. Move any of its assertions about kept routes (import, accounts) into a test of those routes.

**Tests:** `config_admin_test.go`, `adminclient/config_test.go`, `cmd/jam/configgit_test.go`, `configgit_operator_test.go`, `config_git_integration_test.go`. The integration test applies with `config sync` instead of `apply`.

- [ ] **Step 1: Write the failing test** `TestConfigApplyGone`. `POST /admin/config/apply` gets 404 or 405, `POST /admin/config/diff` still answers 200 on a `--dir` body, and `POST /admin/config/sync` with no followed repo answers its existing not-tracked error.
- [ ] **Step 2: Run it and see it fail.**
- [ ] **Step 3: Remove, and migrate.**
- [ ] **Step 4: Run the verification.**
- [ ] **Step 5: Commit:** `git commit -am "jam: config apply and the in-git guard removed"`
