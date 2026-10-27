# cove

**Jam** — a durable service that runs hardened Claude Code studios — and the
studio images it builds.

- **Egress is locked down** —
  a studio reaches only an allow-listed set of domains through an in-studio Squid proxy,
  with `nftables` dropping everything else.
- **Credentials stay in Jam** —
  a studio holds only a scoped identity token; Jam's broker injects the real ones.
- **The hardening is foolproof** —
  the security-critical files ship embedded in `jam` and are layered on *last*,
  so they always win over anything a kit supplies.

## Quickstart

Install `jam` (one binary; minting is `jam mint`) with the one-command installer. The repo is
private today, so it uses your GitHub CLI login (`gh auth login`) to pull the
latest release, verify its checksum, and drop the binary on your PATH:

```bash
gh api -H "Accept: application/vnd.github.raw" \
  /repos/aethons-tools/cove/contents/install.sh | bash
```

Once the repo is public this collapses to the classic anonymous form — same script:

```bash
curl -fsSL https://raw.githubusercontent.com/aethons-tools/cove/main/install.sh | bash
```

`jam update` moves to the latest release. Prefer to build from source? Use
`just install`. Version pinning and the `BINDIR` / `JAM_SYSTEM` overrides are in
[the overview](docs/OVERVIEW.md#installing-the-binaries). To stand a Jam up, start
at [`docs/usage/jam/INDEX.md`](docs/usage/jam/INDEX.md).

## Requirements

- Go (to build) and [`just`](https://github.com/casey/just) (optional task runner — logic lives in `scripts/`).
- A [Colima](https://github.com/abiosoft/colima) host for Jam's launcher to run studios in (see [serve.md](docs/usage/jam/serve.md#the-launcher-runtimelauncher)).
- An `ssh` client.

## Documentation

- **[`docs/INDEX.md`](docs/INDEX.md)** — the docs map; start here.
- **[`docs/OVERVIEW.md`](docs/OVERVIEW.md)** — what this repo builds, the security model, the package map, build/test/run.
- **[`docs/DEVELOPMENT.md`](docs/DEVELOPMENT.md)** — building and testing, including the egress-locked-sandbox toolchain workarounds.
- **[`AGENTS.md`](AGENTS.md)** — orientation and working conventions for coding agents in this repo (the root `CLAUDE.md` just imports this).
- **[`docs/superpowers/`](docs/superpowers/)** — the design history: dated specs and implementation plans.

## Development

```bash
just test          # hermetic unit tests — no docker/network/ssh
just integration   # real-ssh integration tests (needs ssh/sshd/ssh-keygen)
just lint          # go vet + gofmt check + shellcheck/hadolint
just build         # build jam + jam-studio-manager into dist/<os>-<arch>/ (needs Node 22 + npm for the Jam app)
```

Tests are hermetic by default — they drive a fake process runner, so no Docker,
network, or live VM is required.
