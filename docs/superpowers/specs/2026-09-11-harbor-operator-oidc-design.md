---
kind: design-spec
subject: harbor operator identity (slice #2b, cut 1) — server-side OIDC/Auth0 token validation on the admin API, behind the OperatorAuthenticator seam
status: draft
date: 2026-09-11
prereq: 2026-09-11-harbor-control-plane-mvp-design.md (slice #2a — the admin API + OperatorAuthenticator seam this extends)
read-when: implementing or reviewing harbor's OIDC operator authentication (the go-oidc authenticator, config, CLI token, and env-var convention)
---

# Harbor operator identity — server-side OIDC validation

Give harbor's admin API **real operator identity** by validating an Auth0 (any OIDC) bearer token, dropped in
behind the `OperatorAuthenticator` seam slice #2a already built. This is the **keystone for remote/multi-operator**
management; the `at-harbor login` UX, off-loopback TLS exposure, and the `at-harborctl` split are the *next* cut.

## Scope

**In:**
- **`OIDCAuthenticator`** (implements `OperatorAuthenticator`) — validate `Authorization: Bearer <jwt>` via
  **`github.com/coreos/go-oidc/v3`**: issuer discovery → JWKS → RS256 signature → `iss`/`aud`/`exp`. Returns
  `Operator{ID: <sub>}`; rejects missing/invalid/expired.
- **Config-selected authenticator:** an `operator-auth.oidc` block → OIDC; absent → `LoopbackAuthenticator`
  (default, unchanged). A harbor runs local-loopback *or* token auth purely by config.
- **Authz:** default = **any token validly issued for the harbor `audience`** by the configured `issuer`. Optional
  **`require-scope`** gate (satisfied by the token's `scope` **or** `permissions` claim) for Auth0 RBAC.
- **CLI token:** the admin verbs gain `--token` and **`AT_HARBOR_ADMIN_TOKEN`**; `adminclient` sends it as
  `Authorization: Bearer`. Loopback mode ignores it.
- **Env-var convention → `AT_HARBOR_`.** New: `AT_HARBOR_ADMIN_TOKEN`. Rename the slice-1 enroll snippet's
  `HARBOR_IDENTITY_TOKEN` → **`AT_HARBOR_IDENTITY_TOKEN`** (the `export`, the `ANTHROPIC_API_KEY` reference, and
  the git credential helper's `$…` reference in `RenderEnrollSnippet`).
- **Audit:** the admin API logs the operator id (`sub`) on every mutation.

**Out (deferred, next cut):** the `at-harbor login` OAuth **device flow** (+ `golang.org/x/oauth2`), **TLS on the
admin listener + off-loopback exposure**, fine-grained per-resource authz, and the **`at-harborctl` split**.

## Dependency

Adopt **`github.com/coreos/go-oidc/v3`** (pulls `github.com/go-jose/go-jose/v4`). The **first non-stdlib,
non-`yaml.v3` dependency** — a deliberate, security-scoped choice: JWT verification is a notorious footgun
(alg-confusion, skipped claim checks, `kid` confusion → auth bypass), so a vetted OIDC verifier is a posture
*upgrade* over hand-rolled crypto for the crown-jewels broker. In-datacenter dependency risk differs from public
edge, and a security library is a higher-posture case.

> **Sandbox build note:** pulling go-oidc into the egress-locked dev sandbox needs the module fetchable — use
> `GOPROXY=direct` (github.com is allow-listed) or add the Go module proxy to the kit's egress allow-list
> (human-gated per SANDBOX.md). Settle at implementation time.

## OIDCAuthenticator

```go
type OIDCAuthenticator struct {
    verifier     *oidc.IDTokenVerifier // from provider.Verifier(&oidc.Config{ClientID: audience, SkipClientIDCheck:false})
    requireScope string                // "" = any valid token
}

// NewOIDCAuthenticator does discovery against issuer (fetches JWKS) and builds the verifier.
// Fails closed if the issuer is unreachable / misconfigured at startup.
func NewOIDCAuthenticator(ctx context.Context, issuer, audience, requireScope string) (*OIDCAuthenticator, error)

func (a *OIDCAuthenticator) Authenticate(r *http.Request) (Operator, error) {
    // 1. pull the bearer token from Authorization
    // 2. a.verifier.Verify(ctx, raw) — signature + iss + aud + exp (RS256)
    // 3. decode claims {sub, scope, permissions}
    // 4. if requireScope != "": must be in scope (space-split) OR permissions[]  → else reject
    // 5. return Operator{ID: claims.Sub}
}
```

- **Verify covers signature + `iss` + `aud` + `exp`** via go-oidc; we add the scope check. Auth0 access tokens are
  RS256 JWTs; `aud` = the API identifier configured in Auth0.
- **`sub`** is the operator id (stable across a session; used for audit). No email is assumed (access tokens
  often omit it).
- go-oidc's `IDTokenVerifier` verifies any RS256 JWT with the standard claims — it is used here for **access
  tokens**, not only ID tokens; that's a supported use.

## Config

```yaml
# operator-auth absent → LoopbackAuthenticator (slice #2a default, unchanged)
operator-auth:
  oidc:
    issuer: https://<tenant>.us.auth0.com/   # trailing slash per Auth0
    audience: https://harbor.acme/api        # the API identifier in Auth0
    require-scope: "harbor:admin"            # optional; empty/omitted = any valid token
```

`serve` builds the authenticator from this: `oidc` present → `NewOIDCAuthenticator(...)` (fails startup if the
issuer can't be reached); absent → `LoopbackAuthenticator{}`. The admin API's `NewAdminHandler` already takes an
`OperatorAuthenticator`, so nothing else changes there.

## Safety posture (this cut)

**The admin listener stays plain-HTTP-loopback.** OIDC validation is added and testable, but the operator surface
is **not** meant to be exposed over the network yet: a bearer token over plain HTTP off-loopback is interceptable.
Off-loopback exposure requires **TLS on the admin listener**, which lands with the login-flow cut. Until then,
enabling OIDC is for validating the integration + the seam (and for a local operator who prefers token auth), not
for remote management.

## CLI

- `enroll`/`revoke`/`destination` gain `--token` (default from `AT_HARBOR_ADMIN_TOKEN`); passed to
  `adminclient`, which sets `Authorization: Bearer <token>` on every request. Empty token → no header (works
  against a loopback-auth harbor).
- Enroll's printed snippet uses `AT_HARBOR_IDENTITY_TOKEN` (rename).

## Testing

Hermetic — a **fake OIDC provider** test helper: an `httptest` server serving `/.well-known/openid-configuration`
and a JWKS built from a test RSA key, plus a helper that mints RS256 JWTs signed with that key. Point
`NewOIDCAuthenticator` at the fake issuer and assert:
- **accept** — a valid token (right `iss`/`aud`, unexpired, in-scope) → `Operator{ID: sub}`.
- **reject** — expired; wrong `aud`; wrong `iss`; bad signature; `alg:none`; unknown `kid`; and (with
  `require-scope` set) a token lacking the scope/permission.
- admin API: an OIDC-gated handler returns 401/403 without a valid token and processes with one.

A real Auth0 round-trip lives behind the `integration` tag / manual verification.

## Definition of done

- With an `operator-auth.oidc` block, `at-harbor serve` validates admin requests against the IdP; without it,
  loopback auth is unchanged.
- A valid token for the harbor audience is accepted (and, if `require-scope` is set, must carry the
  scope/permission); every reject case above returns 401/403; the operator `sub` is logged per mutation.
- CLI `--token` / `AT_HARBOR_ADMIN_TOKEN` reaches the admin API as a bearer; the enroll snippet uses
  `AT_HARBOR_IDENTITY_TOKEN`.
- `go.mod` gains **only** `coreos/go-oidc/v3` (+ its transitive `go-jose`); the change is called out.
- Hermetic tests (fake OIDC provider) cover accept + all reject cases; docs updated; one PR against `main`.

## Open questions

- `sub` vs a custom email/name claim for the logged operator identity — start with `sub`; revisit if audit wants
  human names (an Auth0 custom claim).
- `require-scope` single value vs a list (all-of / any-of) — start with a single required scope; extend later.
- Startup vs lazy discovery: `oidc.NewProvider` does discovery at startup (fail-closed if the issuer is down) —
  acceptable; note it so operators know the issuer must be reachable when `serve` starts.
