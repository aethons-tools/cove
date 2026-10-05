---
summary: The Jam admin UI's per-entity pages — a project's page (/ui/projects/<name>), a studio's page (/ui/coves/<id>), a role's page (/ui/roles/<project>/<name>), a destination's page (/ui/destinations/<name>), a model-spec's page (/ui/model-specs/<name>) and a kit's page (/ui/kits/<name>) — what each shows and how editing them works.
read_when: You are viewing or editing a project, studio, role, destination, model-spec or kit in the Jam admin UI — a project's roster, escalation or chat service; a studio's runtime, waiting/escalation state, session streams or squawks; a role's scope, egress, allocation or standing sessions; a destination's client env/connector; a kit's versions, diffs or pinning; or who uses any of them — or wondering why the list pages only create.
owns: the project, studio, role, destination, model-spec and kit detail pages (what they show, their edit forms incl. project roster/escalation/chat-service editing, create-only list forms, connector-conflict flags, kit version rail/diff/push)
prereqs: ui.md for reaching the UI, the write banner, and the gate/CSRF/audit rules; projects.md for the project lifecycle; roster.md for roles; connector.md for destination env/git; kits.md for the StudioKit schema and versioning
tier: leaf
updated: 2026-10-05
---

# Admin UI entity pages

Entity names across the [admin UI](ui.md) link to a page that shows the whole
object and edits it in place. The list pages' forms only **create** (an
existing name is refused with "edit it on its page") and then open the new
object's page, where every field is pre-filled — so an edit can't silently drop
a field the form didn't show.

## Project pages

