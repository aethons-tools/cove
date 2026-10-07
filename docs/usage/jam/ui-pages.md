---
summary: The Jam admin UI's per-entity pages outside a project — a user's page, a studio's page (/ui/coves/<id>), a destination's page (/ui/destinations/<name>), a model-spec's page (/ui/model-specs/<name>) and a kit's page (/ui/kits/<name>) — what each shows and how editing them works.
read_when: You are viewing or editing a user, studio, destination, model-spec or kit in the Jam admin UI — a user's logins, OIDC identities or accounts; a studio's runtime, waiting/escalation state, session streams or squawks; a destination's client env/connector; a kit's versions, diffs or pinning; or who uses any of them — or wondering why the list pages only create.
owns: the user, studio, destination, model-spec and kit detail pages (what they show, their edit forms, the users list, create-only list forms, connector-conflict flags, kit version rail/diff/push)
prereqs: ui.md for reaching the UI and the top nav; ui-editing.md for the write banner and the gate/CSRF/audit rules; projects.md for the project lifecycle; roster.md for roles; connector.md for destination env/git; kits.md for the StudioKit schema and versioning
tier: leaf
updated: 2026-10-07
---

# Admin UI entity pages

Entity names across the [admin UI](ui.md) link to a page that shows the whole
object and edits it in place. The list pages' forms only **create** (an
existing name is refused with "edit it on its page") and then open the new
object's page, where every field is pre-filled — so an edit can't silently drop
a field the form didn't show.

## Project and role pages

A project's pages (its tree, sections and role pages) are in
[ui-projects.md](ui-projects.md).

## User pages

`/ui/users` lists and creates users; `/ui/users/<id>` renames one, replaces its
logins and OIDC identities, adds or unlinks accounts, and removes it (rename and
remove are refused while the user owns a live personal session).

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
- **Squawks** — the newest 50 squawks in its conversations (and, from before
  the channel log, to or from `actor:<id>`), rendered as on the Intercom page,
  with a link to the Intercom page filtered to its own posts.

A torn-down studio leaves the registry, but its session and squawks remain, so
its page still renders them under a "not running" banner. An id with no record,
session or squawks is a 404.

## Session context cards

The [role page, the project Overview](ui-projects.md) and the dashboard each carry a **Session context**
card for that layer (role, project, Jam-wide): the core, its size as a session
receives it against the budget (a project's includes the resources pointer), the
leaves and, for projects, the resources. **Edit session context** is one YAML box in
the [`at-jam context` format](session-context-authoring.md) with bodies inline;
saving runs the same checks as the API, and **Clear** removes the layer.

## Destination pages

Each destination name in the Destinations table links to
`/ui/destinations/<name>`. The table itself shows route, upstream, auth
(`identity-in → apply`), the default credential, the studio-connector summary
(env keys, `git`) and how many roles list it.

The page shows:

- **Broker** — route, upstream, identity-in, apply (any custom [header spec](header-specs.md)
  read-only) and default credential.
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
identity-in/apply select a preset (incl. `raw`; a `custom` one stays custom,
spec kept), git routing is a checkbox, and the note is the usage hint
sessions see ([connector.md](connector.md#notes-for-sessions)). Validation matches the admin API
(required fields, a configured default credential, env keys and placeholders).
Changing env on the `/git/` route drops its implied git routing unless **Route
git** is ticked; the form says so. **Delete** is on the page header.

## Model-spec pages

Each model-spec name in the Model-specs table (`/ui/model-specs`) links to
`/ui/model-specs/<name>`. The table shows type, version (with any constraint as a chip), principal,
model, policy mode and provider. The page shows the harness (type, version, constraint, principal
credential *name* and header rules, model, effort, note), the policy (mode, allow/deny rules) and the
claude body (provider, provider-env keys, plugins, settings keys).

**New model-spec** and **Edit model-spec** share one form: type and claude
provider are selects; principal is a select of the configured credential
names, plus `pool` when a [pool](pool.md) is configured (a stored principal no
longer configured stays selected); policy mode is a select (empty = harness
default); version is the exact `X.Y.Z` the image installs, version-constraint the optional
runtime check; allow, deny, plugins and [principal header rules](model-spec-headers.md#in-the-admin-ui)
take one entry per line; provider-env one `KEY=VALUE` per line; settings a JSON object. Writes go through the same
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
