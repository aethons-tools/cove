# jam: the subscription-OAuth account pool — cheap Anthropic credentials for coves

**Status:** design approved (brainstorm complete), pre-plan
**Scope:** replace the per-cove federated (service-account) Anthropic token with a **pool of subscription-OAuth accounts** the broker draws from, so a cove's `claude` runs as a subscription principal (~20× cheaper, and entitled to the models a subscription carries) instead of a federated one.
**Builds on:** the credential-injecting broker (`internal/jam/proxy.go`, `creds.go`), the connector snippet (`internal/jam/snippet`), the cove launch path (`internal/jam/launcher`, `internal/connect/covemaster.go`), and the roster/identity model (actor identity token → `HashToken` → `Lookup`).
**Does not change:** the broker's authenticate → `Decide` → resolve → inject → proxy pipeline, the roster/grant model, `Decide`'s three-question authorization, git routing, or the hardening/egress boundary. The proxy machinery is **reused as-is**; only the *identity ingress header*, the *credential source*, and the *cove seed* change.

## Summary

A managed cove's `claude` reaches Anthropic through Jam's broker: the cove holds a Jam **identity token**, `ANTHROPIC_BASE_URL` points at the broker, and the broker swaps the identity for Jam's **real** downstream Anthropic credential before proxying to `api.anthropic.com`. Today that real credential is a **federated service-account token** (`at-mint anthropic` → `sk-ant-oat01`). Two problems: federated tokens are a **distinct principal** from a personal subscription (so a cove was **not entitled to `opus-5-5`**), and they **cost ~20× more per token**.

This design swaps the federated credential for a **pool of Anthropic subscription-OAuth accounts** — real `claude` logins whose access/refresh tokens live in a secure central store (a file, for now). The broker binds each cove **identity → one pool account for the cove's life**, injects that account's **current** access token, and **owns token refresh** in the background. To the cove, nothing about "how I talk to Anthropic" changes except that its seeded `.credentials.json` now carries a **well-known dummy subscription token** instead of an API key.

The change is small because a **feasibility probe** (below) showed subscription-mode `claude` already does exactly what the broker needs.

## The probe (what subscription-mode `claude` actually does)

Ran `claude -p` in an isolated `CLAUDE_CONFIG_DIR` with a **dummy** `claudeAiOauth` credential (far-future expiry, fabricated tokens) and `ANTHROPIC_BASE_URL` pointed at a local logging server. Findings:

1. **It honors `ANTHROPIC_BASE_URL`.** Subscription mode is *not* hardwired to `api.anthropic.com`; traffic went to the local server as `POST /v1/messages?beta=true`.
2. **It authenticates with `Authorization: Bearer <accessToken-from-.credentials.json>`** — the exact dummy value — and sends **no `x-api-key`**.
3. It carries the subscription markers `anthropic-beta: …,oauth-2025-04-20,…` and `anthropic-version: 2023-06-01`, plus `x-app: cli`.

Consequence: subscription mode **occupies `Authorization`** with the OAuth bearer. So the cove's Jam identity — which today rides on `x-api-key` — can no longer travel there. The resolution (below) makes the *dummy bearer itself* the identity token, which reuses the whole existing broker auth path.

## Decisions (from brainstorm)

- **Credential model: subscription-OAuth pool, not federated tokens.** Cost (~20×) and model entitlement drive it. Federated minting (`at-mint anthropic`) stays in the tree but is no longer the cove credential path.
- **Mapping: by the cove's identity.** The account a cove uses is chosen by its **actor identity**, not per-request.
- **Binding lifetime: one account for the cove's life.** Once a cove's identity is bound to a pool account, it keeps that account until teardown — for rate-limit locality and refresh continuity. An account may back more than one cove, but a subscription carries its own rate limits, so **pool size ≈ target cove concurrency**.
- **Refresh: broker-owned.** The seeded dummy has a far-future expiry, so the cove's `claude` **never refreshes**. The broker refreshes each account's real token out-of-band, ahead of expiry, and writes the new tokens back to the store. The cove never sees a refresh round-trip.
- **Single path for all coves.** The one `anthropic` broker destination flips from `IdentityIn: x-api-key` to `IdentityIn: bearer`; **every** cove goes through the pool. No parallel federated/api-key destination is kept. (`at-mint` federated tokens remain usable out-of-band, just not as the cove path.)
- **Store: a file, for now.** Same posture as the rest of Jam's control plane during its file→Postgres migration; the pool store is defined behind an interface so a Postgres backend drops in later.

## How identity reaches the broker (the key mechanic)

Today (`internal/jam/snippet`): `snippet.Render` sets `ANTHROPIC_API_KEY=<identity-token>`; API-key-mode `claude` puts it on `x-api-key`; the `anthropic` destination is `IdentityIn: x-api-key`; `presentedToken(ApplyXAPIKey)` reads it; `Lookup(HashToken(tok))` finds the actor.

After: the cove is seeded with a dummy `.credentials.json` whose **`claudeAiOauth.accessToken` = the cove's Jam identity token**. Subscription `claude` sends it as `Authorization: Bearer <identity-token>`. The `anthropic` destination becomes `IdentityIn: bearer`; `presentedToken(ApplyBearer)` reads the bearer; **everything downstream is unchanged** — same `HashToken` → `Lookup` → `Decide` → `applyCred`.

