# harbor: browser OIDC login for the admin UI

**Status:** design approved, pre-plan
**Foundation:** the read-only admin UI (`internal/harbor/adminui`, `docs/usage/harbor/ui.md`), the operator-auth gate (`internal/harbor/oidc.go` `OIDCAuthenticator`, `operator.go` `LoopbackAuthenticator`, `admin.go` `authMiddleware`), and the CLI device flow (`internal/harbor/deviceflow`). **Design history:** `docs/superpowers/specs/2026-09-13-harbor-admin-ui.md`, `docs/superpowers/specs/2026-09-10-harbor-design.md`.

## Problem

The read-only admin UI is mounted inside harbor's operator-auth gate. When harbor is configured with `operator-auth.oidc`, that gate requires a **bearer token on every request** — including loopback and including `/ui`. A browser sends no bearer, so `/ui` returns `403 "missing bearer token"` even from `127.0.0.1`. Today the UI is reachable only when harbor runs *without* OIDC (loopback-trusted). This slice makes the UI usable from a browser under OIDC, and fixes local loopback access under OIDC as a side effect.

## What this delivers

1. **Loopback always reaches the UI.** A loopback request to `/ui` is treated as the local operator regardless of whether OIDC is configured — fixing the current localhost 403.
2. **Browser OIDC login for off-loopback access.** When a `browser-client-id` is configured, an off-loopback browser hitting `/ui` without a session is redirected through an OAuth 2.0 **Authorization Code + PKCE** flow (public client, no secret) and, on success, gets a session cookie.
3. The programmatic `/admin/*` API and the CLI device flow are **unchanged**.

## Scope decisions (settled in brainstorming)

- **Flow:** Authorization Code + PKCE, **public client, no client secret.** `state` (CSRF) + PKCE `S256` (code interception) + `nonce` (ID-token replay) all used.
- **Session:** a **JWT in an HttpOnly+Secure+SameSite=Lax cookie, re-verified per request, no server-side signing key.** The cookie holds the **access token** (`aud` = the API audience), verified per request by the **existing `OIDCAuthenticator`** — one vetted verification path, so `require-scope` is enforced on every UI request exactly as for the bearer API. The **ID token** is verified once at the callback (`aud` = client id, `nonce`) for authenticated-identity assurance; it is not stored.
- **Redirect URI:** derived from the incoming request (`scheme://Host/ui/auth/callback`). The operator registers that exact URL in the IdP's Allowed Callback URLs, which backstops Host-header spoofing; `state` + `nonce` add CSRF/replay protection.
- **Loopback bypasses login.** A loopback request to `/ui` is the trusted local operator and skips OIDC entirely (read-only, secret-free surface; matches the "local loopback to start" intent). Accepted posture shift: a process on the harbor host can view the read-only UI without OIDC.
- **Remote-only reach for login.** Because loopback bypasses login and browser login is only exercised off-loopback (https), the session cookie is always `Secure`.

## Config (`operator-auth.oidc` additions)

Two new optional fields, reusing existing `issuer` / `audience` / `require-scope`:

```yaml
operator-auth:
  oidc:
    issuer:   https://TENANT.us.auth0.com/
    audience: https://harbor.example.com/admin   # existing — the API audience
    require-scope: harbor:admin                   # existing — enforced on UI sessions too
    device-client-id: "…"                         # existing — CLI device flow
    browser-client-id: "…"                        # NEW: public SPA/Native client for the browser auth-code+PKCE flow
    browser-scope: "openid profile email"         # NEW: default "openid profile email"; must include openid
```

- Setting `browser-client-id` **enables** browser login. Unset ⇒ off-loopback `/ui` stays refused (loopback still works).
- `browser-scope` must contain `openid`. The flow also requests `audience=<the configured audience>` so the token endpoint returns an API access token (the session token).

## Architecture

### New package `internal/harbor/browserauth`

Stdlib + go-oidc, isolated and unit-testable. Owns the auth-code+PKCE mechanics and the session-cookie verification; imports `harbor` only for the shared `OIDCAuthenticator`/`Operator` types (no cycle — `harbor` never imports `browserauth`).

- **PKCE + params:** `NewVerifier() (verifier, challenge string)` (`crypto/rand`, base64url, `S256` challenge); `AuthCodeURL(authorizeEndpoint, clientID, redirectURI, scope, audience, state, nonce, challenge string) string`.
- **Discovery + exchange:** reuse the deviceflow discovery pattern to get `authorization_endpoint` + `token_endpoint`; `ExchangeCode(ctx, tokenEndpoint, clientID, code, verifier, redirectURI) (idToken, accessToken string, err error)`.
- **ID-token verification (callback):** a go-oidc verifier with `ClientID = browser-client-id` checks signature/iss/exp and the `nonce` claim.
- **Handler:** `Handler(cfg Config, apiAuth *harbor.OIDCAuthenticator) http.Handler` serving `GET /ui/auth/login|callback|logout` (all unauthenticated). `Config` carries issuer/clientID/scope/audience + the resolved endpoints.
- **Session cookie:** `SetSession(w, accessToken, secure)` / `ClearSession(w)` with the fixed name (e.g. `harbor_session`), `HttpOnly`, `Secure`, `SameSite=Lax`, `Path=/ui`.

### The UI gate (in `NewAdminHandler` / adminui mount)

The `/ui` subtree is wrapped by a **UI-specific authenticator**, decoupled from the `/admin/*` authenticator:

