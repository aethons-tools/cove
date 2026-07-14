---
summary: The at-mint usage doc — the host-side token minter binary, its two subcommands (github, anthropic), their flags/env, and the flags=non-secret/env=secret/one-token-to-stdout/fail-closed contract.
read_when: You are configuring a machine-side secret supply that mints a token — a GitHub App installation token or an Anthropic WIF bearer.
owns: the at-mint CLI usage — the github and anthropic subcommands, their flags/env, and the minting contract
prereqs: at-cove-secrets.md — how a kit's demand is supplied by a command: that invokes at-mint
tier: leaf
updated: 2026-07-14
---

# `at-mint` usage

`at-mint` is a **host-side token minter**: a small CLI that mints a short-lived
credential and prints it to stdout, for use as a secret's `command:` resolver
(see [at-cove-secrets.md](at-cove-secrets.md)). It never runs inside a
sandbox — it runs on the host, invoked by `at-cove` when resolving a demand.

It has two subcommands, `github` and `anthropic`, each minting a different
kind of token.

## The contract

Both subcommands share one contract:

- **Flags are non-secret.** Every `--flag` value (App id, tenant, org, rule
  id, a key *path*, …) is safe to write in `secrets.yml` or a shell history.
- **Env is secret.** The one piece of secret material each subcommand needs
  (a private key's contents, a client secret) comes **only** from an
  environment variable — never a flag, never argv.
- **Exactly one token to stdout on success.** Nothing else is printed there.
- **Fail-closed.** Any error (missing input, HTTP failure, malformed
  response) prints a diagnostic to stderr and exits non-zero, with **nothing**
  on stdout.

## `at-mint github`

Mints a repo-scoped GitHub App installation token with `contents`+
`pull_requests` write.

```
at-mint github --app-id <id> --install-id <id> [--app-key-file <path>]
```

| Flag | Meaning |
|------|---------|
| `--app-id` | GitHub App id (non-secret) |
| `--install-id` | GitHub App installation id (non-secret) |
| `--app-key-file` | path to the App private-key PEM — a path is non-secret |

| Env | Meaning |
|-----|---------|
| `AT_MINT_GITHUB_APP_KEY` | the App private-key PEM **content**, an alternative to `--app-key-file` |
| `COVE_RUN_REPO` | the `owner/name` repo to scope the token to; injected by at-cove per run (not operator-supplied) |

The key comes from `--app-key-file` if set, else from
`AT_MINT_GITHUB_APP_KEY`; if neither is set, it fails closed. `COVE_RUN_REPO`
must be set (at-cove injects it during `work`/`dispatch`; see
[the `COVE_RUN_*` passthrough](../orchestration/at-cove-work-interface.md#three-separated-authorities))
— its repo *name* (the part after the `/`) is what the installation-token
request scopes to; the requested permissions are fixed in `at-mint` itself,
not derived from any run parameter.

## `at-mint anthropic`

Mints an Anthropic `sk-ant-oat01` bearer via a two-hop exchange: an Auth0
client-credentials grant (hop 1), federated into an Anthropic service-account
token (hop 2).

```
at-mint anthropic --auth0-tenant <host> --auth0-client-id <id> \
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
| `AT_MINT_AUTH0_CLIENT_SECRET` | the Auth0 client secret for the client-credentials grant |

Hop 1 exchanges `--auth0-client-id`/`AT_MINT_AUTH0_CLIENT_SECRET` at
`https://<auth0-tenant>/oauth/token` for a JWT assertion. Hop 2 exchanges
that assertion at Anthropic's federation endpoint, naming the rule, service
account, org, and (if set) workspace, for the bearer token. Any missing
required flag, a missing `AT_MINT_AUTH0_CLIENT_SECRET`, or a non-2xx at
either hop fails closed.

## Using it as a secret supply

`at-mint` is invoked as a bare `command:` under a kit's `secrets.yml` entry —
there is no special wiring beyond that today:

```yaml
kits:
  reference-worker:
    AT_TASK_GIT_TOKEN:
      command: ["at-mint","github","--app-id","123456","--install-id","7890","--app-key-file","/etc/cove/gh-app.pem"]
```

`AT_MINT_GITHUB_APP_KEY`/`AT_MINT_AUTH0_CLIENT_SECRET` come from the
resolver's own process environment on the host — set them in the shell/
service that runs `at-cove`, not in the kit or in `secrets.yml`. See
[at-cove-secrets.md](at-cove-secrets.md) for the full demand/supply model,
precedence, and fail-closed rules, and how `command:` resolvers additionally
see `COVE_RUN_{REPO,ISSUE,CLASS,TIMEOUT}` during `work`/`dispatch`.

A structured `minters:`/`mint:` profile form — where `secrets.yml` names a
reusable minting identity instead of spelling out the full `at-mint` argv —
is a later plan; it parses and validates today but is not yet runnable (see
at-cove-secrets.md's [`mint:`](at-cove-secrets.md#the-four-supply-sources)
row).
