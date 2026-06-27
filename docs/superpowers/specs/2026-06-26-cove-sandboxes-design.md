# cove — YAML-driven multi-backend sandboxes — Design

**Date:** 2026-06-26
**Status:** Proposed (pre-implementation)
**Repo:** `aethons-tools/cove` (binary `at-cove`)
**Supersedes (extends):** `2026-06-22-cove-design.md`

## 1. Purpose

Turn `cove` from an `sbx` wrapper
into a small, dependency-light tool
that runs hardened Claude Code sandboxes across multiple VM backends
from a single YAML description.
The intended user experience:

1. Add a `.at-cove/` kit directory whose `config.yml` describes the sandbox
   (name, backend, secrets it needs).
2. `cove create` (run in the repo) provisions the VM.
3. `cove connect` SSHes in,
   injects secrets as environment variables,
   and launches `claude` interactively.

The governing principle is **SSH as the universal interface**:
backends differ only in how a VM is provisioned and how its `sshd` is reached.
Once a backend hands `connect` a `host:port` plus a key,
everything downstream — host-key trust, secret injection, launching `claude` —
is identical across backends.

## 2. Scope

**In scope (this spec):**

- The `Backend` interface and a backend registry.
- The **Colima** backend, end to end (native Docker; no `sbx`).
- The layered `.build` assembly from files embedded in the binary.
- Commands: `build`, `create`, `connect`, `destroy`, `status`.
- Auto-managed SSH keypair,
  per-sandbox host-key TOFU,
  and just-in-time secret resolution + memory-only env injection.

**Out of scope (follow-on specs / documented only):**

- **Firecracker** and **Fly machines** backends
  (the interface is designed so each is "provision + reach `sshd`" and nothing more).
- In-VM **git PAT / `insteadOf`** auto-configuration
  (captured in Appendix A as the manual technique; `connect` injects env only).
- Declarative **repo cloning** into an isolated workspace
  (Appendix B; for now the agent clones once secrets are present).
- The **`.local/` override layer** — the slot is reserved and documented (§3.6),
  but its merge semantics are deferred.
- `envsubst` templating (removed — see §6.3).

## 3. Concepts

### 3.1 Backends

A backend knows how to provision a VM from an assembled build context,
report a reachable SSH endpoint for it,
query its state,
and destroy it.
The first and only implemented backend is **Colima** (local Docker via Colima).
Firecracker and Fly come later behind the same interface.

### 3.2 The kit is a directory

The kit is **always a directory**, by convention `.at-cove/` at the repo root:

```
repo/
  .at-cove/
    config.yml        # the spec: name, backend, secrets (§4)
    image-files/      # committed local overrides (layer 2), overlaid onto the VM root
    .local/           # DEFERRED override layer (§3.7)
      config.yml
      image-files/
    .build/           # assembled context output (gitignored)
```

`config.yml` lives inside the kit;
`image-files/` is the conventional source of the user's local overrides,
mirroring `claude-code-oci`'s `image-files/ → /` overlay.
`cove` writes a `.gitignore` into `.at-cove/` covering `.build/` and `.local/`.

**Discovery.**
A command with no kit path **walks up from the cwd to the nearest `.at-cove/`**
(git-like), so it works from subdirectories;
an explicit kit-directory path on the command line overrides discovery.

### 3.3 The build context, as layered overlays

There is no separate "base image" to manage.
Each `create` assembles a fresh build context in `<kit>/.build/`
by stacking overlays, **last writer wins**:

1. **Overridable defaults** — extracted from the binary first.
   Sensible defaults the user may replace:
   `CLAUDE.md`, `settings.json`, stock skills, a default entrypoint, etc.
2. **Local files** — the kit's `image-files/`, copied second;
   they shadow any default at the same path.
3. *(deferred)* **`.local/image-files/`** — uncommitted overrides (§3.7);
   the slot sits here but is inert until its merge semantics are designed.
4. **Non-overridable hardening** — extracted from the binary **last**,
   so it overwrites anything beneath it:
   `nftables.conf`, `squid.conf`, sshd hardening,
   the security-critical entrypoint, and `sshd` `AcceptEnv` config.

