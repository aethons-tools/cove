# Rootless Docker inside the sandbox (opt-in)

**Status:** design approved (brainstorm), pending feasibility spike + implementation
**Date:** 2026-08-07
**Related:** the hardening layer (`internal/assemble/hardening/`), the colima backend
(`internal/backend/colima/`), the egress model (`docs/OVERVIEW.md#egress-three-additive-allow-lists-session-scoped`),
the base/toolchain images (`docs/superpowers/specs/2026-07-16-shared-base-image-design.md`).

## Goal

Most sandbox uses need to **build and run Docker containers for testing** (repos whose
suites do `docker build` / `docker compose up` / testcontainers). Give sandboxes a
working Docker **without weakening the sandbox's core hardening** — no `--privileged`
on the sandbox container, no host docker socket, and the egress lock intact.

**Non-goals:** running `--privileged` containers *inside* the sandbox; a general
container-orchestration platform; persisting built images as first-class artifacts.

## Decisions (resolved in brainstorming)

| Decision | Choice |
|---|---|
| Workload | Build + run for **testing** (compose/testcontainers), not privileged-in-container |
| Path | **Rootless Docker in-sandbox** — real `dockerd` as the agent user; no `--privileged`, no host socket |
| Enablement | **Opt-in** per-kit flag `docker: true` (default false) |
| Registry egress | Through the **existing squid proxy**; kit allow-lists registry domains |
| Nested-container egress | **Contained** — exits as agent uid → nftables drops unless proxied |
| Cache | **Persistent, bounded** Docker volume (BuildKit GC-capped); image store not a storage guarantee |
| Sequencing | A **feasibility spike gates all** other work |

## A. Enablement & config

A top-level boolean `docker: true` in `config.yml` (default false), validated in
`kit.ParseConfig`. It is the single switch: the backend activates the device + daemon
only when set; the shipped/template kits set it true so common uses get it, while
minimal/special kits omit it. Non-docker kits are byte-for-byte unchanged.

## B. Image tooling

Bake the rootless-Docker stack into the shared **`cove-image`** toolchain layer (built
in CI, cached once, present but **inert** unless activated):

- `dockerd` + `containerd` + `runc`, the `docker` CLI, and the Compose v2 + Buildx plugins.
- Rootless deps: `uidmap` (setuid `newuidmap`/`newgidmap`), `slirp4netns`, `fuse-overlayfs`.
- `/etc/subuid` + `/etc/subgid` ranges for the `agent` user.

Present-but-inactive binaries are low risk; the `docker: true` flag gates activation
(the `/dev/fuse` device and the daemon).

## C. Backend activation (run-flags)

Thread `cfg.Docker` through `backend.CreateContext` (and the dispatch `RunEphemeral`
path). When true, the colima backend adds — **only then**:

- `--device /dev/fuse` (for the `fuse-overlayfs` storage driver),
- `-e COVE_DOCKER=1` (activation signal the entrypoint reads),
- `-v atcove-{kit}[-{class}]-docker:<data-root>` (the persistent cache volume, §F),
- any minimal security relaxation the spike proves necessary (§G), scoped to this flag.

No `--privileged`, no socket mount — ever. Absent the flag, the run argv is identical to today.

## D. Daemon lifecycle

The entrypoint (already root at boot, after `nft`/`squid`, before `sshd`) detects
`COVE_DOCKER=1` and launches `dockerd-rootless.sh` **as the `agent` user** in the
background, with `XDG_RUNTIME_DIR=/run/user/<uid>` created and agent-owned. It is
idempotent (reuse a running daemon). Every SSH session receives
`DOCKER_HOST=unix://<runtime>/docker.sock` via `/etc/environment` (through the existing
`COVE_SSHENV` mechanism), so `docker` / `docker compose` / testcontainers work with no
per-repo configuration. `dockerd` is started with the proxy env already in the sandbox
(so pulls route through squid) and a BuildKit GC size cap (§F).

## E. Egress (unchanged model)

Registry pulls ride the **existing proxy**: `dockerd`/BuildKit honor
`HTTPS_PROXY=127.0.0.1:3128` (already in the agent env) → squid → allow-list. The kit
author adds registry domains to `image.allowed-domains`; the docs give the sets for
common registries (e.g. docker.io needs `registry-1.docker.io`, `auth.docker.io`,
`production.cloudflare.docker.com`; ghcr needs `ghcr.io`, `pkg-containers.githubusercontent.com`).

**Nested-container egress stays contained by construction:** a running container's
outbound traffic exits via `slirp4netns` as the agent uid → the sandbox nftables drops
it unless it is proxied. Inter-container and localhost traffic (the testing case) lives
inside the rootless network namespace and is unaffected. So the egress lock extends
*into* the nested containers for free; a test container that needs external network must
be pointed at the proxy explicitly (documented).

