# Admin SPA slice 3 (Projects) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** In the admin app, the Config tree's project nodes work, at parity with the old project pages:
- **Projects** (list and New project);
- a **project** overview (rename, delete, chat service, agent context, tiles);
- **Members** (add, edit delivery, remove), plus each member leaf: the user's page in the project's scope with "Remove from project";
- **Intercom** (rooms add and remove, recent messages);
- **Escalation** (chains: edit, clear, add a category).

**Architecture:** `adminui/api_projects.go` holds the JSON twins of `projects.go` and `project_edit.go`. They call the same store and jam functions, and the section writes answer with the refreshed `ProjectDetail`. New shared app components:
- `TypeAhead`, over the existing `GET /app/admin/suggest`;
- `ContextPanel`, the agent-context card, reused by roles in slice 4 and the dashboard in slice 8;
- `SquawkTable`, reused by Health's channel log in slice 8. Its bodies go through the existing `MessageBody`, the one inner-HTML site.

**Tech Stack:** as slice 1.

**Spec:** `docs/superpowers/specs/2026-10-28-admin-spa-design.md` (§3, §6 slice 3). Conventions: slices 1–2's plans, and `docs/web-app.md` "Admin area".

## Global Constraints

- **Parity confirms.** The drift note is prepended or appended exactly as the old templates do:
  - "Delete project <p>? Its roster, escalation and chat service go with it; its channels are archived (history stays readable) and its name is freed." (with " <drift>")
  - "Remove <name> from <p>?"
  - "Remove room <name> from <p>?"
  - "Clear the <category|default> chain?"
  - "Remove this project agent context?"
  - "<drift> Rename anyway?" and "<drift> Save anyway?" on managed projects' rename, chat service, members, rooms, escalation and context writes.
- Delete is disabled, with the title "Still referenced by <InUseBy> — remove its roles, agents and grants first", when `inUseBy` is set. A successful delete navigates to `config/projects`.
- The project `default` can't be renamed (no rename form), as before.
- A project section write answers with the whole `ProjectDetail`, as the HTML answers with the section. The tree is refreshed (`useAdminMutation`).
- Escalation targets that name nobody show as red badges titled "Not on this project's roster".
- Recent messages: at most 50, newest first (`recentProjectSquawks`). "Full log" links the old `/app/admin/intercom?project=<p>` until Health moves (slice 8).
- **No Agents section here.** `config/projects/:p/agents` and the role leaves stay `NotMovedYet` until slice 4.

## Review Focus

1. **A rename** moves the page to the new name's URL. The tree follows, and the old URL then shows "Project not found". Tested in Task 2 (app) and Task 1 (API).
2. **Deleting a project that's in use** is refused with 409. A project removed in another tab: its page and section writes answer 404 and show "Project not found". Tested in Task 1 and Task 2.
3. **A member added by name** may already be a member. "Add member" with no delivery keeps their delivery, as the HTML route's `add=1` does. Tested in Task 1 (`TestProjectMembersAPI`).
4. **An escalation tier spec that doesn't parse** gets 400 with the parser's message inline, and the textarea keeps its text. Tested in Task 4.
5. **Squawk bodies** render only through `MessageBody`, and a plain body shows literally with its badge. Tested in Task 3 (a `<script>` in a plain body stays text).

---

### Task 1: Projects API

**Files:**
- Create: `internal/jam/adminui/api_projects.go`, `internal/jam/adminui/api_projects_test.go`
- Modify: `internal/jam/adminui/apitypes.go`, `internal/jam/adminui/api.go` (registration; `registerApp` gains `img jam.ImageResolver` and `msgs SquawkReader`)