Each project name (in the Projects table, a role's breadcrumb, or a table cell)
links to `/ui/projects/<name>`, the "everything in this project" view: its
roles (linked, with destinations and kit), the actors holding a grant into it
and which roles they hold, its roster — humans (handle, linked login, delivery
per service with address and user id, OIDC identities) and channels (service,
ref) — its escalation policy (the default chain and each category's chain, as
ordered tiers of targets with their wait), its chat service, and its running
studios.

The roster, escalation and chat service are edited in place; each write answers
with the re-rendered page:

- **Humans** — **Add human** (name, handle, login, delivery and OIDC identity)
  and, per human, **Edit** (pre-filled; every field comes from the form, so a
  removed line is removed) and **Remove**. Delivery is one
  `service:address[:user-id]` per line (a user id is discord-only) and identity
  one `issuer:subject` per line — the CLI's `--delivery`/`--oidc` syntax. The
  rules match `project roster add-human`: a login, and a Discord user id, link at
  most one human per project.
- **Channels** — **Add channel** (name, service, ref; an existing name is
  replaced) and **Remove**.
- **Escalation** — edit the default chain or any category's chain as one
  `targets@timeout` per line (the CLI's `--tier`; the first line is tier 0, the
  timeout a positive duration), add a category chain, or **Clear** one. A target
  that names nobody on this project's roster (e.g. after removing that human) is
  flagged red — flagged, not blocked.
- **Chat service** — none (tracker @-mentions only) or `discord`.

**Delete** (here and in the table) is disabled while a role or an actor's grant
still references the project, and names what does — the same rule as
`project rm` ([projects.md](projects.md)).

## Studio pages

Each studio id in the Studios table opens `/ui/coves/<id>`, the hub for one
studio. The header shows its phase, activity, kind (ephemeral, personal with
its owner, or standing with its name), project, role and unit (linked), with
**Open live timeline** (the [session timeline](ui.md#session-timeline)) and,
when a runtime supervisor is configured, **Teardown**.

- **Runtime** — raised and last seen, lease holder, backend and location.
- **Waiting & escalation** — whether it is waiting and since when (wake-on
  resumes it on a reply past its wait seq), the open escalation (which tier was
  pinged, when, and on which chain), a personal session's owner nags, and how far
  its inbox is committed.
- **Egress** — the fingerprint of the role egress policy it runs under, and
  consecutive re-apply failures, flagged (the supervisor tears the studio down
  at its limit — see [roster.md](roster.md)).
- **Session** — its captured event streams (start, last event, count), each
  opening the timeline on that stream ([session-events.md](session-events.md)).
- **Squawks** — the newest 50 squawks to or from `actor:<id>`, rendered as on
  the Intercom page, with a link to the Intercom page pre-filtered to it for
  the rest.

A torn-down studio leaves the registry, but its session and squawks remain, so
its page still renders them under a "not running" banner. An id with no record,
session or squawks is a 404.

## Session context cards

The role page, the project page and the dashboard each carry a **Session context**
card for that layer (role, project, Jam-wide): the core, its size as a session
receives it against the budget (a project's includes the resources pointer), the
leaves and, for projects, the resources. **Edit session context** is one YAML box in
the [`at-jam context` format](session-context-authoring.md) with bodies inline;
saving runs the same checks as the API, and **Clear** removes the layer.

## Role pages

Each role name (in the Roles table, a roster grant chip, or a studio row) links
to its page, `/ui/roles/<project>/<name>`, which shows and edits the whole
role, one section at a time — each with a pre-filled **Edit** form that saves
only that section:

- **Scope** — destinations with the credential the broker injects for each
  (the role's mapping, or the destination's default), addressing, TTL and kit.
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
- **Standing sessions** — declare (name + prompt) and dismiss; each shows its
  studio's phase. Dismissing tears the studio down
  ([standing-sessions.md](standing-sessions.md)).
- **Holders** and **Studios** — the actors granted the role (marked where the
  grant overrides the scope; grants are managed on the Roster) and the role's
  running studios.

**Request session** and **Delete** are on the page header. These writes share
one lock with the JSON admin API's role, egress and standing routes, so an edit
here and a CLI change can't overwrite each other.

## Destination pages

Each destination name in the Destinations table links to
`/ui/destinations/<name>`. The table itself shows route, upstream, auth
(`identity-in → apply`), the default credential, the studio-connector summary
(env keys, `git`, `oauth-beta`) and how many roles list it.

The page shows:

- **Broker** — route, upstream, identity-in, apply, default credential, and
  whether oauth-beta is on.
- **Studio connector** — the client env a studio sets (`{url}` already resolved
  to `{base}<route>`) and git routing. A destination with no declared env shows
  its route's legacy default, labeled as implied (see
  [connector.md](connector.md)).
- **Used by** — every role whose scope lists it, with the credential each
  injects there (the role's mapping or this default), linked to the role page.
- **Connector conflicts** — flagged, never blocked: if a role using this
  destination also lists one that sets an env variable differently, or that
  also routes git, the page names the role and the other destination. Studios
  holding that role can't assemble a connector (Jam fails closed with 409)
  until one side changes. The check is over each role's own scope; a grant
  override can still differ.

**Edit destination** is one pre-filled form for every field but the name:
client env is one `KEY=TEMPLATE` per line (empty = the route's legacy default),
git routing and oauth-beta are checkboxes, and the note is the usage hint
sessions see ([connector.md](connector.md#notes-for-sessions)). Validation matches the admin API
(required fields, a configured default credential, env keys and placeholders).
Changing env on the `/git/` route drops its implied git routing unless **Route
git** is ticked; the form says so. **Delete** is on the page header.

## Model-spec pages

Each model-spec name in the Model-specs table (`/ui/model-specs`) links to
`/ui/model-specs/<name>`. The table shows type, version, principal, model,
policy mode and provider. The page shows the harness (type, version, principal
credential *name*, model, effort, note), the policy (mode, allow/deny rules)
and the claude body (provider; provider-env keys, plugins and settings keys).

**New model-spec** and **Edit model-spec** share one form: type and claude
provider are selects; principal is a select of the configured credential
names, plus `pool` when a [pool](pool.md) is configured (a stored principal no
longer configured stays selected); policy mode is a select (empty = harness
default); allow, deny and plugins take one entry per line; provider-env one
`KEY=VALUE` per line; settings a JSON object. Writes go through the same
validation as `at-jam model-spec` ([model-specs.md](model-specs.md#validation));
a refusal shows in the page banner and stores nothing. **Delete** is on the
page header and each table row.

## Kit pages

Each kit name in the Kits table links to `/ui/kits/<name>`. The table shows the
current version (of how many), the current version's base kind and egress
count, and how many roles raise it (for `default`, that includes roles with no
kit set). A kit whose current version won't parse is marked **invalid**. The
**New kit** form takes a name and studio-kit YAML and validates it exactly as
`kit push` does ([kits.md](kits.md)); an existing name is refused — push new
versions on the kit's page.

The page has a **version rail** — every version with its short build digest
(versions sharing one reuse one image), **Pin** to make an older or newer
version current, and links to diff a version against its predecessor or
against current. `?v=N` views a version; `?v=N&diff=M` adds a line diff of the
two versions' YAML (long unchanged runs collapsed) and says whether the build
digest changed — i.e. whether that version builds a new image.

For the viewed version it shows the base (image ref, context-files paths, or a
packed `context`'s file list with modes and sizes — read from the tar headers,
nothing extracted), egress with the effective ceiling and anything excluded
(COV-208), build args, secret demands (name + description; never values), the
prompt, the build digest, and the YAML rendered back from the stored kit (a
packed context abbreviated) with **Copy**. A stored version that isn't a valid
studio kit (a pre-studio row) shows its parse error and raw text instead.

**Push new version** is pre-filled with the viewed version's full YAML; pushing
makes a new current version, or reports the kit unchanged when it equals
current. **Used by** links the roles that raise the kit. **Delete** is disabled
while any role uses the kit, and always for the built-in `default` kit (Jam
re-seeds it at serve start).
