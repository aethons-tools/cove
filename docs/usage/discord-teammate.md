---
summary: The operator guide to `at-cove teammate` — the standing Discord conductor (`at-switchboard`), the `teammates.<class>` config block, the one-time login prerequisite, and the known create-lifecycle gap (COV-136).
read_when: You are declaring a `teammates.<class>` block, standing up a Discord-reachable teammate sandbox, or debugging why a launched conductor isn't posting/replying.
owns: the `at-cove teammate` usage story — the `teammates:` config shape, the auth prerequisite, the detached/fail-loud launch model, Discord egress, and the create-lifecycle workaround for a teammate-only class
prereqs: ../OVERVIEW.md for the sandbox + egress model; at-cove-config.md#collaborators for the collaborator class shape this mirrors; at-cove-secrets.md for the secret demand/supply model
tier: leaf
updated: 2026-09-01
---

# The Discord teammate (`at-cove teammate`)

`at-cove teammate <class>` launches a standing **Discord conductor**
(`at-switchboard`) inside an already-running sandbox: a background process that
polls a set of Discord channels, drives a headless `claude -p --resume` turn per
message batch, and posts the replies back — so the sandbox behaves like a
teammate you talk to in Discord instead of over an SSH TUI. See the [remote
teammate design §A](../superpowers/specs/2026-08-26-remote-teammate-design.md#component-a-discord-teammate-loop)
for the *why*; this doc is the *how to run it*.

## Configuring a `teammates.<class>` block

A kit declares a teammate class under the top-level `teammates:` map — its own
map, distinct from `collaborators:` and `workers:`, with the same `<common>`-base
merge shape (own key wins on a scalar; `<common>` must not set `prompt`, `default`,
or `discord`):

```yaml
teammates:
  helper:
    prompt: "You are the team's Discord-facing assistant. Answer questions and make small fixes."
    secrets:
      DISCORD_BOT_TOKEN:
        description: bot token for the #helper channel, scoped to one guild
    allowed-domains: [discord.com]
    discord:
      channels: ["123456789012345678"]      # Discord channel IDs the conductor watches
      error-channel: "123456789012345678"    # optional; defaults to channels[0]
      bot-token-secret: DISCORD_BOT_TOKEN    # must name a secret declared here or in teammates.<common>
```

- **`discord.channels`** — at least one Discord channel ID; the conductor polls
  all of them and tags each message with its channel and author.
- **`discord.error-channel`** — optional; where a failed/recovered turn's error
  summary posts. Defaults to `channels[0]`.
- **`discord.bot-token-secret`** — names the secret (declared in this class's own
  `secrets` or `teammates.<common>.secrets`) that supplies the bot token. Config
  parsing rejects the block if the named secret isn't actually declared.
- **`allowed-domains: [discord.com]`** — Discord egress is additive and
  **scoped to this class only** (unioned with `teammates.<common>.allowed-domains`,
  on top of the kit's root `image.allowed-domains`), exactly like a
  [`collaborators.*.allowed-domains`](at-cove-config.md#collaboratorsclassallowed-domains)
  delta. No other class gains Discord egress by declaring one teammate.

The bot token is the only new secret this feature introduces. It is resolved
host-side and staged into the sandbox over SSH stdin (tmpfs, never disk or
argv) — the same demand/supply model as any other secret; see
[at-cove-secrets.md](at-cove-secrets.md).

## Auth prerequisite: sign in once via `chat` first

The conductor does **not** carry its own bearer credential — it authenticates as
`claude` the same way an interactive session does, by reusing the **subscription
OAuth login saved on the `/agent-data` volume**. Before the first `at-cove
teammate <class>` launch, sign in once with an ordinary interactive session
against the same kit:

```console
$ at-cove chat <any-collaborator-in-this-kit>
# complete the one-time `claude auth login --claudeai` prompt
```

That login is kept in a host-side copy (`~/.config/at-cove/credentials.json`) and
reseeded into **every** instance of the kit — including a teammate instance — before
each launch, so one sign-in covers every collaborator and teammate class the kit
defines; see [Authentication](../OVERVIEW.md#authentication). Do this first: a
teammate instance is launched **detached** (see below) precisely so the launching
`at-cove teammate` command returns as soon as the conductor is backgrounded — an
unauthenticated first run would instead block that command on an interactive OAuth
prompt, defeating the point of a quick, reconnectable launch.

## Running it

```console
$ at-cove teammate <class>
teammate "<class>" launched on atcove-<kit>-<class>; tail /agent-data/switchboard.log there to follow it
```

This assumes the sandbox already exists — `at-cove teammate` launches the
conductor into an **already-created** instance, it does not create one (see
[Create prerequisite](#create-prerequisite-the-teammate-only-gap-cov-136) below
if the class isn't provisionable by `at-cove create` yet). On each run it:

1. resolves and seeds the saved login (above);
2. resolves the class's bot-token secret and stages it, plus the watched
   channels, into the sandbox's tmpfs env file over SSH stdin;
3. applies the class's Discord egress delta to the running container;
4. starts `at-switchboard` **detached** (`setsid`, no PTY, output appended to
   `/agent-data/switchboard.log`) and returns as soon as it's backgrounded.

**Detached, fail-loud, no auto-restart.** Once launched, the conductor is a bare
background process — nothing supervises or restarts it. If it dies (a boot
failure, or the fail-soft turn loop giving up after repeated hard Discord
failures — see the [design's error handling](../superpowers/specs/2026-08-26-remote-teammate-design.md#error-handling)),
the sandbox simply goes quiet in Discord. There is no `at-cove teammate status`;
to recover:

```console
$ at-cove chat <class-or-a-collaborator> --raw   # or: at-cove view <class>
$ tail -f /agent-data/switchboard.log            # see why it stopped
$ at-cove teammate <class>                       # just re-run it to restart
```

`/agent-data/switchboard.log` lives on the persistent `-agent-data` volume, so it
survives the SSH channel closing and a `recreate`.

**Discord egress persists.** Unlike a `chat` session's per-class egress (applied
on start, cleared on exit), the teammate's Discord egress delta is applied and
**never cleared** by `at-cove teammate` — it must remain open for as long as the
detached conductor keeps running in the background, well after the launching
command has exited.

## Create prerequisite: the teammate-only gap (COV-136)

`at-cove create <class>` resolves its class positional only against
`collaborators:` (`instanceFor` → `Config.SelectCollaborator`) — it does not look
at `teammates:` at all. So a class that exists **only** under `teammates:` cannot
be provisioned by `at-cove create` today: `create <class>` fails with `kit "…"
declares no collaborator "<class>"`. This is a known, tracked gap — **COV-136** —
not yet fixed.

**Verified workaround.** `at-cove create`/`chat` and `at-cove teammate` key their
instance identically — both derive `state.Instance(class)` and container
`atcove-<kit>-<class>` from the bare class name (`internal/naming.Container`),
with no cross-check between the `collaborators:` and `teammates:` maps. So
declaring a **minimal matching `collaborators.<class>` entry** alongside the
`teammates.<class>` block provisions the exact same instance `at-cove teammate`
later launches into:

```yaml
collaborators:
  helper: {}          # minimal entry — just to make `create` provision the instance
teammates:
  helper:
    prompt: "..."
    secrets: { DISCORD_BOT_TOKEN: { description: "..." } }
    allowed-domains: [discord.com]
    discord:
      channels: ["123456789012345678"]
      bot-token-secret: DISCORD_BOT_TOKEN
```

```console
$ at-cove create helper     # provisions the sandbox (via the collaborators entry)
$ at-cove chat helper       # one-time sign-in (see Auth prerequisite above)
$ at-cove teammate helper   # launches the conductor into that same sandbox
```

The two blocks are independent — an empty `collaborators.helper: {}` adds no
`prompt` and no extra egress; it exists solely so `create`/`chat` can resolve and
provision the class. Once COV-136 removes the gap, `at-cove create <class>` will
provision a teammate-only class directly and this extra `collaborators.<class>`
entry becomes unnecessary.

## See also

- [at-cove-config.md](at-cove-config.md#collaborators) — the `collaborators.*`
  shape `teammates.*` mirrors (secrets, allowed-domains, `<common>`-merge).
- [at-cove-secrets.md](at-cove-secrets.md) — the demand/supply model for
  `DISCORD_BOT_TOKEN`.
- [observability.md](observability.md) — `/agent-data/switchboard.log` is
  VM-local and outside the structured logging sink; there is nothing to
  correlate host-side for a teammate run.
