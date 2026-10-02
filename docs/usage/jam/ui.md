---
summary: The Jam admin UI — a server-rendered web view of the live studios, the durable squawk Log, and the control-plane roster/roles/kits/destinations, served by `at-jam serve`; reachable on loopback always, and off-loopback via browser OIDC login. Beyond viewing, it can do the roster day-job (enroll/revoke actors, roles, grants), edit the kit registry and destinations, and, with a runtime supervisor configured, raise/tear down managed studios and request a personal session of a role.
read_when: You want to watch a running Jam in a browser — the live studio fleet, the squawk Log, and the roster/roles/kits/destinations — or do the roster day-job, edit kits/destinations, or raise/tear down a managed studio from the browser, without running admin CLI verbs, or you are configuring browser login for it.
owns: the `/ui/` observability + roster/kit/destination-editing + runtime studio raise/teardown surface (what it shows, what it can mutate, how to reach it, its loopback + browser-OIDC-login exposure); and the participant `/me/` surface (its OIDC-always/no-loopback gate, reuse of the operator browser client, the operator/participant boundary, and the `POST /me/send` participant send path)
prereqs: serve.md for the admin listener + the off-loopback fail-closed rule; roster.md for the RBAC model these edits act on; coves.md for the managed-cove lifecycle the runtime actions drive; comms-addressing.md for the squawk targets/wake-on model the send path writes into; INDEX.md for the service overview
tier: leaf
updated: 2026-10-02
---

# The Jam admin UI (`/ui/`)

`at-jam serve` serves a web UI on the same **admin listener** as the JSON
admin API. Point a browser at the admin URL and open `/ui/` (`/` redirects
there):

```
http://127.0.0.1:8081/ui/
```

It renders:

- **Dashboard** (`/ui/`) — summary tiles (live / raising / lost-or-terminating /
  idled studios, and counts of actors, roles, kits, destinations), each linking
  to its page, above the studio table.
