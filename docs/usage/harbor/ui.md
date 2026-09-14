---
summary: The harbor admin UI — a server-rendered web view of the live coves and the control-plane roster/roles/kits/destinations, served by `at-harbor serve`; reachable on loopback always, and off-loopback via browser OIDC login. Beyond viewing, it can do the roster day-job (enroll/revoke actors, roles, grants), edit the kit registry and destinations, and, with a runtime supervisor configured, raise/tear down managed coves.
read_when: You want to watch a running harbor in a browser — the live cove fleet and the roster/roles/kits/destinations — or do the roster day-job, edit kits/destinations, or raise/tear down a managed cove from the browser, without running admin CLI verbs, or you are configuring browser login for it.
owns: the `/ui/` observability + roster/kit/destination-editing + runtime cove raise/teardown surface (what it shows, what it can mutate, how to reach it, its loopback + browser-OIDC-login exposure)
prereqs: serve.md for the admin listener + the off-loopback fail-closed rule; roster.md for the RBAC model these edits act on; coves.md for the managed-cove lifecycle the runtime actions drive; INDEX.md for the service overview
tier: leaf
updated: 2026-09-14
---

# The harbor admin UI (`/ui/`)

`at-harbor serve` serves a web UI on the same **admin listener** as the JSON
admin API. Point a browser at the admin URL and open `/ui/` (`/` redirects
there):

```
http://127.0.0.1:8081/ui/
```

It renders:

- **Dashboard** (`/ui/`) — the live cove fleet + a roster summary.
- **Coves** (`/ui/coves`) — every managed cove's id, project/role, unit, phase,
  activity, lease holder, raised-at, last-seen. The table **auto-refreshes every
  3 seconds** (htmx polling); no page reload. View-only unless a runtime
  supervisor is configured, in which case it can also raise and tear down
  coves — see [Runtime (coves)](#runtime-coves) below and
  [coves.md](coves.md).
- **Roster / Roles / Kits / Destinations** — the control-plane objects as
  tables, all editable from here — see [Editing](#editing-day-job-mutations)
  below.

## Reaching the UI

The UI has its own gate, separate from the JSON admin API's authenticator (see
the fail-closed rule in [serve.md](serve.md#exposing-the-admin-api-fail-closed)):

- **Loopback** (local host, or an SSH tunnel to the admin port) — reachable with
  no login: the local operator is trusted. This holds whether or not
  `operator-auth.oidc` is configured. The request's `Host` must be a loopback
  literal (`127.0.0.1`/`::1`/`localhost`) or a host listed in `ui-hosts` — if you
  reach the UI over a custom name that DNS-binds to loopback (e.g.
  `harbor.local.example`), add it to `ui-hosts` (see [serve.md](serve.md)) or the
  UI refuses it as a possible DNS-rebinding attempt.
- **Off-loopback, with a `browser-client-id`** set in `operator-auth.oidc` — the
  browser is redirected through an OIDC **Authorization Code + PKCE** login
  (`/ui/auth/login` → your IdP → `/ui/auth/callback`); on success a session cookie
  (the API access token; HttpOnly + Secure + SameSite=Lax) lets you browse until
  it expires, then you re-login. `require-scope` is enforced on every request,
  exactly as for the admin API. See [operators.md](operators.md).
- **Off-loopback, without a `browser-client-id`** — the UI is refused. The
  programmatic admin API is still reachable with a bearer token.

Register `https://<your-harbor-host>/ui/auth/callback` in your IdP's Allowed
Callback URLs. Browser login needs TLS (the session cookie is `Secure`).

The UI never renders a token hash, launch secret, or credential value; the one
exception is the identity token shown once at enroll time (below). The login
routes themselves never expose mutation.

## Editing (day-job mutations)

Beyond viewing, the UI can do the roster day-job — the same actions as the CLI
verbs in [roster.md](roster.md):

- **Enroll** an actor (id, project, role, optional destination/repo overrides).
  The identity token is shown **once**, right after enrolling — copy it then; it
  is never shown again, stored in a list, or logged. For the full connection
  snippet (env vars / git config), use the CLI `at-harbor enroll`.
- **Revoke** an actor, **create/delete** a role, and **add/remove** a grant.

Every change obeys the same gate as the views (loopback, or an off-loopback
session with `require-scope`) and is recorded in harbor's audit log against the
operator who made it. Destructive actions ask for confirmation. State-changing
requests are refused unless they originate from the harbor UI itself (an
Origin/Referer check), so another site can't drive them through your browser.

The kit registry and destinations are also editable from here — see
[Config plane (kits & destinations)](#config-plane-kits-destinations) below.
Raising and tearing down coves is editable from the UI when a runtime
supervisor is configured — see [Runtime (coves)](#runtime-coves) below.

### Runtime (coves)

When harbor is configured with a runtime supervisor (`runtime:` in the serve
config — see [coves.md](coves.md)), the Coves page can also:

- **Raise a managed cove** — id, role, optional project/unit and a workload
  prompt. Harbor handles the cove's identity token and launch secret internally;
  they are never shown in the browser (use the CLI `at-harbor cove raise` for
  manual wiring).
- **Tear down a cove** (confirmed).

Without a runtime supervisor, the Coves page is view-only. Setting a cove's
activity is not a UI action — that is reported by the cove itself. These actions
obey the same gate, CSRF, and audit-logging as the roster edits above.

### Config plane (kits & destinations)

- **Kits** — push a new version (name + config), pin the current pointer to an
  existing version, and delete a kit. A kit still referenced by a role cannot be
  deleted (the UI reports a conflict). See [kits.md](kits.md).
- **Destinations** — add a brokered destination (name, route, upstream,
  identity-in, cred-name, apply, repo-scoped) and remove one. A `cred-name` must
  resolve to a configured credential, or the add is rejected. See
  [serve.md#destinations](serve.md#destinations).

A kit config references credentials by name only (no secret values), and a
destination's `cred-name` is a reference, not a secret — the UI shows and logs
neither secret values nor the credential itself. These actions obey the same
gate, CSRF, and audit-logging as the other edits.
