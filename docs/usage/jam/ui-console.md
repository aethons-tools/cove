---
summary: The Jam admin console at `/app/admin/` (the Jam app's admin area) — its tabs (Dashboard · Config · Agents · Health), the Config tree and its filter, every page's URL, the Dashboard, Agents and Health pages, search, the Channel log, and the agent session timeline.
read_when: You want to find your way around the admin console — which tab or Config tree node holds a page, a page's URL, what the Dashboard, Agents list or Health show, how search matches, the Channel log's filters and Legacy log, or an agent's session timeline. For the routes, the login, the title bar and old-path redirects, read ui.md; to change something from the UI, read ui-editing.md.
owns: the `/app/admin/` console's navigation (tabs, the Config tree and filter, the page URL table) and its Dashboard, Agents list and Health pages (tiles, filters, fixed table order); console search; the Channel log page (`/app/admin/health/intercom`); the `/app/admin/agents/{id}/session` timeline
prereqs: ui.md for the routes, the login and the title bar these pages sit under; ui-attention.md for the badges; ui-editing.md for what can be changed
tier: leaf
updated: 2026-10-31
---

# The admin console (`/app/admin/`)

The admin console is the Jam app's admin area: four title-bar tabs —
**Dashboard · Config · Agents · Health** — and a search box. Every page is its
own URL, so it can be linked and bookmarked; the old server-rendered pages'
paths [redirect](ui.md#old-admin-pages) here.

## Finding a page

**Config** pages show the **Config tree** on the left (a drawer behind ☰ on a
narrow screen); Dashboard, Agents and Health are full width. A node's label
opens its page and its chevron opens or closes it; open branches are
remembered, the current page's branch is always open, and the **Filter** box
narrows the tree (opening everything). The tree refreshes every 3 seconds and
carries [attention badges](ui-attention.md). Its nodes:

- **Tracking** — the followed config repo ([ui-config.md](ui-config.md)).
- **Projects** → each project (its overview) → **Members** → each member,
  **Agents** → each role, **Intercom**, **Escalation**
  ([ui-projects.md](ui-projects.md)).
- **Users** → each user; **Kits** → each kit (its Draft is a section of the
  kit's page); **Destinations** and **Model Specs** → each one
  ([ui-pages.md](ui-pages.md)).

Pages have no tabs inside them: a page is a stack of sections, and a role's or
an agent's **Setup** is a section read only when opened
([inspect.md](inspect.md#in-the-admin-ui)). The URLs, under `/app/admin/`:

| Page | Path |
|---|---|
| Dashboard | (the root) |
| Tracking | `config/tracking` (`config` goes there) |
| Projects · a project | `config/projects` · `config/projects/<p>` |
| its Members · a member | `…/<p>/members` · `…/<p>/members/<user>` |
| its Agents · a role | `…/<p>/agents` · `…/<p>/agents/<role>` |
| its Intercom · Escalation | `…/<p>/intercom` · `…/<p>/escalation` |
| Users · a user | `config/users` · `config/users/<id>` |
| Kits · a kit | `config/kits` · `config/kits/<kit>` |
| Destinations · one | `config/destinations` · `config/destinations/<name>` |
| Model Specs · one | `config/model-specs` · `config/model-specs/<name>` |
| Agents · an agent · its timeline | `agents` · `agents/<id>` · `agents/<id>/session` |
| Health · the Channel log | `health` · `health/intercom` |
| Search | `search?q=…` |

An unknown path says "No such page".

## The tabs

- **Dashboard** (`/app/admin/`) — summary tiles: running / waiting / setting-up /
  lost-or-terminating / idled agents, each opening the Agents list filtered to
  it, and counts of projects, agents, users and specs (kits + destinations +
  model-specs) — agents, users and specs open their page, projects are a plain
  count; Jam's **Needs attention** card; the Jam-wide **Agent context** card
  ([ui-pages.md](ui-pages.md#agent-context-cards)); the
  [Managers](studio-managers.md) card when any are configured; then the running
  agents. It refreshes every 3 seconds.
- **Config** (`/app/admin/config/tracking`) — the Config tree and its pages
  (above).
- **Agents** (`/app/admin/agents`) — each enrolled identity and each studio, one row
  per id, with its **kind** (`standing`, `personal`, `ticket` — a session with
  a unit, `manual`, or `enrolled` — no studio), project, role, [status](ui-pages.md#agent-status),
  connector and image status ([coves.md](coves.md#the-studio-verbs)); it
  **auto-refreshes every 3 seconds**. Status filters
  (`?status=running|waiting|setting-up|idled|attention`) match the dashboard
  tiles. Enroll, raise and teardown: [ui-editing.md](ui-editing.md). Each id
  opens the agent's page ([ui-pages.md](ui-pages.md#agent-pages)).
- **Health** (`/app/admin/health`) — operator-attention conditions, open (with
  how to fix each) and resolved in the last 7 days, refreshed every 10 seconds;
  open critical and warning ones badge the Health tab — see [monitoring.md](monitoring.md). It links
  the [Channel log](#channel-log).

## Search

The title bar's search box (press `/` from anywhere but a text field) searches
every page's objects at once: agents (id, unit, owner, standing name,
project/role, grants — one hit per id), roles (project/name, kit,
destinations), projects, kits (name, current prompt and egress), destinations
(name, route, upstream, env keys), model-specs (name, type, model, principal),
users (name, logins, OIDC subject, account handles and ids) and rooms, and
squawk bodies (newest 10; the rest via the Channel log's `q=`).
Matching is case-insensitive substring, at least 2 characters; results are
grouped and link to each object's page. **Enter** jumps to the page when one
object's name is the whole query (e.g. an agent id or `acme/dev`), else opens
`/app/admin/search?q=…`, whose box updates the results as you type. Session
events aren't searched.

Every table has a fixed order — agents by id; roles by project, then
name; kits and destinations by name; squawks newest-first — so rows don't shuffle
across a poll or an edit, and the JSON admin API and CLI lists match it.

## Channel log

The Channel log page (`/app/admin/health/intercom`, linked from **Health**) is
a read-only view of Jam's [channel log](intercom.md#enabling-it) (always
available; it lives in Postgres — see
[serve.md](serve.md#postgres-store-store-postgres)). It shows a filterable,
newest-first table of squawks, each with its sender and channel
(`<label> · <kind>`, linking to that channel's squawks): filter by project,
participant (an id — a user, session, account or channel), a body substring
(`q`), and a date window (the `since`/`until` bounds are interpreted as UTC
day boundaries; a malformed date is ignored, with a notice, rather than
silently applied). **Filter** puts the filters in the URL, so a filtered view
is shareable via link; **Reset** clears them.

The page is a snapshot, not a live tail — **Refresh** to see new squawks. The
**Log** selector's **Legacy** (`?log=legacy`) shows the log from before the
channel log, frozen: filter by `kind:ref` participants, each recipient badged
internal or external.

Unlike the roster/kit/destination pages, the Channel log has no mutation — the UI only
reads the Log (still a full snapshot per load — pagination is a later phase).

## Session timeline

`/app/admin/agents/{id}/session` (linked from the agent's page as **Open live
timeline**, and from each of its streams) shows a managed studio's agent
session in the same Thinking view a member sees
([session-view.md](session-view.md#thinking)): a stream selector (`?stream=`,
else the newest; with none yet it follows the first to start), header totals
(turns, episodes, tool calls, tokens in/out, cost), and the events, live. It
reads `/app/admin/api/agents/{id}/session` and its `/events` feed (SSE:
backfill, then live; reconnects resume via `Last-Event-ID`), operator-only.
The feed is served by `internal/jam/sessionview`, shared with the member view.
Storage, retention, and sensitivity: [session-events.md](session-events.md).
