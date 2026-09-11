---
kind: design-spec
subject: harbor operator identity (slice #2b, cut 2) — `at-harbor login` OIDC device flow, harbor-discovered client config, cached operator token
status: draft
date: 2026-09-11
prereq: 2026-09-11-harbor-operator-oidc-design.md (cut 1 — the OIDC authenticator, operator-auth.oidc config, and --token/AT_HARBOR_ADMIN_TOKEN this builds on)
read-when: implementing or reviewing `at-harbor login`/`logout`/`whoami`, the /admin/login-config endpoint, the device-flow client, or the client settings/token cache under ~/.config/at-harbor
---

# Harbor operator login — OIDC device flow

Give operators a one-command sign-in. `at-harbor login` runs the **OAuth 2.0 device
authorization flow** (RFC 8628) against the same Auth0/OIDC tenant cut 1 validates,
self-configuring from harbor, and caches the resulting token so the whole `at-harbor`
command surface "just works" without hand-carrying `AT_HARBOR_ADMIN_TOKEN`. This is
the ergonomics cut (driver #3, setup friction); it sits on cut 1's server-side
validation and changes nothing about how tokens are *verified*.

Not the Auth0 CLI: `at-harbor login` self-configures **from harbor** (the operator
needn't know the issuer/audience/client-id or install anything else) and caches for
every `at-harbor` verb — a harbor-aware, zero-config-for-the-operator sign-in.

## Scope

**In:**
- **`GET /admin/login-config`** (server) — harbor advertises the public device-flow
  client params `{issuer, audience, client_id, scope}`. **Exempt from the operator
  authenticator** (you call it to *get* a token — chicken-and-egg), which is safe
  because every field is a public OAuth parameter, no secret. Returns **404** when
  harbor is not OIDC-gated (loopback mode → "no login needed").
- **Config:** `operator-auth.oidc.device-client-id` (a public Auth0 **Native** app id —
  not a secret) and optional `operator-auth.oidc.device-scope` (default `openid`).
  These + the existing `issuer`/`audience` populate `/admin/login-config`.
- **Device-flow client — `internal/harbor/deviceflow`** (stdlib only; **not** go-oidc):
  discovery → `RequestDeviceCode` → `PollToken`, honoring `authorization_pending` /
  `slow_down` / `expired_token`, with an injected clock for hermetic tests.
- **Client settings — `~/.config/at-harbor/settings.yml`** (operator-authored,
  non-secret, XDG-aware): a **map of app profile → endpoint defaults**
  (`{admin-url, base-url}`), e.g. `default:` and `dev-app:`. Every verb takes
  **`--app <name>`** (default `default`) to select the profile; resolution is
  **flag → the app's `settings.yml` block → built-in default**.
- **Token cache — per app, `~/.config/at-harbor/{app}-admin-token.json`** (mode
  **0600**, login-owned): `{access_token, sub, expiry, admin_url}`; `sub`/`exp`
  parsed from the JWT payload *unverified* (display only). `login` writes it,
  `logout` clears it. **The per-app token file is the scoping boundary** — a token
  is only ever used under its own `--app`, so one harbor's token never reaches
  another. `--app` names are validated (`[A-Za-z0-9._-]+`) so they stay a single
  filename component.
- **Commands:** all take `--app`. `login` (device flow → cache → `logged in as
  <sub>, expires <t>`; if `--admin-url` is given it is **persisted** into the app's
  `settings.yml`), `logout` (clear the app's token), `whoami` (print the app's
  cached `sub` + `admin_url` + expiry, or "not logged in" / "session expired").
- **Token precedence** for `enroll`/`revoke`/`destination`: `--token` → env
  `AT_HARBOR_ADMIN_TOKEN` → the **selected app's cached token** (when unexpired).
  Loopback harbors ignore all three, unchanged.

**Out (still deferred):**
- **Refresh tokens / silent refresh** — v1 re-logs in on expiry; no `offline_access`,
  no refresh secret on disk.
- **Admin-API TLS / off-loopback exposure** — login works today because the CLI and
  harbor share the loopback host; the bearer must not travel off-loopback until the
  TLS cut. (`/admin/login-config` is public and stays safe off-loopback regardless.)
- **The `at-harborctl` split** — commands stay in `at-harbor`.

## Why device flow

The CLI is a public client with no browser of its own and no client secret to hold.
The device flow is exactly this case: the CLI shows a short `user_code` + URL, the
operator approves in *their* browser (any device), and the CLI polls for the token.
No secret on the client, no redirect/callback server, works headlessly over SSH.

## Interfaces

### Server: `/admin/login-config` + `NewAdminHandler`

`NewAdminHandler` gains one param — an optional `*OperatorLoginConfig` (nil ⇒ the
route returns 404). `cmdServe` builds it from `cfg.OperatorAuth.OIDC` when present.

```go
// OperatorLoginConfig is the public device-flow client config harbor advertises at
// GET /admin/login-config so `at-harbor login` can self-configure. All fields are
// public OAuth parameters — never a secret.
type OperatorLoginConfig struct {
    Issuer   string `json:"issuer"`
    Audience string `json:"audience"`
    ClientID string `json:"client_id"`
    Scope    string `json:"scope"`
}

func NewAdminHandler(store Store, auth OperatorAuthenticator, credExists func(string) bool,
    login *OperatorLoginConfig, log *slog.Logger) http.Handler
```

`authMiddleware` short-circuits `GET /admin/login-config` **before** calling
`auth.Authenticate`, so it is reachable with no token even under `OIDCAuthenticator`.

### Client: `internal/harbor/deviceflow`

Stdlib-only OAuth device-flow client, HTTP-doer + clock injected for tests.

```go
type Config struct{ Issuer, Audience, ClientID, Scope string }

type DeviceCode struct {
    DeviceCode              string
    UserCode                string
    VerificationURI         string
    VerificationURIComplete string
    Interval                int // seconds
    ExpiresIn               int // seconds
}

type Token struct {
    AccessToken string
    ExpiresIn   int
}

// RequestDeviceCode discovers device_authorization_endpoint from the issuer and
// POSTs client_id/scope/audience to start the flow.
func RequestDeviceCode(ctx context.Context, doer HTTPDoer, cfg Config) (DeviceCode, error)

// PollToken polls token_endpoint (grant_type urn:ietf:params:oauth:grant-type:device_code)
// until the user approves, honoring authorization_pending (keep polling), slow_down
// (widen interval), and expired_token/access_denied (terminal error). sleep is
// injected so tests don't wait.
func PollToken(ctx context.Context, doer HTTPDoer, sleep func(time.Duration),
    tokenEndpoint, clientID, deviceCode string, interval int) (Token, error)
```

### Client: settings + token cache (`cmd/at-harbor`)

Mirrors at-cove's `configDir()` (`$XDG_CONFIG_HOME/at-harbor` else `~/.config/at-harbor`):

- `settings.yml` → `map[app]{ admin-url, base-url }` (all optional). `loadSettings(app)`
  reads one profile; `saveSettings(app, s)` upserts it (preserving other profiles);
  flags win. `validateApp(app)` guards the name.
- `{app}-admin-token.json` → `{ access_token, sub, expiry, admin_url }` at 0600.
  `saveToken(app, …)`/`loadToken(app)`/`clearToken(app)`; `resolveToken(app, flag)`.

## Data flow — `at-harbor login`

```
at-harbor login [--app NAME] [--admin-url URL]
  admin-url := flag | settings.yml[app].admin-url | http://127.0.0.1:8081
  if --admin-url given → persist it into settings.yml[app]
  → GET  {admin-url}/admin/login-config
        404 → "harbor is not OIDC-gated; no login needed." (exit 0)
        200 → {issuer, audience, client_id, scope}
  → deviceflow.RequestDeviceCode(issuer, client_id, audience, scope)
        (discovery → device_authorization_endpoint)
  → print: "To sign in, open <verification_uri_complete>\n   and confirm the code: <user_code>"
  → deviceflow.PollToken(...)  // authorization_pending → wait interval; slow_down → widen
        expired before approval → "login timed out; run `at-harbor login` again." (exit 1)
        access_denied           → "login was denied." (exit 1)
  → 200 → access_token
  → parse sub + exp from the JWT payload (unverified, display only)
  → saveToken(app, access_token, sub, exp)  // ~/.config/at-harbor/{app}-admin-token.json, 0600
  → print: "logged in as <sub>; token expires <t>."
```

`enroll`/`revoke`/`destination` resolve their bearer as `--token` → `AT_HARBOR_ADMIN_TOKEN`
→ `loadToken()` (dropped if past `expiry`). A 401 from an OIDC-gated harbor with a
stale cache prints "session expired; run `at-harbor login`."

## Error handling

| Situation | Behavior |
|-----------|----------|
| harbor not OIDC-gated (login-config 404) | `login`: "not OIDC-gated; no login needed", exit 0. Commands keep using loopback auth. |
| admin-url unreachable | Reuse adminclient's "admin API unreachable … is `at-harbor serve` running?" error. |
| device code expires before approval | "login timed out; run `at-harbor login` again." |
| `access_denied` | "login was denied." |
| `slow_down` | Widen the poll interval, keep polling. |
| `whoami`, no cache | "not logged in." |
| `whoami`, cache past expiry | "session expired; run `at-harbor login`." |

## Testing

Hermetic (default): a **fake provider** (`httptest`) serving discovery (with
`device_authorization_endpoint` + `token_endpoint`), a device-auth endpoint returning
a canned `device_code`/`user_code`/`interval`, and a token endpoint that returns
`authorization_pending` N times then an `access_token`. Injected `sleep` ⇒ no real waits.

- `deviceflow`: `RequestDeviceCode` parses the response; `PollToken` — pending→success,
  `slow_down` widens the interval, `expired_token`/`access_denied` are terminal errors.
- Cache: `saveToken`/`loadToken`/`clearToken` round-trip; file mode is `0600`; a
  past-`expiry` cache loads as absent.
- Settings: `loadSettings` parses `admin-url`/`base-url`; flag overrides settings
  overrides default (hermetic temp `XDG_CONFIG_HOME`).
- Server: `/admin/login-config` returns the params when configured, **404** when not,
  and is reachable **without a token** even under an OIDC authenticator.
- CLI: `login` against a fake harbor + fake provider caches a token and prints the
  user-code line; `whoami` prints the `sub`; token precedence flag > env > cache.

Real Auth0 device-flow round-trip behind `//go:build integration` / manual.

## Auth0 setup delta (beyond cut 1)

Cut 1 created the API (audience, RS256) and, optionally, `harbor:admin` permission +
RBAC. For login, add a **Native** application (public client, no secret), enable the
**Device Code** grant on it, and authorize it for the harbor API. Put its client id in
`operator-auth.oidc.device-client-id`. Users approve device logins in their browser;
the token's `sub` is the human operator (not an M2M client), so the audit trail names
real people.

## Manual verification (definition of done)

1. `operator-auth.oidc` has `device-client-id`; `at-harbor serve` starts.
2. `curl -s $ADMIN/admin/login-config` returns `{issuer,audience,client_id,scope}`
   **with no token** (auth-exempt); a loopback-only harbor returns 404.
3. `at-harbor login` prints a code + URL; after browser approval it caches the token
   and prints `logged in as <sub>`.
4. `at-harbor destination list` (no `--token`, no env) succeeds using the cached token.
5. `at-harbor whoami` prints the `sub` + expiry; `at-harbor logout` clears it and a
   subsequent command reports the missing/expired session.
6. `login --admin-url <url>` persists that url into the app's `settings.yml` block,
   so every subsequent verb (same `--app`) runs without `--admin-url`; a second
   `--app` keeps its own settings + token file.
