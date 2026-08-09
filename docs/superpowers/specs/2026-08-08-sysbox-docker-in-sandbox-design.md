# Docker inside the sandbox via Sysbox (opt-in) — design

**Status:** implemented (COV-116–121) and **verified end-to-end on 2026-08-09** — a
`docker: true` kit boots under colima+Sysbox and runs `docker build` / `compose` /
**testcontainers** inside the hardened sandbox on a real host. Three fixes/tests
followed from that live run: **COV-124** (Sysbox setup docs — durable `docker:`
runtime registration, ≥0.7.1 version floor, `.deb` URL), **COV-125** (squid supervised
as a foreground unit so `/run/squid.pid` persists for per-session `reconfigure`), and
**COV-122** (the repeatable integration e2e). Original brainstorm 2026-08-08.
Re-scopes [`2026-08-07-rootless-docker-in-sandbox-design.md`](2026-08-07-rootless-docker-in-sandbox-design.md)
(rootless approach — **escalated**, not viable) onto the **Sysbox** runtime, which the
feasibility spike [`2026-08-08-sysbox-docker-in-sandbox-spike.md`](2026-08-08-sysbox-docker-in-sandbox-spike.md)
(COV-120) proved out end-to-end.
**Date:** 2026-08-08
**Related:** the hardening layer (`internal/assemble/hardening/`), the colima backend
(`internal/backend/colima/`), the base image (`images/cove-base-image/`), the config
(`internal/kit/`), the egress model
(`docs/OVERVIEW.md#egress-three-additive-allow-lists-session-scoped`).

## Goal

Give sandboxes a working Docker for **testing** workloads (repos that `docker build` /
`docker compose up` / use **testcontainers**) **without weakening the sandbox's core
hardening** — no `--privileged` on the sandbox container, no host docker socket, and the
egress lock intact. Opt-in per kit via `docker: true`.

