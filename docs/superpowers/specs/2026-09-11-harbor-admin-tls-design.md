---
kind: design-spec
subject: harbor operator identity (slice #2b, cut 3) — admin-API TLS + a fail-closed guard on off-loopback exposure
status: draft
date: 2026-09-11
prereq: 2026-09-11-harbor-operator-oidc-design.md (cut 1 — OIDC operator auth) and 2026-09-11-harbor-operator-login-design.md (cut 2 — device-flow login)
read-when: implementing or reviewing harbor's admin-API TLS serving, the admin-tls config, or the loopback/off-loopback exposure guard
---

# Harbor admin-API TLS

Serve harbor's admin API over TLS so it can be reached **off-loopback** for
remote/multi-operator use — and make that safe by construction. Cut 1 validated
OIDC bearer tokens but the listener stayed plain-HTTP-loopback (a bearer over
cleartext off-host is interceptable); this cut removes that limitation and adds a
**fail-closed guard**: harbor refuses to bind the admin API to a non-loopback
address unless it has both TLS and OIDC. The broker already serves TLS; this is
the symmetric change for the admin plane.

## Scope

**In:**
- **Config `admin-tls: { cert, key }`** (optional). The admin listener resolves its
  cert as **`admin-tls` if set, else the top-level `tls:`** (the broker's cert —
  same host covers both). Helper `serveConfig.adminTLS() (cert, key string, ok bool)`;
  `ok=false` when neither is set.
- **Exposure guard (fail-closed).** Before any listener starts, `serveConfig`
  validates the admin binding:
  - **Loopback** (`127.0.0.0/8`, `::1`, `localhost`) → permissive, unchanged:
    plain HTTP + any authenticator.
  - **Non-loopback** (a routable IP, or empty host / `0.0.0.0` / `::` = all
    interfaces) → **error, refuse to start** unless **both** admin TLS resolves
    **and** `operator-auth.oidc` is configured.
- **TLS serving.** When admin TLS resolves, serve the admin API with
  `(&http.Server{…}).ListenAndServeTLS(cert, key)`; otherwise plain
  `http.ListenAndServe` (only reachable once the guard has proved loopback).
- **Client.** No code change — `adminclient` uses `http.Client`, which does HTTPS
  against the system trust store. Operators set `admin-url: https://…` (via
  `settings.yml` or `login --admin-url`).

**Out (deferred):**
- Custom CA bundle / `--insecure` skip-verify on the client (system roots only).
- mTLS / client certificates; ACME auto-provisioning.
- The `at-harborctl` split.

## Interfaces

```go
// adminTLS resolves the admin listener's cert/key: admin-tls if set, else the
// top-level tls. ok is false when neither is configured.
func (c serveConfig) adminTLS() (cert, key string, ok bool)

// validateAdminExposure returns an error when admin-listen is bound off-loopback
// without both TLS and OIDC. Loopback bindings always pass.
func (c serveConfig) validateAdminExposure() error

// isLoopbackAddr reports whether a listen address (host:port) binds only the
// loopback interface. Empty host, 0.0.0.0 and :: are treated as non-loopback
// (all interfaces).
func isLoopbackAddr(addr string) bool
```

`validateAdminExposure` uses `isLoopbackAddr(c.AdminListen)`, `c.adminTLS()`, and
`c.OperatorAuth.OIDC != nil`.

## Config

```yaml
listen: "0.0.0.0:8443"            # broker (already TLS)
admin-listen: "0.0.0.0:8081"      # now allowed — off-loopback, because ↓
tls:
  cert: /etc/letsencrypt/live/harbor.example/fullchain.pem
  key:  /etc/letsencrypt/live/harbor.example/privkey.pem
# admin-tls:                       # OPTIONAL — only to give the admin plane a
#   cert: /etc/…/admin-fullchain.pem   # different cert/hostname than the broker
#   key:  /etc/…/admin-privkey.pem
operator-auth:
  oidc: { issuer: …, audience: …, require-scope: harbor:admin, device-client-id: … }
```

With the top-level `tls:` present (the broker requires it) and `operator-auth.oidc`
set, flipping `admin-listen` to a routable address is sufficient — the admin API is
served over TLS and the guard is satisfied.

## Serve flow

```
parse config
validateAdminExposure()            // FAIL FAST here on an unsafe off-loopback bind
build broker + admin handler (auth selected as today)
if admin-listen != "":
    cert,key,ok := adminTLS()
    if ok: go admin.ListenAndServeTLS(cert,key)   // TLS
    else:  go admin.ListenAndServe()              // plain — guard proved loopback
broker.ListenAndServeTLS(tls.cert, tls.key)       // unchanged
```

## Error handling

| Situation | Behavior |
|-----------|----------|
| `admin-listen` off-loopback, no admin TLS resolvable | startup error: "admin-listen <addr> is off-loopback but no TLS is configured (set `tls`/`admin-tls`)"; exit non-zero |
| `admin-listen` off-loopback, no `operator-auth.oidc` | startup error: "admin-listen <addr> is off-loopback but operator auth is loopback-only (configure `operator-auth.oidc`)"; exit non-zero |
| `admin-listen` loopback, plain HTTP | allowed (unchanged) |
| admin TLS cert/key path missing/invalid | `ListenAndServeTLS` fails loudly at startup (as the broker already does) |

## Testing

Hermetic, table-driven over the pure helpers:
- `isLoopbackAddr`: `127.0.0.1:8081`✓, `localhost:8081`✓, `[::1]:8081`✓,
  `:8081`✗, `0.0.0.0:8081`✗, `10.0.0.5:8081`✗.
- `adminTLS`: admin-tls set → returns it; only top-level tls → returns that;
  neither → ok=false.
- `validateAdminExposure`: loopback+plain ✓; loopback+OIDC+plain ✓ (today's setup —
  no regression); non-loopback+TLS+OIDC ✓; non-loopback missing TLS ✗;
  non-loopback missing OIDC ✗.

A real TLS admin round-trip (curl + `at-harbor destination list` over HTTPS) behind
`//go:build integration` / manual.

## Manual verification (definition of done)

1. Loopback config (today's) still starts and works — no regression.
2. `admin-listen` off-loopback with `tls:` + `operator-auth.oidc` → starts, logs the
   admin API listening over TLS.
3. `admin-listen` off-loopback **without** TLS → refuses to start with a clear error;
   likewise **without** `operator-auth.oidc`.
4. `curl https://<host>:8081/admin/login-config` succeeds (system-trusted cert);
   `at-harbor destination list --admin-url https://<host>:8081` (logged in) succeeds.
