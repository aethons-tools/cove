---
kind: design-spec
subject: COV-141 — at-cove auto-enrolls a cove with harbor at session start (mint + revoke), replacing the pre-supplied identity token
status: draft
date: 2026-09-12
prereq: 2026-09-11-harbor-cove-networking-design.md (COV-138 — the harbor: block + connector; this removes its manual-token step)
read-when: implementing or reviewing cove-side harbor auto-enrollment (shelling at-harbor enroll/revoke, the enroll --json mode, mint scope, revoke-on-exit)
---

# Cove auto-enrollment with harbor (COV-141)

COV-138 routes a hardened cove's Anthropic + git through harbor, but the cove's
identity token is **pre-supplied**: the operator runs `at-harbor enroll` once and
supplies the token host-side. This makes at-cove **mint a fresh per-cove identity at
session start and revoke it at exit** — no manual enroll, ephemeral identities.

## Mechanism: shell a sibling `at-harbor`

at-cove resolves an `at-harbor` binary next to its own executable (PATH fallback),
mirroring how `internal/mint` resolves `at-mint` (`mint.atMintBinary`). It runs:
- **`at-harbor enroll --json …`** at session start → mints, prints `{"id","token"}`.
- **`at-harbor revoke --id <id>`** after the session ends → removes the identity.

This **reuses the CLI's operator-auth** (cached `at-harbor login` token /
`AT_HARBOR_ADMIN_TOKEN` / settings-resolved admin-url) and keeps the **at-cove binary
go-oidc-free** — it does *not* import `internal/harbor/adminclient` (which pulls
go-oidc). The minted token rides the child's **stdout**, captured in memory (never on
argv, never logged).

## Scope

**In:**
- **`at-harbor enroll --json`** — a machine-readable output mode: emit
  `{"id":"…","token":"…"}` instead of the human snippet; `--base-url` is not required
  (no snippet rendered). Existing snippet behavior unchanged when `--json` is absent.
- **Mode selection** on COV-138's `harbor:` block: `harbor.identity` **set** →
  pre-supplied token (unchanged; the escape hatch when at-cove can't reach the admin
  API). **Absent** → auto-enroll.
- **Mint at start** — at-cove shells
  `at-harbor enroll --json --id <cove-instance-name> --role guest --destinations anthropic,git --repos <project> --ttl 24h`,
  parses `{id,token}`, and uses `token` as `connect.HarborAuth.Token` (same connector
  injection as COV-138). Defaults are derived, **no new config**:
  - `id` = the cove's instance/container name (stable; one interactive session per
    container, so a reconnect rotates the same id and revoke targets it cleanly).
  - `role` = `guest`; `destinations` = `anthropic,git`; `repos` = the kit's
    source-control project (empty ⇒ omitted ⇒ Anthropic-only); `ttl` = 24h backstop.
- **Revoke at exit** — `harborPlan` returns the auth **plus a revoke closure** (nil for
  the manual path); `doChat` defers `at-harbor revoke --id <id>` after
  `connect.Connect` returns. Best-effort: a failure warns (never masks the session
  outcome); the TTL is the crash backstop.

**Out (deferred):**
- **Configurable enroll scope** (`role`/`destinations`/`repos`/`ttl` knobs) — derived
  defaults only this cut.
- **Teammate + dispatch-worker** auto-enroll — rides on COV-142's connector wiring for
  those paths.
- Auto-enroll for a **non-co-located** harbor whose admin API at-cove can't reach — the
  pre-supplied `harbor.identity` remains the path there.

## Interfaces

```go
// The enroll --json contract is just {"id","token"} — no shared Go type (a shared
// type in internal/harbor would drag go-oidc into at-cove). Each side uses a local
// 2-field struct; the JSON is the contract.

// cmd/at-cove — harborPlan gains an auto-enroll branch and a revoke closure.
// revoke is nil for the pre-supplied (identity-set) path.
func harborPlan(cfg, store, expand, kitName, kitPath, secretsPath, r) (*connect.HarborAuth, func(), error)
```

- `at-harbor enroll --json` prints the JSON to stdout (nothing else); errors go to
  stderr with a non-zero exit, which at-cove surfaces (fail closed — no session
  without an identity).
- at-cove resolves `at-harbor` via a sibling-of-self helper (mirror
  `mint.atMintBinary`), so a `dist/<os-arch>/at-cove` finds its sibling `at-harbor`.

## at-harbor invocation

at-cove passes only the **mint scope** flags + `--json`; it does **not** pass
`--admin-url` or a token — `at-harbor` resolves the admin URL (its `settings.yml` /
default `127.0.0.1:8081`) and operator auth (cached login / `AT_HARBOR_ADMIN_TOKEN`)
itself. So the launching host must have `at-harbor` reachable to harbor's admin API
and an operator credential; otherwise use the pre-supplied `harbor.identity`.

## Error handling

| Situation | Behavior |
|-----------|----------|
| `harbor.identity` set | pre-supplied path (unchanged); no mint, no revoke closure |
| `at-harbor` not found / enroll fails / bad JSON | hard error before the session launches (fail closed) — clear message naming `at-harbor enroll` |
| revoke fails at exit | warn to stderr; never mask the session's own outcome; TTL reclaims the identity |
| enroll `--json` with no `--id` | error (id is required, as today) |

## Testing

Hermetic (`runner.Fake`):
- `harborPlan` auto path: with `harbor.identity` unset, assert the `at-harbor enroll
  --json` argv carries `--id <name> --role guest --destinations anthropic,git
  --repos <project> --ttl 24h`; feed a canned `{"id":"c1","token":"TKN"}` on the
  fake's stdout; assert `HarborAuth.Token == "TKN"` and the token is **not** on argv;
  assert the returned revoke closure shells `at-harbor revoke --id c1`.
- `harborPlan` manual path: `harbor.identity` set → resolves the secret (today),
  revoke closure is nil, no `at-harbor` shelled.
- `cmd/at-harbor`: `enroll --json` prints `{id,token}` and omits the snippet;
  `--base-url` not required with `--json`.
- Regression: no `harbor:` block → no `at-harbor` shelled, auth path unchanged.

Real mint→use→revoke round-trip behind `//go:build integration` / manual.

## Manual verification (definition of done)

1. A `harbor:` kit **without** `identity`, launched where `at-harbor` reaches the admin
   API + an operator is logged in: `at-cove chat` mints an identity (visible via
   `at-harbor destination`/enrollment list), the cove runs `claude` + `git clone`
   through harbor, and on exit the identity is **revoked** (gone from the list).
2. A `harbor:` kit **with** `identity`: behaves exactly as COV-138 (no mint/revoke).
3. No `harbor:` block: unchanged.