**Interfaces:**
- Produces (types, camelCase json, lists never nil):
  ```go
  type ProjectRow struct{ Name string; Members, Roles, Rooms int; Managed *Managed `tstype:"Managed | null"` }
  type ProjectsResponse struct{ Projects []ProjectRow }
  type ContextLeaf struct{ Name, ReadWhen string }
  type ContextResource struct{ Name, Kind, Ref, Note string }
  type ContextPanel struct {
      Scope string; Core string; CoreBytes, Budget int; Over, Empty, ShowResources bool
      Leaves []ContextLeaf; Resources []ContextResource; YAML string
      Managed *Managed `tstype:"Managed | null"` // the layer's config object's mark
  }
  type DeliveryRow struct{ Service, Address string }
  type MemberRow struct{ UserID, Name, Handle string; Delivery []DeliveryRow; DeliverySpec string }
  type RoomRow struct{ Name, Connection, Kind, Ref string; PostOnly bool }
  type TargetRow struct{ Text string; Unknown bool }
  type TierRow struct{ Targets []TargetRow; Timeout string }
  type ChainRow struct{ Category string; Tiers []TierRow; Spec string }
  type SquawkRow struct{ At, From, FromID, AgentID, Channel, ChannelID, Project, Body string; Plain bool } // Body: squawkrender HTML
  type ProjectDetail struct {
      Name string; Managed *Managed `tstype:"Managed | null"`; InUseBy string; Renamable bool
      ChatService string; ChatServices []string
      Members []MemberRow; Rooms []RoomRow; Roles int; Agents, LiveAgents int
      Escalation []ChainRow; DefaultChain ChainRow
      Context ContextPanel
      LogConfigured bool; Squawks []SquawkRow // the newest 50 in this project
  }
  ```
  `Renamable` is `name != "default"`. `Squawks` is filled only for `?section=intercom`, as the HTML builds it only for that section.
- Produces (Go):
  - `contextPanelOf(p contextPanel, marks) ContextPanel`;
  - `projectDetailOf(store, img, msgs, marks, name string, withLog bool) (ProjectDetail, bool)`, built on `buildProjectDetail`.
- Routes:
  - `GET api/projects`, sorted by name;
  - `POST api/projects {name}` → 201 `Created{ID: name, Path: "config/projects/<esc name>"}`;
  - `GET api/projects/{project}[?section=intercom]` → `ProjectDetail`, or 404 `not_found`;
  - `POST api/projects/{project}/rename {name}` → `Created` for the new name;
  - `DELETE api/projects/{project}` → 204 (409 while in use);
  - `POST|DELETE api/projects/{project}/context` (`{yaml}`; DELETE clears);
  - `POST api/projects/{project}/chat-service {service}`;
  - `POST api/projects/{project}/members {user, delivery, add}` (`add` bool);
  - `DELETE api/projects/{project}/members/{user}`;
  - `POST api/projects/{project}/rooms {name, connection, ref}`;
  - `DELETE api/projects/{project}/rooms/{name}`;
  - `POST api/projects/{project}/escalation {category, tiers}`;
  - `DELETE api/projects/{project}/escalation?category=`.

  Every section write → `ProjectDetail`, with the same apply logic and log lines as `registerProjectEdits` and `registerProjects`. Extract the shared apply funcs from `project_edit.go`, so the HTML and JSON routes run one implementation. For example: `putMember(store, project, user, deliverySpec string, add bool) error`, `putRoom`, `setEscalation(store, project, category, tiersSpec string) error`. A missing project is 404 `not_found` before the apply.
- [ ] **Step 1: Write the failing tests** in `api_projects_test.go`:
  - `TestProjectsAPI`: create → list → get → rename (the Created path moves; a GET of the old name gives 404) → delete → 204. Deleting a project that a role references gives 409 `conflict`.
  - `TestProjectSectionsAPI` covers:
    - the chat service set;
    - context set (`yaml: "core: hi\n"`) and then cleared;
    - a room added and removed;
    - a chain set (`tiers: "user:alice@15m"`), where an unknown target is flagged `unknown`, then cleared;
    - a bad tier spec → 400 with the parser's message.
  - `TestProjectMembersAPI`: add by name with a delivery; re-add with `add: true` and no delivery keeps it; edit replaces it; remove; a Discord user id in delivery → 400 with the "is an account" message.
  - `TestProjectAPIGone`: section writes on a missing project → 404.
  - `TestProjectIntercomLog`: with a fake `SquawkReader` holding a project squawk, `?section=intercom` returns it and `logConfigured` is true; without the section, `squawks` is `[]`.
  - The existing `project_edit_test.go` / `projects_test.go` keep passing after the extraction.
- [ ] **Step 2:** `go test ./internal/jam/adminui/ -run 'Project'` → FAIL.
- [ ] **Step 3:** Implement, extract, then regenerate types.
- [ ] **Step 4:** `go test ./internal/jam/adminui/` → PASS.
- [ ] **Step 5: Commit** `feat(admin): projects API`.

