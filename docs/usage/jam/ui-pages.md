---
summary: The Jam admin UI's per-entity pages outside a project — a user's page, an agent's page (/app/admin/agents/<id>: identity, grants, studio, access, session, squawks), a destination's page (/app/admin/destinations/<name>), a model-spec's page (/app/admin/model-specs/<name>) and a kit's page (/app/admin/kits/<name>) — what each shows and how editing them works.
read_when: You are viewing or editing a user, agent, destination, model-spec or kit in the Jam admin UI — a user's logins, OIDC identities or accounts; an agent's grants, studio runtime, waiting/escalation state, who can reach its studio (the Access card, Unshare), session streams or squawks; a destination's client env/connector; a kit's versions, diffs or pinning; or who uses any of them — or wondering why the list pages only create.
owns: the user, agent, destination, model-spec and kit detail pages (what they show, their edit forms, the users list, create-only list forms, connector-conflict flags, kit version rail/diff/push)
prereqs: ui.md for reaching the UI, the rail and tabs; ui-editing.md for the write banner and the gate/CSRF/audit rules; projects.md for the project lifecycle; roster.md for roles; connector.md for destination env/git; kits.md for the StudioKit schema and versioning
tier: leaf
updated: 2026-10-25
---

# Admin UI entity pages

Entity names across the [admin UI](ui.md) link to a page that shows the whole
object and edits it in place. The list pages' forms only **create** (an
existing name is refused with "edit it on its page") and then open the new
object's page, where every field is pre-filled — so an edit can't silently drop
a field the form didn't show.

## Project and role pages

A project's pages (its tabs and role pages) are in
[ui-projects.md](ui-projects.md).

## User pages

`/app/admin/users` lists and creates users; `/app/admin/users/<id>` renames one, replaces its
logins and OIDC identities, adds or unlinks accounts, and removes it (rename and
remove are refused while the user owns a live personal session).

## Agent pages

