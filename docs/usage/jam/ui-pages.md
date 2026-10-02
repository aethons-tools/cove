---
summary: The Jam admin UI's per-entity pages — a role's page (/ui/roles/<project>/<name>) and a destination's page (/ui/destinations/<name>) — what each shows and how editing them works.
read_when: You are viewing or editing a role or destination in the Jam admin UI — its scope, egress, allocation, standing sessions, client env/connector, or who uses it — or wondering why the list pages only create.
owns: the role and destination detail pages (what they show, their edit forms, create-only list forms, connector-conflict flags)
prereqs: ui.md for reaching the UI, the write banner, and the gate/CSRF/audit rules; roster.md for roles; connector.md for destination env/git
tier: leaf
updated: 2026-10-02
---

# Admin UI entity pages

Entity names across the [admin UI](ui.md) link to a page that shows the whole
object and edits it in place. The list pages' forms only **create** (an
existing name is refused with "edit it on its page") and then open the new
object's page, where every field is pre-filled — so an edit can't silently drop
a field the form didn't show.

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
and git routing and oauth-beta are checkboxes. Validation matches the admin API
(required fields, a configured default credential, env keys and placeholders).
Changing env on the `/git/` route drops its implied git routing unless **Route
git** is ticked; the form says so. **Delete** is on the page header.