The broker's `Director` already `Header.Del("Authorization")` (dropping the inbound identity) and then `applyCred(ApplyBearer, realToken)` sets `Authorization: Bearer <real subscription access token>`. The reverse proxy passes through untouched headers, so `anthropic-beta: oauth-2025-04-20` and `anthropic-version` survive. **The request that lands on `api.anthropic.com` is byte-for-byte a genuine subscription request** with the pool account's live bearer swapped in — which is what makes the cove a subscription principal (and model-entitled accordingly).

```
cove claude ──Authorization: Bearer <identity-token>──▶ broker /anthropic
                                                          │  presentedToken(bearer) = identity
                                                          │  Lookup(HashToken) → actor
                                                          │  Decide(...) → NeedCred, CredName
                                                          │  pool.Resolve(actor) → real access token
                                                          ▼
   api.anthropic.com ◀──Authorization: Bearer <real> + anthropic-beta: oauth-…──┘
```

## Architecture (delta from today)

Four pieces; only the first three are new, and none touches `proxy.go`'s pipeline.

### 1. The pool store (`internal/jam`, file-backed behind an interface)

An account definition:

```
PoolAccount {
  Name         string   // stable id for the account (e.g. "pool-a")
  AccessToken  string   // current real subscription access token
  RefreshToken string   // current real refresh token
  ExpiresAt    time.Time
}
```

Plus a **binding** table: `actorIdentityHash → accountName`, assigned on first use and stable for the cove's life. The store defines:

- `Account(name) (PoolAccount, bool)` / `SetAccount(PoolAccount)` — read/write a definition (the refresher writes here).
- `BindingFor(identityHash) (accountName string, ok bool)` / `Bind(identityHash, accountName)` — the identity→account map, assigned once.
- account selection for an unbound identity: pick the account with the fewest current bindings (even spread), bind it, return it.

Concurrency: the store is written by the refresher (token rotation) **and** by request handling (first-use binding). Guard with a mutex; persist atomically (write-temp-rename), matching the existing file-store discipline. **Never log token values.**

### 2. The pool `CredResolver` (slots beside `SecretResolver`)

`proxy.go` calls `b.creds.Resolve(dec.CredName)`. Today that's `SecretResolver` (runs a `secret.Spec`). But `dec.CredName` comes from **static destination config** (`Decide` returns `dest.CredName` verbatim) — it can't carry the per-request identity. And the anthropic pool cred *depends on which account the identity is bound to*. So the resolver needs the identity, which `Resolve(name string)` doesn't provide. Options considered:

- **(chosen)** an **optional capability interface** the broker prefers when present:
  ```go
  // IdentityCredResolver resolves a credential scoped to the requesting identity
  // (the pool: the token depends on the identity's bound account).
  type IdentityCredResolver interface {
      ResolveFor(name, identityHash string) (string, error)
  }
  ```
  In `proxy.go`, when `dec.NeedCred`: if `b.creds` implements `IdentityCredResolver`, call `ResolveFor(dec.CredName, HashToken(tok))` (the hash is already computed for `Lookup`); else `Resolve(dec.CredName)`. Purely additive — no `Decide` change, no `Destination` change, and `SecretResolver` (which doesn't implement it) is unaffected.
- (rejected) set `CredName` to the identity hash — `CredName` is static config, not per-request, so this can't work.
- (rejected) widen `CredResolver` itself to `Resolve(name, actor)` — forces every resolver to change; the capability interface is strictly additive.

Because git and anthropic share **one** broker resolver, a small **`ChainResolver`** composes them: it implements `IdentityCredResolver`, routes the configured pool cred name to the `Pool` (via `ResolveFor`), and delegates every other name to a base `SecretResolver` (identity ignored). So git PATs resolve exactly as today; only the pool cred is identity-scoped.

Resolution returns the account's **`AccessToken` as it currently stands in the store** — it never triggers a refresh inline (refresh is the background loop's job). If the bound account's token is somehow expired at request time (refresher lagging), that's a `BadGateway` the same as any resolve failure; the refresher's safety margin (below) should prevent it.

### 3. The broker-owned refresher (`internal/jam`, background goroutine)

A loop started by `at-jam serve` when a pool is configured:

- Every `refresh-interval` (default 5m), for each account, if `ExpiresAt` is within a `refresh-margin` (default 15m), refresh it.
- Refresh = `POST` to Anthropic's subscription OAuth **token endpoint** with `grant_type=refresh_token`, the account's `RefreshToken`, and the public `claude` **client_id**. The response's new `{access_token, refresh_token, expires_in}` is written back via `store.SetAccount`.
- **Runs on the host, out-of-band from any cove.** The refresh call is Jam's own egress, not a cove's; it does not traverse the broker.

> **To pin during implementation:** the exact token-endpoint URL and the public client_id used by `claude auth login --claudeai`. Confirm with a second probe — seed a dummy with a **past** `expiresAt` and capture the refresh request `claude` emits (method, URL, body, client_id) — before writing the refresher. Recorded here so the plan carries it as an explicit task, not an assumption.

