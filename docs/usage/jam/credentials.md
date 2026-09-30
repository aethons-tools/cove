---
summary: The protected Jam credentials file (`~/.config/at-jam/credentials.yml`) that supplies the real secrets a Jam brokers/uses, and the demand/supply split between it and the serve config's name-only `credentials:` list.
read_when: You are supplying the real secrets a Jam brokers or uses — writing ~/.config/at-jam/credentials.yml, choosing value/command/global/mint per credential, wiring `credentials-file`, or migrating an inline `credentials:`/`bot-token`/`tracker-token` strategy out of a serve config.
owns: the at-jam credentials-file format and location, the demand/supply split (serve config demands by name, the file supplies), which references must name a demanded credential, and fail-closed on an unsupplied demand
prereqs: serve.md for the serve config that demands credentials; ../at-cove-secrets.md for the shared Source/minters vocabulary
tier: leaf
updated: 2026-09-30
---

# The Jam credentials file

Jam's real secrets live in one **protected file**, never in the serve config:
the serve config only **demands** credentials by name, and this file **supplies**
them. The strategy vocabulary (`value` / `command` / `global` / `mint`, the
`minters:` library, and how each resolves) is shared with at-cove and owned by
[at-cove-secrets.md](../at-cove-secrets.md) — this doc points there rather than
re-explaining it. Unlike at-cove's, Jam's file is **flat** (no `kits:`; Jam is a
single service).

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
      audience: "https://jam.example.com/anthropic"
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
| `store-postgres.password-cred` | the DB password ([serve.md](serve.md#postgres-store-backend-store-postgres)) |
| `runtime.discord.bot-token-cred` | the Discord bot token ([discord.md](discord.md)) |
| `runtime.requisitioner.tracker-token-cred` | the tracker token ([requisitioner.md](requisitioner.md)) |
| a destination's `cred-name` | validated at `destination add` ([serve.md](serve.md#destinations)) |
| `pool.cred-name` | the pool's anthropic credential ([pool.md](pool.md)) |

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
token with `bot-token-cred` / `tracker-token-cred`.
