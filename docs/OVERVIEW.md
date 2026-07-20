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
   The allow-list is **additive across three tiers** — a sealed base, the kit's
   baked root list, and a per-session, per-class delta applied at session start
   (see [Egress: three additive allow-lists](#egress-three-additive-allow-lists-session-scoped)) —
   so a kit can *widen* egress for one worker or collaborator class without ever
   weakening the sealed base. A user's own kit files can never weaken this.
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
    image/            # Docker build context for image/Dockerfile (selects/builds the base); not overlaid
    .state/           # records the running instance (state.json) + lockfile (gitignored)
    .build/           # assembled build context (gitignored)
```

at-cove keeps a managed `.gitignore` in the kit covering `.build/` and `.state/` — written whenever a build context is assembled (`install`) or instance/install state is saved (`create`/`recreate`), so no command can leak those artifacts into git.

### `config.yml`

Lean — identity and wiring only.
No secret *values* and no hardening knobs (a collaborator may opt its VM into
sharing the kit repo dir via [`share-repo-dir`](usage/at-cove-config.md#collaborators)).

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
(the base to harden via `image.base` — mutually exclusive with an `image/Dockerfile` — plus additive build customization) are optional.
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

Every command takes an optional project root via the `--project-dir DIR` flag:
the kit is `<DIR>/.at-cove`, so a project root **must** hold a `.at-cove/`
(otherwise the command errors — `no .at-cove/ at project root <DIR>`). Omitted,
the command walks up from the cwd to the nearest ancestor containing `.at-cove/`
(run-from-anywhere). This encodes the "kit at the project root" convention as an
invariant. There is no positional project-dir on any command — `chat`'s one
positional is the optional collaborator (below), not the project dir.

**The install lifecycle** — `config.yml → install → install.json → run commands`.
`config.yml` is *source*; [`at-cove install`](#how-the-build-context-is-assembled)
compiles it once — resolve + **gate** the base, `docker build` + tag, freeze the
resolved run-config — into `.state/install.json`, the *compiled manifest*. Every
other command (`create`/`recreate`/`chat`, `work`/`dispatch`) reads `install.json`
(never `config.yml`), verifies it is still **current** (a cheap, offline
source-hash check), and **consumes the pre-built image** — a missing or stale
install fails fast with `run at-cove install`. There is no separate `build`
command, and no run command builds inline; the provenance gate and
`--allow-unverified-base` live only on `install`, the one place a base is built.
`at-cove uninstall` is its inverse — it removes the compiled artifact (image +
`install.json`), returning the kit to "not installed". This is distinct from
`destroy`, which tears down the running *instance* (container + volumes) but keeps
the image; only `uninstall` removes the image.

| Command | Behavior |
|---|---|
| `at-cove install [--project-dir DIR] [--allow-unverified-base]` | Compile the kit: assemble `<kit>/.build/`, then **build + gate + tag** the hardened image via the backend and freeze the resolved result into `.state/install.json`. The single build+gate path and the **only** home of `--allow-unverified-base`. `--dry-run` assembles + reports without touching docker (the old `build`'s assemble+inspect use). |
| `at-cove create [collaborator] [--project-dir DIR]` | Verify the install is current, then **run the pre-built image** from `.state/install.json` (no build — that is `install`'s job). Secret-free. Records the instance in its per-instance state file (image sourced from the manifest). The optional positional selects a `collaborators:` class, **keying the instance** (see [Per-collaborator instances](#per-collaborator-interactive-instances)); a no-collaborator kit uses the plain `Interactive` instance (`state.json`). A missing/stale install errors `run at-cove install`. The selected collaborator's [`share-repo-dir`](usage/at-cove-config.md#collaborators) picks Shared (bind-mount of the kit repo dir) vs Isolated — see [Workspace and state volumes](#workspace-and-state-volumes). |
| `at-cove chat [collaborator] [--project-dir DIR] [--raw] [--no-auth] [--fresh]` | Resolve secrets, dial the backend, verify host key (TOFU), inject env + the selected collaborator's role, launch `claude`. Run every session. Reads its run-config (collaborators, secret demands) from the current `.state/install.json` — never `config.yml`. The optional leading positional selects a `collaborators:` class (sole/`default: true`/error-if-ambiguous; omitted with none defined launches a plain session — see [below](#the-chat-command-and-collaborator-sessions)), **keying the instance** it operates on (see [Per-collaborator instances](#per-collaborator-interactive-instances)). `--raw` drops to `bash`; `--no-auth` skips the login step; `--fresh` starts a new agent session. |
| `at-cove recreate [collaborator] [--project-dir DIR]` | Destroy the resolved instance's container and **re-run the installed image** (no rebuild), **keeping the volumes** (saved login + workspace). The optional positional selects the collaborator instance, mirroring `chat`. The recorded workspace mount (shared repo dir vs isolated) is recovered from that instance's state, not re-read from config. Verifies currency first, so a stale/missing install fails before teardown. The UAT re-run loop. |
| `at-cove destroy [collaborator] [--project-dir DIR] [--all]` | Force-remove the resolved instance's container **and its volumes**, then delete its state file — teardown of the running *instance*. The optional positional selects the collaborator instance (mirroring `chat`); `--all` removes **every** instance of the kit. Unlike `create`/`recreate`/`chat`, it tolerates a stale/absent install (you can always tear down what you created). The **installed image is kept** — it is an `install` artifact, not a per-create build (a re-`install` overwrites it); removing it would break `recreate` and leave `install.json` pointing at a deleted image. To tear down the *build artifact*, use `uninstall`. |
| `at-cove uninstall [--project-dir DIR]` | The inverse of `install`: remove the compiled *build artifact* — `docker rmi` the image (via the backend) **and** delete `.state/install.json` — returning the kit to "not installed" (a later `create`/`chat` then reports `run at-cove install`). **Refuses while any created instance exists** (an instance — plain or per-collaborator — holds the image), pointing at `at-cove destroy --all` first. **Idempotent**: if `install.json` is present but the image is already gone, it still deletes the manifest (best-effort `rmi`); a not-installed kit is a friendly no-op. `--dry-run` reports the image + manifest it would remove without touching anything. It is the **only** command that removes the image (`destroy`/`recreate` never do — that was the COV-63 bug). |
| `at-cove status [collaborator] [--project-dir DIR]` | With no positional, **list every instance** of the kit (class, running state, container, workspace mode). With a collaborator positional, show just that one instance's `running` / `stopped` / `absent`. Tolerates a stale/absent install. |
| `at-cove version` | Print the build version. |
| `at-cove work [--project-dir DIR] --in <f> --out <f> [--timeout] [--grace] [--reap]` | Run one unit of work in a fresh ephemeral hardened VM. Reads `.state/install.json`, **verifies the install is current** (fails fast with `run at-cove install` if missing/stale), then runs the **pre-built installed image** — it never builds. Injects `--in` as the task, runs the **at-task worker bracket** (`prepare` → agent → `complete`) for the task's `worker.class`, extracts the result to `--out`, destroys. Scavenges crashed dispatch orphans. |
| `at-cove dispatch [--project-dir DIR]` | Poll the tracker and dispatch ready work via `at-cove work`. Reads its tracker/source-control/dispatch/workers run-config from `.state/install.json` (fails fast with `run at-cove install` if missing/stale); dispatched `work` units consume the one warm installed image — no per-unit build. |

Global `--dry-run` (before the subcommand) prints the planned actions —
exact backend/SSH argv included —
without executing anything.
Flags specific to a command (e.g. `--raw`, `--project-dir`) go *after* the
command name; each command only accepts its own flags.

Three more global flags (also before the subcommand) configure structured
logging: `--log-mode attended|unattended` (default: auto-detect via TTY),
`--log-level debug|info|warn|error` (default `info`), and `--no-log-file`
(suppress the attended-mode log file). `dispatch` and `work` wire them into a
per-run `internal/logging` logger: **attended** (TTY) mode writes human text to
stderr plus a JSON debug file under `<kit>/.state/logs/`; **unattended**
(headless — the normal way `dispatch` runs) writes JSON to stderr only, for the
platform to capture. One dispatch is correlated end-to-end — host scheduler →
`at-cove work` (via a `COVE_RUN_ID` handoff) → the VM's merged records — by a
shared `run` id plus `issue`/`class`/`step` attrs. The output-mode model, the
log-file locations, the correlation contract, and the secrets-never-in-logs
invariant are the [observability reference](usage/observability.md); the full
design is [the structured-logging design spec](superpowers/specs/2026-07-15-structured-logging-design.md).

**VM output capture, demux, and merge.** Rather than let the VM's SSH channel
spill raw and undemuxed to the host console, `dispatchrun` uses the runner's
writer-injection seam to **capture** each bracket step's output. The two streams
are **demuxed**: `at-task`'s structured JSONL arrives on **stderr** and is
**merged** into the host stream — each record re-emitted through the host logger
so it inherits the run/issue/class context, with the layer `step` grafted on; an
unparseable line is not dropped but logged at **debug** as raw, scrubbed of any
known secret value (the redaction backstop). The **agent's** raw output is
demuxed the other way: teed to a **VM-local file** (`.at-task/agent.log`) and
never shipped to the structured sink — it is only scanned in memory to classify
the run. After the bracket, `dispatchrun` emits one self-attributing
**agent-outcome** record — `ok` / `needs-input` / `error`, with `auth_failed` (a
`401`) and egress-wall (a squid denial — the same signal the [egress-wall NEEDS
INPUT detection](orchestration/at-cove-work-interface.md#egress-wall-denials-surface-as-needs-input)
uses) classification hooks — so a bare agent `401` becomes a structured,
correlated record instead of an opaque console line.
`at-task` itself emits structured `slog` JSONL to its stderr, each record tagged
with a `step` attr (`prepare`/`complete`) that self-identifies its layer and
secret-free by construction (only self-owned fields; any error text is scrubbed of
the code-host token before it enters a record), so those records merge cleanly
rather than falling to the scrubbed debug path.

### The `chat` command and collaborator sessions

`chat` is the interactive command (a hard rename of the former `connect`, no
alias). Unlike the other state-driven commands, `chat` also reads the resolved
run-config from `.state/install.json` (never `config.yml`) and verifies the
install is current — a missing or stale install is a hard error (`run at-cove
install`), since a collaborator session is defined by that run-config — and uses
its `collaborators:` tree (see
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
`dispatch` concern only. (An isolated workspace is populated by a one-time
repo clone on first session start — a `chat`-side step whose git token is kept
out of the agent's session env; see
[Workspace and state volumes](#workspace-and-state-volumes).) See
[at-cove-config.md](usage/at-cove-config.md#collaborators) for the schema and
[the orchestration work interface](orchestration/at-cove-work-interface.md)
for the dispatched-worker side of that boundary.

**Session-scoped egress.** On start — before the session is handed to the agent —
`chat` scopes the container's egress to the selected collaborator: it resolves the
class's `<common> ∪ class` allowed-domains delta from the current `install.json`
(never `config.yml`) and applies it via `ApplySessionEgress`, so the collaborator
reaches only its role's domains on top of the baked sealed + root lists. On exit it
clears the session file (a deferred clear, so it runs on error paths too), reverting
the idle persistent container to root-only rather than retaining a widened egress. A
plain session (no collaborator) applies an empty delta — root only. Only one active
class per persistent container at a time (a single interactive session) — a
documented constraint, not enforced (COV-39 §5). See the
[per-class egress design](superpowers/specs/2026-07-19-per-class-egress-design.md)
and [at-cove-config.md](usage/at-cove-config.md#collaboratorsclassallowed-domains) for
the config shape.

### State vs. config

`create` records the running instance in a per-instance state file under
`.at-cove/.state/` (`state.json` for the plain instance, `<class>.json` per
collaborator — see [Per-collaborator instances](#per-collaborator-interactive-instances));
**`chat`, `destroy`, and `status` operate on that recorded state, not on `config.yml`**
(all four additionally read the `.state/install.json` `collaborators:` tree to
resolve *which* instance a positional selects — never `config.yml`; `chat`/`create`/
`recreate` require a current install, `destroy`/`status` tolerate a stale/absent one).
A lockfile guards concurrency, **per instance**:
`chat` holds a *shared* lock on its instance for the whole session,
and `destroy` takes an *exclusive* lock on the instance it removes —
so a sandbox can't be torn down underneath a live connection, and one
collaborator's session never blocks another's.

### Per-collaborator interactive instances

When a kit defines `collaborators:`, each collaborator class is its **own
interactive VM** — its own container, state file, and volumes — so a steward and
a planner run side by side without colliding (COV-71). The five instance-aware
verbs (`create`/`chat`/`recreate`/`destroy`/`status`) all resolve the same way,
via the optional collaborator positional:

- explicit `<class>` → that class;
- omitted → the sole class, or the one marked `default: true` (error if several
  are declared with no default);
- a kit that defines **no** collaborators → the plain `Interactive` instance —
  the exact single-VM behavior from before this change, unchanged.

The resolved class keys the instance's identity (`<class>` → state file
`<class>.json`, container `<kit>-<class>`, volumes `<kit>-<class>-workspace` and
`<kit>-<class>-state`; the plain instance keeps `state.json` / `<kit>` / today's
volume names — see [Workspace and state volumes](#workspace-and-state-volumes)).
Two verbs add multi-VM ergonomics on top of that positional: `status` with no
positional **lists every instance** of the kit (rather than resolving a default),
and `destroy --all` **removes every instance**. The saved OAuth login still
propagates across a kit's VMs through the host `credentials.json` (see
[Authentication](#authentication)), so per-class `-state` volumes don't force a
re-login per collaborator. Each instance's workspace mount is decided per
collaborator by [`share-repo-dir`](usage/at-cove-config.md#collaborators), and the
[clone-on-first-session](#workspace-and-state-volumes) still populates an isolated
one.

## How the build context is assembled

`install` writes `<kit>/.build/` — the single build path. The run commands
(`create`/`recreate`/`chat` and `work`/`dispatch`) never assemble; they consume
the image `install` already built. The context
is **just the sealed layer** plus a few generated files — there is no kit overlay
anymore:

1. **Non-overridable hardening** (embedded) —
   `nftables.conf`, `squid.conf` (its three additive allow-list ACLs — base, root, session — and the empty per-session egress file the session ACL reads), sshd hardening, the entrypoint, `sshd` `AcceptEnv` config, the git credential helper, and the version-locked `at-task` binary.
2. **Generated** — the kit's **root** egress allow-list (`config.yml image.allowed-domains`, baked into `allowed_domains.kit.txt`) and the managed public key. The per-session, per-class list is delivered later at session start, not baked here (see [Egress: three additive allow-lists](#egress-three-additive-allow-lists-session-scoped)).

The kit's **`image/`** is *not* overlaid here — it is the Docker **build context**
for the kit's `image/Dockerfile`, which selects/builds the base at-cove hardens
(see the base-image section below). The **overridable startup defaults**
(`settings.json`, `.claude.json`) ship in `cove-base-image`, so a kit's Dockerfile
overrides them the normal way and the sealed layer stays purely sealed.

The hardening extracting last is the **security boundary**:
nothing a kit provides can weaken the egress lock or sshd hardening.
The hardening layer ships inside the binary via Go `embed.FS`,
so it cannot be misplaced or forgotten.
After it,
`install` writes the managed public key into the context's `authorized_keys` —
an explicit assembly step,
keeping overlay precedence pure.

### The base image and the provenance gate

The hardening `Dockerfile` is applied `FROM ${BASE}` — a build arg the backend
resolves when it builds the image (in `Backend.Install`, the single build+gate
path):

1. the kit's `image/Dockerfile` if present (at-cove builds it; the built image is the base),
2. else `config.yml image.base` (mutually exclusive with an `image/Dockerfile`),
3. else the default: the newest **blessed** `cove-base-image`, pinned by digest.

A **kit-chosen** base (1 or 2) must pass a **provenance gate**: at-cove reads the
resolved image's OCI rootfs `diff_ids` and asserts some blessed
`cove-base-image`'s layers are their exact prefix — i.e. it was really built
*FROM* a blessed base, unforgeably (matching the prefix means those bottom layers
*are* that image, byte-for-byte). The blessed digests are embedded
(`internal/basedigest`, a rolling set); the default base (the head) is blessed by
construction, so it skips the gate. A base that descends from no blessed image is
**rejected**, unless `at-cove install --allow-unverified-base` downgrades the
rejection to a loud warning — the gate and that flag live only on `install`, the
one place a base is built. This lets hardening *trust* its prerequisites (the egress stack, the
`agent` user, the expected layout) rather than probe for them. The gate lives in
`internal/baseimage` (pure prefix logic) with the docker execution behind the
backend seam; the full model is in
[the design spec](superpowers/specs/2026-07-16-kit-selectable-base-image-design.md).

**How the blessed set is maintained — low-watermark + registry snapshot.** The
repo commits exactly one digest in `internal/basedigest/blessed/watermark.txt`:
the **low-watermark**, the oldest `cove-base-image` still trusted. Before `go
build`, `cmd/gen-blessed` lists the published `cove-base-image` digests from the
registry and walks newest→oldest **through** the watermark, writing that list to
the sibling `generated.txt` (gitignored) for `go:embed` (`internal/blessgen` holds
the pure walk; the GHCR query sits behind a seam). `basedigest.Blessed` prefers
`generated.txt` and falls back to the committed watermark, so a routine base
republish is trusted automatically with no commit-back loop, a breaking base
change is a one-line watermark bump that drops everything older, and a fresh clone
or offline build (no `GITHUB_TOKEN`) still compiles and trusts the watermark
alone. A watermark absent from the registry **fails the build loudly**. The full
model is in [the release-pipeline spec](superpowers/specs/2026-07-17-monolithic-release-pipeline-design.md#4-blessing-the-low-watermark--the-registry).

Because hardening trusts the base, `cove-base-image` carries the overridable
startup defaults every sandbox needs (`settings.json`, `.claude.json` in
`/home/agent/.init-agent-data`). The sealed layer then, last: installs the
embedded version-locked `at-task`; populates `/etc/environment` (so `pam_env`
exposes it to every SSH session) via `apply-sshenv.sh`; and re-asserts the
egress/sshd hardening.

**Session env — `COVE_SSHENV`.** SSH sessions read `/etc/environment`, which a
bare `docker run` / CI container does *not* — so the toolchain an image sets via
`ENV` (e.g. `PATH` with `/usr/local/go/bin`) wouldn't reach sessions without help.
`apply-sshenv.sh` bridges the gap: it copies the image's live `PATH` (intrinsic)
plus every variable named in the image's `COVE_SSHENV` (colon-separated) into
`/etc/environment`. So one `ENV` statement feeds both `docker run`/CI and SSH
sessions — no separate fragment to keep in sync. The egress proxy vars and
`CLAUDE_CONFIG_DIR` are the exception: the sealed layer writes them itself (never
image `ENV`, which would poison the build), last, so they always win.

### Egress: three additive allow-lists, session-scoped

The sealed `squid.conf` is default-deny with **three additive allow-list ACLs** — a
request passes if it matches **any**, so a kit can only *widen* egress, never bypass
the sealed base or the `nftables` lock:

| file | source | when | scope |
|---|---|---|---|
| `allowed_domains.txt` | sealed hardening | baked | base, unconditional |
| `allowed_domains.kit.txt` | `config.yml image.allowed-domains` (root) | baked at `install` | every session |
| `allowed_domains.session.txt` | `<common> ∪ class` **delta** | delivered per session | this session's handler class |

So a class's effective egress is the union **`root ∪ <common> ∪ class`**: root is
baked into *every* session, and only the per-class *delta* is delivered at session
start, so no domain is written twice. The session file is **baked empty** (header-only)
by the hardening layer, so the ACL never dangles and a no-class session (`create`)
simply stays root-only. See [`at-cove-config.md`](usage/at-cove-config.md#imageallowed-domains)
for the config shape and union semantics.

**Applied at session start, privileged — not baked.** Per-class egress is *not* baked
into the image; the one warm image stays class-agnostic (COV-38). at-cove resolves the
class's delta from the currency-pinned `install.json` (never a live `config.yml`) and
applies it to the running container via the backend's `ApplySessionEgress` — a host
`docker exec` (as root) of the sealed `apply-session-domains.sh`, which overwrites the
session file from stdin and runs `squid -k reconfigure` (squid re-reads its ACL files
without dropping the process). The non-root `agent` workload reaches the VM only over
SSH and cannot run this, so it can never widen its own egress. The ephemeral
(`work`/`dispatch`) path applies the worker class's delta **before the agent step**;
the persistent (`chat`) path applies the collaborator's on start and **clears it on
exit**, so an idle container reverts to root-only. Full model:
[the per-class egress design](superpowers/specs/2026-07-19-per-class-egress-design.md);
the per-command wiring is under [The `chat` command](#the-chat-command-and-collaborator-sessions),
[Backends](#backends), and [the work interface](orchestration/at-cove-work-interface.md).

## Workspace and state volumes

The working directory is realized one of two ways, chosen per collaborator at
`create` time by the selected class's
[`share-repo-dir`](usage/at-cove-config.md#collaborators):

- **Isolated (default — `share-repo-dir` absent/false)** —
  a backend-managed volume; `chat` clones the repo's default branch into it on
  the **first** collaborator session (see below), so the checkout is ready when
  the chat opens.
- **Shared (`share-repo-dir: true`)** —
  a bind-mount of the **kit's repo dir** (the directory that contains `.at-cove/`),
  so host and VM share the live `.git`. Only that dir is shareable — arbitrary host
  paths are not mountable (the old `--workspace`/`--ws` flag is gone).

**Clone-on-first-session (isolated only).** An isolated `create` leaves the
workspace volume empty, so the first `chat` populates it: it clones the target
`source-control.github` repo (`https://github.com/<owner>/<name>.git`) on its
`main-branch` into `/home/agent/workspace`, **once for the VM's lifetime**. A
reconnect that finds an existing checkout reuses it verbatim (in-progress work is
never re-cloned or clobbered) — the guard is a `.git` probe on session start.
Auth reuses the worker path's git plumbing: `chat` resolves the
`source-control.github.secrets.AT_TASK_GIT_TOKEN` demand host-side (like `work`
does) and the token flows into the VM via the same env-only askpass as
`internal/dispatch/worker/git.go` — never on disk, argv, or in the **agent's
session env** (the code-host air-gap: a collaborator reaches GitHub through the
human's claude.ai connectors, not this token). If `source-control` (or the
`AT_TASK_GIT_TOKEN` demand) is not configured, the clone is **skipped** and the
session starts in an empty workspace (the prior behavior); if it *is* configured
but the clone **fails**, session start is a **hard error** (fail closed). A
`share-repo-dir` shared workspace is never cloned — the host checkout is shared live.

A second volume, **`<name>-state`**, is always a persistent backend volume mounted at `/agent-data` (`CLAUDE_CONFIG_DIR`).
It preserves Claude session history and the saved OAuth login across recreates,
and is seeded once (guarded by a `.seeded` marker).

**Volume naming is per instance.** Here `<name>` is the *instance* name, not
always the bare kit name: a collaborator instance keys its volumes
`<kit>-<class>-workspace` / `<kit>-<class>-state` (container `<kit>-<class>`),
while a no-collaborator kit keeps `<kit>-workspace` / `<kit>-state` (container
`<kit>`) — see [Per-collaborator instances](#per-collaborator-interactive-instances).
The saved login still crosses a kit's VMs via the host `credentials.json`, so a
per-class `-state` volume is not a per-class re-login.

> **No migration when adopting collaborators.** A kit that previously ran as a
> plain single VM recorded its state in `state.json` (container = the bare kit
> name). Once you add `collaborators:` and re-`install`, `create`/`chat` target
> the class-keyed instances instead, leaving that pre-existing VM **orphaned**
> (its `state.json` is never touched by the class verbs; only `destroy --all`,
> which enumerates state files, still reaches it). There is deliberately **no
> migration code** — `destroy` (or `destroy --all`) the old instance **before**
> upgrading, then `create` the collaborator instances fresh.

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
demands. Unlike a general secret demand, a worker with *neither* bearer
declared-and-resolved is not a warn-and-continue: `at-cove work` **fails closed
on the host**, before launching a VM, naming the bearer names it
looked for and the kit — a keyless worker is a guaranteed 401, so at-cove
refuses to launch a doomed container. The gate accepts
either well-known name, so a class declaring only `ANTHROPIC_API_KEY` clears it.
As a second, independent layer, at-cove also deliberately
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
  `Install` resolves + gates the base and `docker build`s the assembled context (the single build site);
  `Create` is **run-only** — `docker run -d` the pre-built image with `NET_ADMIN`,
  the state + workspace volumes,
  and a published `localhost:<port>` mapped to the in-VM `sshd`.
  `Dial` returns that port;
  `Destroy` is `docker rm -f` (volumes retained).
  Also implements `backend.DispatchOps` (ephemeral labeled runs + scavenge) for `dispatch`,
  and `backend.SessionEgress` — `ApplySessionEgress` `docker exec`s the sealed
  `apply-session-domains.sh` (as root, domains piped on stdin) to apply a session's
  per-class egress delta + `squid -k reconfigure` (COV-39 §5). The ephemeral
  (`work`/`dispatch`) path wires it in before the agent step (see
  [the work interface](orchestration/at-cove-work-interface.md)); the persistent
  (`chat`) path applies the selected collaborator's delta on start and clears it
  on exit (see [The `chat` command and collaborator sessions](#the-chat-command-and-collaborator-sessions)).
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
internal/dispatchrun/         `at-cove work` orchestration (scavenge → run → inject → exec → extract → destroy) + VM output capture/demux/merge (below)
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
internal/backend/colima/      Colima impl: Install (build+gate+tag) / run / inspect / rm
internal/connect/             backend-agnostic: resolve → dial → TOFU → launch
internal/secret/              run each host command, capture value
internal/sshargs/             pure argv builders for the ssh client
internal/keys/                managed SSH keypair (~/.config/at-cove/id_ed25519)
internal/state/               per-kit state file + shared/exclusive locking
internal/install/             install.json manifest: Compile + currency hash + read/write (pure); written by `at-cove install`
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

The sandbox images are built from a shared, pinned, multi-arch **image tree**
(`cove-base-image` → `cove-image`) published to GHCR and consumed by both CI and
the sandboxes — see [DEVELOPMENT.md](DEVELOPMENT.md#the-image-tree). The images,
the blessed-base snapshot, and the at-cove binaries are all built by **one
monolithic continuous-delivery pipeline** on every push to `main`, which decides
from the repo diff what to rebuild and tags everything `<N>-<MMDD>` — see
[the release pipeline](DEVELOPMENT.md#ci--the-release-pipeline).

## Status and roadmap

Implemented and on `main`:
the full `install`/`uninstall`/`create`/`chat`/`recreate`/`destroy`/`status`/`work`/`dispatch` surface
(every command's project root is a uniform `--project-dir` flag, not a positional),
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
per-collaborator interactive instances — each class its own VM/state/volumes,
with `status` listing all and `destroy --all` removing all (see
[Per-collaborator instances](#per-collaborator-interactive-instances)),
and worker/collaborator secret segregation — a `workers.<class>.secrets`
bucket (`<common>`-merged, work-only, resolved lazily right before the agent
step) that a dispatched worker's Anthropic bearer must live in instead of the
kit root, which `config.yml` now rejects as a hard error (see
[Migrating the worker bearer off the root bucket](usage/at-cove-secrets.md#migrating-the-worker-bearer-off-the-root-bucket)).

Designed but deferred (see the specs):
the `.local/image/` override layer,
the Firecracker and Fly backends,
and fully declarative (config-driven, multi-repo) cloning — the isolated
workspace is already auto-cloned on the first collaborator session (see
[Workspace and state volumes](#workspace-and-state-volumes)).
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
