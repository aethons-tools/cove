# at-cove — Project Overview

`at-cove` is a small, dependency-light Go CLI that runs **hardened Claude Code sandboxes**.
You describe a sandbox once in a `.at-cove/` kit directory,
and `at-cove` provisions a locked-down VM,
SSHes into it,
injects your secrets in memory only,
and launches `claude` interactively.

- **Module:** `github.com/aethons-tools/cove`
- **Binary:** `at-cove`
- **Dependencies:** standard library plus `gopkg.in/yaml.v3` (config parsing) — no CLI framework.

## Why it exists

Running an autonomous coding agent against your real machine is risky:
it can exfiltrate secrets,
reach arbitrary network destinations,
or touch files it shouldn't.
`at-cove` puts the agent inside a sandbox with three properties baked in:

1. **Egress is locked down** —
   the VM can only reach an allow-listed set of domains (Anthropic, GitHub, PyPI, …)
   through an in-VM Squid proxy,
   with `nftables` dropping everything else.
   A user's own kit files can never weaken this.
2. **Secrets never touch disk or the host process table** —
   they are resolved on the host just-in-time at `chat`/`work`/`dispatch` time,
   streamed into the VM over SSH stdin into a tmpfs file,
   sourced,
   and deleted.
   Nothing is persisted,
   nothing appears in `ps` or shell history.
3. **The hardening is foolproof** —
   the security-critical files (egress rules, sshd config, entrypoint) ship embedded in the binary
   and are layered on *last*,
   so they always win over anything the user supplies.

The governing design principle is **SSH as the universal interface**:
backends differ only in how a VM is provisioned and how its `sshd` is reached.
Once a backend hands `chat` a `host:port` and a key,
everything downstream — host-key trust, secret injection, launching `claude` — is identical.

## The kit: `.at-cove/`

A kit is always a directory,
by convention `.at-cove/` at a repo root.
Commands with no explicit path **walk up from the cwd** to the nearest `.at-cove/` (git-style),
so they work from subdirectories.

```
repo/
  .at-cove/
    config.yml        # the spec: name, secrets, workers, image
    image-files/      # your local overrides, overlaid onto the VM root (image-files/ → /)
    .state/           # records the running instance (state.json) + lockfile (gitignored)
    .build/           # assembled build context (gitignored)
```

at-cove keeps a managed `.gitignore` in the kit covering `.build/` and `.state/` — written whenever a build context is assembled (`build`/`create`/`work`) or instance state is saved, so no command can leak those artifacts into git.

### `config.yml`

Lean — identity and wiring only.
No secret *values*, no hardening knobs, no workspace mode.

```yaml
name: claude-on-myrepo          # sandbox/VM name; also keys the per-sandbox known_hosts
source-control:                 # the target repo (required for `dispatch`)
  github:
    project: acme/myrepo
    main-branch: main
```

`name` is the only always-required field;
`source-control` (the target repo — a github union; required for `dispatch`, the single source
of the repo), `secrets`, `workers` (the classes `at-cove work` can launch), and `image`
(additive build customization) are optional.
The full field-by-field schema, validation, and a complete example live in
[`docs/usage/at-cove-config.md`](usage/at-cove-config.md);
the secret model — kits **demand** secrets by name only, the machine **supplies**
values out of source control — lives in
[`docs/usage/at-cove-secrets.md`](usage/at-cove-secrets.md).

> **Trust boundary:** a kit's `secrets:` entries carry a name and a description
> only — never a command, a value, or a machine-specific identifier. Every value
> is supplied host-side, from `~/.config/at-cove/secrets.yml`/`secrets.local.yml`
> (never committed), keyed explicitly per kit. A committed `config.yml` can never
> smuggle in a resolver command of its own. See
> [at-cove-secrets.md](usage/at-cove-secrets.md).

## Command surface

Every command takes an optional kit directory via the `--kit-dir DIR` flag
(default: the current dir / cwd walk-up single-kit resolution). There is no
positional kit-dir on any command — `chat`'s one positional is the optional
collaborator (below), not the kit dir.

