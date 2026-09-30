---
summary: The subscription-OAuth account pool — running cove `claude` as a pooled subscription principal instead of a federated service-account token, how the broker binds identity→account and injects the real bearer, the `at-jam pool` verb, broker-owned token refresh, and the egress/rollout notes.
read_when: You are enabling or operating the subscription account pool — seeding accounts, flipping the anthropic destination to bearer, sizing the pool, or reasoning about token refresh and the egress it needs.
owns: the subscription account pool — the `pool:` behavior, the identity→account binding + bearer injection, the `at-jam pool` verb, the broker-owned refresher (endpoint + egress), and the pool rollout
prereqs: serve.md for the `pool:` config block + the broker model and `destination` verb; coves.md for how a raised cove is credentialed
tier: leaf
updated: 2026-09-30
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

With a `pool:` block, each raised cove is seeded **`ANTHROPIC_AUTH_TOKEN` = its
Jam identity token** (via the connector snippet — no `.credentials.json`, no
`ANTHROPIC_API_KEY`). `claude` sends that identity as a **static
`Authorization: Bearer`** with **no OAuth session** — it never validates,
refreshes, or contacts `platform.claude.com`, so a `401` can't make it
self-destruct its own credentials (the failure mode of the earlier dummy-
`claudeAiOauth` approach — see the spec's Revision B).

The anthropic destination carries the identity on the bearer
(`--identity-in bearer --apply bearer`, `--cred-name` = the pool's `cred-name`,
`--oauth-beta`). The broker authenticates the identity exactly as before
(`HashToken` → actor → `Decide` — see the [broker model](serve.md#the-broker-model)),
resolves the credential from the pool — binding the identity to a pool account
**for the cove's life** (spreading new identities across the least-loaded
accounts) and injecting that account's **current** access token — and, because
`AUTH_TOKEN` mode does **not** send it, **adds the `oauth-2025-04-20` beta** to
the forwarded `anthropic-beta` header (that's what `--oauth-beta` does). So
`api.anthropic.com` sees a genuine subscription request with the pool account's
real bearer.

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
  --identity-in bearer --cred-name anthropic-sub --apply bearer --oauth-beta
```

The pool's `cred-name` does **not** need a `credentials:` entry — with a `pool:`
block configured, destination validation accepts it and the pool resolves it by
identity. `--oauth-beta` makes the broker add the `oauth-2025-04-20`
`anthropic-beta` on forwarded requests, which Anthropic requires to accept a
subscription-OAuth token (a cove on `ANTHROPIC_AUTH_TOKEN` doesn't send it).

**Account exclusivity.** A pool account must be a `claude auth login` **grant
nothing else holds.** Subscription refresh tokens rotate on every use, and reusing
a rotated token trips the provider's theft defense and revokes the whole grant —
so copying an *active* interactive login into the pool guarantees an eventual
`invalid_grant`. Give each pool account its own dedicated login (same Anthropic
account is fine; the *grant* must be exclusive), and don't use that login
interactively elsewhere.

> **Egress lock (COV-208):** brokered coves run a **StudioKit** whose egress
> ceiling structurally excludes `anthropic.com`/`claude.com`/`claude.ai` (and
> subdomains), so a cove reaches Anthropic **only** through the jam host; the
> interactive `claude auth login` path keeps the full allow-list on the
> interactive kit. It's defense-in-depth — the cove holds no real Anthropic
> credential (only the fake identity). The ceiling and `kit show` output are in
> [kits.md](kits.md#the-egress-ceiling-cov-208); the raise-side build protocol is
> in [coves.md](coves.md#the-studiokit-and-its-kit-prepare-protocol).

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
