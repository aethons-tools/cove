# atsbx — YAML-driven multi-backend sandboxes — Design

**Date:** 2026-06-26
**Status:** Proposed (pre-implementation)
**Repo:** `aethons-tools/at-sbx` (binary `atsbx`)
**Supersedes (extends):** `2026-06-22-atsbx-design.md`

## 1. Purpose

Turn `atsbx` from an `sbx` wrapper
into a small, dependency-light tool
that runs hardened Claude Code sandboxes across multiple VM backends
from a single YAML description.
The intended user experience:

1. Write a `.yml` that describes a sandbox (name, backend, secrets it needs).
2. `atsbx create <sandbox>.yml` provisions the VM.
3. `atsbx connect <sandbox>.yml` SSHes in,
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
- The three-layer `.build` assembly from files embedded in the binary.
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
- `envsubst` templating (removed — see §6.3).

## 3. Concepts

### 3.1 Backends

A backend knows how to provision a VM from an assembled build context,
report a reachable SSH endpoint for it,
query its state,
and destroy it.
The first and only implemented backend is **Colima** (local Docker via Colima).
Firecracker and Fly come later behind the same interface.

### 3.2 The kit, as three embedded layers

There is no separate "base image" to manage.
Each `create` assembles a fresh build context in `.build/`
by stacking three layers, **last writer wins**:

1. **Overridable defaults** — extracted from the binary first.
   Sensible defaults the user may replace:
   `CLAUDE.md`, `settings.json`, stock skills, a default entrypoint, etc.
2. **Local files** — the user's per-project overrides
   (the optional `files:` directory from the `.yml`), copied second;
   they shadow any default at the same path.
3. **Non-overridable hardening** — extracted from the binary **last**,
   so it overwrites anything beneath it:
   `nftables.conf`, `squid.conf`, sshd hardening,
   the security-critical entrypoint, and `sshd` `AcceptEnv` config.

Layer 3 extracting last is a **security boundary**:
a user's local files can never weaken the egress lock or sshd hardening,
because the locked files always land on top.
Both embedded layers ship inside the `atsbx` binary via Go `embed.FS`
(vendored from `agent-infrastructure` at `atsbx` build time),
so the hardening cannot be misplaced or forgotten — a foolproof default.

After the three layers, `create` performs one programmatic step:
it writes the **managed public key**
into the context's `home/agent/.ssh/authorized_keys` (§3.5).
This is an explicit assembly step owned by `atsbx`,
not a templated or user-managed file,
which keeps the three-layer precedence pure.

### 3.3 Workspace modes (create-time)

The working directory is realized one of two ways,
chosen at `create` time, not in the `.yml`
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
It never appears in the `.yml`.

### 3.4 Secrets

The `.yml` declares secrets **by name plus a host command** that produces the value.
Values are **resolved just in time** at `connect`
(never at `create`, which stays secret-free)
by running the command and capturing stdout.
Nothing is written to disk.
Values are injected into the VM session as environment variables (§7.3).
The agent inside the VM is explicitly *not* a threat in this model;
the protected surface is the host
(no values in `ps`/shell history)
and at-rest storage (no value ever persisted).

### 3.5 Managed SSH keypair

`atsbx` owns a dedicated keypair at `~/.config/atsbx/id_ed25519`,
created on first use.
Its public half is written into every build context's `authorized_keys` during `create`;
`connect` authenticates with the private half.
The user never handles SSH keys.

## 4. The `.yml` schema

Lean: identity and wiring only.
No secret values, no hardening knobs, no workspace mode.

```yaml
name: claude-on-myrepo          # sandbox/VM name; also keys per-sandbox known_hosts
backend: colima                 # colima | firecracker | fly  (only colima now)

files: ./sandbox-files          # OPTIONAL: local override layer (layer 2). Omit for none.

secrets:                        # declared by NAME; values resolved at connect time
  - name: GITHUB_TOKEN
    command: ["op", "read", "op://Personal/github-pat/token"]
  - name: ANTHROPIC_API_KEY
    command: ["pass", "show", "anthropic/api-key"]
```

Rules:

- `name` and `backend` are required.
  Unknown `backend` → clear error listing supported backends.
- `files` is optional;
  relative paths resolve against the `.yml`'s directory so specs are cwd-independent.