| Command | Behavior |
|---|---|
| `at-cove build [--kit-dir DIR]` | Assemble `<kit>/.build/` from the overlays and inject the managed public key. No backend, no VM — for authoring/inspection. |
| `at-cove create [--kit-dir DIR] [--workspace\|--ws <path>]` | `build`, then create the VM via the backend. Secret-free. Records the instance in `.state/state.json`. `--workspace` selects Shared (bind-mount) mode. |
| `at-cove chat [collaborator] [--kit-dir DIR] [--raw] [--no-auth] [--fresh]` | Resolve secrets, dial the backend, verify host key (TOFU), inject env + the selected collaborator's role, launch `claude`. Run every session. The optional leading positional selects a `collaborators:` class (sole/`default: true`/error-if-ambiguous; omitted with none defined launches a plain session — see [below](#the-chat-command-and-collaborator-sessions)). `--raw` drops to `bash`; `--no-auth` skips the login step; `--fresh` starts a new agent session. |
| `at-cove recreate [--kit-dir DIR] [--workspace\|--ws <path>]` | Destroy the container and create it again, **keeping the volumes** (saved login + workspace). The UAT rebuild loop. |
| `at-cove destroy [--kit-dir DIR]` | Remove the container (volumes retained) and image, then delete the state file. |
| `at-cove status [--kit-dir DIR]` | Report `running` / `stopped` / `absent`. |
| `at-cove version` | Print the build version. |
| `at-cove work [--kit-dir DIR] --in <f> --out <f> [--timeout] [--grace] [--reap]` | Run one unit of work in a fresh ephemeral hardened VM: inject `--in` as the task, run the **at-task worker bracket** (`prepare` → agent → `complete`) for the task's `worker.class` (declared in the kit's `workers`), extract the result to `--out`, destroy. Scavenges crashed dispatch orphans. |
| `at-cove dispatch [--kit-dir DIR]` | Poll the kit's tracker and dispatch ready work via `at-cove work`. |

Global `--dry-run` (before the subcommand) prints the planned actions —
exact backend/SSH argv included —
without executing anything.
Flags specific to a command (e.g. `--raw`, `--ws`, `--kit-dir`) go *after* the
command name; each command only accepts its own flags.

Three more global flags (also before the subcommand) configure structured
logging: `--log-mode attended|unattended` (default: auto-detect via TTY),
`--log-level debug|info|warn|error` (default `info`), and `--no-log-file`
(suppress the attended-mode log file). They're parsed into `cli.Globals`;
`dispatch` wires them into a per-run `internal/logging` logger. In **attended**
(TTY) mode the logger writes human-friendly text to stderr **and** a JSON
debug-level file at `<kit-dir>/.state/logs/at-cove-dispatch.jsonl` (unless
`--no-log-file`). In **unattended** (headless / non-TTY) mode — the normal way
`dispatch` runs as a service — it writes JSON to stderr only, with no file; the
platform capturing stderr is the log sink. Each
dispatched issue's log lines carry a `run` id and `issue`/`class`/`step`
attrs, so one dispatch's logs are grep-able out of interleaved concurrent
dispatches. `work` does not yet consume these flags (see
[`docs/superpowers/specs/2026-07-15-structured-logging-design.md`](superpowers/specs/2026-07-15-structured-logging-design.md)).

### The `chat` command and collaborator sessions

