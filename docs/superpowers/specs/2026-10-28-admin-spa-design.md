# The admin UI in the Jam app, with a Config tree — design

**Status:** draft, awaiting the user's approval.
**Context:** the user area (`/app/user/`) is the React app in `web/app` (the
[Jam SPA design](2026-10-26-jam-spa-design.md), slices 1–9, merged). The admin area
(`/app/admin/`, package `adminui`) is still server-rendered Go templates plus htmx:
about 7,200 lines of Go, 2,700 of templates, 135 routes. That design said the admin
area would follow on the same foundation, and this is that sub-project.

## Intent

Bring the whole admin UI into the Jam app, so admin and user pages share one look and
one set of components that grows as each screen moves over.

The main change goes beyond the port itself: **Projects, Users and Specs become one tree in a
left rail, under a title-bar tab named Config**, the way the Inbox has its rail of
conversations. Today's Config page (the tracked config source: plan, apply, managed
marks) becomes the tree's **Tracking** node.

**The rule (the user's): the tree is the navigation.** A page has no significant
navigation of its own: no project tab row, no Specs sub-tabs, no Overview/Setup or
Overview/Draft sub-tabs, no Projects rail.

## Decisions (the user, 2026-10-28)

- **Config tree**, top to bottom (the user's sketch, with "Agents" replacing their
  first label "Agent Roles"):
  ```
  Tracking
  Projects
    <project>              → the project's overview
      Members
        <user>
      Agents               → today's project Agents view: its running agents and its roles
        <role>
      Intercom
      Escalation
  Users
    <user>
  Kits
    <kit>
  Destinations
    <destination>
  Model Specs
    <model spec>
  ```
  There's no "Specs" node: Kits, Destinations and Model Specs are top-level.
- **Dashboard, Agents and Health stay standalone title-bar tabs** for now, beside Config.
- **All of them move to the app** in this sub-project, Config and the three tabs alike,
  for a consistent look and so components are shared as they're built.
- **Running agents live under the Agents tab only.** They are not tree leaves. A
  running agent clicked anywhere (a project's Agents view, an attention card) opens
  `agents/{id}` on the Agents tab, full width, leaving Config.
- **Badges split by kind.** Runtime trouble (a lost, terminating or egress-failing
  studio; a stale image or connector) counts on the **Agents** badge. Config trouble
  (a role naming a missing kit or model spec, an escalation target naming nobody, a
  broken kit, a conflicted destination) counts on the **Config** badge and its tree
  nodes. Health keeps its conditions; Dashboard keeps the total.

## Goals, non-goals, anti-goals

- **Goals:** the tree above; every admin screen in the app with today's behaviour;
  a shared admin component set (below); one old-UI deletion at the end.
- **Non-goals:** new admin features; changing who may do what; changing what the
  CLI or the config file can express; changing the user area beyond what sharing the
  shell needs.
- **Anti-goals (a failure even if it works):**
  - tabs or sub-navigation creeping back into pages;
  - an admin write that skips today's checks (origin guard, `If-Match` etags, managed
    marks, live-session refusals), or a refusal that hides why;
  - a long-lived split where some admin screens are old and some new.

## 1. Architecture and serving (my calls, following the SPA design)

- **One bundle, two areas.** `web/app` serves both `/app/user/` and `/app/admin/`. The
  admin screens are lazily loaded route chunks, so the Inbox's initial size budget
  (350 KB gzipped) doesn't grow; the admin's initial chunk gets its own budget of
  the same size.
- **Shared assets path.** Today the bundle's assets live under `/app/user/assets/`,
  behind the member gate, and an admin needn't be a member (a loopback admin has no
  user). Vite's `base` becomes `/app/`, and the hashed assets are served at
  **`/app/assets/`**, ungated like `/app/static/`: they are code, not data. Each area's
  `index.html` is served by `webapp` behind that area's own gate with its own
  bootstrap.
- **Admin bootstrap** (`GET /app/admin/…` page, and `GET /app/admin/api/bootstrap`):
  brand, principal (name, member or not, Local or not), the title-bar tabs with their
  badges, and the feature switches the admin screens need (setup enabled, shares live,
  config source configured, …).
- **Shell, per area.** The `AppShell`, `TitleBar` and theme are shared. The Inbox
  area keeps its rail and live stream. In the admin area, **Config pages** show the
  Config tree in the navbar, and **Dashboard, Agents and Health** are full width (no
  navbar), as today. The admin area does not open `/app/user/events` (a non-member
  admin would be refused).
- **Title bar.** For an admin: `Inbox` (members only) · `Dashboard` · `Config` ·
  `Agents` · `Health`, plus today's search box. Projects, Users and Specs leave the
  title bar. Badges follow the split in Decisions: Config sums its nodes' config
  items, Agents counts runtime items, and Dashboard is the total.
- **Migration: parallel, then one cutover**, as for the user area. The new admin grows
  at **`/app/admin/next/`** (inside the admin gate), slice by slice, while
  `/app/admin/` stays the old UI. Until cutover the old UI's title bar is unchanged,
  and the new one links only to new pages.