Each agent id (in the Agents list, a running-agents table, a role's holders, a
project's identities, or search) opens `/app/admin/agents/<id>`, the page for one
agent identity. It sits under Jam's Agents tab, or — linked from a project's
pages (`?project=<name>`) — under that project's. The header shows its kind, its [status](#agent-status), standing name or
personal owner, project, role and unit (linked), with **Open live timeline**
(the [session timeline](ui-console.md#session-timeline); shown when session capture is
configured), **Teardown** (returns to the Agents list) when it has a
studio and a runtime supervisor runs, and **Revoke** when it is enrolled.
While it has a studio, its **Setup** tab shows what the agent is set up with — see [inspect.md](inspect.md#in-the-admin-ui).

- **Identity** — its grants as `project/role` chips (× removes one; hover for
  the effective destinations), **+ Grant**, and its token expiry; or "not
  enrolled" for a studio whose identity is gone.
- **Runtime** — raised and last seen, lease holder, backend and location.
- **Waiting & escalation** — whether it is waiting and since when (wake-on
  resumes it on a reply past its wait seq), the open escalation (which tier was
  pinged, when, and on which chain), a personal session's owner nags, and how far
  its inbox is committed.
- **Egress** — the fingerprint of the role egress policy it runs under, and
  consecutive re-apply failures, flagged (the supervisor tears the studio down
  at its limit — see [roster.md](roster.md)).
- **Access** — who can reach the running studio and why: operators always;
  a personal session's requester; project members as the role's collaboration
  policy says (`preview`/`ssh`: `members` or `none`); and each share grant
  (user, rights, expiry, granted by) with an **Unshare** button — a
  CSRF-guarded write that removes the grant and closes that user's open SSH
  connections unless they still hold `ssh` another way. Below, the open
  connections (who, right — `ssh` or `preview` — since when) and the
  **Previews** list: each exposed [preview](previews.md) as a link opening in a new tab
  (or "Previews are off on this Jam" without a [`previews:`](serve.md)
  domain). With neither SSH collaboration nor previews configured the card
  says so instead. Sharing itself is
  `jam studio share` ([collaboration.md](collaboration.md#sharing)).
- **Session** — its captured event streams (start, last event, count), each
  opening the timeline on that stream ([session-events.md](session-events.md)).
- **Squawks** — the newest 50 squawks in its conversations (and, from before
  the channel log, to or from `actor:<id>`), rendered as on the Channel log,
  with a link to the Channel log filtered to its own posts.

A torn-down studio leaves the registry, but its session and squawks remain, so
its page still renders them under a "not running" banner. An id with no record,
session or squawks is a 404.

## Agent status

Everywhere the UI lists or shows an agent it gives one **status**, folding its
studio's phase (Jam's) and the agent's reported activity into one axis:

| Status | When |
|---|---|
| pending | no studio (enrolled only, or never raised) |
| setting up · orienting | its studio is being raised · is up, but the agent hasn't reported yet |
| running · holding | in a turn · turn over, background tasks still running |
| waiting · blocked | turn over, waiting for its next message (it may or may not have asked for a person) · says it can't proceed |
| idled · done | paused · reported done (teardown next) |
| terminating · lost · gone | being torn down · declared dead · torn down |

The Agents list's filters and the dashboard's tiles group them: **Running**
(running, holding), **Waiting** (waiting, blocked), **Setting up** (setting up,
orienting), **Idled**, **Lost / terminating**. The API and CLI keep phase and
activity as separate fields.

## Agent context cards

The [role page, the project Overview](ui-projects.md) and the dashboard each carry an **Agent context**
card for that layer (role, project, Jam-wide; the concept is [session
context](session-context.md)): the core, its size as an agent receives it
against the budget (a project's includes the resources pointer), the leaves and,
for projects, the resources. **Edit agent context** is one YAML box in
the [`jam context` format](session-context-authoring.md) with bodies inline;
saving runs the same checks as the API, and **Clear** removes the layer.

## Destination pages

Each destination name in the Destinations table links to
`/app/admin/destinations/<name>`. The table itself shows route, upstream, auth
(`identity-in → apply`), the default credential, the studio-connector summary
(env keys, `git`) and how many roles list it.

The page shows:

- **Broker** — route, upstream, identity-in, apply (any custom [header spec](header-specs.md)
  read-only) and default credential.
- **Agent connector** — the client env an agent's studio sets (`{url}` already resolved
  to `{base}<route>`) and git routing. A destination with no declared env shows
  its route's legacy default, labeled as implied (see
  [connector.md](connector.md)).
- **Used by** — every role whose scope lists it, with the credential each
  injects there (the role's mapping or this default), linked to the role page.
- **Connector conflicts** — flagged, never blocked: if a role using this
  destination also lists one that sets an env variable differently, or that
  also routes git, the page names the role and the other destination. Agents
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

Each model-spec name in the Model-specs table (`/app/admin/model-specs`) links to
`/app/admin/model-specs/<name>`. The table shows type, version (with any constraint as a chip), principal,
model, policy mode, provider and how many roles use it. The page shows the harness (type, version, constraint, principal
credential *name* and header rules, model, effort, note), the policy (mode, allow/deny rules), the
roles that run it (**Used by** — for the default spec, also the roles bound to none) and the
claude body (provider, provider-env keys, plugins, settings keys).

**New model-spec** and **Edit model-spec** share one form: type and claude
provider are selects; principal is a select of the configured credential
names, plus `pool` when a [pool](pool.md) is configured (a stored principal no
longer configured stays selected); policy mode is a select (empty = harness
default); version is the exact `X.Y.Z` the image installs, version-constraint the optional
runtime check; allow, deny, plugins and [principal header rules](model-spec-headers.md#in-the-admin-ui)
take one entry per line; provider-env one `KEY=VALUE` per line; settings a JSON object. Writes go through the same
validation as `jam model-spec` ([model-specs.md](model-specs.md#validation));
a refusal shows in the page banner and stores nothing. **Delete** is on the
page header and each table row.

## Kit pages

Each kit name in the Kits table links to `/app/admin/kits/<name>`. The table shows the
current version (of how many), the current version's base kind and egress count,
and how many roles raise it (for `default`, that includes roles with no kit
set). A kit built on a parent shows `kit: <parent>[@vN]` as its base. A kit
whose current version won't parse is marked **invalid**. The **New kit** form
takes a name and studio-kit YAML and validates it exactly as `kit push` does
([kits.md](kits.md)); an existing name is refused — push new versions on the
kit's page. **Import from git** takes `<repo>[//<dir>]@<ref>` and an optional
name and imports the kit as a new version, then opens its page. Its **Track this
source** box (on by default) also tracks the source, like `kit track`; uncheck
it for a one-off `kit import`.

The page has a **version rail** — every version with its short build digest
(versions sharing one reuse one image), **Pin** to make an older or newer
version current, and links to diff a version against its predecessor or
against current. `?v=N` views a version; `?v=N&diff=M` adds a line diff of the
two versions' YAML (long unchanged runs collapsed) and says whether the build
digest changed — i.e. whether that version builds a new image.

For the viewed version it shows the base (image ref, `Dockerfile`, or the
blessed default; `kit: <parent>` for a chained kit), the kit's files and its
`kit.yml` ([kits.md](kits.md#the-kit-verbs) has the table, the diff and
`&file=`), egress with the effective ceiling and anything excluded (COV-208),
build args, secret demands (name + description; never values), the prompt, the
build digest, and its `kit.yml` as stored, with **Copy**. A stored version that isn't a valid
studio kit (a pre-studio row) shows its parse error and raw text instead. A
packed context left in its `base` is masked as `«packed context, not shown»`,
with a note above the push form to remove it before pushing; a text the page
cannot analyse (not one plain YAML document: anchors, aliases and merge keys
count against it) that mentions a context is not shown at all, and the page says to get it with `jam config write`. A broken version,
one the migration to trees could not convert, shows its migration error.

A kit built on a parent kit ([kits-inheritance.md](kits-inheritance.md)) also
shows **Built on** — each parent, root first, linked to its page with the
version it resolves to and whether the kit follows the parent's current version
or pins one — and an **Effective** card: the chain merged (egress union and
ceiling, MCP servers, build args, secret demands, notes, joined prompt), i.e.
what a role binding the kit gets. An unchained kit has neither. A chain that
doesn't resolve (a missing or invalid parent, a cycle) shows the reason in a
banner instead. Built on and Effective describe the viewed version; Used by and
Delete (below) reflect each kit's current version.

The **Source** card shows the tracked git source — repo (linked when `https`),
dir, ref and the last synced commit (12 chars, linked on github.com and
gitlab hosts) — with **Sync** and **Untrack** (versions are kept), or a
**Track…** form when the kit has none. It also notes a
[kit set](kits-path-parents.md#where-a-set-shows). The **Versions** table lists
each version's push time, source and commit. The import, track, sync and
untrack controls need the git fetcher (always present in `jam serve`),
disable their button while the fetch runs, and follow the admin API's rules and
errors ([kits-git.md](kits-git.md)); without one the page only shows
provenance.

**Push new version** is pre-filled with the viewed version's full YAML; pushing
makes a new current version, or reports the kit unchanged when it equals
current. **Used by** links the roles that raise the kit and the kits whose
current version builds on it (follows or pinned). **Delete** is disabled while
any role or kit uses the kit, and always for the built-in `default` kit (Jam
re-seeds it at serve start).

The kit's **Draft** tab edits and publishes a draft: see [kits-drafts-ui.md](kits-drafts-ui.md).