- **Loopback** remote address ⇒ allow as the local operator (`LoopbackAuthenticator` semantics), regardless of OIDC.
- **Off-loopback** ⇒ if `browser-client-id` is configured, read the `harbor_session` cookie and verify it with the **existing `OIDCAuthenticator`** (signature/iss/aud/exp + `require-scope`); on success proceed, on missing/invalid/expired cookie **302 to `/ui/auth/login`** (for a navigational GET). If `browser-client-id` is not configured, refuse.
- **Always-open exceptions to the gate:** the login routes `/ui/auth/*` (login/callback/logout) must be reachable by an *unauthenticated off-loopback browser* — that is how a session is established — and `/ui/static/*` (assets, nothing sensitive) is open too so pages can load htmx. Every other `/ui…` path requires loopback-or-valid-session as above.
- `/admin/*` keeps the configured bearer/loopback authenticator, unchanged.

### Routes (new, unauthenticated, under `/ui/auth/`)

| Route | Behavior |
|-------|----------|
| `GET /ui/auth/login` | mint `state`/`nonce`/PKCE verifier (`crypto/rand`); set them in short-lived HttpOnly+Secure+SameSite=Lax cookies (`Path=/ui/auth`); 302 to the authorize URL. Optional `?return_to=` captured into the state cookie (validated at callback). |
| `GET /ui/auth/callback` | constant-time compare `state` vs cookie; exchange `code`+verifier; verify ID token (`aud`=client id, `nonce`); set the session cookie (access token); clear the temp cookies; 302 to the validated local `return_to` (default `/ui/`). |
| `GET /ui/auth/logout` | clear the session cookie; 302 to a logged-out landing (`/ui/auth/login` or a small page). |

### Data flow

```
off-loopback browser ─GET /ui/─▶ UI gate: no session cookie
   └─302─▶ /ui/auth/login ─(set state/nonce/pkce cookies)─302─▶ IdP authorize
        IdP ─302 code+state─▶ /ui/auth/callback
             ├ state == cookie? (constant-time)         ── else 400
             ├ exchange code+verifier → id_token, access_token
             ├ verify id_token (aud=client_id, nonce)   ── else 401
             ├ Set-Cookie harbor_session=<access_token> (HttpOnly,Secure,Lax,Path=/ui)
             └─302─▶ return_to (validated /ui path; default /ui/)
   next GET /ui/… ─▶ UI gate: cookie present
        └ OIDCAuthenticator.verify(access_token)  (iss/aud/exp + require-scope) ─ ok ─▶ render
```

## Security specifics (from golang-security)

- **`crypto/rand`** for `state`, `nonce`, PKCE `code_verifier`; never `math/rand`.
- **`crypto/subtle.ConstantTimeCompare`** for the `state` check.
- **PKCE `S256`** (never `plain`); `code_verifier` 43–128 chars.
- **Open-redirect guard:** `return_to` must be a path beginning `/ui` and must reject `//`, `/\`, backslashes, and any absolute/scheme-relative URL; default `/ui/` on any doubt.
- **Cookies:** session and temp cookies are `HttpOnly`, `Secure`, `SameSite=Lax` (Lax is required so cookies survive the top-level cross-site redirect back from the IdP). Session `Path=/ui`; temp cookies `Path=/ui/auth`, short `Max-Age`, cleared at callback.
- **SSRF:** the authorize/token/JWKS endpoints come only from the configured `issuer`'s discovery document — never from request input.
- **Secrets never logged:** tokens, codes, verifiers, and cookie values are never logged at any level (harbor's structured-logging invariant); errors log a reason, not the material.
- **Fail closed:** any verification failure denies (401/redirect); never fall through to an authenticated state on error.

## Testing (hermetic)

Extend the existing `fakeOIDC` harness (`internal/harbor/oidc_test.go`) with `authorization_endpoint` + `token_endpoint` that mint RS256 ID/access tokens, driven via `httptest`. No live IdP, no network, no `integration` tag.

- **browserauth unit:** PKCE verifier/challenge shape (`S256`), `AuthCodeURL` carries `response_type=code`, `code_challenge`, `state`, `nonce`, `scope`, `audience`, `redirect_uri`; `ExchangeCode` posts `grant_type=authorization_code`+verifier and returns both tokens.
- **callback:** state-mismatch → 400; good state → session cookie set with the correct flags (HttpOnly/Secure/SameSite=Lax/Path); `nonce` mismatch → 401; `return_to` open-redirect attempts (`//evil`, `https://evil`, `/\evil`) → forced to `/ui/`.
- **UI gate:** loopback with no cookie → allowed; off-loopback with no cookie and `browser-client-id` set → 302 to `/ui/auth/login`; off-loopback with a valid session cookie → allowed; expired/invalid cookie → 302; `require-scope` unmet → denied; `browser-client-id` unset + off-loopback → refused.
- **No-leak:** no token/code/verifier value appears in any log line captured in tests.

## Docs

Update `docs/usage/harbor/ui.md`: replace the "not reachable from a remote browser yet" note with the browser-login story (config fields, the login redirect, loopback bypass), cross-linking `operators.md`/`serve.md` for `operator-auth.oidc`. Add the two new fields to the `operator-auth.oidc` reference in `operators.md`.

## Non-goals

- Refresh-token handling / silent renewal (session lives to the access token's `exp`; expiry re-triggers login, which is silent if the IdP session is still valid).
- Confidential-client / client-secret flows.
- IdP-side (RP-initiated) logout / single-logout (local cookie clear only).
- Any mutation from the UI (still read-only).
- Making `/admin/*` accept the session cookie (the programmatic API keeps header bearer / loopback).
