---
summary: The harbor admin UI — a server-rendered web view of the live coves and the control-plane roster/roles/kits/destinations, served by `at-harbor serve`; reachable on loopback always, and off-loopback via browser OIDC login. Beyond viewing, it can do the roster day-job (enroll/revoke actors, roles, grants).
read_when: You want to watch a running harbor in a browser — the live cove fleet and the roster/roles/kits/destinations — or do the roster day-job from the browser, without running admin CLI verbs, or you are configuring browser login for it.
owns: the `/ui/` observability + roster-editing surface (what it shows, what it can mutate, how to reach it, its loopback + browser-OIDC-login exposure)
prereqs: serve.md for the admin listener + the off-loopback fail-closed rule; roster.md for the RBAC model these edits act on; INDEX.md for the service overview
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
  3 seconds** (htmx polling); no page reload. Read-only — see
  [coves.md](coves.md) to raise or tear one down.
- **Roster / Roles / Kits / Destinations** — the control-plane objects as
  tables. Roster and Roles are editable from here (below); Kits and
  Destinations are read-only in the UI.

## Reaching the UI

The UI has its own gate, separate from the JSON admin API's authenticator (see
the fail-closed rule in [serve.md](serve.md#exposing-the-admin-api-fail-closed)):

- **Loopback** (local host, or an SSH tunnel to the admin port) — always
  reachable, no login: the local operator is trusted. This holds whether or not
  `operator-auth.oidc` is configured.
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

Not editable from the UI (use the CLI): raising/tearing down coves, the kit
registry, and destinations.
