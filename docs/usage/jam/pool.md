---
summary: The subscription-OAuth account pool — running cove `claude` as a pooled subscription principal instead of a federated service-account token, how the broker binds identity→account and injects the real bearer, the `at-jam pool` verb, broker-owned token refresh, and the egress/rollout notes.
read_when: You are enabling or operating the subscription account pool — seeding accounts, flipping the anthropic destination to bearer, sizing the pool, or reasoning about token refresh and the egress it needs.
owns: the subscription account pool — the `pool:` behavior, the identity→account binding + bearer injection, the `at-jam pool` verb, the broker-owned refresher (endpoint + egress), and the pool rollout
prereqs: serve.md for the `pool:` config block + the broker model and `destination` verb; coves.md for how a raised cove is credentialed
tier: leaf
updated: 2026-09-29
---

# The subscription account pool

By default the `anthropic` [destination](serve.md#destinations) injects a single
configured credential (e.g. an `at-mint anthropic` federated service-account
token). Those tokens are a **distinct principal** from a personal subscription —
not entitled to the same models — and cost markedly more per token. The
**subscription account pool** runs coves as a *subscription* principal instead,
drawing from a pool of real `claude` subscription logins. It is enabled by the
[`pool:`](serve.md#the-serve-config) serve-config block.

## How it works

With a `pool:` block, the anthropic destination is set to carry the identity on
**`Authorization: Bearer`** (`--identity-in bearer`, `--apply bearer`,
`--cred-name` = the pool's `cred-name`). Each raised cove is seeded a well-known
**dummy** `claudeAiOauth` credentials file whose `accessToken` **is** the cove's
Jam identity token (far-future expiry, so the cove's `claude` never tries to
refresh). Subscription-mode `claude` sends that identity as the bearer; the
broker authenticates it exactly as before (`HashToken` → actor → `Decide` — see
the [broker model](serve.md#the-broker-model)), then resolves the credential from
the pool: it binds the identity to a pool account **for the cove's life**
(spreading new identities across the least-loaded accounts) and injects that
account's **current** access token. `claude`'s `anthropic-beta: oauth-2025-04-20`
header is preserved, so `api.anthropic.com` sees a genuine subscription request.

Because a subscription carries its own rate limits, **size the pool to your cove
concurrency** — one account can back several coves but will throttle.

The launcher chooses the mode; the cove is unaware. See
[coves.md](coves.md#raising-a-real-managed-studio) for where credentialing lands
in the raise sequence.

## Seeding accounts (`at-jam pool`)

Capture a real login once (`claude auth login` on a trusted machine → its
`.credentials.json`) and add it to the host-side pool store:

```
at-jam pool add  --store /var/lib/jam/pool.json --name pool-a --from-file ./credentials.json
at-jam pool list --store /var/lib/jam/pool.json    # names + expiries, never tokens
```

`pool add`/`list` write the host-side store file **directly** (not via the admin
API); `--from-file` defaults to stdin. Then point the anthropic destination at the
pool:

```
at-jam destination add --name anthropic --route /anthropic/ --upstream https://api.anthropic.com \
  --identity-in bearer --cred-name anthropic-sub --apply bearer
```

## Broker-owned refresh

A background refresher rotates each account's token ahead of expiry (within
`refresh-margin`, on the `refresh-interval` cadence) by
`POST https://platform.claude.com/v1/oauth/token` (`grant_type=refresh_token`,
JSON body). This is **Jam's own host egress**, not a cove path — so if `at-jam
serve` runs inside a hardened sandbox, add `platform.claude.com` to **Jam's**
egress allow-list. Token values are never logged; a refresh failure logs the
endpoint's OAuth `error`/`error_description` (e.g. `invalid_grant`) so it is
diagnosable without exposing secrets.

## Rollout

The `pool:` block gates everything: with it absent, the anthropic destination
keeps its configured (`x-api-key`/federated) credential and coves launch in
API-key mode — nothing changes until you seed a pool and flip the destination.

Design rationale + the probes behind it live in
[`../../superpowers/specs/2026-09-29-subscription-oauth-account-pool-design.md`](../../superpowers/specs/2026-09-29-subscription-oauth-account-pool-design.md).
