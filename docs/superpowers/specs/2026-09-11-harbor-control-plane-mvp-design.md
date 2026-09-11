---
kind: design-spec
subject: harbor slice #2a — control plane MVP (self-config): a loopback admin API for destinations + enrollments, backed by a live store
status: draft
date: 2026-09-11
prereq: 2026-09-10-harbor-design.md (overarching) and 2026-09-10-harbor-broker-guest-mvp-design.md (slice #1, which this extends)
read-when: implementing or reviewing harbor's first control-plane cut — the admin API, the live destination/identity store, and the CLI-as-client shift
---

# Harbor slice #2a — control plane MVP (harbor self-config)

Turn harbor's static `harbor.yaml` + one-shot file store into a **managed store + a minimal, loopback-only
admin API**, so an operator manages harbor's **destinations** and **enrollments** at runtime — no YAML edits,
no restart. Credentials stay config-sourced. This is the walking skeleton of the control plane: it forces the
store/API/CLI/operator-auth decisions that the later object-model and kits-out-of-repos work will build on.

## Scope

**In:**
- **Admin API** (REST) on a **loopback listener**, distinct from the cove-facing broker:
  - `GET/POST /admin/destinations`, `DELETE /admin/destinations/{name}` — the routing/policy table.
  - `GET/POST /admin/enrollments` (POST = enroll → returns the token **once**), `DELETE /admin/enrollments/{id}` (= revoke).
  - `GET /admin/healthz`.
- **Live store** — slice-1's file-backed JSON store, extended to hold **destinations + identities**. The
  broker reads the **current** destination table from the store per request, so admin changes take effect live.
- **CLI becomes an admin-API client** — `at-harbor enroll`/`revoke` POST/DELETE to the running harbor (live);
  add `at-harbor destination add|list|rm`. `at-harbor serve` reads only bootstrap config.
- **`harbor.yaml` → bootstrap only** (`listen`, `admin-listen`, `tls`, `store`, `credentials:` resolvers);
  destinations move into the store, with `at-harbor destination import <yaml>` (or first-boot seed from a
  `destinations:` block when the store is empty) to migrate a slice-1 config.
- **Operator-auth seam** — every admin request resolves to an operator identity via an `OperatorAuthenticator`
  interface; the MVP implementation is **loopback ⇒ local operator**.
- **Client/server code seam** — the admin API client + wire types live in a dedicated, server-dep-free package
  so a later `at-harborctl` split is mechanical.

**Out (deferred):**
- **Credentials in the API** — they stay resolved from config / `at-mint`, never stored or writable via the
  API (a secrets-vault concern of its own).
- **Auth0 / OIDC operator identity** and per-operator authz/audit — the **next slice**, dropped in behind the
  `OperatorAuthenticator` seam; ships with the `at-harborctl` split and an `at-harbor login` device flow.
- **Projects / Roles / Kits object model**, **kits-out-of-repos** (at-cove side), a **real DB**,
  **remote/multi-operator**, and a **UI** — later slices.

## Architecture — one writer, two surfaces, one store

The **`serve` process is the sole writer.** It runs two listeners:

- the existing **cove-facing broker** on the public TLS `listen` (slice #1, unchanged), and
- a new **operator-facing admin API** on `admin-listen` bound to loopback (`127.0.0.1:<port>`), reachable only
  from the harbor host.

Both read/write **one file-backed store** holding two collections — `destinations` and `identities`. Because
`serve` is the only writer (the CLI mutates *through the API*, over HTTP, not by touching the file), there is
no multi-process file contention; all mutations go through admin handlers → in-memory update → atomic persist
under the existing mutex.

**Broker refactor:** `NewBroker` takes the **store** (a destination provider) instead of a static `Config`, and
resolves the matching destination from the current table per request. This is what makes destination edits live.

## Admin API

Minimal REST, JSON bodies, loopback only. Every handler runs through the operator-auth seam first.

| Method + path | Body / result | Notes |
|---|---|---|
| `GET /admin/destinations` | list of destinations | `cred_name` shown; no secret values anywhere |
| `POST /admin/destinations` | a `Destination` | **validates `cred_name` resolves** against the configured credentials before accepting — an unresolvable/misrouted destination is rejected, since a destination controls where a credential is sent |
| `DELETE /admin/destinations/{name}` | — | |
| `GET /admin/enrollments` | list of identities (id/project/role/destinations/repos/expiry) — **hashes only, never tokens** | |
| `POST /admin/enrollments` | enroll params → `{id, token}` | the raw token is returned **once** in the response and never again |
| `DELETE /admin/enrollments/{id}` | — | revoke |
| `GET /admin/healthz` | ok | |

**Security posture:** the admin API is **credential-routing-sensitive** — a `POST /admin/destinations` that
sets `cred_name: anthropic-key, upstream: attacker.example` would make harbor send the real key to the
attacker. That is why the surface is **loopback-only** in this slice and why destination creates validate the
`cred_name`. Exposing it off-host is gated on the operator-auth (Auth0) slice, never a casual config toggle.

## Operator-auth seam

```
type OperatorAuthenticator interface {
    // Authenticate resolves the operator behind a request, or returns an error to reject it.
    Authenticate(r *http.Request) (Operator, error)
}
```

- **MVP impl — `LoopbackAuthenticator`:** the admin listener is bound to loopback, so a connected client is a
  local operator; returns a fixed `Operator{ID: "local"}`. (Defence in depth: also assert the remote addr is
  loopback.)
- **Next slice — `OIDCAuthenticator`:** verify an Auth0 JWT (JWKS + `iss`/`aud`/`exp` + scopes) → the operator
  identity. Handlers already receive an `Operator`, so per-operator authz + audit come for free with no handler
  changes. (This is where the OIDC/JWT dependency and the `at-harbor login` device flow are introduced.)

Operator identity (humans managing harbor) is a **separate plane** from actor identity (the enrollment tokens
coves present to the broker) — two identity systems, deliberately distinct.

## Store

File-backed JSON, extended from slice #1:
- Two collections: `identities` (as slice #1 — token-hash keyed, hashes only) and `destinations`
  (name-keyed rows: `route`, `upstream`, `identity_in`, `cred_name`, `apply`, `repo_scoped`).
- Single-writer (the `serve` process); mutations persist atomically (write-temp-then-rename) under the mutex.
- Loaded once at boot; kept authoritative in memory; the file is the durable copy. (No cross-process reload is
  needed because the CLI writes through the API, not the file — which is exactly why this subsumes COV-139.)

## CLI (admin-API client)

- `at-harbor serve --config <bootstrap.yaml>` — starts both listeners; reads bootstrap only.
- `at-harbor enroll …` / `at-harbor revoke --id …` — now **HTTP calls to the running harbor's admin API**
  (default `--admin-url http://127.0.0.1:<port>`), so the effect is live. Enroll prints the one-time token +
  the Guest snippet (unchanged rendering).
- `at-harbor destination add|list|rm …` — manage the routing table via the API.
- All client verbs use the shared, server-dep-free client package.

## `harbor.yaml` → bootstrap

```yaml
listen: "0.0.0.0:8443"          # cove-facing broker (TLS)
admin-listen: "127.0.0.1:8081"  # operator-facing admin API (loopback)
tls: { cert: …, key: … }
store: /var/lib/at-harbor/store.json
credentials:                    # resolvers only — never in the store/API
  anthropic-key: { command: [at-mint, anthropic, …] }
  git-pat:       { command: [at-mint, github, …] }
```

`destinations:` are no longer a `serve` config key; add them via the CLI/API, or `at-harbor destination import`
a slice-1-style file once.

## Testing

Hermetic: admin handlers via `httptest` (create/list/delete destinations + enrollments; `cred_name` validation
rejects; enroll returns a token once; no secret/token in responses or logs); the extended store; and the
**broker-reads-live-store** behavior (add a destination via the store → a broker request now matches it). CLI
verbs via a fake admin server + the `run()` harness. A real two-listener round-trip behind the `integration` tag.

## Definition of done

- `at-harbor serve` runs the broker + a loopback admin API from a bootstrap config with **no `destinations:`**.
- With harbor running: `at-harbor destination add …` then a brokered request uses it **without a restart**;
  `at-harbor enroll` / `revoke` take effect **live**; a revoked identity is rejected on the next request.
- Admin API rejects a destination whose `cred_name` doesn't resolve; no credential value or identity token ever
  appears in an API response or a log.
- Slice-1 behavior (Anthropic + git connectors, x-api-key, git challenge) is unchanged.
- COV-139 (live reload) is **folded in** — close it referencing this slice.
- Docs updated; one PR against `main`.

## Open questions

- Admin transport: a loopback **TCP port** (simple, what the CLI dials) vs a **unix socket** (filesystem-perm
  trust, no port). Lean TCP for MVP; note the socket option.
- `import` vs first-boot **auto-seed** from a `destinations:` block — pick one (lean: an explicit
  `at-harbor destination import`, no magic seeding).
- Whether `serve` should refuse to start with an **empty destination table** (fail-soft warn vs hard error).
- The `at-harborctl` split trigger: the remote/multi-operator + Auth0 slice — recorded here, not built now.
