---
summary: Section index for the usage/reference docs of what this repo ships — Jam (`jam`, including `jam mint`), the `jam-studio-manager` binary, and docker-in-sandbox.
read_when: You are running `jam`, `jam mint` or `jam-studio-manager` directly (not developing them) and need its command surface, configuration, or I/O contract.
owns: the map of the per-binary usage docs
prereqs: none — see ../OVERVIEW.md for what the project is
tier: section
updated: 2026-10-26
---

# Usage reference

Operator-facing usage for the binaries this repo ships. For *why* it is built
this way, see [`../OVERVIEW.md`](../OVERVIEW.md).

| Doc | What it covers | Read when |
|-----|----------------|-----------|
| [jam/](jam/INDEX.md) | Operating the `jam` central service — running it (`serve`, the broker, destinations, TLS), signing operators in, the roster/RBAC, the kit registry, studios, the Requisitioner and the intercom. A section index. | You are running or administering a Jam. |
| [jam-mint.md](jam-mint.md) | The `jam mint` token minter — the `github` and `anthropic` subcommands, their flags/env, and the flags=non-secret/env=secret/one-token-to-stdout/fail-closed contract. | You are supplying a credential that mints a token — a GitHub App installation token or an Anthropic WIF bearer. |
| [studio-manager.md](studio-manager.md) | The `jam-studio-manager` binary — install, `config.yml`, token, TLS, reconnect backoff, capacity, labels, image builds. | You are setting up or debugging a Studio Manager host. |
| [docker-in-sandbox.md](docker-in-sandbox.md) | Docker inside a studio — `runtime.launcher.docker`, the one-time Sysbox setup in the colima VM (`jam colima setup-docker`), registry allow-list recipes, nested-container egress, limitations. | You are enabling Docker inside studios, installing Sysbox, allow-listing a registry, or debugging a nested container's network. |
