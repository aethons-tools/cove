---
summary: The `jam mint` usage doc — the host-side token minter (a `jam` subcommand), its two subcommands (github, anthropic), their flags/env, and the flags=non-secret/env=secret/one-token-to-stdout/fail-closed contract.
read_when: You are configuring a machine-side secret supply that mints a token — a GitHub App installation token or an Anthropic WIF bearer.
owns: the jam mint CLI usage — the github and anthropic subcommands, their flags/env, and the minting contract
prereqs: jam/credentials.md — how a credential is supplied by a mint: profile or a command: that invokes jam mint
tier: leaf
updated: 2026-10-26
---

# `jam mint` usage

`jam mint` is a **host-side token minter**: a `jam` subcommand that mints a short-lived
credential and prints it to stdout, for use as a credential's `command:`
source (see [jam/credentials.md](jam/credentials.md#sources)). It never runs
inside a studio — it runs on the host, invoked by `jam` when resolving a
demand.

It has two subcommands, `github` and `anthropic`, each minting a different
kind of token.

## The contract

Both subcommands share one contract:

- **Flags are non-secret.** Every `--flag` value (App id, tenant, org, rule
  id, a key *path*, …) is safe to write in `credentials.yml` or a shell history.
- **Env is secret.** The one piece of secret material each subcommand needs
  (a private key's contents, a client secret) comes **only** from an
  environment variable — never a flag, never argv.
- **Exactly one token to stdout on success.** Nothing else is printed there.
- **Fail-closed.** Any error (missing input, HTTP failure, malformed
  response) prints a diagnostic to stderr and exits non-zero, with **nothing**
  on stdout.

## `jam mint github`

Mints a repo-scoped GitHub App installation token with `contents`+
`pull_requests` write.

```
jam mint github --app-id <id> --install-id <id> [--app-key-file <path>]
```

| Flag | Meaning |
|------|---------|
| `--app-id` | GitHub App id (non-secret) |
| `--install-id` | GitHub App installation id (non-secret) |
| `--app-key-file` | path to the App private-key PEM — a path is non-secret |
| `--repo` | the `owner/name` to scope the token to (non-secret) |

| Env | Meaning |
|-----|---------|
| `JAM_MINT_GITHUB_APP_KEY` | the App private-key PEM **content**, an alternative to `--app-key-file` |

The key comes from `--app-key-file` if set, else from
`JAM_MINT_GITHUB_APP_KEY`; if neither is set, it fails closed. `--repo`
is required (Jam's `mint:` expander supplies none, so pass it in a
`command:` — [jam/credentials.md](jam/credentials.md#minters)) — its
repo *name* (the part after the `/`) is what the installation-token request
scopes to; the requested permissions are fixed in `jam mint` itself, not derived
from any input. Every secret reaches `jam mint` via env only; every non-secret
identifier — including `--repo` — is a flag.

## `jam mint anthropic`

Mints an Anthropic `sk-ant-oat01` bearer via a two-hop exchange: an Auth0
client-credentials grant (hop 1), federated into an Anthropic service-account
token (hop 2).

```
jam mint anthropic --auth0-tenant <host> --auth0-client-id <id> \
  --auth0-audience <aud> --anthropic-org <id> --anthropic-rule <fdrl_...> \
  --anthropic-service-account <svac_...> [--anthropic-workspace <id>]
```

| Flag | Meaning |
|------|---------|
| `--auth0-tenant` | Auth0 tenant domain, e.g. `tenant.us.auth0.com` (non-secret) |
| `--auth0-client-id` | Auth0 machine-to-machine client id (non-secret) |
| `--auth0-audience` | Auth0 API identifier / token `aud` (non-secret) |
| `--anthropic-org` | Anthropic organization id (non-secret) |
| `--anthropic-rule` | Anthropic federation rule id, `fdrl_…` (non-secret) |
| `--anthropic-service-account` | Anthropic service account id, `svac_…` (non-secret) |
| `--anthropic-workspace` | Anthropic workspace id (optional, non-secret) |

| Env | Meaning |
|-----|---------|
| `JAM_MINT_AUTH0_CLIENT_SECRET` | the Auth0 client secret for the client-credentials grant |

Hop 1 exchanges `--auth0-client-id`/`JAM_MINT_AUTH0_CLIENT_SECRET` at
`https://<auth0-tenant>/oauth/token` for a JWT assertion. Hop 2 exchanges
that assertion at Anthropic's federation endpoint, naming the rule, service
account, org, and (if set) workspace, for the bearer token. Any missing
required flag, a missing `JAM_MINT_AUTH0_CLIENT_SECRET`, or a non-2xx at
either hop fails closed.

The token's lifetime is set **server-side by the federation rule** (and bounded
by the Auth0 assertion's own expiry) — jam mint requests no TTL. It logs the
minted token's `expires_in` and whether a `refresh_token` was offered to stderr.
If `JAM_RUN_TIMEOUT` is set in its environment (nothing in Jam sets it), it
also **fails closed if `expires_in` is shorter than that timeout**.

## Using it as a credential supply

Normally you don't invoke `jam mint` yourself: Jam's
[credentials file](jam/credentials.md#minters) names a `minters:` profile once and
a `{ mint: <profile> }` credential runs `jam mint` from it on every resolve, or a
`command:` spells the invocation out. `JAM_MINT_GITHUB_APP_KEY` /
`JAM_MINT_AUTH0_CLIENT_SECRET` come from the resolving process's own environment
or from the profile's source field — never from argv.
