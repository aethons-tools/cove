# Admin SPA slice 4 (a project's Agents and its roles) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** In the admin app, a project's **Agents** node and its **role** leaves work, at parity with the old pages:
- the project Agents view: its roles, with New role and Request; and its agents.
- a role's page:
  - facts, Request agent, Delete;
  - Scope, Egress, Agent context and Allocation, each with its editor;
  - Standing agents: upgrade, reset, dismiss, declare;
  - Holders and Running;
  - **Setup** as a lazily loaded section (no sub-tab), with its section and manager selectors.

**Architecture:** `adminui/api_roles.go` holds the JSON twins of `role_detail.go`, `role_edit.go`, `role_request.go`, the role create and delete in `writes.go`, and the role routes of `setup_page.go`.
- Shared apply functions are extracted from `role_edit.go` and `writes.go`, so both routes run one implementation.
- New shared app components:
  - `StatusBadge` (an agent's display status);
  - `CovesTable` (the studio table; slice 8's Dashboard reuses it);
  - `SetupView` (sections, files and diffs; slice 7's agent Setup reuses it).
- Running agents link to the Agents tab (`agents/<id>`, `NotMovedYet` until slice 7). Spec decision (a): they aren't tree leaves.

**Tech Stack:** as slice 1.

**Spec:** `docs/superpowers/specs/2026-10-28-admin-spa-design.md` (§3 "Pages without their sub-tabs": a role's Setup becomes a section; §6 slice 4). Conventions: slices 1–3's plans and `docs/web-app.md` "Admin area".

## Global Constraints

- **Parity confirms** (drift note appended or prepended exactly as `role.html` does):
  - "Delete role <r> from <p>?" (+ " <drift>")
  - "Upgrade standing agent <n>? It restarts on the current image; its conversation and workspace are kept."
  - "Reset standing agent <n>? Its conversation and workspace are deleted; Jam raises it fresh."
  - "Dismiss standing agent <n>? It stops, and its conversation and workspace are deleted." (+ " <drift>")
  - "Drop <r>'s egress policy and use the kit default?" (+ " <drift>")
  - "Remove this role agent context?" (+ " <drift>")
  - "<drift> Save anyway?" on the scope, egress, allocation, context and standing-declare writes of a managed role.
- **Messages:**
  - The flash messages of reset and upgrade (including a pending reset's reason) come back as `message` and show as a green notification.
  - Request answers "Requested agent <name> (<id>); it will squawk you on the intercom." as a green notification.
  - Its refusals (403 "sign in to request an agent…", "no member of <p> is linked to your login", capacity) show as red notifications that stay.
- **Action availability:**
  - Request, Upgrade and Reset show only when `canRequest`, which is true when a supervisor runs.
  - Setup shows only when `setupEnabled`.
- **The Setup section:**
  - It loads only when opened.
  - Selectors: "All" plus each section name, and the manager (with two or more managers).
  - Its text is `jam.SetupDisplayText`'d on the server, as today.
  - Every inspection is audit-logged (`auditSetup`).
- **Role create:** project from the page; destinations as `name=cred,…`; TTL in seconds; kit; model-spec. A 409 adds "; edit it on its page", as today. Success navigates to the new role's leaf.
- **Delete** navigates to `config/projects/<p>/agents`.

## Review Focus

1. **A role whose kit was removed after binding** shows its attention flag. It can still be edited, but the scope save refuses a missing kit with 400 "kit "x" does not exist", shown inline. Tested in Task 1 and Task 3.
2. **Request as the loopback admin**, who has no member login, gets 403 with the sign-in message as a red notification. Nothing is raised. Tested in Task 1 and Task 2.
3. **A standing reset that's pending** (the agent is mid-turn) answers 200 with the reason as the message, not an error. Tested in Task 1.
4. **The Setup section with no supervisor or setup deps** isn't offered. A bad `?sections=` gets 400 with the parser's text. A manager error maps its status. Tested in Task 1 and Task 4.
5. **Running agents and holders link to `agents/<id>`** (the Agents tab), not to tree nodes. Tested in Task 2 and Task 3.

---

### Task 1: Roles API

**Files:**
- Create: `internal/jam/adminui/api_roles.go`, `internal/jam/adminui/api_roles_test.go`
- Modify:
  - `internal/jam/adminui/apitypes.go`;
  - `internal/jam/adminui/role_edit.go`, `writes.go` and `role_request.go`, extracting `setRoleScope(store, credExists, project, name, f roleScopeIn) error`, `setRoleAllocation(store, project, name, f roleAllocIn) error`, `createRole(store, credExists, project, f roleCreateIn) error` and `requestRole(ctx, store, sup, alloc, log, login, project, role) (jam.PersonalSessionResult, int, error)`, used by both the HTML and the JSON routes;
  - `internal/jam/adminui/api.go`: `registerApp` takes `sup *jam.Supervisor`, `alloc jam.SessionAllocator`, `credExists`, `setup *jam.SetupDeps` and `setupManagers`.

**Interfaces:**
- Produces (types, camelCase json, lists never nil):
  ```go
  type CoveRow struct { ID, Name, Project, Role, Unit, Phase, Activity, Status, Connector, Image, Manager, LeaseHolder, RaisedAt, LastSeen string; Attn *AttnFlag `tstype:"AttnFlag | null"` }
  type AttnFlag struct { Kind, Why string }            // a row's worst attention item
  type AgentRow struct { ID, Name, Kind, Unit, Status, Image, LastSeen string; Roles []string; Attn *AttnFlag `tstype:"AttnFlag | null"` }
  type RoleRow struct { Name string; Destinations []DestCred; TTL, Kit string; Attn *AttnFlag `tstype:"AttnFlag | null"` }
  type DestCred struct { Name, Cred string }          // cred "" = the destination default
  type ProjectAgents struct { Project string; Roles []RoleRow; Agents []AgentRow; CanRequest bool }
  type DestRow struct { Name, Cred, Route string; CredIsRole, Missing bool }
  type Setting struct { Label, Value string; Unset bool }
  type StandingRow struct { Name, Prompt, ActorID, Status, Image, Upgrade string }
  type HolderRow struct { ID string; Override bool }
  type RoleForm struct { Destinations, Addressing, TTL, Kit, ModelSpec, MaxEphemeral, MaxPersonal, MaxPersonalPerOwner, IdleAfter, NagEvery, ReclaimAfter, Egress string }
  type RoleDetail struct {
      Project, Name string; Managed *Managed `tstype:"Managed | null"`
      Kit, ModelSpec string; ModelSpecDefault bool; TTL string
      Dests []DestRow; Addressing []string; EgressManaged bool; EgressDomains []string
      Allocation []Setting; Standing []StandingRow; Holders []HolderRow; Running []CoveRow
      Form RoleForm; Context ContextPanel; CanRequest, SetupEnabled bool; Attn *AttnFlag `tstype:"AttnFlag | null"`
  }
  type RoleResult struct { Role RoleDetail; Message string }
  type Requested struct { ID, Name, Message string }
  type SetupFile struct { Path, Body string; Bytes int }
  type SetupSection struct { Name, Source, Reason, At, Body, Head string; Stale, Open, Diffed, DiffTooLarge, DiffSame bool; Files []SetupFile; Diff []DiffLine; DiffNote string; DiffPath string } // DiffPath: the section's diff route under the API ("" = none)
  type SetupResponse struct { Note string; Stale int; Sections []SetupSection; SectionNames []string; Managers []string; Manager string }
  ```
  - Status is `jam.StatusOf(...)`, as a string.
  - Attention flags come from `attention()`: a row's agent id, or a `p/role` subject.
  - Times are "Jan 2 15:04" (raised) and "15:04:05" (last seen), as the table shows them. "" means none.
- Routes:
  - `GET api/projects/{project}/agents` → `ProjectAgents`, or 404.
  - `POST api/projects/{project}/roles {name, destinations, ttlSeconds, kit, modelSpec}` → 201 `Created{ID: name, Path: config/projects/<p>/agents/<r>}`.
  - `GET api/projects/{project}/roles/{role}` → `RoleDetail`, or 404.
  - `DELETE api/projects/{project}/roles/{role}` → 204.
  - `POST api/projects/{project}/roles/{role}/request` → `Requested`. A refusal is 403 or the `jam.PersonalSessionStatus` status.
  - These each answer `RoleResult`:
    - `POST …/scope {destinations, addressing, ttl, kit, modelSpec}`;
    - `POST …/allocation {maxEphemeral, maxPersonal, maxPersonalPerOwner, idleAfter, nagEvery, reclaimAfter}`;
    - `POST|DELETE …/context`;
    - `POST|DELETE …/egress {domains}`;
    - `POST …/standing {name, prompt}`;
    - `DELETE …/standing/{session}`;
    - `POST …/standing/{session}/reset`;
    - `POST …/standing/{session}/upgrade`.
  - `GET …/setup?sections=&manager=` → `SetupResponse`. It's 404 `not_found` when setup is off or the role is missing, 400 for a bad `sections`, and otherwise `setupFail`'s mapping as JSON.

  Log lines are the HTML routes'.
- [ ] **Step 1: Write the failing tests** in `api_roles_test.go`:
  - `TestRolesAPI`: create (and 409 on the same name with the suffix) → get → scope with a missing kit is 400 "kit "nope" does not exist" → scope ok → allocation with "x" is 400 → allocation ok → egress set/clear → context set/clear → declare standing → dismiss → delete → 204. The agents list shows the role.
  - `TestRoleRequestLocal`: Request as `Principal{Admin, Local}` → 403 with the sign-in message.
  - `TestRoleStandingReset`: with a nil supervisor, reset answers what `jam.ResetStanding` answers. Assert it's either a 200 `RoleResult` with a message or the same status the HTML route gives (compare against `post(…/reset)`).
  - `TestRoleSetupOff`: without `WithSetupDeps` → 404; `canRequest`/`setupEnabled` false.
  - `TestRoleSetup`: with fake `SetupDeps` (reuse the fixture in `setup_page_test.go`), it returns sections and the names, and `?sections=bogus` → 400.
  - The existing role and setup HTML tests keep passing after the extractions.
- [ ] **Step 2:** `go test ./internal/jam/adminui/ -run 'Role'` → FAIL.
- [ ] **Step 3:** Implement, extract, then regenerate types.
- [ ] **Step 4:** `go test ./internal/jam/adminui/` → PASS.
- [ ] **Step 5: Commit** `feat(admin): roles API`.

### Task 2: StatusBadge, CovesTable and the project Agents view

**Files:**
- Create: `web/app/src/admin/ui/StatusBadge.tsx`, `web/app/src/admin/ui/CovesTable.tsx`, `web/app/src/admin/ui/AttnFlag.tsx`, `web/app/src/admin/roles/AgentsPage.tsx`, `web/app/src/admin/roles/agents.test.tsx`
- Modify: `web/app/src/admin/AdminApp.tsx` (route `config/projects/:project/agents`)

**Interfaces:**
- Produces:
  - `StatusBadge({ status })` colours:
    - running → green;
    - waiting, holding, orienting → blue;
    - setting up → yellow;
    - blocked, lost → red;
    - idled, done, pending, terminating, gone → gray.
  - `AttnFlag({ flag })` is a small red (broken) or yellow badge "!" titled with `why`.
  - `CovesTable({ rows })` has the old `coves-tbl` columns: Agent (a link to `nodeHref("agents/<id>")` plus a "timeline" link to `agents/<id>/session`), Status, Connector, Image, Project, Role (a link to the role leaf), Manager, Unit, Lease, Raised and Last seen. Its empty text is "No agents running.".
  - **AgentsPage** (polled every 3 s while visible) has:
    - the heading "Agents" and the sub "<p>'s roles — what a grant of each lets an agent reach — and its agents";
    - "Roles": New role (Role name, Destinations `TypeAhead kind="destinations" list`, TTL seconds, Kit `TypeAhead kind="kits"`, Model-spec `TypeAhead kind="model-specs"`), then a `DataTable` with Role (a leaf link plus `AttnFlag`), Destinations (badges `name → cred`), TTL, Kit (a link to `config/kits/<k>` or "default") and Request (when `canRequest`; a green or red notification);
    - "Agents": a `DataTable` with Agent (a link to `agents/<id>`), Kind, Roles (links), Status, Image and Last seen. Its empty text is "No agents in this project.".
- [ ] **Step 1: Write the failing tests** in `agents.test.tsx`:
  - the roles and agents render, with the links as in Review Focus 5;
  - New role navigates to the leaf;
  - a 409 shows "…; edit it on its page" inline;
  - Request shows the green message;
  - a 403 Request shows the red message;
  - no Request button when `canRequest` is false;
  - `StatusBadge` colours "lost" red.
- [ ] **Step 2:** `npx vitest run src/admin/roles` → FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** `npm run check` → PASS.
- [ ] **Step 5: Commit** `feat(app): admin project agents view`.

### Task 3: The role page

**Files:**
- Create: `web/app/src/admin/roles/RolePage.tsx`, `web/app/src/admin/roles/useRole.ts`, `web/app/src/admin/roles/role.test.tsx`
- Modify: `web/app/src/admin/AdminApp.tsx` (route `config/projects/:project/agents/:role`)

**Interfaces:**
- Produces:
  - `useRole(p, r)` uses the query `["admin","role",p,r]`. `useRoleWrite(p, r)` sets that query to `RoleResult.role` and shows a non-empty `message`.
  - **RolePage** has:
    - a header with the name, `ManagedBadge`, `AttnFlag`, and the facts: project link, kit (link or "no kit"), model-spec (a link to `config/model-specs/<m>`, plus "default"), TTL. Request agent (when `canRequest`) and Delete (`ConfirmAction`, then navigate to Agents) sit beside them.
    - Scope: the destinations `DataTable` with the parity cells ("unregistered" red badge; credential plus "role mapping" or "destination default"; route). Then the Addressing badges, or "No comms targets — it can't squawk anyone.". "Edit scope" holds a form whose fields are all prefilled from `form`: Destinations `TypeAhead list`, Addressing `TypeAhead kind="targets" project list`, TTL, Kit, Model-spec.
    - Egress: the domain badges, "Managed, empty — …" or "Kit default — the role sets no egress policy.". "Edit egress" holds the domains textarea, Save, and "Reset to kit default" (`ConfirmAction`, when managed).
    - `ContextPanel` (scope "role").
    - Allocation: a definition list of `allocation`, each value dimmed when unset. "Edit allocation" holds six fields with the hint "Leave a field blank to unset it (the value shown above for unset applies).".
    - Standing agents: a `DataTable` with Name, Prompt, Agent (StatusBadge plus "image stale" plus "upgrade: <state>" plus the actor id), and actions Upgrade and Reset (when `canRequest`) and Dismiss. "Declare standing agent" holds Name and Prompt.
    - Holders: badges linking `agents/<id>`, each with "override" titled, or "No agents hold this role.".
    - Running: `CovesTable`.
    - Setup: `<details>` "Setup — what a studio of this role raised now would get" (when `setupEnabled`), rendering `SetupView` only once opened (Task 4).

    Not found → "Role not found" with "Back to roles".
- [ ] **Step 1: Write the failing tests** in `role.test.tsx`:
  - the facts and the scope table cells;
  - a scope save posts the prefilled values;
  - a 400 missing kit shows inline;
  - the egress reset confirm;
  - an allocation save;
  - standing declare;
  - Upgrade confirms and then shows the server's message;
  - Dismiss confirms with the drift note on a managed role;
  - holders link `agents/<id>`;
  - Delete navigates to Agents;
  - not found.
- [ ] **Step 2:** `npx vitest run src/admin/roles/role` → FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** `npm run check` → PASS.
- [ ] **Step 5: Commit** `feat(app): admin role page`.

### Task 4: SetupView (the role's Setup section)

**Files:**
- Create: `web/app/src/admin/ui/SetupView.tsx`, `web/app/src/admin/ui/setup.test.tsx`
- Modify: `web/app/src/admin/roles/RolePage.tsx`

**Interfaces:**
- Produces: `SetupView({ path, managers? })`. `path` is the API route that answers `SetupResponse`, here `projects/<p>/roles/<r>/setup`. It holds:
  - the section chips ("All" plus `sectionNames`, as in-page state);
  - the manager chips (when `managers.length > 1`);
  - "Loading the setup… (a first read of the image on the launcher host can take a few seconds)" while pending;
  - the note banner;
  - the stale count line;
  - one `<details>` per section (open when `open`): name, source badge, stale badge, time, reason, `head` plus per-file `<details>` (path and byte count), or the body `<pre>`, then the diff note and the diff (`DiffView`, "Too large to diff here." or "Same as a raise now.").

  Each selector change refetches with `?sections=`/`?manager=` (query key includes them). A stale badge with `diffPath` loads that section's diff in place. That's used by agents in slice 7; roles have none.
- [ ] **Step 1: Write the failing tests** in `setup.test.tsx`:
  - nothing is fetched until the role's Setup `<details>` opens;
  - the sections render, with the context section's files;
  - clicking a section chip refetches with `sections=identity`;
  - the manager chips show with two managers;
  - a 400 shows the parser's message.
- [ ] **Step 2:** `npx vitest run src/admin/ui/setup` → FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** `npm run check` → PASS.
- [ ] **Step 5: Commit** `feat(app): admin setup view on the role page`.

### Task 5: Browser test and docs

**Files:**
- Modify: `internal/jam/adminui/app_browser_test.go` (`TestBrowserAdminRoleScope`), `docs/web-app.md`, `docs/usage/jam/ui-console.md` (all projects pages now in the preview), and the spec's status line.

- [ ] **Step 1:** Write `TestBrowserAdminRoleScope`:
  - open `config/projects/acme/agents/coder`;
  - open "Edit scope";
  - set the TTL to `2h` (`browsertest.SetInput`) and click "Save scope";
  - wait for "TTL 2h" in the facts;
  - assert the stored role's TTL is 2h and there are no page problems.
- [ ] **Step 2:** `just app && just test-browser` → PASS.
- [ ] **Step 3:** Update the docs, then run the docs audit.
- [ ] **Step 4:** `just test && just lint && (cd web/app && npm run check)` → PASS.
- [ ] **Step 5: Commit** `test(admin): browser test for a role scope edit; docs`.