### Task 2: Projects list, project overview, and the shared TypeAhead and ContextPanel

**Files:**
- Create:
  - `web/app/src/admin/ui/TypeAhead.tsx`, `web/app/src/admin/ui/ContextPanel.tsx`;
  - `web/app/src/admin/projects/ProjectsPage.tsx`, `ProjectPage.tsx`, `useProject.ts`, `projects.test.tsx`.
- Modify: `web/app/src/admin/AdminApp.tsx` (routes `config/projects`, `config/projects/:project`)

**Interfaces:**
- Produces:
  - `TypeAhead({ label, kind, project?, list?, multiline?, value, onChange, required?, description? })`:
    - a Mantine `Autocomplete`; with `multiline`, a `Textarea` that completes the current line's last comma token;
    - suggestions from `GET /app/admin/suggest?kind=&project=`, cached per kind and project with `useQuery` (staleTime 30 s);
    - with `list`, it completes the last comma-separated token.
  - `ContextPanel({ panel: ContextPanel; onSave: (yaml: string) => Promise<unknown>; onClear: () => Promise<unknown> })`:
    - the facts badges ("N / B bytes", red when `over`; leaves; resources);
    - the core `<pre>`, the leaves list and the resources table;
    - "None — agents get no <scope> section." when `empty`;
    - an "Edit agent context" `<details>` holding a YAML `EditForm` (with the drift confirm when managed) and Clear (`ConfirmAction` "Remove this <scope> agent context?" plus the drift note), Clear only when not empty.
  - `useProject(name, section?)`: query key `["admin","project",name,section ?? ""]`. `useProjectWrite(name)` is a mutation helper: it posts or deletes and sets all `["admin","project",name,*]` queries to the answer.
  - **ProjectsPage** has a `DataTable` (Name link with `ManagedBadge`, Members, Roles, Rooms) and a "New project" form that navigates to the created path.
  - **ProjectPage** (overview) has:
    - a header with the name, `ManagedBadge` and the chat chip (or "tracker @-mentions only");
    - the Rename inline form (when `renamable`) and Delete (`ConfirmAction`, or a disabled button with the in-use title);
    - tiles linking members, agents, intercom and escalation, showing their counts as the old tiles do;
    - Chat service (a select: "" → "none — tracker @-mentions only");
    - `ContextPanel` (scope "project").

    Not found → "Project not found" with a link to `config/projects`.
- [ ] **Step 1: Write the failing tests** in `projects.test.tsx`:
  - the list rows and New project navigation;
  - a rename navigates to the new path;
  - Delete is disabled with the in-use title; otherwise it confirms with the parity text and navigates to `config/projects`;
  - a chat service save posts `{service}`;
  - ContextPanel saves YAML, and Clear asks "Remove this project agent context?";
  - not found;
  - TypeAhead offers suggestions from a mocked `/app/admin/suggest?kind=users` and completes the last token in list mode.
- [ ] **Step 2:** `npx vitest run src/admin/projects` → FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** `npm run check` → PASS.
- [ ] **Step 5: Commit** `feat(app): admin projects list and overview`.

### Task 3: Members, member leaves, Intercom and the shared SquawkTable

**Files:**
- Create: `web/app/src/admin/projects/MembersPage.tsx`, `MemberPage.tsx`, `IntercomPage.tsx`, `web/app/src/admin/ui/SquawkTable.tsx`, `members.test.tsx`, `intercom.test.tsx`
- Modify: `web/app/src/admin/AdminApp.tsx` (routes `…/members`, `…/members/:user`, `…/intercom`), `web/app/src/admin/users/UserPage.tsx` (exports `UserView({ refKey, ref, extra })` so the member leaf reuses it)