`chat` is the interactive command (a hard rename of the former `connect`, no
alias). Unlike the other commands, `chat` also loads the kit's `config.yml` —
a malformed or absent kit config is a hard error, since a collaborator session
is kit-defined — and uses its `collaborators:` tree (see
[at-cove-config.md](usage/at-cove-config.md#collaborators)) to select a role:

- an explicit `at-cove chat <collaborator>` positional must match a declared
  class;
- omitted, with one class defined, that one is used;
- omitted, with several, the one marked `default: true` (error if none/several
  are marked);
- omitted, with none defined, `chat` launches a plain session — no role
  injected, the exact behavior of the old `connect`.

A collaborator's `prompt:` is injected as session context (not a headless
`-p`) and states the session's **boundary**: a collaborator session plans and
grooms the board — turning ideas into well-formed Linear issues, decomposed
into dispatchable sub-issues — and lets **dispatched `at-cove work` runs do
the implementation**. The one exception is **review or troubleshooting**,
where the same session may make direct fixes in place. The session reaches
GitHub and Linear through the human's own **claude.ai account connectors**
(subscription OAuth) — never a minted token; token minting stays a `work`/
`dispatch` concern only. See
[at-cove-config.md](usage/at-cove-config.md#collaborators) for the schema and
[the orchestration work interface](orchestration/at-cove-work-interface.md)
for the dispatched-worker side of that boundary.

### State vs. config

`create` records the running instance in `.at-cove/.state/state.json`;
**`chat`, `destroy`, and `status` operate on that recorded state, not on `config.yml`**
(`chat` additionally loads `config.yml` for its `collaborators:` tree, as above).
A lockfile guards concurrency:
`chat` holds a *shared* lock for the whole session,
and `destroy` takes an *exclusive* lock —
so a sandbox can't be torn down underneath a live connection.

## How the build context is assembled

Each `build` stacks overlays into `<kit>/.build/`,
**last writer wins**:

1. **Overridable defaults** (embedded) —
   sensible defaults you may replace:
   `CLAUDE.md`, `settings.json`, stock skills, default entrypoint.
2. **Kit `image-files/`** —
   your committed local files;
   shadow any default at the same path.
3. *(deferred)* **`.local/image-files/`** —
   uncommitted machine-specific overrides;
   the slot is reserved.
4. **Non-overridable hardening** (embedded, applied last) —
   `nftables.conf`, `squid.conf`, sshd hardening, the entrypoint, `sshd` `AcceptEnv` config, the git credential helper.

Layer 4 extracting last is the **security boundary**:
local files can never weaken the egress lock or sshd hardening.
Both embedded layers ship inside the binary via Go `embed.FS`,
so the hardening cannot be misplaced or forgotten.
After the overlays,
`create` writes the managed public key into the context's `authorized_keys` —
an explicit assembly step,
keeping overlay precedence pure.

## Workspace and state volumes

The working directory is realized one of two ways,
chosen at `create` time (not in `config.yml`, so a spec stays portable):

- **Isolated (default)** —
  a backend-managed volume;
  the agent clones the repo in once secrets are present.
- **Shared** —
  a bind-mount of a host folder via `--workspace <path>` (host and VM share `.git/config`).

A second volume, **`<name>-state`**, is always a persistent backend volume mounted at `/agent-data` (`CLAUDE_CONFIG_DIR`).
It preserves Claude session history and the saved OAuth login across recreates,
and is seeded once (guarded by a `.seeded` marker).

The seed also carries the Claude Code **plugins** enabled in managed settings
(the `claude-plugins-official` marketplace and `superpowers`),
pre-installed into the image at build time by `seed-plugins.sh`
rather than left to Claude Code's boot-time auto-installer.
That installer would clone the marketplace and each plugin through the egress proxy at runtime,
where two installs racing into the same directory can leave it half-written
(`could not lock config file .git/config`) —
so the plugin never appears.
Provisioning at build (open network, like the Claude Code binary itself),
rewriting the recorded absolute paths to `/agent-data`,
and folding the result into the seed
sidesteps both the proxy round-trip and the race.

## Secret injection (the `chat` data flow)

Backend-agnostic, in `internal/connect`:

1. **Resolve secrets** on the host: for each name the kit demands (the root
   `secrets` bucket, plus a `chat` session's selected `collaborators.*.secrets`),
   look up its supply (`~/.config/at-cove/secrets.yml`/`secrets.local.yml`) and
   run it — see [at-cove-secrets.md](usage/at-cove-secrets.md) for the
   demand/supply model. Held in memory only;
   an unresolved *required* secret (the git or tracker token) aborts before
   SSH — a general demand instead warns and is left unset.
2. **Dial** the backend for an `Endpoint` (+ cleanup).
   If the VM isn't running,
   return an actionable error.
3. **Verify host key (TOFU)** against a per-sandbox `known_hosts.d/<name>` file with `accept-new`.
   First connection pins the key;
   later mismatches fail loudly.
4. **Inject env + launch.**
   The primary transport writes `export NAME=…` lines over SSH **stdin** into a tmpfs file (`/dev/shm/cove-env-*`, mode 600);
   a second interactive `ssh -tt` sources it,
   removes it,
   and `exec`s `claude` (named `-n "<kit> cove"` so a cove session is easy to tell apart from a remote-control one).
   A `SendEnv`/`AcceptEnv` transport is the proven fallback.
   Both keep values off every command line and off disk.

## Authentication

Claude Code picks its credential by env-driven precedence — an injected
`ANTHROPIC_AUTH_TOKEN` wins over a subscription OAuth login — and the managed
settings no longer force a login method, so the two agent paths differ
deliberately:

**Interactive `chat` → subscription OAuth.**
On the first session `chat` probes `claude auth status` over SSH
and runs interactive `claude auth login --claudeai` only when needed;
the credentials persist on the `/agent-data` volume, so later sessions skip it.
To make one login reusable across sandboxes (and across recreates),
`chat` keeps a host-side copy at `~/.config/at-cove/credentials.json` (mode `0600`):
it **seeds** that file into the VM (`/agent-data/.credentials.json`) *before* the auth probe,
so a login obtained on any sandbox validates the next one without re-prompting,
and it **saves** the VM's copy back to the host after a fresh login
or whenever a session rotates the token —
keeping the shared copy current as the OAuth refresh token rolls over.
When the credentials finally expire, the probe fails and the login flow re-mints them.
`--no-auth` skips this entirely.

> **Note:** this writes your subscription credentials to the host disk
> (distinct from injected *secrets*, which stay memory-only) —
> the user's own OAuth tokens, in the user-owned config dir at `0600`,
> the same trust boundary as the saved login already on the VM volume.

**Dispatched `work` → short-lived bearer.**
A worker runs unattended, where a personal subscription is neither permitted nor
practical, so its agent authenticates with a short-lived bearer —
**`ANTHROPIC_AUTH_TOKEN`** or **`ANTHROPIC_API_KEY`** — declared under the
dispatched class's `workers.<class>.secrets` (or `workers.<common>.secrets`)
*demand* and supplied machine-side (memory-only, like any secret; see
[at-cove-secrets.md](usage/at-cove-secrets.md)). It may **not** be declared at
the kit root: the root `secrets` bucket is injected into `chat` too, where the
env key would outrank the subscription OAuth login and disable the session's
connectors, so `config.yml` rejects it there as a hard error — see
[Migrating the worker bearer off the root bucket](usage/at-cove-secrets.md#migrating-the-worker-bearer-off-the-root-bucket).
Because the worker bucket is resolved only on the `work`/`dispatch` path
(agent-step only — see the [bucket-visibility
table](usage/at-cove-config.md#secret-buckets)), this bearer never reaches a
`chat` session by declaration alone, whatever else that kit's root `secrets`
demands. Unlike a general secret demand, an unresolved `ANTHROPIC_AUTH_TOKEN`
is not a warn-and-continue: `at-cove work` **fails closed on the host**,
before building or launching a VM, naming the secret and the kit — a keyless
worker is a guaranteed 401, so at-cove refuses to build one rather than
launch a doomed container. This pre-flight gate is name-specific: it
recognizes only `ANTHROPIC_AUTH_TOKEN`, and treats that name being absent as
unresolved — so a class that declares only `ANTHROPIC_API_KEY` isn't
gate-covered by that name, and still fails closed today (the gate sees no
`ANTHROPIC_AUTH_TOKEN` at all) rather than being waved through. As a second,
independent layer, at-cove also deliberately
does **not** seed the OAuth `credentials.json` on the work path: with no OAuth
token below the bearer in the precedence chain, a worker that somehow still
launched keyless would fail closed *inside* the VM too, instead of silently
falling back to — and burning — a subscription.

Private-repo git uses the code-host token, not SSH:
the egress lock blocks port 22, `/etc/gitconfig` rewrites GitHub remotes to HTTPS,
and a credential helper feeds the token from the session env (memory-only).
For dispatched work that token is `source-control.github.secrets.AT_TASK_GIT_TOKEN`,
minted per git step and withheld from the agent.

## Backends

A `Backend` knows how to provision a VM from a build context,
report a reachable SSH endpoint,
query its state,
and destroy it.
Backends self-register into a registry keyed by name (at-cove defaults to `colima`).

- **Colima** — the only implemented backend.
  Native Docker via Colima (no `sbx`):
  `docker build` the assembled context,
  `docker run -d` with `NET_ADMIN`,
  the state + workspace volumes,
  and a published `localhost:<port>` mapped to the in-VM `sshd`.
  `Dial` returns that port;
  `Destroy` is `docker rm -f` (volumes retained).
  Also implements `backend.DispatchOps` (ephemeral labeled runs + scavenge) for `dispatch`.
- **Firecracker / Fly** — designed-for but not built.
  Each is "provision + reach `sshd`";
  `Dial` returns a `cleanup func()` so tunnel-based backends (e.g. a `fly proxy` child) fit the same interface.

## Architecture

A pure **plan** (what to do) is separated from **execution** (doing it),
so the interesting logic is unit-testable without Docker, SSH, or a live VM.
A `Runner` interface abstracts process execution:
the OS implementation streams stdio and propagates exit codes;
a `Fake` records calls for tests.

```
cmd/at-cove/                  at-cove entry: parse argv, discover kit, select backend + work + dispatch
internal/dispatchrun/         `at-cove work` orchestration (scavenge → run → inject → exec → extract → destroy)
internal/dispatch/            dispatcher control plane, live and wired into `at-cove dispatch` (owned by docs/orchestration/)
internal/dispatch/scheduler/  scheduler engine (poll → claim → dispatch via at-cove → broker) + Tracker/Executor interfaces
internal/dispatch/linear/     real Tracker: Linear GraphQL client (live calls behind the integration tag)
internal/dispatch/exec/       real Executor: headless command run with injected env + timeout
cmd/at-task/                  at-task entry: prepare / complete (git/PR worker)
internal/dispatch/worker/     at-task orchestration: Prepare + Complete, Git/CodeHost interfaces
internal/dispatch/github/     at-task's real CodeHost: GitHub PR client (live calls behind the integration tag)
internal/kit/                 locate kit (cwd walk-up); load + validate config.yml
internal/assemble/            layered .build assembly from embed.FS; key injection
internal/backend/             Backend interface + registry
internal/backend/colima/      Colima impl: docker build / run / inspect / rm
internal/connect/             backend-agnostic: resolve → dial → TOFU → launch
internal/secret/              run each host command, capture value
internal/sshargs/             pure argv builders for the ssh client
internal/keys/                managed SSH keypair (~/.config/at-cove/id_ed25519)
internal/state/               per-kit state file + shared/exclusive locking
internal/runner/              Runner interface (OS impl + Fake)
```

This module builds **three binaries**: `at-cove` (the sandbox substrate, which
also hosts the `dispatch` scheduler and the one-shot `work` runner), `at-task`
(the git/PR worker), and `at-mint` (a host-side token minter invoked either as
a secret's bare `command:` or assembled by at-cove from a `minters:` profile
via `{ mint: <name> }`; see [at-mint.md](usage/at-mint.md)). The scheduler
drives work by shelling `at-cove work` — it never imports at-cove's
internals. See the [orchestration design](orchestration/INDEX.md).

A reference dispatch worker implementation lives at `kits/reference-worker/`; see `RUNBOOK.md` for the end-to-end run with `just e2e`.

## Building, testing, running

Logic lives in `scripts/` so CI never needs `just` installed.
Common tasks (`just` to list them all):

```
just build           # build both binaries into dist/<os>-<arch>/{at-cove,at-task}
just build-all       # cross-compile every supported target
just run <args>      # build the host binary, then run it with <args>
just install         # install the host binary onto your PATH (no sudo by default)
just test            # hermetic unit tests — no docker/network/ssh
just integration     # real-ssh integration tests (needs ssh/sshd/ssh-keygen)
just lint            # go vet + gofmt check + shellcheck/hadolint (if present)
just setup           # install dev/test tooling (podman, shellcheck, hadolint, jq)
```

Default `go test ./...` is **hermetic** —
every test drives the `Fake` runner,
so no Docker, network, or live VM is required.
The real-ssh integration suite (build tag `integration`) boots a throwaway `sshd` on loopback with a fake `claude`
and exercises the transports and TOFU end-to-end without Docker.

## Status and roadmap

Implemented and on `main`:
the full `build`/`create`/`chat`/`recreate`/`destroy`/`status`/`work`/`dispatch` surface
(every command's kit directory is a uniform `--kit-dir` flag, not a positional),
the Colima backend,
layered assembly with embedded hardening,
managed keypair + TOFU,
both secret transports,
the demand/supply secret model (kit `secrets:` demand-only; machine-side
`~/.config/at-cove/secrets.yml`/`secrets.local.yml` supply — see
[at-cove-secrets.md](usage/at-cove-secrets.md)),
per-kit state + locking,
subscription OAuth login,
git-over-HTTPS with a PAT,
the `at-mint` binary (`github`/`anthropic` token minting via a secret's
`command:` — see [at-mint.md](usage/at-mint.md)),
the `mint:` supply expansion — a `minters:` profile resolved through
`at-mint` by name, end to end (see
[at-cove-secrets.md](usage/at-cove-secrets.md#the-four-supply-sources)),
`chat` collaborator sessions — a `collaborators:` class selected by an
optional positional, its `prompt:` injected as role context and its
`secrets:` resolved like the agent bucket (see
[The `chat` command and collaborator sessions](#the-chat-command-and-collaborator-sessions)),
and worker/collaborator secret segregation — a `workers.<class>.secrets`
bucket (`<common>`-merged, work-only, resolved lazily right before the agent
step) that a dispatched worker's Anthropic bearer must live in instead of the
kit root, which `config.yml` now rejects as a hard error (see
[Migrating the worker bearer off the root bucket](usage/at-cove-secrets.md#migrating-the-worker-bearer-off-the-root-bucket)).

Designed but deferred (see the specs):
the `image-files/.local/` override layer,
the Firecracker and Fly backends,
and declarative repo cloning.
Further out, the [agent-orchestration design](orchestration/INDEX.md) proposes turning at-cove into a **dispatch substrate** for autonomous workers —
non-interactive `run --detach`, per-run lifecycle verbs, and per-task scoped-token minting —
a net-new direction beyond the deferred items above.

## Further reading

The authoritative design history lives under `docs/superpowers/`:

- `specs/2026-06-26-cove-sandboxes-design.md` —
  the current design (multi-backend, YAML-driven).
  Read this first.
- `specs/2026-06-22-cove-design.md` —
  the original `sbx`-wrapper design (superseded).
- `plans/` — the implementation plans.
- `CHECKPOINT-cove-sandboxes.md` —
  a detailed running log of implementation decisions and environment notes.

Usage/reference (how to run the binaries):

- [`usage/INDEX.md`](usage/INDEX.md) —
  per-binary command surface, environment, and I/O contracts (with JSON Schemas).

Forward-looking design (layers *on* at-cove, not yet built):

- [`orchestration/INDEX.md`](orchestration/INDEX.md) —
  the Linear-driven agent workflow and the at-cove work interface it needs.
