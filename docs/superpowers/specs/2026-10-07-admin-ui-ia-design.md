# Admin UI information architecture: project tree, Agents, Specs

**Status:** design approved in brainstorming (2026-10-07), pre-plan.
**Scope:** restructure the Jam operator admin UI (`/ui/`, `internal/jam/adminui`):
a six-section top nav, a project page organized as a left-side tree with nested
routes, and **Agent** as the identity that sessions and studios hang off. Pure UI
restructuring.
**Does not change:** write endpoints and their semantics, htmx fragment
endpoints, `/me`, the at-jam CLI, the admin API, the store, or the
shared stylesheet (`internal/jam/uiassets`) beyond new component styles.
**Builds on:** the redesign arc #289–#302 (detail pages, type-ahead, search) and
intercom slices 1–4 (users registry, rooms, session ids = actor ids).

## Problem

The top nav is ten flat, global tabs (Dashboard, Projects, Studios, Users,
Actors, Roles, Kits, Destinations, Model-specs, Intercom), but most things are
scoped to one project: roles, members, rooms, escalation, the studios raised for
it. The project page is one long page that points elsewhere for its own roles
("add one on Roles"). Actors, studios and sessions are three views of one thing
(since intercom 1b an actor's id *is* its session id, and a studio is that
session's current runtime) spread over two tabs and two page families, and
actors have no page at all. Several links dead-end: a role's kit opens the kit
*list*, role holders and actor ids are plain text, model-specs list no users and
aren't searchable, and the four dashboard studio tiles open the same unfiltered
list. Tab highlighting keys on the page title, so detail pages can only ever
highlight the tab whose name they borrow.

## Vocabulary (as the UI presents it)

- **User** — a person: logins, OIDC identities, accounts, project memberships.
- **Agent** — an identity an agent runs as: an enrolled actor (id + token +
  grants). **Sessions and studios are details of an agent**: the session is the
  agent's logical run (same id), the studio its current runtime (at most one).
  An agent enrolled by hand (`enroll`) may have no studio at all.
- **Specs** — the reusable definitions roles point at: kits, destinations,
  model-specs.

## 1. Top nav

`Dashboard · Projects · Users · Agents · Specs · Intercom — [search]`

| Section | Landing | Holds |
|---|---|---|
| Dashboard | `/ui/` | unchanged content; tiles re-pointed (§6) |
| Projects | `/ui/projects` | project list; each project's tree (§2) |
| Users | `/ui/users` | people only (today's Users list + user pages) |
| Agents | `/ui/agents` | every agent (§3) |
| Specs | `/ui/specs` → 302 `/ui/kits` | sub-tabs **Kits · Destinations · Model-specs** over today's list + detail pages (URLs unchanged) |
| Intercom | `/ui/intercom` | unchanged (Jam-wide channel log) |

**Highlighting is by section, not title.** Every page declares its nav section
explicitly (one of `dashboard`, `projects`, `users`, `agents`, `specs`,
`intercom`, or none — search). The layout highlights from that value; `.Title`
goes back to being only the document title. A page with sub-tabs (Specs,
project tree) also declares its sub-section the same way.

The global **Roles** and **Actors** tabs are removed (their content moves into
the project tree and Agents respectively; old URLs redirect, §5).

## 2. Project page: a left-side tree with nested routes

Every project-scoped page renders a shared two-column frame: the **project tree**
on the left, the page content on the right.

```
▾ acme
   Overview
   Members
 ▸ Agents (3)
 ▾ Roles
     implementor
     reviewer
 ▸ Intercom
   Escalation
│ [content]
```

| Tree node | Route | Content |
|---|---|---|
| project (root) / Overview | `/ui/projects/{p}` | summary: counts (members, agents live/total, roles, rooms), chat service, the project context panel; each count links to its section; rename/remove as today |
| Members | `/ui/projects/{p}/members` | today's Members card + add-member form |
| Agents | `/ui/projects/{p}/agents` | agents of this project (an instance or a grant in it), same table as the Agents tab; children = its live/raising agents |
| Roles | `/ui/projects/{p}/roles` | this project's roles + **New role** form (project implied by the URL; no project picker); children = every role |
| a role | `/ui/projects/{p}/roles/{r}` | today's role page, unchanged content |
| Intercom | `/ui/projects/{p}/intercom` | the project's rooms (today's Rooms card: list, add, remove) above the channel log filtered to this project (every channel in it, as the full Intercom log's `project=` filter); children = its rooms |
| Escalation | `/ui/projects/{p}/escalation` | today's escalation chains + forms |

- **Server-rendered, no JS required.** Each branch with children is a
  `<details>`; the branch containing the current page renders `open`, the
  current node is marked `aria-current="page"`. Others start collapsed and expand
  natively. Agents' children are capped (live/raising only; the section page has
  them all); its label shows the live count.
- **Narrow screens** (≤ 720px, the existing breakpoint): the tree collapses to a
  `<details>` "acme ▸ Roles" disclosure above the content.
- **Breadcrumbs follow the path**, every segment a link:
  `Projects / acme / Roles / reviewer`.
- **Writes are unchanged.** The cards keep posting to today's write endpoints;
  only where the forms render moves. A create on the project's Roles page
  responds with the same HX-Redirect to the new role page (now the nested URL).
- **Project names stay in the URL** (as today). A renamed project's old name
  404s as it does today; no alias resolution is added here.

## 3. Agents

**List — `/ui/agents`:** every actor, live or not. Columns: id, kind
(standing / ticket / personal / manual / enrolled — derived: `SessionKind`, an
ephemeral instance with a `Unit` is a ticket, without one manual, an actor with no
instance enrolled), project · role (linked), phase, activity, last seen. A filter
matching the dashboard's four studio tiles (`?phase=live|raising|idled|attention`, attention = lost or terminating,
counted exactly as the tiles count today) and
the existing 3s htmx poll for the table. **Enroll** (today's Actors form) and
**Raise** (today's Studios form) live here as collapsed panels.

**Agent page — `/ui/agents/{id}`** (canonical; not nested under a project — an
enrolled agent may hold grants in several projects):

- **Header:** id, kind, standing name or personal owner (linked to the user),
  project and role (linked to the nested role page), phase pill; actions
  **Teardown** (when it has a studio and a supervisor is configured) and
  **Revoke**.
- **Identity:** grants as role chips linking to role pages, with grant / ungrant
  (today's Actors-row controls), token expiry.
- **Studio:** today's studio hub content (runtime, waiting/escalation state,
  egress failures, streams, newest-50 squawks). With no studio: "No studio" and
  the audit view (streams/squawks) as today's gone-studio page.
- **Session:** **Open live timeline** → `/ui/agents/{id}/session` (today's
  session page, unchanged content).
- Breadcrumb `Agents / {id}`; reached from a project tree, the nav section is
  still Agents and the header's project link leads back.

Role holders (role page) and actor ids anywhere in the UI link to the agent page.

## 4. Users and Specs

- **Users** keeps today's user list and pages; it never held the Actors roster
  as a sub-tab, and that roster now lives in Agents. Where a user page lists a
  personal session, it links to that agent.
- **Specs** is a grouping: `/ui/specs` redirects to `/ui/kits`; the Kits,
  Destinations and Model-specs list and detail pages render a sub-tab strip
  (Kits · Destinations · Model-specs) under the Specs section. URLs unchanged.

## 5. Redirects (301, query string preserved)

| Old | New |
|---|---|
| `/ui/roles` | `/ui/projects` |
| `/ui/roles/{p}/{r}` | `/ui/projects/{p}/roles/{r}` |
| `/ui/coves` | `/ui/agents` |
| `/ui/coves/{id}` | `/ui/agents/{id}` |
| `/ui/coves/{id}/session` | `/ui/agents/{id}/session` |
| `/ui/actors` | `/ui/agents` |

GET only; write endpoints keep their current paths (`/ui/coves`, `/ui/roles/…`,
`/ui/enrollments`, `/ui/actors/{id}/grants…`) — renaming them is out of scope.
The session-events fragment the session page polls follows the page
(`/ui/agents/{id}/session/events`), with the old path kept working.

## 6. Dead ends and polish

- A role's kit links to the kit's page (`/ui/kits/{name}`, pinned version via
  `?v=` when pinned), not the list.
- Model-spec pages list the roles using them ("Used by", like kits and
  destinations); search covers model-specs.
- Dashboard: the four studio tiles link to `/ui/agents?phase=…`; the count tiles
  follow the new sections (Projects, Agents, Users, Specs counts).
- Search: actor hits land on the agent page; role hits on the nested role page;
  studio hits on the agent page.

## 7. Slices (each one PR, shippable alone)

1. **Nav + project tree.** Section-based highlighting; new top nav (Agents/Specs
   tabs pointing at today's Studios/Kits pages until slices 2–3 land, with
   interim sub-tab strips — Studios · Actors, Kits · Destinations ·
   Model-specs — so no list page loses its nav entry); the
   project frame, tree and nested routes (§2) with role pages moved; redirects
   for `/ui/roles…`; breadcrumbs.
2. **Agents.** `/ui/agents` list, the agent page merging studio + session +
   actor (§3), Enroll/Raise relocated, `/ui/coves…` and `/ui/actors` redirects,
   holders/ids linked, Users without Actors.
3. **Specs + dead ends.** Specs grouping (§4), §6 fixes.

## 8. Testing (hermetic, `httptest` against the existing in-memory store)

- Every new route renders 200 with the expected section highlighted
  (`aria-current` on exactly one top-nav link) and, for project pages, the
  expected tree node current and its branch open.
- Every redirect in §5: status 301, `Location` correct, query preserved.
- A not-found project / role / agent renders the 404 page with the right section.
- Role create from the project's Roles page lands on the nested role page.
- Links: role → kit page; holder → agent page; dashboard tile → filtered Agents;
  model-spec "used by"; search hits → new URLs.
- Existing write-endpoint tests unchanged and passing.

## 9. Docs

Update with each slice: `docs/usage/jam/ui.md` (nav, sections, search) and
`docs/usage/jam/ui-pages.md` (project tree pages, agent page, Specs), keeping
both within budget (ui.md is already over; move page-level detail to
ui-pages.md rather than growing it). Note the URL moves and redirects.
