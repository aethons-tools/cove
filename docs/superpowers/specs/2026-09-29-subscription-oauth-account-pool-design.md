# jam: the subscription-OAuth account pool — cheap Anthropic credentials for coves

**Status:** shipped (#249 and follow-ups) — **revised 2026-09-29(b): see "Revision B" at the end.** The dummy-`.credentials.json` identity hop is superseded by `ANTHROPIC_AUTH_TOKEN`.
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

#### Refresh endpoint (probed 2026-09-29)

Captured the real refresh request by MITM'ing `claude`'s egress (a local CONNECT proxy presenting a `platform.claude.com` leaf cert trusted via `NODE_EXTRA_CA_CERTS`) with a **past-expiry** dummy credential:

- **Endpoint:** `POST https://platform.claude.com/v1/oauth/token` — a **fixed host**, *not* `ANTHROPIC_BASE_URL`. The refresher dials it directly (Jam host egress), so **`platform.claude.com` must be on Jam's own egress allow-list** — it is not a cove path.
- **Encoding:** **JSON** — `Content-Type: application/json` (not form-urlencoded).
- **Body:**
  ```json
  {"grant_type":"refresh_token",
   "refresh_token":"<the account's refresh token>",
   "client_id":"9d1c250a-e61b-44d9-88ed-5944d1962f5e",
   "scope":"user:profile user:inference user:sessions:claude_code user:mcp_servers user:file_upload user:plugins"}
  ```
  `client_id` is the public Claude Code OAuth client (confirmed present in the `claude` bundle beside the endpoint). The `scope` string is sent verbatim by `claude`; the refresher sends the same.
- **Response:** standard OAuth JSON `{access_token, refresh_token, expires_in}` (`claude` accepted a canned reply of that shape and proceeded).

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

---

# Revision B (2026-09-29): the `ANTHROPIC_AUTH_TOKEN` identity hop

The original design seeded the cove a **dummy `claudeAiOauth` `.credentials.json`** (subscription mode) whose `accessToken` was the cove's Jam identity, so `claude` would send it as `Authorization: Bearer` to the broker. Live bring-up proved that mechanism **fragile and self-destructing**, and this revision replaces it.

## Why the dummy-subscription hop failed (observed)

In subscription mode `claude` **manages the OAuth session**: it validates and refreshes the `claudeAiOauth` token, and **on any `401` it tries to refresh and then blanks its own credentials** (`accessToken:""`, `expiresAt:0`). Observed end-to-end in a real cove:

1. broker proxied the initial inference (`broker proxy destination=anthropic` — identity accepted, pool cred resolved), so the whole broker path works;
2. `api.anthropic.com` returned `401` (stale/rotated pool token — see the account-exclusivity note below);
3. `claude` treated that as *its* session expiring, tried to refresh against `platform.claude.com` (a **hardcoded** host, not `ANTHROPIC_BASE_URL`), failed, and **wiped the dummy creds** — so every subsequent (woken) turn failed locally with `OAuth session expired and could not be refreshed`, never reaching the broker again.

The initial-far-future-expiry dummy masked this in the isolated probe (no `401` there); in a real cove the first `401` permanently breaks the cove's credential. The approach is inherently fragile.

## The replacement: `ANTHROPIC_AUTH_TOKEN` (probed 2026-09-29)

`ANTHROPIC_AUTH_TOKEN` makes `claude` send `Authorization: Bearer <value>` as a **pure static token** — no `.credentials.json`, no OAuth session, no validation, no refresh. Two probes confirmed, with **no** credentials file:

- With `ANTHROPIC_AUTH_TOKEN=<id>` + `ANTHROPIC_BASE_URL=<logger>`, `claude` sent `POST /v1/messages?beta=true` with `Authorization: Bearer <id>` **and touched no other host** — no `platform.claude.com`, no `api.anthropic.com`, no `claude.ai`. So **no self-destruct is possible** (there is no session to invalidate).
- It **does not** send the `oauth-2025-04-20` beta (subscription mode did); its `anthropic-beta` is the `claude-code-…` set only.
- It survives `forceLoginMethod: claudeai` (user-level `settings.json`): still a static bearer, still no OAuth hosts. *(To re-confirm against the cove's system `/etc/claude-code/managed-settings.json`; env auth is normally the top override.)*

## Revised design (supersedes the original "cove seed" + "identity ingress")

- **Cove connector (`internal/jam/snippet.RenderSubscription`):** export **`ANTHROPIC_AUTH_TOKEN=$AT_JAM_IDENTITY_TOKEN`** (plus `ANTHROPIC_BASE_URL` + the identity + git routing). **No `.credentials.json`, no `ANTHROPIC_API_KEY`.** `internal/jam/snippet.DummyCredentials` and the cove-side creds write in `internal/connect/covemaster.go` are **removed**.
- **Broker destination:** stays `identity_in: bearer` — the bearer slot already matches, **no flip**. The identity arrives on `Authorization: Bearer`, `Decide`/pool-resolve are unchanged.
- **Broker inject (new):** on the pool path, after swapping the identity for the pool subscription bearer, **append `oauth-2025-04-20` to the `anthropic-beta` header** (merged with `claude`'s existing betas) — because `AUTH_TOKEN` mode doesn't send it and Anthropic requires it for a subscription-OAuth token. Gated per-destination (a Destination flag), so only the pool anthropic destination adds it.
- **Cove egress (hardening):** a brokered cove should reach **only the jam host**. Today `.anthropic.com`/`.claude.com`/`claude.ai` live in the *sealed base* `allowed_domains.txt` (allowed first, unconditional — a role egress policy cannot remove them). Move direct-Anthropic **out of the always-on base** and make it **opt-in for the interactive `claude auth login` path** (kit `image.allowed-domains`), so a brokered cove is locked to the broker. **Blast radius:** interactive/collaborator coves must keep direct Anthropic via their kit — handle as its own task. This is defense-in-depth: in this model the cove holds **no real Anthropic credential** (only the fake identity), so the direct-Anthropic path leaks nothing today; the lock guarantees no bypass.

## Account exclusivity (carried forward, made explicit)

A pool account must be a **grant nothing else holds**. Subscription refresh tokens rotate per use and providers revoke the whole token family on detecting reuse of a rotated token — so copying an *active* interactive login into the pool guarantees an eventual `invalid_grant`. Each pool account needs its own dedicated `claude auth login`, not shared with any interactive session. (Same account is fine; the *grant* must be exclusive.)

## Slices (this revision)

- **A — snippet + cove seed:** `RenderSubscription` sets `ANTHROPIC_AUTH_TOKEN`; drop `DummyCredentials` + the creds write. *(implemented in this change)*
- **B — broker appends the `oauth` beta** on the pool destination (a Destination flag). *(implemented in this change)*
- **C — egress lock** for brokered coves (hardening restructure; interactive-login as a sub-task). *(ticketed — a security-boundary change, done deliberately)*
- **D — in-cove `forceLoginMethod` confirmation** against the real `managed-settings.json`. *(ticketed — live verification)*

Slices A+B make the pool **work** (no self-destruct); C is the security hardening; D is a verification.
