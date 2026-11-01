---
summary: The admin console's project pages under Config › Projects — the project list, a project's overview, its Members (and a member's page), Agents (roles and agents), Intercom and Escalation pages at /app/admin/config/projects/<name>[/…], what each shows and edits, and the role page at …/agents/<role>.
read_when: You are viewing or editing one project in the Jam admin console — its members, agents, roles, rooms and recent messages, escalation chains, chat service or agent context — or a role's page (scope, egress, allocation, standing agents), or you open Config › Projects.
owns: a project's pages (/app/admin/config/projects/<name>[/members[/<user>]|agents|intercom|escalation]), creating a project and a role, the role page (/app/admin/config/projects/<project>/agents/<role>), and the project list (/app/admin/config/projects)
prereqs: ui-console.md for the Config tree and URLs; ui-attention.md for the badges; ui-editing.md for the write rules; projects.md for the project lifecycle; roster.md for roles and grants
tier: leaf
updated: 2026-11-01
---

# Admin console project pages

Everything scoped to one project lives under **Config › Projects** in the
[Config tree](ui-console.md#finding-a-page): the project's node opens its
overview, and under it sit **Members** (each member), **Agents** (each role),
**Intercom** and **Escalation**, each its own page and URL under
`/app/admin/config/projects/<name>`. Roles sit under Agents because they are
what its agents are granted. The project list
(`/app/admin/config/projects`) shows every project (members, roles, rooms)
with **New project**, which creates one and opens it. The tree's nodes carry
the project's [attention badges](ui-attention.md).

## Sections

Each page's edits are in place: a write answers with the page's content
refreshed. Config edits (project, members, rooms, escalation, chat service,
roles, context) are offered only on a branch of the config repo, where each
save is a commit ([config-branches.md](config-branches.md)); a standing
reset or upgrade, and Request, stay live.

- **Overview** (`…/<name>`) — counts (members, agents live of all, roles,
  rooms, escalation chains), each linking to its page; the **chat service** (none — tracker
  @-mentions only — or `discord`); the project's **Agent context** card
  ([ui-pages.md](ui-pages.md#agent-context-cards)). **Rename** (except
  `default`) renames the project and opens its new URL; **Delete** is disabled
  while a member, a role, a session not yet gone or an actor's grant still
  references the project, and names what does — the same rule as a project's
  delete in the config repo ([projects.md](projects.md)).
- **Members** (`…/members`) — the users agents here can address (with handle
  and delivery). **Add member** (a user, and delivery: one `service:address`
  per line); per member, **Edit** delivery and **Remove**. A member's page
  (`…/members/<user>`) shows their delivery here with **Remove from
  <project>**, above the user's own page ([ui-pages.md](ui-pages.md#user-pages)).
- **Agents** (`…/agents`) — first the project's **Roles** (linked, with destinations, TTL and
  kit; with a runtime supervisor, a **Request** per role —
  [ui-editing.md](ui-editing.md#runtime-studios)); **New role** creates one in
  this project and opens its page. Then one table of the project's agents: each
  running in it or holding a grant into it (one row per agent), with its kind,
  roles here, [status](ui-pages.md#agent-status), image and last seen; each opens its agent page.
- **Intercom** (`…/intercom`) — the project's rooms (service, ref): **Add room** (name;
  connection: a connection name, or a service for its connection; ref; an
  existing name is rebound) and **Remove**. Below them, the project's newest 50
  messages from the channel log, with **Full log** opening the
  [Channel log](ui-console.md#channel-log) filtered to the project.
- **Escalation** (`…/escalation`) — the default chain and each category's chain, as ordered
  tiers of targets with their wait ([escalation.md](escalation.md)). Edit a
  chain as one `targets@timeout` per line (the CLI's `--tier`; the first line is
  tier 0, the timeout a positive duration), add a category chain, or **Clear**
  one. A target that names nobody on this project's roster is flagged red —
  flagged, not blocked.

## Role pages

Each role name (in the tree, on a project's Agents page, a grant chip, or a
running-agent row) links to its page,
`/app/admin/config/projects/<project>/agents/<name>`, which shows and edits the whole
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

**Request agent** and **Delete** (which returns to the project's Agents page) are on the page header.

The role's **Agent context** card is in
[ui-pages.md](ui-pages.md#agent-context-cards). The page's **Setup** section, read
only when opened, shows what a studio of the role raised now would get — see
[inspect.md](inspect.md#in-the-admin-ui).
