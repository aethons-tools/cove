---
summary: The Jam admin console's observability surface at `/app/admin/` — the Projects rail and project tabs, the Specs sub-tabs, each page's contents (Dashboard, Agents, Health, Config, Users and the Specs tables), search, the Channel log, and the agent session timeline.
read_when: You want to find your way around the admin console — what a tab or list shows, the Projects rail, how search matches, the Agents status filters, the Channel log's filters and Legacy tab, or an agent's session timeline. For the routes, the login and the title bar, read ui.md; to change something from the UI, read ui-editing.md.
owns: the `/app/admin/` console's page-by-page contents (Dashboard tiles, Agents list, Health, Users/Specs tables, fixed table order); the Projects rail and Specs sub-tabs; console search; the Channel log page (`/app/admin/intercom`); the `/app/admin/agents/{id}/session` timeline
prereqs: ui.md for the routes, the login and the title bar these pages sit under; ui-attention.md for the badges; ui-editing.md for what can be changed
tier: leaf
updated: 2026-10-24
---

# The admin console (`/app/admin/`)

The pages behind the [title bar](ui.md#the-title-bar-and-look)'s admin tabs.

**Projects** (`/app/admin/projects`, the project list) keeps a left **rail** of
every project with **+ New project**; selecting one puts its tabs — **Overview ·
Members · Agents · Intercom · Escalation** — over its pages
([ui-projects.md](ui-projects.md)). On a narrow screen the rail is a drawer (☰
in the slim bar). Every other tab is full width with no rail. A detail page
sits under its tab (a role page: Projects, its project's Agents; a kit: Specs)
with a breadcrumb below it. **Specs** (`/app/admin/specs`, opening on Kits)
groups **Kits · Destinations · Model-specs** under a sub-tab strip. The
title-bar tabs, project rail entries, project tabs and rows carry **attention
badges** ([ui-attention.md](ui-attention.md)). The console's pages:

- **Dashboard** (`/app/admin/`) — summary tiles: running / waiting / setting-up /
  lost-or-terminating / idled agents, each opening the Agents list filtered to it, and counts
  of projects, agents, users and specs (kits + destinations + model-specs) —
  agents, users and specs open their tab, projects are a plain count; Jam's **Needs attention** card; then the Jam-wide **Agent context** card
  ([ui-pages.md](ui-pages.md#agent-context-cards)), above the running agents.
- **Search** — the box in the slim bar (press `/` from anywhere) searches every
  page's objects at once: agents (id, unit, owner, standing name,
  project/role, grants — one hit per id), roles (project/name, kit,
  destinations), projects, kits (name, current prompt and egress), destinations
  (name, route, upstream, env keys), model-specs (name, type, model, principal),
  users (name, logins, OIDC subject, account handles and ids) and rooms, and
  squawk bodies (newest 10; the rest via the Channel log's `q=`).
  Matching is case-insensitive substring, at least 2 characters; results are
  grouped and link to each object's page. **Enter** jumps to the page when one
  object's name is the whole query (e.g. an agent id or `acme/dev`), else opens
  `/app/admin/search?q=…`, which updates as you type. Session events aren't searched.
- **Agents** (`/app/admin/agents`) — each enrolled identity and each studio, one row
  per id, with its **kind** (`standing`, `personal`, `ticket` — a session with
  a unit, `manual`, or `enrolled` — no studio), project/role, [status](ui-pages.md#agent-status),
  connector and image status ([coves.md](coves.md#the-studio-verbs)); it
  **auto-refreshes every 3 seconds**. Status filters
  (`?status=running|waiting|setting-up|idled|attention`; old `?phase=` links still work) match the dashboard tiles. Enroll,
  raise and teardown: [ui-editing.md](ui-editing.md). Each id opens the agent's
  page, its **Access** card included ([ui-pages.md](ui-pages.md#agent-pages));
  `/app/admin/coves…` and `/app/admin/actors` redirect here.
- **Health** (`/app/admin/health`) — operator-attention conditions (open with fix; resolved in 7 days); open critical/warning also badge the Health and Dashboard tabs — see [monitoring.md](monitoring.md). It links the **Channel log** (`/app/admin/intercom`): a read-only, filterable, newest-first table of
  the channel log, with the frozen legacy log on a Legacy tab. See [Channel log](#channel-log).
- **Config** (`/app/admin/config`) — the tracked config repo, its managed objects and their drift, Diff/Apply: [ui-config.md](ui-config.md).
- **Users / Kits / Destinations / Model-specs** — control-plane tables, editable
  here ([ui-editing.md](ui-editing.md)); roles live in their project.

Every table has a fixed order — agents by id; roles by project, then
name; kits and destinations by name; squawks newest-first — so rows don't shuffle
across a poll or an edit, and the JSON admin API and CLI lists match it.

## Channel log

The Channel log page (`/app/admin/intercom`, under **Health**) is a read-only view of Jam's
[channel log](intercom.md#enabling-it) (always available; it lives in Postgres — see
[serve.md](serve.md#postgres-store-store-postgres)). It shows a filterable, newest-first table of squawks,
each with its sender and channel (`<label> · <kind>`, linking to that channel's
squawks): filter by project, participant (an id — a user, session, account or
channel), a body substring, and a date window (the `since`/`until` bounds are interpreted as UTC
day boundaries; a malformed date is ignored, with a notice, rather than
silently applied). Filters live in the URL, so a filtered view is shareable via
link.

The page is a manual-refresh snapshot, not a live tail — reload to see new
squawks. The **Legacy** tab (`?log=legacy`) shows the log from before the
channel log, frozen, as it always did: filter by `kind:ref` participants, each
recipient badged internal or external.

Unlike the roster/kit/destination pages, the Channel log has no mutation — the UI only
reads the Log (still a full snapshot per load — pagination is a later phase).

## Session timeline

`/app/admin/agents/{id}/session` (linked from running-agents tables and the agent's page) shows a managed
studio's agent session: a stream selector (current and past streams), header
totals (turns = results answered, episodes, tool calls, tokens in/out, cost = last total per episode), and a flat event list, each
event tagged with its turn (`tN`) (text, thinking, tool use/results expandable, results, gap and truncation
markers), with a raw-JSON toggle. `system`/`thinking_tokens` events are hidden
behind **show progress events**. It updates live over SSE from `/app/admin/agents/{id}/session/events` (backfill, then
live; reconnects resume via `Last-Event-ID`). The feed and the event model are
served by `internal/jam/sessionview`, shared with the member view (which tails
the newest events instead of replaying the whole stream). Storage, retention,
and sensitivity: [session-events.md](session-events.md).
