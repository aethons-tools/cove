---
summary: The admin UI's project pages — the Projects tab's project list and rail, a project selected in it, its tabs (Overview, Members, Agents, Intercom, Escalation) with one URL per tab under /app/admin/projects/<name>, what each tab shows and edits, and the role page at /app/admin/projects/<project>/roles/<name>.
read_when: You are viewing or editing one project in the Jam admin UI — its members, agents, roles, rooms and recent messages, escalation chains, chat service or agent context — or a role's page (scope, egress, allocation, standing agents), or you open the Projects tab's project list, or you followed an old /app/admin/roles link.
owns: a project's tabs and their pages (/app/admin/projects/<name>[/members|agents|roles|intercom|escalation]), creating a role from a project, the role page (/app/admin/projects/<project>/roles/<name>), the project list (/app/admin/projects), and the /app/admin/roles redirects
prereqs: ui.md for reaching the UI, the rail and tabs; ui-attention.md for the badges and Needs attention; ui-editing.md for the write banner and the gate/CSRF/audit rules; projects.md for the project lifecycle; roster.md for roles and grants
tier: leaf
updated: 2026-10-23
---

# Admin UI project pages

The **Projects** title-bar tab opens the project list (`/app/admin/projects`):
every project with its attention badge, and the rail of projects beside it —
the rail stays on every project page and only there. Select a project and its tabs — **Overview · Members ·
Agents · Intercom · Escalation** — sit over its pages. Each tab is its own URL,
so it can be linked and bookmarked. Roles are listed on the Agents tab (they are
what its agents are granted); a role page sits under Agents with an `Agents /
dev` breadcrumb. Everything scoped to one project lives here, and its
tabs carry the project's [attention badges](ui-attention.md).

| Tab | URL |
|---|---|
| Overview | `/app/admin/projects/<name>` |
| Members | `/app/admin/projects/<name>/members` |
| Agents | `/app/admin/projects/<name>/agents` |
| (a role) | `/app/admin/projects/<name>/roles/<role>`, under Agents; `…/roles` redirects to Agents |
| Intercom | `/app/admin/projects/<name>/intercom` |
| Escalation | `/app/admin/projects/<name>/escalation` |

**Old links.** `/app/admin/roles` redirects (301) to `/app/admin/`, and `/app/admin/roles/<project>/<role>` to the role's page
here. The write endpoints keep their paths. A new project is created from the
rail's **+ New project**, which opens it.

## Sections

Each tab's edits are in place: a write answers with that tab's content
re-rendered.

- **Overview** — the project's **Needs attention** card; counts (members,
  agents live of all, roles, rooms, escalation chains), each linking to its tab; the **chat service** (none — tracker
  @-mentions only — or `discord`); the project's **Agent context** card
  ([ui-pages.md](ui-pages.md#agent-context-cards)). **Rename** (except
  `default`) renames the project and opens its new URL; **Delete** is disabled
  while a member, a role, a session not yet gone or an actor's grant still
  references the project, and names what does — the same rule as `project rm`
  ([projects.md](projects.md)).
- **Members** — the users agents here can address (linked to their user page,
  with handle and delivery). **Add member** (a user, and delivery: one
  `service:address` per line); per member, **Edit** delivery and **Remove**.
- **Agents** — first the project's **Roles** (linked, with destinations, TTL and
  kit; with a runtime supervisor, a **Request** per role —
  [ui-editing.md](ui-editing.md#runtime-studios)); **New role** creates one in
  this project and opens its page. Then one table of the project's agents: each
  running in it or holding a grant into it (one row per agent), with its kind,
  roles here, [status](ui-pages.md#agent-status), image and last seen; each opens its agent page in
  the project.
- **Intercom** — the project's rooms (service, ref): **Add room** (name;
  connection: a connection name, or a service for its connection; ref; an
  existing name is rebound) and **Remove**. Below them, the project's newest 50
  messages from the channel log, with **Full log** opening the
  [Channel log](ui-console.md#channel-log) filtered to the project.
- **Escalation** — the default chain and each category's chain, as ordered
  tiers of targets with their wait ([escalation.md](escalation.md)). Edit a
  chain as one `targets@timeout` per line (the CLI's `--tier`; the first line is
  tier 0, the timeout a positive duration), add a category chain, or **Clear**
  one. A target that names nobody on this project's roster is flagged red —
  flagged, not blocked.

## Role pages

Each role name (on a project's Agents tab, a grant chip, or a running-agent row)
links to its page, `/app/admin/projects/<project>/roles/<name>`, which shows and edits the whole
role, one section at a time — each with a pre-filled **Edit** form that saves
only that section:

- **Scope** — destinations with the credential the broker injects for each
  (the role's mapping, or the destination's default), addressing, TTL, kit and
  [model-spec](model-specs.md#binding-a-role) binding (blank = `claude-default`,
  shown in the page head; the new-role form takes one too).
  The destinations field uses the `name=credential` syntax; a bare name uses
  the destination's default credential. Empty addressing means the role can't
  squawk anyone.
- **Egress** — the role's domain list, or "kit default" when it sets none.
  Saving sets a policy (an empty list allows nothing beyond the sealed base and
  the kit's infra domains); **Reset to kit default** removes it. Running
  agents pick up the change on the supervisor's next reconcile.
- **Allocation** — session caps and the personal-session idle ladder.
  Durations take `30m`/`1h30m` (or bare seconds); a blank field is unset, and
  the page says what applies when unset.
- **Standing agents** ([standing sessions](standing-sessions.md)) — declare, [upgrade](standing-sessions.md#upgrading-a-standing-session), [reset](standing-sessions.md#reset) and dismiss;
  each shows its [status](ui-pages.md#agent-status), flagged **image stale** per [coves.md](coves.md#the-studio-verbs) (its Upgrade button highlighted) and any pending upgrade; a queued upgrade or pending reset flashes as accepted.
- **Holders** and **Running** — the agents granted the role (linked; marked where the
  grant overrides the scope; grants are managed on the agent's page) and the role's
  running agents.

**Request agent** and **Delete** (which returns to the project's Agents tab) are on the page header. These writes share
one lock with the JSON admin API's role, egress and standing routes, so an edit
here and a CLI change can't overwrite each other.