**Interfaces:**
- Produces:
  - **MembersPage**: a heading and the sub "the users agents in <p> can address, and where they get DMs here", then a `DataTable` with these columns:
    - User (a link to the member leaf `config/projects/<p>/members/<uid>`);
    - Handle;
    - Delivery;
    - actions: an Edit `<details>` with a delivery textarea and "Save <name>", plus Remove (`ConfirmAction`).

    Below it, "Add member": a User `TypeAhead kind="users"` and a Delivery textarea, posting `add: true`.
  - **MemberPage**: the user's page (the same component as `config/users/:user`), with a "Member of <p>" section on top showing their delivery and "Remove from <p>" (`ConfirmAction` "Remove <name> from <p>?", then navigate to `…/members`). When the user isn't a member of `p`, it says "<name> is not a member of <p>." and offers no remove.
  - **IntercomPage**:
    - a heading with "Full log" (anchor `/app/admin/intercom?project=<p>`);
    - a Rooms `DataTable` (Name, Connection plus kind badge, Ref plus a post-only badge, Remove);
    - "Add room" (Name; Connection `TypeAhead kind="services"`; Ref);
    - "Recent messages": a `SquawkTable`, or "The message log is unavailable." when `!logConfigured`.
  - `SquawkTable({ rows, filtered? })` has the columns Time, From (a link to `nodeHref("agents/<id>")` when `agentId`), Channel, Project and Body (`MessageBody`). Its empty text is "No squawks logged yet." or, when `filtered`, "No squawks match.".
- [ ] **Step 1: Write the failing tests:**
  - in `members.test.tsx`:
    - add a member through the type-ahead;
    - edit delivery;
    - remove with the parity confirm;
    - the member leaf shows "Member of acme" and "Remove from acme", which navigates to Members;
    - the member leaf for a non-member shows the not-a-member line.
  - in `intercom.test.tsx`:
    - add and remove a room;
    - the post-only badge;
    - the squawk table renders a markdown body as HTML;
    - a plain body containing `<script>` shows as text;
    - "The message log is unavailable.".
- [ ] **Step 2:** `npx vitest run src/admin/projects` → FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** `npm run check` → PASS.
- [ ] **Step 5: Commit** `feat(app): admin project members and intercom`.

### Task 4: Escalation

**Files:**
- Create: `web/app/src/admin/projects/EscalationPage.tsx`, `escalation.test.tsx`
- Modify: `web/app/src/admin/AdminApp.tsx` (route `…/escalation`)

**Interfaces:**
- Produces: **EscalationPage**, with:
  - the heading sub "who is pinged, in order, when an agent in <p> is waiting";
  - each chain ("default chain" or "category <c>") as an ordered list of tiers: target badges (unknown in red, titled "Not on this project's roster") and "then wait <timeout>";
  - a category chain's Edit `<details>` holding the chain form;
  - "No escalation policy." when there are none;
  - "Edit default chain" (the chain form for `defaultChain`);
  - "Add category chain" (Category plus Tiers).

  The chain form is a Tiers `TypeAhead kind="targets" project=<p> list multiline` with the hint "one per line, user:alice,channel:eng@15m — first line is tier 0", "Save chain", and Clear (`ConfirmAction`) when the chain has tiers.
- [ ] **Step 1: Write the failing tests** in `escalation.test.tsx`:
  - the chains render, with the unknown target flagged;
  - saving the default chain posts `{category: "", tiers}`;
  - a 400 shows inline and keeps the textarea;
  - Clear confirms "Clear the default chain?";
  - adding a category chain.
- [ ] **Step 2:** `npx vitest run src/admin/projects/escalation` → FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** `npm run check` → PASS.
- [ ] **Step 5: Commit** `feat(app): admin project escalation`.

### Task 5: Browser test and docs

**Files:**
- Modify: `internal/jam/adminui/app_browser_test.go` (`TestBrowserAdminProjectRename`), `docs/web-app.md` (the routes list), `docs/usage/jam/ui-console.md` (the preview now covers projects except Agents), and the spec's status line.

- [ ] **Step 1:** Write `TestBrowserAdminProjectRename`:
  - open `config/projects/acme`;
  - rename it to `acme2` (using the native-setter helper from slice 2's test, moved into `browsertest.SetInput(sel, value)`);
  - wait for the URL `…/config/projects/acme2` and the tree link `acme2`;
  - assert there are no page problems.
- [ ] **Step 2:** `just app && just test-browser` → PASS.
- [ ] **Step 3:** Update the docs, then run the docs audit.
- [ ] **Step 4:** Run `go test ./internal/jam/... ./cmd/jam/`, `just lint` and `npm run check` → PASS. Space is tight, so skip the full `just test` if the disk is under 2 GB free, and say so in the PR.
- [ ] **Step 5: Commit** `test(admin): browser test for a project rename; docs`.
