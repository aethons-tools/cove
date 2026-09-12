---
summary: Operator sign-in and the admin client — gating the admin API with `operator-auth.oidc`, the `login`/`logout`/`whoami` device flow, the `--token`/env fallback, and `settings.yml` app profiles (`--app`).
read_when: You are gating harbor's admin API behind Auth0/OIDC, signing an operator in, passing an operator token from CI, or managing several harbors from one machine with `--app` profiles.
owns: the operator-auth.oidc server block, the login/logout/whoami device flow, the admin-token resolution (flag → env → cached login), and the settings.yml app-profile model
prereqs: serve.md for where operator-auth.oidc sits in the serve config; INDEX.md for the admin-verb client story
tier: leaf
updated: 2026-09-12
---

# Operator sign-in & the admin client

Every `at-harbor` admin verb (`destination`, `role`, `grant`, `ungrant`, `roster`,
`enroll`, `revoke`, `kit`) is a client of a running harbor's admin API. This doc
covers how that client authenticates and how one machine targets several harbors.

By default the admin API is **loopback-only** — no login required; the verbs just
work against `127.0.0.1`. Gate it with OIDC when the admin API is reachable beyond
loopback (see the fail-closed rule in [serve.md](serve.md)).

## Gating the admin API (`operator-auth.oidc`)

Add the block to the serve config to require a validated operator identity on
every admin request:

```yaml
operator-auth:
  oidc:
    issuer:   https://YOUR_TENANT.us.auth0.com/
    audience: https://harbor.example.com/admin
    require-scope: harbor:admin          # optional; matched against the token's scope/permissions
    device-client-id: "…"                # the Native app's client id (for `login`)
    device-scope: "openid profile"       # optional device-flow scopes
```

Harbor validates the bearer's signature, `iss`, `aud`, and `exp` via go-oidc, and
logs the operator `sub` on every mutation. `require-scope`, when set, is matched
against the token's space-delimited `scope` **or** its `permissions[]` array (the
latter needs Auth0 RBAC "Add Permissions in the Access Token" + the permission
assigned to the user).

**Auth0 app type matters:** `login` uses the OAuth 2.0 **device flow**, which
requires a **Native** application with the **Device Code** grant enabled — *not* a
Machine-to-Machine app (an M2M app returns `403` on device authorization).

## `login` / `logout` / `whoami`

```
at-harbor login   --admin-url https://harbor.example.com   # device-flow sign-in
at-harbor whoami                                            # show the cached identity + expiry
at-harbor logout                                            # clear the cached token
```

`login` self-configures from harbor's auth-exempt `GET /admin/login-config`
(`{issuer, audience, client_id, scope}`), runs the device flow (prints a URL +
user code to approve in a browser), and caches the resulting operator token at
`~/.config/at-harbor/{app}-admin-token.json` (mode `0600`). `whoami` prints the
`sub` and expiry; an expired cache triggers a fresh `login` on the next verb.

`login` against a harbor with no `operator-auth.oidc` exits cleanly (the
login-config endpoint returns 404 → "not OIDC-gated; no login required").

## How the admin token is resolved

Each admin verb picks its operator token in this precedence:

1. `--token <value>` flag,
2. `AT_HARBOR_ADMIN_TOKEN` environment variable,
3. the cached login session for the `--app` profile.

A stale `AT_HARBOR_ADMIN_TOKEN` in the environment **shadows** a fresh login
session — if a verb unexpectedly `401`s or `403`s right after a successful
`login`, check for a leftover env var (the CLI warns when the env shadows a cached
session).

## App profiles (`settings.yml`, `--app`)

One machine often talks to several harbors (prod, a dev harbor, …). `settings.yml`
at `~/.config/at-harbor/settings.yml` holds **named app profiles**, each with the
endpoints for one harbor:

```yaml
default:
  admin-url: https://harbor.example.com
  base-url:  https://harbor.example.com      # broker base, for the enroll snippet
dev-app:
  admin-url: http://127.0.0.1:8081
  base-url:  http://127.0.0.1:8080
```

- Every verb takes `--app <name>` (default `default`) to pick the profile; the
  token cache is per-app (`{app}-admin-token.json`).
- `--admin-url` overrides the profile's `admin-url` for one invocation; passing it
  to `login` **persists** it to the profile.
- `--base-url` (on `enroll`) overrides `base-url` for the printed snippet.

So `at-harbor --app dev-app roster` lists the dev harbor's roster using the dev
profile's endpoint + cached token. The admin verbs themselves are documented in
[roster.md](roster.md) (RBAC + enrollment) and [kits.md](kits.md) (registry);
`destination` is in [serve.md](serve.md).
