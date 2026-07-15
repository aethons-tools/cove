# at-cove

Run **hardened Claude Code sandboxes** from a single YAML description.

`at-cove` is a small, dependency-light Go CLI.
You describe a sandbox once in a `.at-cove/` kit directory,
and `at-cove` provisions a locked-down VM,
SSHes into it,
injects your secrets in memory only,
and launches `claude` interactively.

- **Egress is locked down** —
  the VM reaches only an allow-listed set of domains through an in-VM Squid proxy,
  with `nftables` dropping everything else.
- **Secrets never touch disk or the host process table** —
  resolved on the host just-in-time, streamed in over SSH stdin, sourced, and deleted.
- **The hardening is foolproof** —
  the security-critical files ship embedded in the binary and are layered on *last*,
  so they always win over anything a kit supplies.

## Quickstart

```bash
just install                 # build and put `at-cove` on your PATH

# In a repo you want to sandbox:
mkdir -p .at-cove
$EDITOR .at-cove/config.yml  # name + backend + secrets (see below)

at-cove create               # build the context and provision the VM
at-cove chat                 # ssh in, inject secrets, launch claude
at-cove status               # running / stopped / absent
at-cove destroy              # tear down (volumes retained)
```

A minimal `.at-cove/config.yml`:

```yaml
name: claude-on-myrepo
backend: colima              # only colima is implemented today
secrets:
  GITHUB_TOKEN:              # value resolved at chat time on the host
    command: ["op", "read", "op://Personal/github-pat/token"]
```

Commands with no path argument walk up from the cwd to the nearest `.at-cove/`.
Add `--dry-run` to any command to print the planned actions without executing.

## Requirements

- Go (to build) and [`just`](https://github.com/casey/just) (optional task runner — logic lives in `scripts/`).
- A Colima/Docker host to actually run a sandbox.
- An `ssh` client.

The sandbox authenticates via **claude.ai subscription OAuth** on first `chat`
(not an API key);
the login persists on a state volume across recreates.

## Documentation

- **[`docs/OVERVIEW.md`](docs/OVERVIEW.md)** —
  the full picture: kit format, command surface, security model, architecture, build/test/run.
- **[`docs/DEVELOPMENT.md`](docs/DEVELOPMENT.md)** —
  building and testing, including the egress-locked-sandbox toolchain workarounds.
- **[`AGENTS.md`](AGENTS.md)** —
  orientation and working conventions for coding agents in this repo
  (the root `CLAUDE.md` just imports this).
- **[`docs/superpowers/`](docs/superpowers/)** —
  the authoritative design spec and implementation plans.

## Development

```bash
just test          # hermetic unit tests — no docker/network/ssh
just integration   # real-ssh integration tests (needs ssh/sshd/ssh-keygen)
just lint          # go vet + gofmt check + shellcheck/hadolint
just build         # build both binaries into dist/<os>-<arch>/{at-cove,at-task}
```

Tests are hermetic by default —
they drive a fake process runner,
so no Docker, network, or live VM is required.

## Status

The `build` / `create` / `chat` / `recreate` / `destroy` / `status` surface
and the Colima backend are implemented. Every command's project root is a
uniform `--project-dir` flag (its `.at-cove/` is the kit; default: cwd walk-up);
there is no positional project-dir. `chat` additionally selects an optional
`collaborators:` class (see `docs/OVERVIEW.md`).
The Firecracker and Fly backends, the `.local/` override layer, and declarative repo cloning
are designed but deferred — see the specs.

The repo also builds `at-task`, the git/PR worker `at-cove work` drives; the
Linear-driven scheduler is the `at-cove dispatch` subcommand (see
[`docs/orchestration/`](docs/orchestration/INDEX.md)).