The final layer extracting last is a **security boundary**:
a user's local files can never weaken the egress lock or sshd hardening,
because the locked files always land on top.
Both embedded layers ship inside the `cove` binary via Go `embed.FS`
(vendored from `agent-infrastructure` at `cove` build time),
so the hardening cannot be misplaced or forgotten — a foolproof default.

After the overlays, `create` performs one programmatic step:
it writes the **managed public key**
into the context's `home/agent/.ssh/authorized_keys` (§3.6).
This is an explicit assembly step owned by `cove`,
not a templated or user-managed file,
which keeps the overlay precedence pure.

### 3.4 Workspace modes (create-time)

The working directory is realized one of two ways,
chosen at `create` time, not in `config.yml`
(so a spec is portable across machines):

- **Isolated (default)** — `workspace` is a backend-managed volume.
  No host folder.
  The agent clones the repo in once secrets are present.
- **Shared** — `workspace` is a bind-mount of a host folder,
  selected by `--workspace <path>` (alias `--ws`).
  This is the "mapped repo" case
  (host and VM share `.git/config`; see Appendix A).
  **Rejected on the Fly backend** (no host to bind).

A second conventional volume, **`agent-state`**,
is always a backend-managed persistent volume mounted at `/agent-data`
(`CLAUDE_CONFIG_DIR`), seeded from the image's `.init-agent-data`.
It preserves Claude session history/config
and is the hedge against ephemeral rootfs on Fly.
It never appears in `config.yml`.

### 3.5 Secrets

`config.yml` declares secrets **by name plus a host command** that produces the value.
Values are **resolved just in time** at `connect`
(never at `create`, which stays secret-free)
by running the command and capturing stdout.
Nothing is written to disk.
Values are injected into the VM session as environment variables (§7.3).
The agent inside the VM is explicitly *not* a threat in this model;
the protected surface is the host
(no values in `ps`/shell history)
and at-rest storage (no value ever persisted).

**Security — resolver commands are an execution vector.**
A `secrets[].command` runs on the **host** at `connect`.
If it ships in a committed `config.yml`,
then cloning an untrusted repo and running `cove connect`
executes arbitrary commands chosen by the repo author —
a command-injection / supply-chain risk.

Today (before `.local`), the `command` lives in `config.yml`,
which is safe **only for repos you trust** (your own); this is a stated assumption.
**When the `.local/` layer lands (§3.7), the rule becomes:**
a committed `config.yml` may declare each secret's `name`
(and an optional human-readable `description`) **but not its `command`**;
the `command` may come **only** from the source-control-excluded `.local/config.yml`.
A `command` present in a committed `config.yml` is then **rejected**.
This guarantees the only resolver a repo can trigger
is one the local user authored on their own machine.

### 3.6 Managed SSH keypair

`cove` owns a dedicated keypair at `~/.config/at-cove/id_ed25519`,
created on first use.
Its public half is written into every build context's `authorized_keys` during `create`;
`connect` authenticates with the private half.
The user never handles SSH keys.

### 3.7 The `.local/` override layer (deferred)

`.at-cove/.local/` is a **source-control-excluded** override layer:
a `config.yml` and an `image-files/` tree that a developer keeps off VCS
(machine-specific tweaks, personal defaults).
It slots into the precedence between the committed `image-files/`
and the non-overridable hardening (§3.3).

It is **documented now but deferred**: the hard part is the **merge semantics**
(how `.local/config.yml` blends with the committed `config.yml`, list vs scalar
override rules, deep vs shallow) — the class of feature GitLab's CI
`include`/`override` history shows is easy to get badly wrong.
We name the slot and exclude it from source control today;
the blending rules are designed in a later spec.

When this layer lands it becomes the **only** source of secret resolver
`command`s (§3.5): committed `config.yml` carries secret `name`/`description`,
`.local/config.yml` carries the `command`.
That is the primary motivation for getting `.local` right,
not just a convenience for machine-specific overrides.

## 4. The `config.yml` schema

