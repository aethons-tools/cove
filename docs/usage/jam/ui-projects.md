---
summary: The admin UI's project pages — a project selected in the rail, its tabs (Overview, Members, Agents, Roles, Intercom, Escalation) with one URL per tab under /ui/projects/<name>, what each tab shows and edits, and the role page at /ui/projects/<project>/roles/<name>.
read_when: You are viewing or editing one project in the Jam admin UI — its members, agents, roles, rooms and recent messages, escalation chains, chat service or session context — or a role's page (scope, egress, allocation, standing sessions), or you followed an old /ui/roles link.
owns: a project's tabs and their pages (/ui/projects/<name>[/members|agents|roles|intercom|escalation]), creating a role from a project, the role page (/ui/projects/<project>/roles/<name>), and the /ui/roles and /ui/projects redirects
prereqs: ui.md for reaching the UI, the rail and tabs; ui-attention.md for the badges and Needs attention; ui-editing.md for the write banner and the gate/CSRF/audit rules; projects.md for the project lifecycle; roster.md for roles and grants
tier: leaf
updated: 2026-10-07
---

# Admin UI project pages

Select a project in the [rail](ui.md) and its tabs — **Overview · Members ·
Agents · Roles · Intercom · Escalation** — sit over its pages. Each tab is its
own URL, so it can be linked and bookmarked; a role page sits under Roles with
a `Roles / dev` breadcrumb. Everything scoped to one project lives here, and its
tabs carry the project's [attention badges](ui-attention.md).

| Tab | URL |
|---|---|
| Overview | `/ui/projects/<name>` |
| Members | `/ui/projects/<name>/members` |
| Agents | `/ui/projects/<name>/agents` |
| Roles | `/ui/projects/<name>/roles`, each role `…/roles/<role>` |
| Intercom | `/ui/projects/<name>/intercom` |
| Escalation | `/ui/projects/<name>/escalation` |

**Old links.** `/ui/projects` and `/ui/roles` redirect (301) to `/ui/` (the
rail is the project list), and `/ui/roles/<project>/<role>` to the role's page
here. The write endpoints keep their paths. A new project is created from the
rail's **+ New project**, which opens it.

## Sections

Each tab's edits are in place: a write answers with that tab's content
re-rendered.

- **Overview** — the project's **Needs attention** card; counts (members,
  agents live of all, roles, rooms, escalation chains), each linking to its tab; the **chat service** (none — tracker
  @-mentions only — or `discord`); the project's **Session context** card
  ([ui-pages.md](ui-pages.md#session-context-cards)). **Rename** (except
  `default`) renames the project and opens its new URL; **Delete** is disabled
  while a member, a role, a session not yet gone or an actor's grant still
  references the project, and names what does — the same rule as `project rm`
  ([projects.md](projects.md)).
- **Members** — the users agents here can address (linked to their user page,
  with handle and delivery). **Add member** (a user, and delivery: one
  `service:address` per line); per member, **Edit** delivery and **Remove**.
- **Agents** — the project's studios (the shared studio table) and the actors
  holding a grant into it, with the roles they hold.
- **Roles** — the project's roles (linked, with destinations, TTL and kit) and,
  with a runtime supervisor, a **Request** per role
  ([ui-editing.md](ui-editing.md#runtime-studios)). **New role** creates one in this project
  (the project comes from the page) and opens its page.
- **Intercom** — the project's rooms (service, ref): **Add room** (name;
  connection: a connection name, or a service for its connection; ref; an
  existing name is rebound) and **Remove**. Below them, the project's newest 50
  messages from the channel log, with **Full log** opening the
  [Intercom](ui.md#intercom) page filtered to the project.
- **Escalation** — the default chain and each category's chain, as ordered
  tiers of targets with their wait ([escalation.md](escalation.md)). Edit a
  chain as one `targets@timeout` per line (the CLI's `--tier`; the first line is
  tier 0, the timeout a positive duration), add a category chain, or **Clear**
  one. A target that names nobody on this project's roster is flagged red —
  flagged, not blocked.

## Role pages

Each role name (in a project's Roles, a grant chip, or a studio row)
links to its page, `/ui/projects/<project>/roles/<name>`, which shows and edits the whole
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
  studios pick up the change on the supervisor's next reconcile.
- **Allocation** — session caps and the personal-session idle ladder.
  Durations take `30m`/`1h30m` (or bare seconds); a blank field is unset, and
  the page says what applies when unset.
- **Standing sessions** — declare, [upgrade](standing-sessions.md#upgrading-a-standing-session), [reset](standing-sessions.md#reset) and dismiss;
  each shows its studio's phase, flagged **image stale** per [coves.md](coves.md#the-studio-verbs) (its Upgrade button highlighted) and any pending upgrade; a queued upgrade or pending reset flashes as accepted.
- **Holders** and **Studios** — the agents granted the role (linked; marked where the
  grant overrides the scope; grants are managed on the agent's page) and the role's
  running studios.

**Request session** and **Delete** (which returns to the project's Roles) are on the page header. These writes share
one lock with the JSON admin API's role, egress and standing routes, so an edit
here and a CLI change can't overwrite each other.
