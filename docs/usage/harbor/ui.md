---
summary: The read-only harbor admin UI — a server-rendered web view of the live coves and the control-plane roster/roles/kits/destinations, served by `at-harbor serve`; reachable on loopback always, and off-loopback via browser OIDC login.
read_when: You want to watch a running harbor in a browser — the live cove fleet and the roster/roles/kits/destinations — without running admin CLI verbs, or you are configuring browser login for it.
owns: the `/ui/` read-only observability surface (what it shows, how to reach it, its loopback + browser-OIDC-login exposure)
prereqs: serve.md for the admin listener + the off-loopback fail-closed rule; INDEX.md for the service overview
tier: leaf
updated: 2026-09-13
---

# The harbor admin UI (`/ui/`)

`at-harbor serve` serves a **read-only** web UI on the same **admin listener** as
the JSON admin API. Point a browser at the admin URL and open `/ui/` (`/`
redirects there):

```
http://127.0.0.1:8081/ui/
```

It renders, all read-only:

- **Dashboard** (`/ui/`) — the live cove fleet + a roster summary.
- **Coves** (`/ui/coves`) — every managed cove's id, project/role, unit, phase,
  activity, lease holder, raised-at, last-seen. The table **auto-refreshes every
  3 seconds** (htmx polling); no page reload.
- **Roster / Roles / Kits / Destinations** — the control-plane objects as tables.

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

The UI never renders a token, token hash, launch secret, or credential value, and
adds **no mutation paths** — enroll/raise/teardown/edit stay on the
[admin verbs](operators.md); the login routes never expose mutation.
