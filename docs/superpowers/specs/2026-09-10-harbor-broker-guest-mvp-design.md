---
kind: design-spec
subject: harbor slice #1 — credential broker + identity/enrollment, serving Guests (localhost walking skeleton)
status: draft
date: 2026-09-10
prereq: 2026-09-10-harbor-design.md (the overarching harbor design; read its Core model + Security first)
read-when: implementing or reviewing harbor's first slice — the Anthropic+git connector broker, identity, and `at-harbor enroll`
---

# Harbor slice #1 — credential broker + enrollment, serving Guests

The [walking skeleton](2026-09-10-harbor-design.md#decomposition--sequencing) of harbor: a Go service that
brokers **Anthropic** and **git** credentials to an **enrolled Guest** cove over real client-addressed TLS,
so the downstream secrets live in harbor and **never in the cove**. Smallest slice that proves the keystone
(the broker) and delivers real value (secrets off the box), with **no remote compute** — coves stay local.

## Scope

**In:**
- `at-harbor serve` — the broker service: two connector endpoints + identity auth.
  - **`/anthropic/*`** — reverse-proxy to `api.anthropic.com`, swapping the caller's identity for the real
    Anthropic bearer.
  - **`/git/*`** — git-over-HTTPS reverse-proxy to the code host, swapping identity for the real git PAT.
- **Identity + `at-harbor enroll` / `at-harbor revoke`** — mint/record/revoke a scoped bearer bound to a
  Project/Role, with a per-identity connector allow-list.
- Real TLS on a test domain (`harbor.local.aethons.tools`), containerized (docker/colima), reachable from a
  local Guest cove.

**Out (deferred, tracked in the overarching spec):**
- The **messaging MCP** (belongs to the Manager/conductor slice #4).
- The **filtering forward-proxy** for non-brokered hosts (PyPI/apt) — a Guest keeps its own cove squid for
  everything it does *not* route through harbor; harbor here offers *connectors only*, it is not the cove's
  whole egress path (that is the managed-cove concern in slice #3).
- Managed lifecycle, wake/suspend, Fly, the kit registry, and the UI (later slices).
- mTLS client identity (see Open questions) — MVP uses bearer tokens.

## Run model (localhost walking skeleton)

- Harbor runs as a **container** (docker/colima). Chosen path (b): localhost first, but exercised through a
  **real domain + real TLS**, so the addressing/cert model is the true one and the later lift to a shared
  host is only a DNS-target change, not a reshape.
- **DNS:** `/etc/hosts` maps `harbor.local.aethons.tools → 127.0.0.1` on the operator's host.
- **TLS:** harbor serves a cert for `harbor.local.aethons.tools`. Preferred: a real **DNS-01** cert for
  `*.local.aethons.tools` — then there is **no CA to install in the cove** (matches the "no MITM CA"
  property; the client legitimately addresses harbor). A local dev CA (mkcert-style) is the fallback. Cert +
  key paths come from harbor config.
- **Cove → harbor reachability:** inside a Colima/docker cove, `127.0.0.1` is the *cove's* loopback, so the
  cove must resolve `harbor.local.aethons.tools` to the **host** (`host.docker.internal` / docker gateway),
  and that hop must be permitted by the cove's own network setup. A concrete MVP networking detail; tractable.

## Identity & enrollment

**One scoped bearer per identity, presented through each connector's natural auth slot, swapped by harbor.**
This is the crux and it's clean because the identity token *doubles* as the credential the client already
knows how to send:

- **Anthropic:** the cove sets `ANTHROPIC_BASE_URL=https://harbor.local.aethons.tools/anthropic` and
  `ANTHROPIC_AUTH_TOKEN=<identity-token>`. Harbor reads the `Authorization: Bearer <identity-token>`,
  authenticates the actor, **replaces** it with the real Anthropic bearer, forwards to `api.anthropic.com`.
- **Git:** the cove's remote is `https://harbor.local.aethons.tools/git/<owner>/<repo>` (via
  `git config url.…​.insteadOf` or the remote directly), and its credential helper/askpass supplies the
  identity token as the git **password**. Harbor authenticates, **replaces** it with the real git PAT, proxies
  to the code host.

So the cove holds **only its identity token** — never the Anthropic bearer, never the git PAT. Harbor injects
both at the boundary. This is the SVID-shaped "one minimal secret in the cove" from the overarching design,
realized as a bearer for the MVP.

**`at-harbor enroll`** — mints a random high-entropy bearer, records `{token-hash, project, role, connectors
allow-list (anthropic; git + allowed owners/repos), created, expiry}` in the store, and prints:
- the token (once),
- the connector base URLs,
- a ready-to-source **env + gitconfig snippet** for the Guest cove (`ANTHROPIC_BASE_URL`,
  `ANTHROPIC_AUTH_TOKEN`, the `insteadOf` line, an askpass shim).

**`at-harbor revoke <id>`** — removes/expires the record; the next request fails closed.

Harbor stores only the **hash** of the token (never the raw token on disk or in logs), consistent with the
repo's secrets-never-hit-logs invariant.

## Harbor's own downstream credentials

Harbor holds the **real** Anthropic bearer and git PAT and supplies them host-side, resolved into memory —
reusing at-cove's demand/supply mental model with **harbor as the supply side** (conceptually `internal/secret`
resolution). For MVP: harbor config names a resolver command (or references the existing `at-mint`) per
downstream credential; values are resolved at startup / just-in-time and held in memory only. They are never
written to disk, argv, or logs, and never appear in any proxied response error surfaced to the caller.

## The proxy is general — a three-question pipeline

Harbor is **not** bespoke per-service code (an "Anthropic handler", a "git handler"). It is **one general
credential-injecting proxy** whose per-request pipeline answers three questions, all resolved from
configuration (secrets + `at-mint`), so a new destination is *config, not code*:

1. **Can this caller reach the requested destination?** (identity authenticated → destination in its
   allow-list → for a repo destination, the `<owner>/<repo>` is permitted). No → fail closed.
2. **Does the call require credentials?** Some destinations need injection, some are plain passthrough.
3. **Which credentials, and how are they applied to the request?** The destination policy names the credential
   (resolved via secrets / `at-mint`) and its **application method** (bearer `Authorization` swap; git basic-auth
   password; etc.).

Then swap-and-forward, or forward as-is. **Anthropic and git are simply the two configured destinations** for
this slice (routes `/anthropic/*` and `/git/<owner>/<repo>/*`); the engine has no service-specific branches.
`at-mint` + the secrets configuration are what **scope security within the destination** (which credential,
which scope, how minted). Any failure is fail-closed with a structured, secret-free error, mirroring
at-cove's worker fail-closed posture.

> MVP configures only the two **credential-requiring** destinations. The general no-credential passthrough
> (question 2 = "no") is supported by the engine but is **not** wired as the cove's whole egress path — that
> filtering-forward-proxy role stays deferred (managed-cove concern, slice #3).

## Architecture

New binary `cmd/at-harbor`; logic under `internal/harbor/`, preserving the repo's **pure-plan / execution**
split so the interesting logic is hermetically testable:

```
cmd/at-harbor/            entry: parse argv, load config, `serve` / `enroll` / `revoke`
internal/harbor/          service wiring (http.Server, TLS, destination route table)
internal/harbor/proxy/    the general pipeline (pure): given (identity, requested destination, request) →
                          a decision {reach? cred? which cred + how to apply} → the rewritten upstream request
internal/harbor/policy/   destination policy: per-destination {reachable-by, credential-ref, application-method};
                          resolves creds via secrets / at-mint. Anthropic + git are config rows here.
internal/harbor/identity/ identity store (interface) + file-backed impl; enroll/revoke; token hashing
```

- **Pure & unit-tested:** the three-question decision + request rewrite (identity + destination + request →
  reach/cred/apply → upstream request), token hashing/verification, and the enroll env-snippet renderer
  (golden output). A new destination is exercised by adding a policy row in a test, not new code.
- **Behind seams:** the upstream HTTP transport (tested against an `httptest` fake `api.anthropic.com` /
  code host), the identity store (in-memory fake), and the credential resolver (fake). No network, no real
  Anthropic/GitHub, no Docker in unit tests.
- **Store (MVP):** file-backed (JSON) — harbor is single-node localhost here. SQLite/Postgres is the scale
  path, deferred to the control-plane slice #2.
- **Logging:** structured via the existing `internal/logging` posture; injection paths (resolved cred
  values, identity tokens) are never logged at any level.

## Definition of done

A local Colima cove, enrolled via `at-harbor enroll` and sourcing the printed snippet, can — with **no
Anthropic key and no git PAT anywhere in the cove**:

1. Run `claude` against `https://harbor.local.aethons.tools/anthropic` (harbor injects the bearer), and
2. `git clone` **and** `git push` a **private** repo through `https://harbor.local.aethons.tools/git/…`
   (harbor injects the PAT),

over real TLS on the test domain, with harbor holding both real credentials and **no secret value appearing
in the cove or in harbor's logs**. A revoked identity fails both, closed.

Hermetic unit tests cover the rewrite/authz/enroll logic; a real-TLS + real-cove round-trip lives behind the
`integration` build tag.

## Open questions

- **mTLS upgrade path** — bearer for MVP; when/how to move Guest identity to an mTLS client cert (SVID). The
  connector clients (Claude Code, git) make bearer-in-natural-slot far easier than client certs today.
- **Harbor credential supply/rotation** — exact config shape for harbor's real Anthropic/git creds; reuse
  `at-mint` directly?
- ~~Git connector depth~~ → **resolved:** no bespoke git handler. The general three-question pipeline treats
  git as a configured destination (smart-HTTP passthrough with a basic-auth password swap); LFS/edge cases are
  just further policy/passthrough, not new code. Verify clone+push+LFS in the integration test.
- ~~Cove-side ergonomics~~ → **resolved:** `enroll` emits a complete, **env-only** git credential — the
  token is exported once as `HARBOR_IDENTITY_TOKEN`, and a `!`-prefixed git credential helper reads it at run
  time, so the token never lands in gitconfig on disk and `git clone` works headlessly (no prompt).
  `ANTHROPIC_AUTH_TOKEN` references the same env var.
- **Multi-tenant store & concurrency** — file-backed is fine single-node; the move to a real DB is slice #2.

## Post-MVP findings (from the DoD dry run)

The harbor side of the DoD was rehearsed end-to-end (self-signed cert, real TLS, live `serve`/`enroll`/`revoke`,
brokered request against a local upstream). All checks passed (credential swap, 401 on unknown, 403 on
out-of-scope repo, 401 after revoke, no secrets in logs). Three operational items surfaced:

- **No live store reload (follow-up).** `serve` loads the identity store once at startup, so `enroll`/`revoke`
  only take effect after a `serve` restart. Acceptable for the MVP; fix by reload-on-change (fsnotify or a
  cheap mtime re-read) — or it dissolves when the store becomes a DB (slice #2). *Tracked as a follow-up.*
- **Cove must exclude harbor's host from its egress proxy.** A cove routes its own traffic through squid; it
  must keep harbor's endpoint in `no_proxy` (else the cove's proxy `CONNECT`s to harbor and is denied). Document
  in the cove-side usage.
- **Cove→harbor DNS.** Inside a Colima/docker cove, `harbor.local.aethons.tools` must resolve to the **host**
  (`host.docker.internal` / docker gateway), not the cove's loopback. Document alongside the `no_proxy` note.