**Non-goals:** running `--privileged` containers inside the sandbox; a general
container-orchestration platform; durable image storage (the cache is bounded/GC'd).

## Why Sysbox (history)

The original plan — rootless `dockerd` as the agent user — **failed the feasibility
spike (COV-115)**: nested unprivileged uid-mapping (`newuidmap`) is refused on the colima
kernel unless the container holds `CAP_SYS_ADMIN`, which is an unacceptable relaxation.
**Sysbox** (`sysbox-runc`) dissolves that wall: it runs the *sandbox container itself*
under a purpose-built runtime that provides userns/uid-shifting/`procfs` emulation
host-side, so a normal **rootful** `dockerd` runs inside an **unprivileged** container.
COV-120 proved build/run/compose + testcontainers work with the egress lock intact and
**no** `--privileged`/socket/`--device`/`--security-opt`/extra caps.

## Decisions (resolved in brainstorming)

| Decision | Choice |
|---|---|
| Runtime | Sandbox runs under **`--runtime=sysbox-runc`** when `docker: true` |
| Inner daemon | Normal **rootful** `dockerd`, native `overlayfs` storage |
| Enablement | **Opt-in** per-kit `docker: true` (default false) |
| Init model | **systemd as PID 1**, gated behind `docker:true`; non-docker sandboxes **unchanged** (runc + current script entrypoint) |
| Egress-first invariant | A sealed **`cove-egress.service`** ordered before `sshd`/`docker` reconstructs the "nft lock first" guarantee under systemd |
| Nested-egress containment | An **always-on** nftables `forward`-chain drop in the base hardening |
| VM provisioning | at-cove **detects + guides** (preflight checks `sysbox-runc`; does not install) |
| Image home | Engine + systemd land in **`cove-base-image`** (universal floor); **podman removed** |
| Registry egress | Through the existing squid proxy; kit allow-lists registry domains (incl. `.cloudfront.docker.com`) |
| Cache | Persistent, **bounded** volume at the inner `/var/lib/docker` (BuildKit GC-capped) |

## A. Enablement & config

A top-level boolean `docker: true` in `config.yml` (`kit.Config.Docker`,
`yaml:"docker,omitempty"`, default false), validated in `kit.ParseConfig` (plain bool;
reject non-bool). It is the single switch. Shipped/template kits set it true; minimal
kits omit it. **A non-docker kit's *runtime behavior* is unchanged** — no
`--runtime`, no `COVE_DOCKER` env, no systemd (keeps the runc + script-entrypoint boot),
no docker volume, identical `docker run` argv. Two shared-image changes *do* reach every
sandbox but are functionally inert for non-docker kits: the base image carries the (idle)
Docker binaries + systemd (§B), and the sealed `nftables.conf` gains the always-on
`forward` drop (§E), which has no effect absent nested containers.

## B. Base image (`cove-base-image`)

Docker-in-sandbox is a *general* sandbox capability, so it lives in the universal floor,
not cove's repo-specific `cove-image`. Present-but-inert unless activated.

- **Add:** the Docker **engine** (`docker-ce`/`containerd.io`/`runc`) + `docker-ce-cli` +
  the Compose v2 and Buildx plugins + **`systemd`**. Put the `agent` user in the `docker`
  group so it can reach the root-owned `/var/run/docker.sock` (default `DOCKER_HOST` —
  no per-session env needed).
- **Remove (superseded by the above):** `podman`, `podman-docker` (it owns
  `/usr/bin/docker` and collides with `docker-ce-cli`), and the rootless plumbing that
  existed only for podman — `uidmap`, `fuse-overlayfs`, `slirp4netns`, the
  `/etc/containers/nodocker` touch, and the `agent` subuid/subgid `usermod` (Sysbox does
  uid-shifting host-side with its own subuid range; the container's `/etc/subuid` is not
  used — confirm in impl). Pins/Renovate entries for the removed packages go too.

Nothing in the repo consumes podman *inside* a sandbox (verified: no Go/kit references;
the e2e/reference-worker path is claude + at-task, no containers). `scripts/setup-test-tools.sh`
installs podman-as-`docker`-shim on the **dev/CI host** (not the image) for this repo's
own colima-backend tests — a separate concern, left as-is here (whether to switch host
test-tooling to real docker is out of scope).

## C. Backend activation & preflight (`internal/backend/colima`)

Thread `cfg.Docker` → `backend.CreateContext.Docker` (populated in `createInstance` and
the dispatch `RunEphemeral` path, mirroring how DNS was threaded). When true, the colima
backend adds — **only then** — to the `docker run` argv (Create **and** `RunEphemeral`):

- `--runtime=sysbox-runc`,
- `-e COVE_DOCKER=1` (activation signal the entrypoint reads),
- `-v atcove-{kit}[-{class}]-docker:/var/lib/docker` (the persistent cache volume, §F),
  recorded in the instance `VolumeSet` and removed on `destroy`.

No `--privileged`, no socket mount, no `--device`, no `--security-opt` — ever. Absent the
flag, the run argv is identical to today.

**Preflight (detect + guide).** `colima.preflight()` gains a check used when the instance
is `docker:true`: parse `docker info -f '{{json .Runtimes}}'`; if `sysbox-runc` is absent,
fail fast with an actionable message (how to install Sysbox in the colima VM and make it
persist — see §H). at-cove does **not** install into the VM.

## D. Init model & daemon lifecycle

`docker:true` sandboxes boot **systemd as PID 1**; non-docker sandboxes keep the current
`entrypoint.sh` → `exec sshd` under runc (systemd needs Sysbox to run cleanly, so it is
naturally coupled to the docker path). One shared image branches at PID 1: the entrypoint
detects `COVE_DOCKER=1` → `exec /sbin/init`; unset → the current script (unchanged).

Under systemd, the sealed hardening ships units (activated only on the systemd path):

- **`cove-egress.service`** (oneshot): `DefaultDependencies=no`; runs
  `nft -f /etc/nftables.conf` then starts squid; ordered **`Before=network-pre.target`**
  and **`Before=sshd.service docker.service`**. `sshd.service` and `docker.service` gain
  `After=cove-egress.service` + `Requires=cove-egress.service`. This reconstructs the
  sequential "drop-all egress lock is up before anything touches the network" guarantee
  that `entrypoint.sh` provides today — the **load-bearing security invariant**, with its
  own test (§I).
- A sealed drop-in writes `/etc/docker/daemon.json` (§E proxy) before `docker.service`.
- `docker.service` is the stock unit; the inner `dockerd` runs rootful with native
  `overlayfs`. The agent reaches it via the default socket (`docker` group).

## E. Egress

- **Registry pulls ride the existing proxy.** `dockerd`/containerd **and buildkit** honor
  the daemon proxy: a sealed `daemon.json` sets
  `proxies.{http,https}-proxy = http://127.0.0.1:3128`, `no-proxy=localhost,127.0.0.1,::1`.
  (The daemon shares the sandbox netns, so squid is reachable at loopback directly.) The
  `proxies` block — not just process env — is required so **buildkit** builds route
  through squid, not just `docker run` pulls. Kit authors add registry domains to
  `image.allowed-domains`; docs give the sets, including Docker Hub:
  `registry-1.docker.io`, `auth.docker.io`, `index.docker.io`, and **`.cloudfront.docker.com`**
  (the blob CDN — CloudFront, not the `cloudflare.docker.com` some docs cite).
- **Nested-container egress is contained by an always-on `forward` drop.** Nested traffic
  is masqueraded and *forwarded*, bypassing the hardening's `output`-only rule (proven in
  the spike). The sealed `nftables.conf` gains:
  ```
  chain forward {
      type filter hook forward priority 0; policy drop;
      ct state established,related accept;
  }
  ```
  It is **always-on** (a strict tightening; non-docker sandboxes generate no forwarded
  traffic, so zero-impact, and the sealed egress file stays uniform). Same-network
  container-to-container is L2-bridged (bypasses the forward hook) → unaffected; the
  daemon's proxied pulls are loopback/`output` → unaffected. A nested container that needs
  *external* network must be pointed at the proxy explicitly (documented).

## F. Persistent, bounded cache

