# Admin UI: a permanent scope rail with attention badges

**Status:** design approved in brainstorming (2026-10-07), pre-plan.
**Scope:** replace the admin UI's top nav and per-project tree (from the IA
redesign, #390–#392) with a **permanent left rail** — **Jam** first, then every
project — whose selection decides the tab strip and content on the right, and
give the rail, the tabs and list rows **attention badges** computed by one shared
function.
**Does not change:** page URLs (every route from the IA slices keeps working),
write endpoints, the content of each page, `/me`, the CLI, the admin API, the store.
**Builds on:** [the IA spec](2026-10-07-admin-ui-ia-design.md) — its sections,
URLs and Agent vocabulary carry over unchanged.

## Problem

The IA redesign put projects behind a tree that only exists once you are inside
one project, and the top nav mixes cross-project pages with one Projects entry.
There is nowhere permanent to see *which* project needs attention. A rail that is
always on screen — Jam plus every project — fixes both: it is the project list,
and it is where per-scope attention lives.

## 1. Layout

```
┌──────────────────────────────────────────────── Jam ADMIN ──── [search /] ┐
│ ◉ Jam    │ acme                                                           │
│──────────│ Overview  Members  Agents③  [Roles]  Intercom  Escalation      │
│ ● acme ③ │ ──────────────────────────────────────────────────────────────  │
│   beta   │ Roles / reviewer                                               │
│   ops  ① │ …page content…                                                 │
│ + New    │                                                                │
└──────────┴────────────────────────────────────────────────────────────────┘
```

- **Title bar** (unchanged): the "Jam ADMIN" brand and the search box (`/`
  focuses it). The six-section top nav is removed.
- **Rail** (every page): **Jam**, a divider, every project (live, by name), then
  **+ New project** (opens the create form — today's Projects-page form — in a
  small panel; a created project is selected). The current scope is highlighted
  (`aria-current="page"` on its link). On narrow screens (≤ 720px) the rail is a
  drawer opened from a ☰ button in the title bar.
- **Content**: the scope's name, its **tab strip**, then the page. Below tab level
  a breadcrumb names the detail (`Roles / reviewer`, `Agents / s-123`); a tab's
  own page has none.

## 2. Scopes and tabs

**Jam** — `Dashboard · Agents · Users · Specs · Intercom`

| Tab | Route(s) |
|---|---|
| Dashboard | `/ui/` |
| Agents | `/ui/agents`, `/ui/agents/{id}[/session]` |
| Users | `/ui/users`, `/ui/users/{id}` |
| Specs | `/ui/specs` → Kits; `/ui/kits[/{n}]`, `/ui/destinations[/{n}]`, `/ui/model-specs[/{n}]` with the Kits · Destinations · Model-specs sub-strip as a second row |
| Intercom | `/ui/intercom` |

**A project** — `Overview · Members · Agents · Roles · Intercom · Escalation`, at
`/ui/projects/{p}[/members|/agents|/roles|/intercom|/escalation]`; role pages
`/ui/projects/{p}/roles/{r}` sit under Roles.

- **The project tree is removed** (children — roles, live agents, rooms — appear
  only on their section pages). Its out-of-band refresh on project writes goes
  with it; project writes answer with their section as before.
- **`/ui/projects`** (the projects list) is retired: the rail is the list. It
  301-redirects to `/ui/`; the create form moves to the rail's **+ New project**.
- **Agent pages** live under Jam → Agents (`/ui/agents/{id}`). Reached from a
  project, the page keeps the project scope when the link carries
  `?project=<name>` (links from a project's pages add it);
  otherwise Jam is the scope. The page content is the same either way.
- **Search** keeps no scope (rail shows no selection).
- Scope and tab are declared per page (extending today's `navSection`): a page
  names its scope kind (Jam or project) and its tab; a project page's scope name
  comes from its payload.

## 3. Attention

One pure function computes what needs attention:

```go
type attnKind string // "broken" | "stale" | "config"
type attnItem struct {
    Kind    attnKind
    Scope   string // "" = Jam, else the project name
    Tab     string // the tab that owns it: "agents", "roles", "escalation", "specs"
    Subject string // what it is about (agent id, role name, kit name, …)
    Href    string // where to fix it
    Why     string // one line, e.g. "lost", "image stale", "escalation target user:ghost names nobody"
}
func attention(store jam.Store, img jam.ImageResolver) []attnItem
```

| Kind | Severity | Raised when | Scope · tab |
|---|---|---|---|
| broken | red | a studio is `lost` or `terminating`, or has egress re-apply failures | its project · Agents |
| stale | amber | a studio's image or connector is `stale` | its project · Agents |
| config | amber | an escalation tier target names nobody on the project | the project · Escalation |
| config | amber | a role's kit or model-spec does not resolve | the project · Roles |
| config | amber | a destination is in a connector conflict | Jam · Specs |
| config | amber | a kit's current version does not parse | Jam · Specs |

**Badges** are one count of items, colored by the worst kind inside (red if any
broken, else amber), with a hover title listing the breakdown
("1 broken · 2 out of date"):

- **Rail:** each project's badge counts its own items; **Jam's counts only Jam-scope
  items** (never the sum of projects).
- **Tabs:** each tab shows the count of its scope's items with that `Tab`.
- **Rows:** the list rows those items name carry the same flag (agent rows, role
  rows, escalation targets — several already flag; they read from `attention`).
- **Overview / Dashboard:** a **Needs attention** card lists the scope's items
  (Why + link), or says nothing needs attention.

**Live:** the rail is a fragment re-fetched every 3 s (`hx-trigger="every 3s"`,
its own small GET endpoint) so badges stay current on every page; tab badges and
the Needs attention card update on navigation and on the pages that already poll.

## 4. Testing (hermetic)

- `attention`: a table test per row of the §3 table, plus scope assignment (Jam
  vs project) and no double counting.
- Layout: every route renders the rail with the right scope current, the right
  tab strip and tab current; Search shows no scope.
- Badges: rail counts and colors (red vs amber, Jam-only for Jam), tab counts,
  hover breakdown, the Needs attention card; the rail fragment endpoint.
- Redirect: `/ui/projects` → `/ui/`; + New project creates and selects.
- Agent page scope via `?project=`.
- Existing page tests keep passing with nav assertions moved from the top nav to
  the rail/tabs.

## 5. Docs

`ui.md` (layout: rail, scopes, tabs, badges, search), `ui-projects.md` (tabs
instead of the tree; Needs attention), `ui-pages.md` (agent page scope), keeping
`ui.md` ≤ 200 lines. Use docs-author / docs-audit.

## 6. Slicing

One slice if the plan stays reviewable; otherwise (a) rail + scopes + tabs with
the tree and top nav removed, then (b) `attention` + badges + Needs attention.