## 2. URLs

Each tree node has its own URL, so deep links, the back button and links from
elsewhere work. After cutover (`next/` dropped):

| Node | Path under `/app/admin/` |
|---|---|
| Tracking | `config/tracking` |
| Projects (group) | `config/projects` |
| a project | `config/projects/{p}` |
| Members, a member | `config/projects/{p}/members`, `…/members/{user}` |
| Agents, a role | `config/projects/{p}/agents`, `…/agents/{role}` |
| Intercom, Escalation | `config/projects/{p}/intercom`, `…/escalation` |
| Users, a user | `config/users`, `config/users/{user}` |
| Kits, a kit | `config/kits`, `config/kits/{kit}` |
| Destinations, one | `config/destinations`, `config/destinations/{name}` |
| Model Specs, one | `config/model-specs`, `config/model-specs/{name}` |
| Dashboard | `` (the area root) |
| Agents, an agent, its timeline | `agents`, `agents/{id}`, `agents/{id}/session` (today's `?project=` scope is dropped; old links with it redirect without it) |
| Health, the channel log | `health`, `health/intercom` |
| Search | `search?q=` |

`config` alone redirects to `config/tracking`. At cutover every old page path
(`projects/…`, `users/…`, `kits/…`, `destinations/…`, `model-specs/…`, `config`,
`projects/{p}/roles/{r}[/setup]`, `kits/{k}/draft`, `agents/{id}/setup`, `intercom`)
redirects to its new path, query kept, so bookmarks and pasted links keep working.

## 3. The Config tree

- **Data:** one `GET /app/admin/api/tree`: the projects with their members and roles,
  the users, kits, destinations and model specs, and each node's attention badge
  (today's `attention.go` config items mapped to nodes: a kit or destination item to
  that node, a role's missing kit or model spec to the role and its project's Agents
  node, Escalation items to Escalation; a group shows the sum of its children).
  Runtime (studio) items are not on the tree: they badge the Agents tab, and the
  project Agents page still flags its running agents' rows. It's polled every 3 s
  while visible, as today's rail is.
- **Behaviour (my calls):**
  - Group nodes (Projects, Members, Agents, Users, Kits, Destinations, Model Specs)
    expand and collapse; the expansion state is kept in localStorage; the branch
    holding the current page always opens.
  - Clicking a group node opens its list page, which carries the group's "+ Add"
    form (new project, new user, new kit or import, …). Clicking a leaf opens its page.
  - A **member** leaf opens that user's page at the project-scoped URL, with the
    project membership shown and "Remove from project" offered. The user's global
    page stays at `config/users/{user}`.
  - The tree has a filter box at the top that narrows the tree as you type, for Jams
    with many projects or users.
  - On a narrow screen the tree is a drawer, as the Inbox rail is.
- **Pages without their sub-tabs.** What a tab or sub-tab split becomes one page with
  sections:
  - project: today's Overview (context, chat service, rename, delete, its
    attention items). The other sections are their own tree nodes.
  - role: overview, then **Setup** as a section (loaded when expanded).
  - kit: overview (versions, pin, source, sync, the file and diff views as in-page
    selectors), then **Draft** as a section (the editor, uploads, publish).
  - an agent (under the Agents tab): overview, then **Setup** as a section.
  Selectors inside a page (a kit's version, a file, a diff, the agents' status filter)
  are controls, not navigation, and stay.

## 4. JSON API

