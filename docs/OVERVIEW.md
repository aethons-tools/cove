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
   they are resolved on the host just-in-time at connect,
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
Once a backend hands `connect` a `host:port` and a key,
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

`at-cove build` writes a managed `.gitignore` into the kit covering `.build/` and `.state/`.

### `config.yml`

Lean — identity and wiring only.
No secret *values*, no hardening knobs, no workspace mode.

```yaml
name: claude-on-myrepo          # sandbox/VM name; also keys the per-sandbox known_hosts
```

`name` is the only required field;
`secrets`, `workers` (the classes `at-cove dispatch` can launch), and `image`
(additive build customization) are optional.
The full field-by-field schema, validation, and a complete example live in
[`docs/usage/at-cove-config.md`](usage/at-cove-config.md);
secret declaration and value resolution (including `~/.config/at-cove/secrets.yml`) in
[`docs/usage/at-cove-secrets.md`](usage/at-cove-secrets.md).

> **Security caveat (current state):**
> a secret resolver `command` lives in the committed `config.yml`,
> so it is a host-execution vector —
> only run `at-cove connect` against repos you **trust** (your own).
> The planned `.local/` layer will move `command` out of the committed file.
> See [at-cove-secrets.md](usage/at-cove-secrets.md).

## Command surface

Every command takes an optional kit directory (otherwise discovered by cwd walk-up).

| Command | Behavior |
|---|---|
| `at-cove build [kit-dir]` | Assemble `<kit>/.build/` from the overlays and inject the managed public key. No backend, no VM — for authoring/inspection. |
| `at-cove create [kit-dir] [--workspace\|--ws <path>]` | `build`, then create the VM via the backend. Secret-free. Records the instance in `.state/state.json`. `--workspace` selects Shared (bind-mount) mode. |
| `at-cove connect [kit-dir] [--raw] [--no-auth]` | Resolve secrets, dial the backend, verify host key (TOFU), inject env, launch `claude`. Run every session. `--raw` drops to `bash`; `--no-auth` skips the login step. |
| `at-cove recreate [kit-dir] [--workspace\|--ws <path>]` | Destroy the container and create it again, **keeping the volumes** (saved login + workspace). The UAT rebuild loop. |
| `at-cove destroy [kit-dir]` | Remove the container (volumes retained) and image, then delete the state file. |
| `at-cove status [kit-dir]` | Report `running` / `stopped` / `absent`. |
| `at-cove version` | Print the build version. |
| `at-cove dispatch <kit> --in <f> --out <f> [--timeout] [--grace] [--reap]` | Run one unit of work in a fresh ephemeral hardened VM: inject `--in` at the kit's `dispatch.input` VM path, run the kit's `dispatch.command`, extract `dispatch.output` to `--out`, destroy. Scavenges crashed dispatch orphans. |

Global `--dry-run` (before the subcommand) prints the planned actions —
exact backend/SSH argv included —
without executing anything.
Flags specific to a command (e.g. `--raw`, `--ws`) go *after* the
command name; each command only accepts its own flags.

### State vs. config

`create` records the running instance in `.at-cove/.state/state.json`;
**`connect`, `destroy`, and `status` operate on that recorded state, not on `config.yml`.**
A lockfile guards concurrency:
`connect` holds a *shared* lock for the whole session,
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

## Secret injection (the `connect` data flow)

Backend-agnostic, in `internal/connect`:

1. **Resolve secrets** on the host (run each `command`, capture stdout).
   Held in memory only;
   any failure aborts before SSH.
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

The VM ships managed settings with `forceLoginMethod=claudeai`,
so the agent authenticates via **claude.ai subscription OAuth**,
not an API key.
On the first session `connect` probes `claude auth status` over SSH
and runs interactive `claude auth login --claudeai` only when needed;
the credentials persist on the `/agent-data` volume,
so subsequent connects skip it.

To make one login reusable across sandboxes (and across recreates),
`connect` keeps a host-side copy at `~/.config/at-cove/credentials.json` (mode `0600`):
it **seeds** that file into the VM (`/agent-data/.credentials.json`) *before* the auth probe,
so a login obtained on any sandbox validates the next one without re-prompting,
and it **saves** the VM's copy back to the host after a fresh login
or whenever a session rotates the token —
keeping the shared copy current as the OAuth refresh token rolls over.
When the credentials finally expire, the probe fails and the normal login flow re-mints them.
`--no-auth` skips this entirely.

> **Note:** this writes your subscription credentials to the host disk
> (distinct from injected *secrets*, which stay memory-only).
> The file is the user's own OAuth tokens, in the user-owned config dir at `0600` —
> the same trust boundary as the saved login already on the VM volume.

> **Implication:** a kit must **not** inject `ANTHROPIC_API_KEY` on this path —
> managed settings block startup if it is present.
> A `GITHUB_TOKEN` secret is fine,
> and is what enables private-repo git over HTTPS
> (SSH git / port 22 is blocked by the egress lock;
> `/etc/gitconfig` rewrites GitHub remotes to HTTPS
> and a credential helper feeds the token from the session env, memory-only).

## Backends

A `Backend` knows how to provision a VM from a build context,
report a reachable SSH endpoint,
query its state,
and destroy it.
Backends self-register into a registry keyed by the `backend:` string.

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
cmd/at-cove/                  at-cove entry: parse argv, discover kit, select backend, dispatch
internal/dispatchrun/         `at-cove dispatch` orchestration (scavenge → run → inject → exec → extract → destroy)
cmd/at-dispatch/              at-dispatch entry: version + serve --config (runs the scheduler)
internal/dispatch/            dispatcher control plane (doc-only today; owned by docs/orchestration/)
internal/dispatch/config/     at-dispatch config: YAML schema, secret resolution, load/validate
internal/dispatch/scheduler/  scheduler engine (poll → claim → dispatch via at-cove → broker) + Tracker/Executor interfaces
internal/dispatch/linear/     real Tracker: Linear GraphQL client (live calls behind the integration tag)
internal/dispatch/exec/       real Executor: headless command run with injected env + timeout
cmd/at-work/                  at-work entry: prepare / complete (git/PR worker)
internal/dispatch/worker/     at-work orchestration: Prepare + Complete, Git/CodeHost interfaces
internal/dispatch/github/     at-work's real CodeHost: GitHub PR client (live calls behind the integration tag)
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

This module builds **two binaries**. `at-cove` is the sandbox substrate.
`at-dispatch` is a **separate executable** that *consumes* the `at-cove` CLI
(it never imports at-cove's internals) to schedule Linear-driven work onto
sandboxes — see the [orchestration design](orchestration/INDEX.md). It is a
skeleton today.

A reference dispatch worker implementation lives at `kits/reference-worker/`; see `RUNBOOK.md` for the end-to-end run with `just e2e`.

## Building, testing, running

Logic lives in `scripts/` so CI never needs `just` installed.
Common tasks (`just` to list them all):

```
just build           # build both binaries into dist/<os>-<arch>/{at-cove,at-dispatch}
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
the full `build`/`create`/`connect`/`recreate`/`destroy`/`status` surface,
the Colima backend,
layered assembly with embedded hardening,
managed keypair + TOFU,
both secret transports,
per-kit state + locking,
subscription OAuth login,
and git-over-HTTPS with a PAT.

Designed but deferred (see the specs):
the `.local/` override layer (and with it, moving secret `command`s out of committed config),
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
  the Linear-driven agent workflow and the at-cove dispatch interface it needs.
