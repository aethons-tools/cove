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

Install `at-cove` (and its sibling `at-mint`) with the one-command installer. The
repo is private today, so it uses your GitHub CLI login (`gh auth login`) to pull
the latest release, verify its checksum, and drop the binaries on your PATH:

```bash
gh api -H "Accept: application/vnd.github.raw" \
  /repos/aethons-tools/cove/contents/install.sh | bash
```

Once the repo is public this collapses to the classic anonymous form — same script:

```bash
curl -fsSL https://raw.githubusercontent.com/aethons-tools/cove/main/install.sh | bash
```

Prefer to build from source? Use `just install`. Version pinning and the
`BINDIR` / `COVE_SYSTEM` overrides are in
[the overview](docs/OVERVIEW.md#installing-the-binaries).

Then, in a repo you want to sandbox:

```bash
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
- A [Colima](https://github.com/abiosoft/colima) host to actually run a sandbox. Start it on its **default** profile — at-cove pins the `colima` docker context:

  ```bash
  colima start --cpu 4 --memory 8 --disk 60 --vm-type vz --mount-type virtiofs
  ```

  A reasonable starting size; tune to taste. `--vm-type vz --mount-type virtiofs`
  are macOS (Apple Silicon) options — drop them on other hosts. Colima persists a
  profile's settings, so a later `colima start` resumes with them; you only re-pass
  a flag to change it. Don't use `--profile` — at-cove needs the default `colima` context.
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