The same pattern as `/app/user/api/` ([web-app.md](../../web-app.md), "Adding an API
route"), under **`/app/admin/api/`**:

- **Reuse the view models and checks.** Each page and fragment handler already
  builds a typed view for its template and runs its writes through `jam` with its
  checks. The API serializes those views and calls the same write paths, so
  authorization and logic don't move. A write answers with the refreshed view of
  what it changed (the JSON twin of the fragment it swaps today), or with the new
  location for creates and renames (today's `HX-Redirect`).
- **Writes:** `POST`/`DELETE` with JSON bodies (`415` otherwise). The kit draft's
  `etag` becomes `If-Match`, and uploads (draft add and replace) stay multipart with
  today's size cap. Errors are `{"error", "code"}` with the real status; a stale
  etag is `412` with a code the editor turns into "reload or overwrite".
- **Origin:** the whole `/app/admin/api/` is wrapped in `RequireSameOrigin`, as
  `/app/user/` is, and today's `guardWrite` stays in the handlers it already guards.
- **Types:** `adminui/apitypes.go`, generated by tygo into
  `web/app/src/admin/api/types.ts` (a second tygo package), with the same CI staleness
  check.
- **Live data:** the admin has no signal stream today, and this keeps it that way.
  The app polls at today's rates (studio and agents tables 3 s, the tree 3 s, health
  10 s) using TanStack Query's `refetchInterval`, only while the tab is visible. The
  agent session timeline keeps its server-sent stream, gaining the JSON-rows form the
  Thinking tab uses, so the timeline **reuses the Thinking tab's components**.
- **Suggest:** `/suggest` (type-ahead) is already JSON; the app uses it from one
  shared type-ahead input.

## 5. Shared components (built where first needed, then reused)

- `DataTable`: sortable, with row attention flags and empty states.
- `Section`: a titled page section, collapsible, optionally lazily loaded.
- `EditForm`: field layout, submit with a pending state, the server's refusal shown
  inline, reset on success (today's `data-reset`).
- `ConfirmAction`: delete, revoke, reset and other destructive actions behind a confirm.
- `AttentionList` and `AttnFlag`: the Needs attention cards and row flags.
- `TypeAhead`: tokens and lists over `/suggest` (today's `data-ta`).
- `CopyField`: tokens and YAML with a copy button.
- `DiffView`: the server-rendered diff lines.
- `ManagedMark`: the "managed by the tracked config" badge and drift warning.
- The Inbox's existing pieces (title bar, badges, `Problem`, error notifications, the
  Thinking event list) are shared, not copied.

## 6. Slices (one PR each, usable at `/app/admin/next/` on merge)

1. **Foundation:** shared assets path and Vite base; the admin mount, gate, bootstrap
   and API skeleton with the error envelope and origin wrap; tygo's second package;
   the per-area shell and title bar; the Config tree (read-only, with badges and the
   filter); `DataTable`, `Section`, `EditForm`, `ConfirmAction`.
2. **Tracking and Users:** the config source page (plan, apply, track/untrack,
   managed marks) and users (list, create, rename, logins, OIDC, accounts, delete).
3. **Projects:** list and create, overview (context, chat service, rename, delete),
   Members, Intercom (rooms), Escalation.
4. **Project Agents and roles:** the project Agents view, role create, request and
   delete, every role edit (scope, allocation, context, egress, standing), and the
   role's Setup section.
5. **Destinations and Model Specs.**
6. **Kits:** list, create, import, the kit page (versions, pin, source, sync, files
   and diffs) and the Draft section (editor with the unsaved-edits guard, add,
   rename, delete, replace, mode, publish).
7. **Agents tab:** list with the status filter, start and remove studios,
   enrollments, the agent page (grants, shares, Setup section), and the session
   timeline (on the Thinking components).
8. **Dashboard, Health, search:** the Jam context panel, the studio table, attention,
   Health with the channel log, and title-bar search with its `/` shortcut and
   `go=` jump.
9. **Cutover:** `/app/admin/` serves the app; old page paths redirect (§2);
   `adminui/templates`, the fragment handlers and their tests, `appui`'s admin tab
   list and the admin-only static JS are deleted; the user checks parity on their
   local Jam before merge. Rollback is reverting the PR.

The parity rule from the SPA design holds: a slice is done when everything the old
screen did works in the new one; new features wait until after cutover.

## 7. Security

- Same gate (`browserauth.Gate` + `RequireAdmin`); a loopback admin keeps working.
- `/app/admin/api/` under `RequireSameOrigin`; JSON-only writes; `no-store` and
  `nosniff` on every response; the app page's CSP is the user area's (no frames are
  needed on admin pages: `frame-src 'none'`).
- Authorization stays server-side. The app hiding a control is never the check.
- The ungated `/app/assets/` serves only the built bundle's hashed files (no
  listings, no `index.html` fallback), so it exposes nothing that isn't already in
  the code.
- The session timeline stays operator-only (the policy from 2026-10-24).

## 8. Testing and docs

- **Go:** each `/app/admin/api/` route gets httptest coverage of its JSON shape, of a
  non-admin's refusal, and of a cross-origin write's refusal. Today's adminui tests
  that assert write behaviour get JSON twins before the HTML routes go at cutover.
- **App:** Vitest + Testing Library + MSW as in the user area, for the tree, each
  page and the shared components.
- **Browser:** chromedp tests for the tree (navigate, deep link, back), one write per
  slice, and the kit draft editor.
- **Docs:** `docs/web-app.md` gains the admin area (mount, API, types, shell);
  the admin usage docs under `docs/usage/jam/` that describe tabs and pages are updated in
  the slice that moves them; this spec is marked built at cutover.

## Left to the builder

- Component internals, file layout under `web/app/src/admin/`, and query-key naming.
- How each fragment handler is split into a view builder shared by the HTML and JSON
  routes until cutover.
- The exact attention-to-node mapping where today's item names no single node (fall
  back to the nearest ancestor).

## Calls I made (for the user to check)

- One bundle for both areas; hashed assets moved to an ungated `/app/assets/`.
- The parallel admin at `/app/admin/next/`, then one cutover with redirects.
- URL scheme under `config/…` (§2); `config` opens Tracking.
- A tree filter box; expansion remembered; group nodes open list pages with "+ Add".
- A member leaf opens the user's page scoped to the project, with "Remove from project".
- Role Setup, agent Setup and kit Draft become page sections.
- No new live stream for admin: polling at today's rates; the agent timeline reuses
  the Thinking tab's components.
- Slice order in §6.
