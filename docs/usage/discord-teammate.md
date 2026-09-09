---
summary: The operator guide to `at-cove teammate` — the standing Discord conductor (`at-switchboard`), the `teammates.<class>` config block, and the one-time login handled by the first launch.
read_when: You are declaring a `teammates.<class>` block, standing up a Discord-reachable teammate sandbox, or debugging why a launched conductor isn't posting/replying.
owns: the `at-cove teammate` usage story — the `teammates:` config shape, the create→teammate provisioning flow, the auth prerequisite, the detached/fail-loud launch model, and Discord egress
prereqs: ../OVERVIEW.md for the sandbox + egress model; at-cove-config.md#collaborators for the collaborator class shape this mirrors; at-cove-secrets.md for the secret demand/supply model
tier: leaf
updated: 2026-09-09
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

## Provisioning: `create` then `teammate`

A `teammates.<class>` block is provisioned the same way a `collaborators.<class>`
one is: `at-cove create <class>` resolves the class positional against **both**
the `collaborators:` and `teammates:` maps (`instanceFor` → `Config.SelectClass`)
and provisions the isolated sandbox directly — no matching `collaborators.<class>`
entry is needed or allowed (config parsing rejects a name declared in both maps
as a fatal error). Then launch the conductor into that instance:

```console
$ at-cove create helper     # provisions the sandbox for the teammate class
$ at-cove teammate helper   # signs in (first run only) and launches the conductor
```

`recreate`, `destroy`, `status`, and `view` all resolve a teammate class the same
way. `at-cove chat` does not: it rejects a teammate-class positional outright
with a usage error — `"<class>" is a teammate; launch it with at-cove teammate
<class>` — so a teammate is never launched or inspected through `chat`.

## Auth prerequisite: the first `teammate` launch signs you in

The conductor does **not** carry its own bearer credential — it authenticates as
`claude` the same way an interactive session does, by reusing the **subscription
OAuth login saved on the `/agent-data` volume**. You do not need a separate
`at-cove chat` session to seed it: `at-cove teammate <class>` performs the
one-time sign-in itself.

`at-cove teammate` runs the auth step (`ensureAuthenticated`,
[`internal/connect/teammate.go`](../../internal/connect/teammate.go)) **before**
backgrounding the conductor, over the same foreground SSH connection the CLI
command itself is running on — not inside the detached process. Concretely, on
each run:

1. it seeds any host-saved login (`~/.config/at-cove/credentials.json`) into the
   VM and probes `claude auth status`;
2. if that probe reports **not logged in** (the very first launch of any
   instance under this kit, on any collaborator or teammate class), it runs
   `claude auth login --claudeai` over an interactive `ssh -tt` connection with
   the real terminal's stdin/stdout attached — so the OAuth prompt appears right
   there in the terminal where you typed `at-cove teammate <class>`, and the
   command blocks until you complete it;
3. a fresh login is saved back to the host copy, so it's reused by every other
   collaborator and teammate instance in the kit without asking again; see
   [Authentication](../OVERVIEW.md#authentication).

Because step 2 needs a real terminal attached to the invoking process, run the
first `at-cove teammate <class>` for a kit by hand, interactively — not from a
script or a non-interactive trigger. Once any instance in the kit has signed in
once (via `chat` or `teammate`), every subsequent `at-cove teammate` launch reuses
the saved login silently and returns as soon as the conductor is backgrounded,
with no prompt.

## Running it

```console
$ at-cove teammate <class>
teammate "<class>" launched on atcove-<kit>-<class>; tail /agent-data/switchboard.log there to follow it
```

This assumes the sandbox already exists — `at-cove teammate` launches the
conductor into an **already-created** instance ([Provisioning](#provisioning-create-then-teammate)
above), it does not create one. On each run it:

1. resolves and seeds the saved login, signing in interactively on the first
   run for the kit (above);
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
$ at-cove view <class>                # print an ssh config; connect with it, or plain ssh — read-only, no egress change
$ tail -f /agent-data/switchboard.log # see why it stopped
$ at-cove teammate <class>            # just re-run it to restart
```

`/agent-data/switchboard.log` lives on the persistent `-agent-data` volume, so it
survives the SSH channel closing and a `recreate`.

**`at-cove chat`/`at-cove work` can't clobber a teammate's egress.** An earlier
revision of this doc warned that inspecting a running teammate with `chat`
against the same container would silently revoke the conductor's Discord egress
on session exit (tracked as **COV-137**, "one egress scope per shared
container"). That warning applied when a teammate shared its container with a
same-named `collaborators.<class>` entry (the now-removed dual-declaration
workaround for provisioning). Now that class names are unique across both maps
(a name declared in both is a fatal config error), a teammate always has its
own container, and `chat` rejects a teammate-class positional outright — so
`chat` can never resolve to a teammate's container, and this can no longer
happen.
`at-cove work` is unrelated: it dispatches ephemeral, separately-labeled
containers from the `workers:` map, never a teammate's persistent instance. The
residual, truthful caveat: `at-cove view <class>` and a direct SSH session never
touch egress, so they remain the safe way to inspect a live teammate from
outside `at-cove teammate` itself.

**Discord egress persists.** Unlike a `chat` session's per-class egress (applied
on start, cleared on exit), the teammate's Discord egress delta is applied and
**never cleared** by `at-cove teammate` — it must remain open for as long as the
detached conductor keeps running in the background, well after the launching
command has exited.

## Validating the agent side in isolation (`at-switchboard once`)

Before wiring up a real bot, you can exercise the whole claude side of the
pipeline — headless `claude -p --continue` and the `.switchboard/turn-result.json`
contract — **without Discord or a bot token**. Inside a created, signed-in
sandbox (`at-cove chat <class>` once to log in, then `at-cove chat --raw <class>`
for a shell), run:

```console
$ at-switchboard once --message "say hello back"
── turn input (as the agent sees it) ──
New Discord messages:
[#demo] tracer: say hello back
── raw /home/agent/workspace/.switchboard/turn-result.json ──
{"messages":[{"channel":"demo","content":"hello!"}],"action":"wait"}
── verdict ──
OK: action="wait", 1 message(s) the conductor would post:
  → #demo: hello!
```

It feeds one canned message to a real agent turn and prints exactly what the
conductor *would* post, plus a verdict. A `FAIL` verdict tells you which of the
never-fully-exercised assumptions broke — the agent answering in prose instead
of writing the result file (headless write-permission / prompt clarity), or an
unparseable result — so you can fix the claude side in isolation before adding a
bot. `--channel`/`--author` set the tags; `--message` reads stdin if omitted.

## See also

- [at-cove-config.md](at-cove-config.md#collaborators) — the `collaborators.*`
  shape `teammates.*` mirrors (secrets, allowed-domains, `<common>`-merge).
- [at-cove-secrets.md](at-cove-secrets.md) — the demand/supply model for
  `DISCORD_BOT_TOKEN`.
- [observability.md](observability.md) — `/agent-data/switchboard.log` is
  VM-local and outside the structured logging sink; there is nothing to
  correlate host-side for a teammate run.
