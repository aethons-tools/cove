---
summary: Section index for the usage/reference docs of what this repo ships — Jam (`jam`, including `jam mint`), the `jam-colima` binary, and docker-in-sandbox.
read_when: You are running `jam`, `jam mint` or `jam-colima` directly (not developing them) and need its command surface, configuration, or I/O contract.
owns: the map of the per-binary usage docs
prereqs: none — see ../OVERVIEW.md for what the project is
tier: section
updated: 2026-11-05
---

# Usage reference

Operator-facing usage for the binaries this repo ships. For *why* it is built
this way, see [`../OVERVIEW.md`](../OVERVIEW.md).

| Doc | What it covers | Read when |
|-----|----------------|-----------|
| [jam/](jam/INDEX.md) | Operating the `jam` central service — running it (`serve`, the broker, destinations, TLS), signing operators in, the roster/RBAC, the kit registry, studios, the Requisitioner and the intercom. A section index. | You are running or administering a Jam. |
| [jam-mint.md](jam-mint.md) | The `jam mint` token minter — the `github` and `anthropic` subcommands, their flags/env, and the flags=non-secret/env=secret/one-token-to-stdout/fail-closed contract. | You are supplying a credential that mints a token — a GitHub App installation token or an Anthropic WIF bearer. |
| [jam/container.md](jam/container.md) | Jam's container image (`ghcr.io/aethons-tools/jam`) — tags, contents, the dev compose service, ports, admin access, volumes. | You are running Jam in a container. |
| [jam/studio-manager-live-test.md](jam/studio-manager-live-test.md) | The manual Studio Manager live-test checklist. | You are validating a new Studio Manager build or host. |
| [jam-colima.md](jam-colima.md) | The `jam-colima` binary (the Studio Manager for Colima) — install, upgrade from `jam-studio-manager`, `config.yml` and `colima.profile`, token, TLS, reconnect backoff, capacity, host stats, labels, image builds. | You are setting up, upgrading or debugging a Studio Manager host. |
| [docker-in-sandbox.md](docker-in-sandbox.md) | Docker inside a studio — `colima.docker` on a Studio Manager, the one-time Sysbox setup in the colima VM (`jam-colima colima setup-docker`), registry allow-list recipes, nested-container egress, limitations. | You are enabling Docker inside studios, installing Sysbox, allow-listing a registry, or debugging a nested container's network. |