A dedicated persistent volume `atcove-{kit}[-{class}]-docker` (named via `internal/naming`,
recorded in `VolumeSet`, removed on `destroy`), created **only when `docker: true`**,
mounted at the inner `/var/lib/docker`. It survives `recreate` (like `-agent-data`), so
build cache and pulled base layers persist. **BuildKit is configured with a GC size cap**
(in the sealed `daemon.json`) so the volume behaves as a *cache*, not an archive. *Verify
in impl:* Sysbox does its own uid-shifting on a `/var/lib/docker` volume — the spike did
not exercise persistence, so an integration test must confirm the volume initializes and
survives correctly.

## G. Security posture / threat model

- **No `--privileged`, no host socket, no caps beyond baseline `NET_ADMIN`** — proven
  (COV-120). The sandbox container is unprivileged; Sysbox provides isolation via a
  dedicated userns + syscall/`procfs` emulation.
- **Egress boundary preserved and extended.** The drop-all `output` lock still gates the
  agent (unchanged); the new always-on `forward` drop closes the nested-container bypass.
  Nested containers reach external networks only via the proxy + allow-list.
- **New trust dependency: Sysbox.** This is the deliberate trade the COV-115 escalation
  identified — a purpose-built isolation runtime instead of granting `CAP_SYS_ADMIN`. Net
  a **stronger** posture than the rejected alternative, but it adds a host/VM-side runtime
  to keep patched (track Sysbox CE releases).
- **systemd blast radius is contained** behind `docker:true`: non-docker sandboxes keep
  the runc + script init, un-re-reviewed and unchanged.

## H. VM provisioning (colima)

Sysbox is a VM-level dependency; at-cove detects but does not install it (§C preflight).

- **Install:** Sysbox CE (matching the VM arch, e.g. arm64) in the colima Lima VM
  (`sysbox-ce_<ver>.linux_<arch>.deb` + `jq`); the `.deb` registers `sysbox-runc` and
  starts `sysbox`/`sysbox-mgr`/`sysbox-fs`. Kernel ≥6.3 → idmapped mounts (no shiftfs).
- **Persistence:** the install must survive `colima stop/start` via a **colima provision
  hook** (`colima start --edit` provisioning), so it is not an ephemeral in-VM change.
  Documented as a prerequisite with copy-paste steps; the preflight message points here.

## I. Testing

- **Hermetic (`runner.Fake`):** config parse/validate (`docker:true` accepted, non-bool
  rejected, default false); backend argv (Create + `RunEphemeral` include
  `--runtime=sysbox-runc` + `-e COVE_DOCKER=1` + the `-docker` volume when set, and are
  **byte-for-byte unchanged** when unset); preflight fails with the actionable message
  when `docker info` runtimes lack `sysbox-runc`; `VolumeSet`/destroy includes the docker
  volume.
- **Integration (`integration` tag / real colima+Sysbox, not in the hermetic suite):**
  daemon-up under `sysbox-runc`; build/run/compose through squid; the
  **un-proxied-nested-egress drop**; the **egress-first ordering** invariant (nothing
  reaches the network before `cove-egress.service`); cache-volume persistence across
  recreate. Aligns with the repo's existing real-ssh/integration split.

## J. Limitations (document up front)

- The `forward` drop also blocks cross-*network* container routing (multi-network compose
  that routes between bridges). Fine for typical single-network testcontainers; widen only
  if a real case needs it.
- Container egress requires the proxy + allow-list; tests hitting external networks need
  configuration. Inter-container/localhost is unaffected.
- The docker cache is bounded/GC'd — not durable image storage.
- Requires Sysbox installed in the colima VM (preflight guides).

## Decomposition (re-drafted parts 2–5)

The original COV-116…119 were drafted for the rootless approach; re-draft against this
design. Order: {2,3} → 4 → 5, gated by nothing further (COV-120 proved feasibility).

1. **Base-image tooling** — add the Docker engine + systemd + `agent` docker-group to
   `cove-base-image`; remove podman + rootless plumbing; update image docs. (was COV-116)
2. **Config + backend plumbing** — `docker: true` flag + validation; thread
   `CreateContext.Docker`; colima backend adds `--runtime=sysbox-runc`, `-e COVE_DOCKER=1`,
   the `-docker` cache volume (+ `VolumeSet`/destroy); the **preflight `sysbox-runc`
   check**. (was COV-117)
3. **Init + daemon launch** — the PID-1 systemd branch; the sealed `cove-egress.service`
   (egress-first ordering) + `sshd`/`docker` ordering; the proxy `daemon.json` drop-in +
   BuildKit GC cap. (was COV-118)
4. **Egress hardening** — the always-on `forward`-chain drop in `nftables.conf`. (new;
   small, security-critical — may fold into 3 or stand alone)
5. **Docs + VM provisioning** — the `docker` flag, the Sysbox install + colima
   provision-hook prerequisite, registry allow-lists (incl. `.cloudfront.docker.com`),
   nested-egress behavior, limitations. (was COV-119)

## Migration notes

- Removing podman changes `cove-base-image` contents: update `docs/DEVELOPMENT.md`,
  `docs/OVERVIEW.md`, and the shared-base-image spec's "what's in the base" lines in the
  same change.
- `scripts/setup-test-tools.sh` (host test tooling) is intentionally **not** changed here.