- **Studios** (`/ui/coves`) — every managed studio's id, project/role, unit, phase,
  activity, lease holder, raised-at, last-seen. The table **auto-refreshes every
  3 seconds** (htmx polling); no page reload. View-only unless a runtime
  supervisor is configured, in which case it can also raise and tear down
  studios — see [Runtime (studios)](#runtime-studios) below and
  [coves.md](coves.md).
- **Intercom** (`/ui/intercom`) — a read-only, filterable, newest-first table of
  the durable squawk Log (`intercom-log:` in the serve config). Filter by
  project, participant (`kind:ref`, e.g. `channel:eng`), a body substring, and a
  date window; filters live in the URL, so a filtered view is shareable. Manual
  refresh (not a live tail); each recipient carries an internal/external reach
  badge. Empty until the log has writers, and absent-config renders a
  "not configured" notice. See [Intercom](#intercom) below.
- **Roster / Roles / Kits / Destinations** — the control-plane objects as
  tables, all editable from here — see [Editing](#editing-day-job-mutations)
  below.

Every table has a fixed order — studios and actors by id; roles by project,
then name; kits and destinations by name; squawks newest-first — so rows don't
shuffle across the Studios poll or after an edit. The order comes from the
store, so the JSON admin API and CLI lists match it.

## Reaching the UI

The UI has its own gate, separate from the JSON admin API's authenticator (see
the fail-closed rule in [serve.md](serve.md#exposing-the-admin-api-fail-closed)):

- **Loopback** (local host, or an SSH tunnel to the admin port) — reachable with
  no login: the local operator is trusted. This holds whether or not
  `operator-auth.oidc` is configured. The request's `Host` must be a loopback
  literal (`127.0.0.1`/`::1`/`localhost`) or a host listed in `ui-hosts` — if you
  reach the UI over a custom name that DNS-binds to loopback (e.g.
  `jam.local.example`), add it to `ui-hosts` (see [serve.md](serve.md)) or the
  UI refuses it as a possible DNS-rebinding attempt.
  A loopback viewer is the anonymous operator `local`, unless they have signed
  in via `/ui/auth/login`: a valid session is used even on loopback, so the UI
  knows *who* you are (the [role Request](#runtime-studios) action needs this).
  A missing or expired session falls back to `local` without a login redirect.
  For UI development, [`dev-identity`](serve.md) makes loopback requests act as
  a chosen roster human on `/ui` and `/me` with no login at all.
- **Off-loopback, with a `browser-client-id`** set in `operator-auth.oidc` — the
  browser is redirected through an OIDC **Authorization Code + PKCE** login
  (`/ui/auth/login` → your IdP → `/ui/auth/callback`); on success a session cookie
  (the API access token; HttpOnly + Secure + SameSite=Lax) lets you browse until
  it expires, then you re-login. `require-scope` is enforced on every request,
  exactly as for the admin API. See [operators.md](operators.md).
- **Off-loopback, without a `browser-client-id`** — the UI is refused. The
  programmatic admin API is still reachable with a bearer token.

Register `https://<your-jam-host>/ui/auth/callback` in your IdP's Allowed
Callback URLs. Browser login needs TLS (the session cookie is `Secure`).

The UI never renders a token hash, launch secret, or credential value; the one
exception is the identity token shown once at enroll time (below) — and the
Intercom view, which shows comms bodies (agent/human squawks), not secrets. The
login routes themselves never expose mutation.

## The participant intercom (`/me/`)

`/me/` is a **separate, participant-facing** surface on the same admin listener,
distinct from the operator `/ui/`. It is where a **roster human** — not the
operator — reads and replies to their intercom channels: the two-pane inbox UI
(see [intercom-ui.md](intercom-ui.md)) and the `/me/send` path are mounted here.
It has its own gate, and a participant session
**never** carries operator scope and cannot reach the `/admin/*` or `/ui/*`
routes (the participant cookie is path-scoped to `/me`, and `/admin`/`/ui` are
gated independently).

Auth differs from the operator UI in two deliberate ways:

- **Always requires OIDC login — no loopback bypass.** Unlike `/ui/` (where a
  loopback request is trusted as the local operator), `/me/` must know *which*
  human you are, and loopback cannot say — so even a local request logs in via
  `/me/auth/login`. The operator god-view stays the loopback affordance.
- **Reuses the operator's `browser-client-id`.** There is no separate IdP client
  to configure; operator vs participant is decided by mapping the login's OIDC
  subject to a roster human, not by the client. `/me/` is mounted only when
  browser login (`operator-auth.oidc.browser-client-id`) is configured.

The session (cookie `jam_participant`, Path `/me`) is the ID token, verified
against the browser client id; its `(issuer, subject)` is matched to a roster
`Human.Identity` binding (bind one with `at-jam project roster add-human --oidc
<issuer>:<subject>`; see [comms-addressing.md](comms-addressing.md)). A **global
person**: the same subject bound in several projects is one participant whose
view spans them. An unbound subject — one that authenticates at the IdP but is
not bound to any roster human — is refused with **403** (fail closed), not
redirected back to login (which would loop); the operator adds the binding to
let them in.

### Sending (`POST /me/send`)

`POST /me/send` is the participant's send — the human analog of the agent
`send` tool. It takes a JSON body `{"to": "<recipient>", "body": "<text>"}`, plus
an optional `"content_type"` (`text/markdown` by default, or `text/plain`; see
[content type](intercom.md#content-type-markdown-or-plain-text)), and returns
**204** on success. It writes to the **same** durable squawk Log the
agent `send` tool and the relay ingress write (never a parallel path); the UI is
an in-process Log writer, not an egress engine.

- **Identity is the resolved session**, never the body: the sender is the
  gate-injected participant. The outgoing `from` is that person's roster name in
  the *target's* project (a global person may have a different roster name/handle
  per project); when a bare recipient is ambiguous across the participant's
  projects, the first project (in roster-listing order) that resolves it wins.
- **`to` is a recipient or a channel** — a New Message recipient target
  (`human:<name>`, `actor:<session-id>`, `channel:<name-or-unit>`) or a reply to
  an existing channel id from the inbox (`studio:<unit>`, `named:<name>`,
  `dm:<x>|<y>`). A **studio** target (and a **session DM**) resolves to the
  studio's *session actor*, so the append is external-origin and addressed to the
  session — **wake-on resumes a waiting/idled studio** exactly as a relayed reply
  does (unpause if idled; see [comms-addressing.md](comms-addressing.md) and the
  wake-on engine). Any currently-active recipient is allowed — open addressing to
  start, with no comms access-graph check.
- **Errors mirror the agent send** (`/squawks`): a recipient that does not
  resolve → **404**; no intercom-log configured → **503**; an append failure →
  **502**; an empty `to`/`body` → **400**.

## Intercom

The Intercom page (`/ui/intercom`) is a read-only view of Jam's durable
squawk Log — enabled by setting `intercom-log:` in the serve config (see
[serve.md](serve.md)). It shows a filterable, newest-first table of squawk
records: filter by project, participant (`kind:ref`, e.g. `channel:eng`), a body
substring, and a date window (the `since`/`until` bounds are interpreted as UTC
day boundaries; a malformed date is ignored, with a notice, rather than
silently applied). Filters live in the URL, so a filtered view is shareable via
link.

The page is a manual-refresh snapshot, not a live tail — reload to see new
squawks. Each recipient carries a badge showing whether it was reached
internally or externally. The table is empty until the log has writers, and if
`intercom-log:` is unset the page renders a "not configured" notice instead of
an error.

Unlike the roster/kit/destination pages, Intercom has no mutation — the UI only
reads the Log. Its write-ownership model lives with the `intercom-log` field —
see [serve.md](serve.md#the-serve-config). When `store-postgres` is set, the
Log — and so this view — is served from Postgres instead of the JSONL file;
behavior here is unchanged (still a full snapshot per load — pagination is a
later phase). See [serve.md's Postgres store backend section](serve.md#postgres-store-backend-store-postgres)
for the backend-selection rule.

## Editing (day-job mutations)

Beyond viewing, the UI can do the roster day-job — the same actions as the CLI
verbs in [roster.md](roster.md):

- **Enroll** an actor (id, project, role, optional destination overrides).
  The identity token is shown **once**, right after enrolling — copy it then; it
  is never shown again, stored in a list, or logged. For the full connection
  snippet (env vars / git config), use the CLI `at-jam enroll`.
- **Revoke** an actor, **create/delete** a role, and **add/remove** a grant.
  On the Roster page each actor's grants are chips (`project/role`, with a ×
  to remove; hover for the effective destinations), and **+ Grant** on the
  actor's row opens its add-grant form.
- Destination fields (role, enroll/grant overrides) take the CLI's
  `name=credential` syntax ([roster.md](roster.md#roles)); an unknown credential
  or a mapping for a destination not in scope is rejected. Credential *names*
  are references, not secrets, so the UI shows them (the Roles table renders
  `git → git-pat`); credential *values* never appear. Re-saving a role with a
  bare destination name **keeps** its existing mapping (use the CLI's
  `role add` to clear one).

### Role pages

Each role name (in the Roles table, a roster grant chip, or a studio row) links
to its page, `/ui/roles/<project>/<name>`, which shows the whole role:
destinations with the credential the broker injects for each (the role's
mapping, or the destination's default), addressing, the egress policy (or "kit
default" when the role sets none), allocation caps and the personal-session
idle ladder (unset values say what applies), declared standing sessions with
their studio's phase, the actors holding a grant (marked where the grant
overrides the scope), and the role's running studios. **Request session** and
**Delete** are on the page header. The page is view-only for now; fields the
Roles form doesn't edit are changed with the CLI (`role add`, `egress set`,
`standing add`).

Create forms sit in collapsed **+ Add …** panels above each table. The
outcome of a write shows in a banner at the top of the page: a refused write
(validation error, conflict, CSRF refusal) appears as a dismissible error with
the server's message, rather than failing silently.

Every change obeys the same gate as the views (loopback, or an off-loopback
session with `require-scope`) and is recorded in Jam's audit log against the
operator who made it. Destructive actions ask for confirmation. State-changing
requests are refused unless they originate from the Jam UI itself (an
Origin/Referer check, plus any exact origins listed in
[`ui-origins`](serve.md)), so another site can't drive them through your browser.

The kit registry and destinations are also editable from here — see
[Config plane (kits & destinations)](#config-plane-kits-destinations) below.
Raising and tearing down studios is editable from the UI when a runtime
supervisor is configured — see [Runtime (studios)](#runtime-studios) below.

### Runtime (studios)

When Jam is configured with a runtime supervisor (`runtime:` in the serve
config — see [coves.md](coves.md)), the Studios page can also:

- **Raise a managed studio** — id, role, optional project/unit and a workload
  prompt. Jam handles the studio's identity token and launch secret internally;
  they are never shown in the browser (use the CLI `at-jam studio raise` for
  manual wiring).
- **Tear down a studio** (confirmed).

The Roles page gains a **Request** action per role: it raises a
[personal session](personal-sessions.md) of that role **for you**, with the
prompt `Squawk me (human:<your roster name>) and we will get to work.`, so the
session opens the conversation with you on the intercom. You must be signed in
(`/ui/auth/login`) as a login linked to a roster human in the role's project.
As anonymous loopback `local`, the action asks you to sign in. Admission,
delivery checks, and errors are exactly those of `at-jam session request`, and
the outcome (the new session id, or the refusal) shows in the page's banner.

Without a runtime supervisor, the Studios page is view-only. Setting a studio's
activity is not a UI action — that is reported by the studio itself. These actions
obey the same gate, CSRF, and audit-logging as the roster edits above.

### Config plane (kits & destinations)

- **Kits** — push a new version (name + config), pin the current pointer to an
  existing version (chosen from a list of the kit's versions), and delete a kit. A kit still referenced by a role cannot be
  deleted (the UI reports a conflict). See [kits.md](kits.md).
- **Destinations** — add a brokered destination (name, route, upstream,
  identity-in, cred-name, apply) and remove one. A `cred-name` must
  resolve to a configured credential, or the add is rejected. See
  [serve.md#destinations](serve.md#destinations).

A kit config references credentials by name only (no secret values), and a
destination's `cred-name` is a reference, not a secret — the UI shows and logs
neither secret values nor the credential itself. These actions obey the same
gate, CSRF, and audit-logging as the other edits.