## F. Persistent, bounded cache

A dedicated persistent volume `atcove-{kit}[-{class}]-docker` (named via
`internal/naming`, recorded in the instance `VolumeSet`, removed on `destroy`), created
**only when `docker: true`**, mounted at the rootless `dockerd` data-root. It survives
`recreate` (like `-agent-data`), so build cache and pulled base layers persist —
fast rebuilds and re-pulls. **BuildKit is configured with a GC size cap** so the volume
self-prunes and behaves as a *cache*, not an archive; built/pulled images persist only
until GC evicts them (a speed win, not a storage guarantee). The volume mountpoint must
come up **agent-owned** — reuse the workspace-volume-ownership pattern (an agent-owned
dir baked in the image so a fresh named volume initializes agent-owned).

## G. Security posture

Preserving the hardening is the whole point:

- **No `--privileged`, no host socket.** Nested containers can escape no further than the
  agent already can; their egress stays behind squid/nftables.
- **Gated, minimal relaxations only.** Rootless `dockerd` nested inside the hardened
  container likely needs `--security-opt apparmor=unconfined` (colima's Ubuntu apparmor
  can block nested rootless), and possibly a small seccomp allowance. Any such relaxation
  is **scoped strictly to `docker: true` kits** (non-docker kits keep full
  seccomp/apparmor), kept minimal, and **must be proven necessary by the spike** (§H).
  The sealed egress files (nftables/squid/sshd) are untouched.
- If the spike shows an *unacceptable* relaxation is required, we stop and revisit
  (Sysbox becomes the fallback path — a host-side runtime, out of scope here).

## H. Feasibility spike (gates everything)

Before building the productionized feature, a spike must prove, on a real colima VM:

1. Rootless `dockerd` starts as the `agent` user inside the hardened sandbox container
   and runs `docker build` + `docker run` + `docker compose up`.
2. The **minimal** set of run-flags/relaxations required (expected: `--device /dev/fuse`;
   determine whether `--security-opt apparmor=unconfined` and/or a seccomp tweak are
   actually needed).
3. The storage driver that works on colima's kernel (`fuse-overlayfs` vs native
   rootless-overlay) and that `slirp4netns` networking coexists with the sandbox
   nftables (registry pulls succeed through the proxy; un-proxied container egress is
   dropped).
4. BuildKit GC with a size cap keeps the cache volume bounded.

The spike's output is the exact run-flag/entrypoint recipe the rest of the work
implements. If any requirement can't be met without weakening the hardening
unacceptably, escalate before proceeding.

## I. Testing

- **Hermetic** (`runner.Fake`): config-parse (`docker: true` accepted/validated; default
  false), and backend argv — when `docker: true`, Create/RunEphemeral include
  `--device /dev/fuse`, `-e COVE_DOCKER=1`, and the `-docker` volume; when absent, the
  argv is unchanged and no docker volume is recorded.
- **Integration** (`integration` build tag / real VM, not in the hermetic suite): the
  actual `dockerd`/build/run behavior — a hermetic test can't run a daemon. Aligns with
  the repo's existing real-ssh/integration split.

## J. Limitations (document up front)

- No `--privileged` **inside** containers (rootless).
- `fuse-overlayfs` is slower than native overlay (kernel-dependent; the spike may enable
  native rootless-overlay where available).
- Container egress requires the proxy + allow-list; tests hitting external networks need
  configuration. Inter-container/localhost testing is unaffected.
- The docker cache is bounded/GC'd — not durable image storage.

## Decomposition (for planning)

1. **Feasibility spike** — prove the recipe on a real colima VM (§H). *Gates all below.*
   Output: exact run-flags + entrypoint launch recipe + storage/network findings.
2. **`cove-image` tooling** — add the rootless-Docker stack + subuid/subgid to the
   toolchain image.
3. **Config + backend plumbing** — `docker: true` flag + validation; thread
   `CreateContext.Docker`; colima backend adds `--device /dev/fuse`, `-e COVE_DOCKER=1`,
   and the `-docker` cache volume (+ VolumeSet/destroy); an agent-owned data-root
   mountpoint in the image.
4. **Entrypoint daemon-launch** — start rootless `dockerd` as agent on `COVE_DOCKER=1`,
   export `DOCKER_HOST`, configure proxy + BuildKit GC cap; scope any `--security-opt`
   from the spike behind the flag.
5. **Docs** — the `docker` flag, required registry allow-lists, nested-container egress
   behavior, and the limitations (§J).

Order: 1 → {2, 3} → 4 → 5. The spike (1) must complete and confirm feasibility before
2–4 begin.