- `secrets[].command` is an **argv array**, not a shell string —
  no quoting surprises;
  executed directly, stdout captured, trailing newline trimmed.
  A nonzero exit aborts `connect` before any SSH happens (fail closed).
- The file contains no secret values and is safe to commit.

## 5. Command surface

| Command | Behavior |
|---|---|
| `atsbx build <sandbox>.yml` | Assemble `.build/` from the three layers (+ local files) and inject the managed public key. No backend, no VM. For authoring/inspection. |
| `atsbx create <sandbox>.yml [--workspace/--ws <path>]` | `build`, then hand the `.build` path to the backend to create the VM. Secret-free. `--workspace <path>` selects Shared mode (hard error on Fly). |
| `atsbx connect <sandbox>.yml` | Resolve secrets, `Dial` the backend for an endpoint, verify host key (TOFU), inject env, launch `claude` interactively. Run every session. |
| `atsbx destroy <sandbox>.yml` | Backend `Destroy` for the sandbox's VM. |
| `atsbx status <sandbox>.yml` | Backend `GetStatus`: `Running` / `Stopped` / `Absent`. |

`--dry-run` remains global (before or after the subcommand)
and prints planned actions — including exact backend/SSH commands —
without executing.

## 6. Architecture

### 6.1 Package layout

Extends the existing pure-plan / execution split.

```
main.go                       parse argv, load .yml, select backend, dispatch
internal/spec/                parse + validate the .yml (pure)
internal/assemble/            three-layer .build assembly from embed.FS; key injection
  (embeds)                    overridable/ and hardening/ file trees (vendored)
internal/backend/             Backend interface + registry
  internal/backend/colima/    Colima impl: docker build / run / inspect / rm
internal/connect/             backend-AGNOSTIC: secret resolve -> transport -> TOFU -> launch
internal/secret/              resolver: run each host command, capture value (Runner-injected)
internal/sshargs/             pure argv builders for the ssh client
internal/runner/              existing Runner interface (OS impl + Fake)
```

Dependency direction:
`spec`, `sshargs`, `secret` are leaves;
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
   ("run `atsbx create` / start the VM first").
3. **Verify host key (TOFU).**
   Use a per-sandbox known_hosts file at `~/.config/atsbx/known_hosts.d/<name>`
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
`-i ~/.config/atsbx/id_ed25519`, `-p <port>`, `agent@<host>`,
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
  to `/dev/shm/atsbx-env-<rand>` (mode 600);
  a second interactive `ssh -tt` does `set -a; . <file>; rm -f <file>; exec claude`.
  Values touch only tmpfs (RAM) and are removed immediately after sourcing.
  Keeps the interactive TTY clean for `claude`.
- **Fallback — `SendEnv` (proven, simple).**
  `atsbx` places resolved values in its **own** process environment,
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

- **Create:** `docker build -t atsbx/<name> <BuildDir>`
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
- Missing/invalid `.yml`, missing required fields → usage + non-zero exit.
- `--workspace` on the Fly backend → hard error
  (documented; enforced once Fly exists).
- Any secret command failing (nonzero exit) → abort `connect` before SSH,
  naming the failed secret; never partially inject.
- VM not `Running` at `connect` → actionable message to `create`/start first.
- Host-key mismatch on reconnect → loud failure (do not auto-accept).
- Backend/`ssh` non-zero exit → propagate the exit code;
  stream child stdout/stderr live.

## 10. Testing (TDD)

All tests run without Docker/Colima or a live VM, via the `Fake` runner.

- **spec:** valid/invalid YAML, required fields, unknown backend,
  relative-path resolution, secrets parsed as argv arrays.
- **assemble:** three layers compose with correct last-writer-wins precedence;
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
  and the `sbx`-based path are superseded by `atsbx` subcommands.
- `claude-code-oci/image-files` + `Dockerfile` become the source of the embedded layers
  (split into overridable vs non-overridable trees),
  vendored into `atsbx` at build time.
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
from a `.yml` `git:` block.

## Appendix B — declarative repo clone (deferred)

A future `repo:` field in the `.yml`
plus a connect-time first-run clone
(clone into an empty isolated workspace using resolved secrets)
would make sandboxes reproducible/unattended.
Deferred to preserve the secret-free `create`
and git-auth-out-of-`connect` invariants
until reproducibility is actually needed.