`config.yml` lives inside the kit directory (`.at-cove/config.yml`).
Lean: identity and wiring only.
No secret values, no hardening knobs, no workspace mode, no local-files path
(the kit's `image-files/` is the layer-2 source by convention — §3.2).

```yaml
name: claude-on-myrepo          # sandbox/VM name; also keys per-sandbox known_hosts
backend: colima                 # colima | firecracker | fly  (only colima now)

secrets:                        # declared by NAME; values resolved at connect time
  - name: GITHUB_TOKEN
    # command is TRUSTED today; moves to .local/config.yml once supported (§3.5)
    command: ["op", "read", "op://Personal/github-pat/token"]
  - name: ANTHROPIC_API_KEY
    description: Anthropic API key   # human-readable; safe to commit
    command: ["pass", "show", "anthropic/api-key"]
```

Rules:

- `name` and `backend` are required.
  Unknown `backend` → clear error listing supported backends.
- `secrets[].command` is an **argv array**, not a shell string —
  no quoting surprises;
  executed directly, stdout captured, trailing newline trimmed.
  A nonzero exit aborts `connect` before any SSH happens (fail closed).
- `secrets[].description` is an optional human-readable string.
  Today (pre-`.local`) `command` lives here and is **trusted** —
  only run `connect` against repos you trust (§3.5).
  Once `.local/` lands, `command` moves out: committed `config.yml` keeps
  `name`/`description`, `.local/config.yml` supplies `command`,
  and a committed `command` is rejected.
- The file contains no secret values and is safe to commit.

## 5. Command surface

Every command takes an optional **kit directory**;
omitted, it is discovered by walking up from cwd to the nearest `.at-cove/` (§3.2).

| Command | Behavior |
|---|---|
| `cove build [kit-dir]` | Assemble `<kit>/.build/` from the overlays and inject the managed public key. No backend, no VM. For authoring/inspection. |
| `cove create [kit-dir] [--workspace/--ws <path>]` | `build`, then hand the `.build` path to the backend to create the VM. Secret-free. `--workspace <path>` selects Shared mode (hard error on Fly). |
| `cove connect [kit-dir]` | Resolve secrets, `Dial` the backend for an endpoint, verify host key (TOFU), inject env, launch `claude` interactively. Run every session. |
| `cove destroy [kit-dir]` | Backend `Destroy` for the sandbox's VM. |
| `cove status [kit-dir]` | Backend `GetStatus`: `Running` / `Stopped` / `Absent`. |

`--dry-run` remains global (before or after the subcommand)
and prints planned actions — including exact backend/SSH commands —
without executing.

## 6. Architecture

### 6.1 Package layout

Extends the existing pure-plan / execution split.

```
main.go                       parse argv, discover kit, select backend, dispatch
internal/kit/                 locate kit dir (cwd walk-up); load+validate config.yml (pure)
internal/assemble/            layered .build assembly from embed.FS; key injection
  (embeds)                    overridable/ and hardening/ file trees (vendored)
internal/backend/             Backend interface + registry
  internal/backend/colima/    Colima impl: docker build / run / inspect / rm
internal/connect/             backend-AGNOSTIC: secret resolve -> transport -> TOFU -> launch
internal/secret/              resolver: run each host command, capture value (Runner-injected)
internal/sshargs/             pure argv builders for the ssh client
internal/runner/              existing Runner interface (OS impl + Fake)
```

Dependency direction:
`kit`, `sshargs`, `secret` are leaves;
`assemble` uses `embed.FS` + `runner`;
`backend/*` use `runner`;
`connect` uses `secret`, `sshargs`, `backend`, `runner`;
`main` wires all.
No cycles.

`internal/kit/template.go` (`Substitute`) is **removed** (§6.3).

### 6.2 The Backend interface

```go
type State int // Absent, Stopped, Running

type WorkspaceMode int // Isolated, Shared

type WorkspaceMount struct {
    Mode     WorkspaceMode
    HostPath string // set iff Shared
}

type Endpoint struct {
    Host string
    Port int
    User string
}

type CreateContext struct {
    Name      string
    BuildDir  string         // the assembled .build context
    Workspace WorkspaceMount
}

type Backend interface {
    Create(ctx CreateContext) error
    Dial(name string) (Endpoint, func(), error) // addr + cleanup (tears down any tunnel)
    Destroy(name string) error
    GetStatus(name string) (State, error)
}
```

`Dial` returns a `cleanup func()` so tunnel-based backends fit uniformly:
Colima returns a published `localhost:port` with a no-op cleanup;
Fly will later return a local port backed by a `fly proxy` child whose cleanup kills it.
`connect` treats both identically.

Backends self-register into a registry keyed by `backend:` string.

### 6.3 No templating

`envsubst` was a concession to how `sbx` needed secrets passed.
Secrets now flow at `connect` time,
so no build-time substitution is needed.
`build`/`create` are **pure layered file copies**;
non-overridable files are copied **verbatim**
so a stray `$VAR` can never perturb the hardening.

## 7. The `connect` data flow

Backend-agnostic, in `internal/connect`:

1. **Resolve secrets.**
   For each `secrets[]` entry,
   run its `command` via the `Runner`,
   capture stdout, trim the trailing newline.
   Any nonzero exit aborts here — before any SSH.
   Values are held in memory only.
2. **Dial.**
   `backend.Dial(name)` → `Endpoint` + `cleanup`; `defer cleanup()`.
   If `GetStatus` is not `Running`,
   return an actionable error
   ("run `cove create` / start the VM first").
3. **Verify host key (TOFU).**
   Use a per-sandbox known_hosts file at `~/.config/at-cove/known_hosts.d/<name>`
   with `StrictHostKeyChecking=accept-new`.
   First connection pins the key; later mismatches fail loudly.
   Destroying and recreating the VM resets the pin.
   No cross-VM `localhost:port` collisions.
4. **Inject env + launch.**
   Transport the resolved values into the session
   and `exec claude` interactively (§7.3).
   Authenticate with the managed private key.

### 7.1 SSH invocation basics

`connect` drives the `ssh` client directly
(argv built in `internal/sshargs`):
`-i ~/.config/at-cove/id_ed25519`, `-p <port>`, `agent@<host>`,
the per-sandbox `UserKnownHostsFile`, and `StrictHostKeyChecking=accept-new`.

### 7.2 Transport interface

Env transport is pluggable so the primary and fallback are both real:

```go
type Transport interface {
    // Launch runs `claude` interactively on the endpoint with env injected.
    Launch(ep Endpoint, env map[string]string, sshBaseArgs []string) error
}
```

### 7.3 Transports

- **Primary — stdin export-script (memory-only, no argv exposure).**
  Inject via a transient tmpfs file:
  a first non-interactive `ssh` writes `export NAME=...` lines
  (fed over **stdin**, so values never appear on any argv)
  to `/dev/shm/cove-env-<rand>` (mode 600);
  a second interactive `ssh -tt` does `set -a; . <file>; rm -f <file>; exec claude`.
  Values touch only tmpfs (RAM) and are removed immediately after sourcing.
  Keeps the interactive TTY clean for `claude`.
- **Fallback — `SendEnv` (proven, simple).**
  `cove` places resolved values in its **own** process environment,
  then runs `ssh -tt` with `SendEnv NAME` per secret.
  Values never appear on any argv;
  the VM's `sshd` accepts them via an `AcceptEnv` allowlist
  shipped in the **non-overridable** layer.
  Interactive TTY works directly.

Both keep values off every command line
(no host `ps`/history leak)
and off disk.
The implementation starts with whichever lands cleanly first;
the interface guarantees the fallback is a drop-in.

## 8. The Colima backend

Native Docker (Colima provides the Docker host),
mirroring the working `claude-code-oci` recipes — no `sbx`.

- **Create:** `docker build -t cove/<name> <BuildDir>`
  (the assembled context includes the `Dockerfile` from the embedded layers),
  then `docker run -d --name <name> --init --cap-add=NET_ADMIN --dns 1.1.1.1 -p <hostport>:2222`
  with volumes:
  - `agent-state`: `-v <name>-state:/agent-data`
  - workspace: Isolated → `-v <name>-workspace:/home/agent/workspace`;
    Shared → `-v <abs hostpath>:/home/agent/workspace`.
  The host port is allocated and recorded so `Dial` can report it.
- **Dial:** report `localhost:<hostport>`, user `agent`; cleanup is a no-op.
- **GetStatus:** `docker inspect` → Running / Stopped / Absent.
- **Destroy:** `docker rm -f <name>`
  (volumes retained unless explicitly pruned — out of scope to auto-delete).

No secrets are passed at `docker run`
(the old `--env-file secrets.env` path is gone);
secrets arrive only via `connect`.

## 9. Error handling

- Unknown `backend:` → error listing supported backends; non-zero exit.
- No `.at-cove/` found on cwd walk-up (and none given) → actionable error.
- Missing/invalid `config.yml`, missing required fields → usage + non-zero exit.
- `--workspace` on the Fly backend → hard error
  (documented; enforced once Fly exists).
- Any secret command failing (nonzero exit) → abort `connect` before SSH,
  naming the failed secret; never partially inject.
- *(once `.local/` lands)* a `command` in a committed `config.yml` → rejected,
  pointing the user to declare it in `.local/config.yml` (§3.5).
- VM not `Running` at `connect` → actionable message to `create`/start first.
- Host-key mismatch on reconnect → loud failure (do not auto-accept).
- Backend/`ssh` non-zero exit → propagate the exit code;
  stream child stdout/stderr live.

## 10. Testing (TDD)

All tests run without Docker/Colima or a live VM, via the `Fake` runner.

- **kit:** kit discovery (cwd walk-up; explicit path overrides);
  valid/invalid `config.yml`, required fields, unknown backend,
  secrets parsed as argv arrays.
- **assemble:** active layers compose with correct last-writer-wins precedence
  (overridable defaults < kit `image-files/` < non-overridable hardening);
  non-overridable files always win over local files and defaults;
  public key injected into `authorized_keys`;
  verbatim copy (no substitution).
- **secret:** resolver runs each command and trims output;
  nonzero exit aborts; values not logged.
- **sshargs:** exact argv incl. identity file, per-sandbox known_hosts,
  `accept-new`, port/user.
- **connect:** ordering (resolve → dial → TOFU → launch);
  abort-before-SSH on secret failure;
  `cleanup` always invoked (defer) even on launch error.
- **colima backend:** exact `docker build`/`run`/`inspect`/`rm` argv via the Fake runner,
  including both workspace modes and the state volume.
- **dry-run:** no runner calls; planned commands printed.

## 11. Migration / impact

- The root `agent-infrastructure` justfile recipes
  (`build`/`create`/`run`/`destroy`)
  and the `sbx`-based path are superseded by `cove` subcommands.
- `claude-code-oci/image-files` + `Dockerfile` become the source of the embedded layers
  (split into overridable vs non-overridable trees),
  vendored into `cove` at build time.
- `internal/sbx` and `internal/kit/template.go` retire;
  nothing else depends on them.

## Appendix A — git PAT for a shared (mapped) repo (documented, not built)

When `--workspace` bind-mounts a repo whose `origin` is SSH-form
(`git@github.com:...`),
do **not** edit the repo's `.git/config` (it is shared with the host).
Instead, in the VM's **own** global git config,
rewrite SSH→HTTPS and feed the PAT from the injected env:

```ini
# VM ~/.gitconfig
[url "https://github.com/"]
    insteadOf = git@github.com:
    insteadOf = ssh://git@github.com/
[credential]
    helper = "!f() { echo username=x; echo password=$GITHUB_TOKEN; }; f"
```

This keeps the host on SSH and the VM on HTTPS+PAT against the same shared repo,
with the token never on disk.
A future iteration may have `connect` apply this automatically
from a `config.yml` `git:` block.

## Appendix B — declarative repo clone (deferred)

A future `repo:` field in `config.yml`
plus a connect-time first-run clone
(clone into an empty isolated workspace using resolved secrets)
would make sandboxes reproducible/unattended.
Deferred to preserve the secret-free `create`
and git-auth-out-of-`connect` invariants
until reproducibility is actually needed.
