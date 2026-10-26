---
summary: The protected Jam credentials file (`~/.config/at-jam/credentials.yml`) that supplies the real secrets a Jam brokers/uses, and the demand/supply split between it and the serve config's name-only `credentials:` list.
read_when: You are supplying the real secrets a Jam brokers or uses — writing ~/.config/at-jam/credentials.yml, choosing value/command/global/mint per credential, wiring `credentials-file`, or migrating an inline `credentials:`/`bot-token`/`tracker-token` strategy out of a serve config.
owns: the at-jam credentials-file format and location, the value/command/global/mint source vocabulary and the minters: library, the `exchange: gcp` demand setting, the demand/supply split (serve config demands by name, the file supplies), which references must name a demanded credential, and fail-closed on an unsupplied demand
prereqs: serve.md for the serve config that demands credentials
tier: leaf
updated: 2026-10-26
---

# The Jam credentials file

Jam's real secrets live in one **protected file**, never in the serve config:
the serve config only **demands** credentials by name, and this file **supplies**
them, each from one of four [sources](#sources) — optionally through a
[minter](#minters).

## Location

Set `credentials-file: <path>` in the [serve config](serve.md#the-serve-config).
When omitted it defaults to `${XDG_CONFIG_HOME:-~/.config}/at-jam/credentials.yml`.
Keep it mode `0600` and **never commit it**. A missing file is an empty store, so
every demand then fails closed (below).

## Format

```yaml
# ~/.config/at-jam/credentials.yml   (mode 0600, never committed)
minters:                       # inert library; reached only via a mint: reference
  anthropic-fed:
    anthropic:
      oidc:
        auth0:
          tenant: tenant.us.auth0.com
          client-id: jam-minter
          audience: "https://jam.example.com/anthropic"
          client-secret: { command: ["cat", "/run/secrets/auth0-client-secret"] }
      federation: { org: "<org-id>", rule: fdrl_example, service-account: svac_example }
global:                        # optional inert library; reached only via a global: reference
  gh-token: { command: ["gh", "auth", "token"] }
credentials:                   # name -> source: exactly one of value | command | global | mint
  anthropic-key: { mint: anthropic-fed }
  git-pat:       { global: gh-token }
  jam-db:        { value: "dev-only-password" }
  discord-bot:   { command: ["cat", "/run/secrets/discord"] }
  linear-bot:    { command: ["cat", "/run/secrets/linear"] }
```

Each `credentials:` entry sets **exactly one** source, validated on load.
`minters:` and `global:` are **inert** — reached only through an explicit
`mint:`/`global:` reference, and an entry supplies nothing until the serve
config demands its name.

## Sources

| Source | Form | Resolves to |
|---|---|---|
| `value:` | a literal string | that string, verbatim |
| `command:` | a host **argv array** | stdout of running it on the host, trailing newline trimmed |
| `global: <name>` | a name | `global[<name>]`, which must itself be a `value` or `command` (a chain is an error) |
| `mint: <name>` | a name | a token minted by `at-mint`, built from `minters[<name>]` ([below](#minters)) |

A `command:` runs on the Jam host — only supply commands you trust.

`global:` and `minters:` are **never matched by demand name**: a demand called
`anthropic-key` does not pick up a library entry of the same name — only a
`credentials:` entry you wrote reaches one.

## Minters

A `minters:` profile is a structured identity for [`at-mint`](../at-mint.md),
exactly one of:

- `github:` — `app-id`, `install-id`, `app-key` (a `value:` is a **path** to the
  PEM, passed as `--app-key-file`; a `command:`/`global:` supplies the key
  **contents**, passed as `AT_MINT_GITHUB_APP_KEY`).
- `anthropic:` — `oidc.auth0` (`tenant`, `client-id`, `audience`,
  `client-secret` — a source, passed as `AT_MINT_AUTH0_CLIENT_SECRET`) and
  `federation` (`org`, `rule`, `service-account`, optional `workspace`).

Resolving `{ mint: <profile> }` runs `at-mint <provider>` with the non-secret
fields as flags and the secret ones resolved on the host and passed as env —
never argv. Jam passes no `--repo`, which `at-mint github` requires, so a
`github` profile cannot mint through `mint:`: write the invocation as a
`command:` instead (`["at-mint", "github", "--app-id", "…", "--install-id", "…",
"--app-key-file", "/path/key.pem", "--repo", "owner/name"]`).

## Demand and supply

The serve config's `credentials:` is a **name-only list** (the demand):

```yaml
credentials:
  anthropic-key:
  jam-db:
  discord-bot:
```

Every place the serve config refers to a secret must name a **demanded**
credential:

| Reference | Where |
|---|---|
| `store-postgres.password-cred` | the DB password ([serve.md](serve.md#postgres-store-store-postgres)) |
| a connection's credential (`at-jam connection add --cred` / `connection cred`), checked when serve starts the connection — the Discord bot token, the tracker token | [serve.md](serve.md) (`runtime.discord.connection`, `runtime.requisitioner.connection`; the deprecated `bot-token-cred` / `tracker-token-cred` name one directly) |
| a destination's `cred-name` | validated at `destination add` ([serve.md](serve.md#destinations)) |
| `pool.cred-name` | the pool's anthropic credential ([pool.md](pool.md)) |
| `ssh.ca-cred` | the studio-collaboration SSH CA key ([serve.md](serve.md), [collaboration.md](collaboration.md)) |
| `metrics.token-cred` | the `/metrics` scrape token ([monitoring.md](monitoring.md#metrics)) |
| `kits.git[].cred` | a git token for kit sources, by repo-URL prefix; plain static only. The prefix must be https (or loopback http), end in `/`, and have a dotted host or loopback; refs containing `/` need a portless host ([serve.md](serve.md), [kits-git.md](kits-git.md)) |

A demand may carry one non-secret setting, **`exchange: gcp`**:
`vertex-gcp: { exchange: gcp }`. The supplied value is then a Google credentials
JSON (`service_account`, `authorized_user`, `external_account` or
`impersonated_service_account`). The broker swaps it for short-lived
`cloud-platform` access tokens and caches each one, refreshing it 5 minutes
before it expires. If a refresh fails, the broker re-reads the supply (at most
every 10 seconds), so a re-login or a rotated key needs no restart. A destination naming the credential injects the current token,
and the JSON never leaves the host. `serve` refuses an unknown `exchange`, a JSON
of another type, or the pool's credential. See [vertex.md](vertex.md) for its use.

At `serve` startup Jam loads the file, resolves each demanded name on the host in
memory, and **fails closed**: a demanded name with no entry in the file aborts
`serve` before the broker starts, naming the credential and the file. Resolved
values are never written to disk, put on a command line, or logged — errors name
only credentials.

## Migrating an inline strategy

An inline strategy is a **hard parse error**, not a silent alias (unlike the
[Harbor renames](renamed-from-harbor.md)): a `command:`/`value:` under
`credentials:`, or an inline `runtime.discord.bot-token` /
`runtime.requisitioner.tracker-token`. The error names the field and points at
this file. To migrate, move each strategy into `credentials.yml` under the same
name, leave only the bare name under `credentials:`, and replace the inline
token with a connection whose credential names it (or the deprecated
`bot-token-cred` / `tracker-token-cred`).
