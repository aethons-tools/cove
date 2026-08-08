# Rootless Docker inside the sandbox (opt-in)

**Status:** feasibility spike **failed → escalate** (COV-115, 2026-08-08). Rootless
`dockerd` is **not viable** in the non-privileged hardened container on colima;
the productionized approach below (parts 2–5) is **blocked** pending a re-scope to
the Sysbox runtime. See [Spike findings (COV-115)](#spike-findings-cov-115--verdict-escalate).
**Date:** 2026-08-07 (spike appended 2026-08-08)
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

## Spike findings (COV-115) — verdict: **ESCALATE**

**Date:** 2026-08-08 · **Attended** (a human iterated `docker run` flags against a
live colima VM). **Environment:** colima (Lima) on Apple Silicon, macOS
Virtualization.framework; VM kernel `6.8.0-117-generic` (Ubuntu 24.04 *noble*);
Docker `29.5.2`. **Spike image:** the `cove-image` floor (already ships
`uidmap`/`fuse-overlayfs`/`slirp4netns` + `agent` `subuid/subgid`) + Docker CE engine
& `docker-ce-rootless-extras` baked in, plus faithful copies of the hardening egress
layer (`nftables.conf`/`squid.conf`/allow-list) and an entrypoint that raises
nft+squid then idles. Throwaway assets (Dockerfile, `run.sh`, `daemon.sh`, `tests.sh`)
were **not committed** (exploratory, per the ticket).

### Verdict

**Rootless `dockerd` is not viable inside the non-privileged hardened container on
colima.** The nested unprivileged user namespace's subuid/gid **`uid_map` range write
is refused** unless the outer container is given `--cap-add=SYS_ADMIN` or
`--privileged` — both ~root-equivalent for container escape and an **unacceptable**
relaxation of the core hardening (§G). This matches upstream reality: the official
`docker:dind-rootless` image itself requires `--privileged`. **Recommend re-scoping
COV-114 around the Sysbox fallback** (see below). Parts 2–5 (the `docker:true`
implementation) are **blocked** — do not build to the current design.

### What was established (evidence chain)

Baseline sandbox run-flags are `--init --cap-add=NET_ADMIN --dns 1.1.1.1` (from
`internal/backend/colima/colima.go` `Create`). Adding docker's expected delta and
iterating:

1. **`--device /dev/fuse` alone → dockerd fails at once.** rootlesskit cannot create
   the nested userns: `[rootlesskit:parent] error: failed to start the child:
   fork/exec /proc/self/exe: operation not permitted`.
2. **Cause = the default Docker seccomp profile blocks `unshare(CLONE_NEWUSER)`.**
   Isolated with `unshare -Ur`: fails under default seccomp *and* under
   `--security-opt apparmor=unconfined`; succeeds **only** with
   `--security-opt seccomp=unconfined`. So a seccomp allowance is *required* just to
   create the userns.
3. **Then it dies at uid mapping:** `failed to setup UID/GID map: newuidmap …
   [0 1001 1 1 165536 65536] failed: newuidmap: write to uid_map failed: Operation
   not permitted`. The helpers are correct — `newuidmap`/`newgidmap` are setuid-root
   (`-rwsr-xr-x`, no stray file-caps) and `/etc/subuid`/`/etc/subgid` carry
   `agent:165536:65536`, matching the requested map.
4. **The `uid_map` *range* write is refused for any `ruid≠0` writer, even with
   `CAP_SETUID`.** Proven with setuid-root probes: a setuid binary run by `agent`
   *does* gain `euid=0` and `CapEff` including `CAP_SETUID` (so setuid elevation and
   the legacy cap-grant work; `securebits=0`, `NoNewPrivs=0`, rootfs not `nosuid`).
   Yet an identical `/proc/<pid>/uid_map` range write returns `EPERM` when `ruid=1001`,
   while a real-root (`ruid=0`) writer succeeds. `unshare -Ur` (a process self-mapping
   its *own* uid — no cross-process privilege) does succeed once the sysctl is lifted,
   but rootless docker needs `newuidmap` to write a *range* into another process, which
   is the refused operation.
5. **Every AppArmor / userns lever was exhausted with no effect on the range write:**
   `kernel.apparmor_restrict_unprivileged_userns=0`, `…_unconfined=0`,
   `unprivileged_userns_apparmor_policy=0`, `…_userns_force=0`, container
   `--security-opt apparmor=unconfined`, **and** unloading all AppArmor profiles
   VM-wide. Re-tested in a **freshly created** container (born after the sysctls were
   flipped) to rule out a creation-time confound — still `EPERM`. So the block is
   **not** AppArmor.
6. **Boundary pinned:** with `--cap-add=SYS_ADMIN` (no `--privileged`) the `newuidmap`
   range write **succeeds** (`0 1001 1` / `1 165536 65536`); `--privileged` likewise.
   `CAP_SYS_ADMIN` is therefore the *minimum* that makes nested rootless docker work
   here — and it is not an acceptable grant for the sandbox.

### Recommended path: Sysbox

Re-scope COV-114 around **Sysbox** (`docker+sysbox-runc`): a host-installed OCI
runtime that gives a container the *illusion* of the privileges nested Docker needs
(userns, `uid_map`, etc.) via syscall interception, **without** exposing
`CAP_SYS_ADMIN`/`--privileged` to the workload. This is a **colima-backend/runtime**
change (install Sysbox in the Lima VM; run the sandbox container with
`--runtime=sysbox-runc`), not an in-container flag — materially different from §§A–G
and requiring its own design + threat-model pass.

### Not reached (untested — gated by the wall above)

Because `dockerd` never started, these design points were **not** verified and must be
re-run against whatever runtime the re-scope adopts:

- **Registry pulls through squid.** Note: rootlesskit launched slirp4netns with
  `--disable-host-loopback`, so a rootless daemon reaching squid at `127.0.0.1:3128`
  would need the slirp host-gateway (e.g. `DOCKERD_ROOTLESS_ROOTLESSKIT_DISABLE_HOST_LOOPBACK=false`)
  — untested.
- **Un-proxied nested-container egress being dropped** by the sandbox nftables.
- **Storage driver** `fuse-overlayfs` vs native rootless `overlay2` (`/dev/fuse` *is*
  present in the VM, world-rw, so the device passthrough itself is fine).
- **BuildKit GC size cap** bounding the cache volume.