Seeding the pool (bootstrap): an account definition is captured **once** from a real `claude` login — an operator runs `claude auth login --claudeai` on a trusted machine and hands Jam the resulting `claudeAiOauth` block (access + refresh + expiry). An admin verb (`at-jam pool add --name <n>`, reading the token block from stdin/file, never argv) writes it into the store. This is out-of-scope-detailed here; the plan defines the exact verb.

### 4. The cove seed (`internal/jam/snippet` + launch path)

- `snippet` grows a subscription variant: instead of `ANTHROPIC_API_KEY=<token>`, the cove is seeded with a **dummy `.credentials.json`** at `CLAUDE_CONFIG_DIR` containing:
  ```json
  {"claudeAiOauth":{"accessToken":"<identity-token>","refreshToken":"jam-dummy-refresh",
    "expiresAt":<far-future-ms>,"refreshTokenExpiresAt":<far-future-ms>,
    "scopes":["user:inference","user:profile"],"subscriptionType":"pro","rateLimitTier":"default"}}
  ```
  `accessToken` **is** the identity token (that's what reaches the broker as the bearer). `ANTHROPIC_BASE_URL` still points at `<jam>/anthropic`. `ANTHROPIC_API_KEY` is **no longer set** (it would force API-key mode and defeat the whole scheme).
- The launch path (`connect.LaunchCoveMaster` / the launcher) writes the dummy credentials file into the cove over the existing in-memory SSH-stdin channel — never argv, never host disk — the same way the env is staged today. The far-future expiry guarantees the cove's `claude` never attempts a refresh (which would hit the broker with a refresh request the broker isn't designed to answer).
- The `anthropic` destination config flips `IdentityIn` to `bearer` and marks its cred as **identity-scoped pool** (see §2).

## Security properties (preserved)

- **Real tokens never reach a cove or its disk.** The cove holds only the dummy (whose `accessToken` is its own identity token — already known to it). The broker injects the real bearer in-memory on the proxy hop, exactly as it injects the federated token today.
- **Secrets never hit logs or argv.** The pool store, resolver, and refresher follow the existing rule: token values are never logged at any level; the dummy seed goes in over SSH stdin, not argv.
- **Authorization is unchanged.** `Decide`'s three questions still gate every request; a cove with no valid grant is still denied *before* any credential is resolved. Moving identity from `x-api-key` to `bearer` changes only *where the token is read*, not *whether it's authorized*.
- **Egress boundary intact.** Coves still reach only `<jam>/anthropic` (443, via squid). The refresher's OAuth calls are **Jam's** egress on the host, added to Jam's own allow-list — not a cove's.
- **Blast radius of a leaked identity token:** unchanged in kind (it was always the cove's bearer to the broker); it still authorizes only what the actor's grants allow, and it is **not** the real Anthropic token.

## What this does not solve (deferred)

- **Rate-limit exhaustion / fair-share across coves on one account.** Pool size ≈ concurrency is the blunt mitigation; smarter scheduling (least-loaded live account, backpressure on 429) is later work.
- **Refresh-token rotation failure recovery.** If a refresh fails (revoked login, network), the account goes stale; slice-1 logs it and the account's coves fail `BadGateway`. Health-marking a dead account and skipping it in selection is a follow-up.
- **Postgres pool store.** File first; the interface admits a `pool_pg` backend alongside the control-plane migration.
- **Cove `claude` self-refresh path.** Explicitly avoided by far-future dummy expiry; if a future `claude` ignores the expiry and refreshes anyway, the broker would need a `/anthropic` refresh-endpoint handler — noted, not built.

## Testing (hermetic, per the repo's TDD split)

- `snippet`: golden test that the subscription variant emits the dummy `.credentials.json` with `accessToken == identityToken`, far-future expiry, and **no** `ANTHROPIC_API_KEY`.
- pool store: bind-once stability, even-spread selection, atomic persist, concurrent bind + refresh under `-race`.
- `PoolResolver`: identity-keyed resolution returns the bound account's current token; rebinding never happens for a known identity.
- refresher: with a fake clock and a fake token endpoint (`runner`/HTTP stub), refreshes only within margin, writes back new tokens, never logs a value.
- broker: an integration-style test (existing broker test harness) that a bearer-identity request resolves the pool token and the outbound request carries `Authorization: Bearer <real>` + preserved `anthropic-beta`. Drive the token endpoint and store with fakes — no live VM, no network.

## Rollout

Additive and flag-gated: a `pool:` serve-config block enables the pool path; with it absent, the anthropic destination keeps `x-api-key` + the existing resolver (federated), so nothing regresses until an operator seeds a pool and flips the destination. The cove seed change is chosen by the same config, so old and new coves don't mix mid-rollout.

---

*Design rationale grounded in the 2026-09-29 subscription-mode probe (base-URL honored; `Authorization: Bearer`; no `x-api-key`; `oauth-2025-04-20` beta) and the current broker in `internal/jam/proxy.go` + `creds.go` + `snippet`.*
