# Docker inside the sandbox via Sysbox — feasibility spike (COV-120)

**Status:** spike complete — verdict **FEASIBLE**. Supersedes the rootless approach in
[`2026-08-07-rootless-docker-in-sandbox-design.md`](2026-08-07-rootless-docker-in-sandbox-design.md)
(which escalated: rootless `dockerd` is not viable in the non-privileged hardened
sandbox — COV-115). The full re-scope **design** (config flag, backend wiring, VM
provisioning, entrypoint, egress rule, testing) is a **follow-up**; this doc records the
proven recipe it must build to.
**Date:** 2026-08-08 · **Attended** (a human drove a live colima VM).
**Related:** the hardening layer (`internal/assemble/hardening/`), the colima backend
(`internal/backend/colima/`), the egress model
(`docs/OVERVIEW.md#egress-three-additive-allow-lists-session-scoped`), and the rootless
design + its escalation (link above).

## Verdict

**Feasible — inner Docker runs inside the hardened sandbox via the Sysbox runtime with
no weakening of the core hardening.** No `--privileged`, no host docker-socket mount, no
`--device`, no `--security-opt`, and no capabilities beyond the baseline `NET_ADMIN`.
The egress lock (nftables + squid) stays intact and is *extended* to contain
nested-container traffic. `docker build` / `run` / `compose` and a full testcontainers
pattern (pull + wait + inter-container + Ryuk) all pass.

This is the direct answer to the COV-115 escalation: where rootless `dockerd` hit an
unavoidable `CAP_SYS_ADMIN` wall at nested subuid mapping, **Sysbox dissolves that wall**
by running the sandbox container itself under `sysbox-runc` — the runtime provides the
userns/uid-mapping/procfs emulation host-side, so a **normal rootful** `dockerd` runs
inside an *unprivileged* container.

## Environment

colima (Lima) on Apple Silicon (aarch64), macOS Virtualization.framework; VM kernel
`6.8.0-117-generic` (Ubuntu 24.04 *noble*); Docker `29.5.2` (host) / `29.7.2` (inner).
Sysbox CE **v0.7.1** (arm64). Spike container = the `cove-docker-spike` image from
COV-115 (a `cove-image` floor + Docker CE engine/CLI/compose/buildx + faithful copies of
the hardening egress files + entrypoint). Throwaway; not committed.

## The proven recipe

1. **VM runtime.** Install Sysbox CE (arm64) in the colima Lima VM
   (`sysbox-ce_0.7.1.linux_arm64.deb` + `jq`). The `.deb` registers `sysbox-runc` in
   `/etc/docker/daemon.json` and starts `sysbox`/`sysbox-mgr`/`sysbox-fs`; the **default
   runtime stays `runc`** (Docker opts into Sysbox per-container). Kernel ≥6.3 means
   idmapped mounts are used (no shiftfs needed). *Must be made persistent across
   `colima stop/start` via a colima provision hook — see Open items.*
2. **Run-flags (the whole delta).** Add **`--runtime=sysbox-runc`** to the existing
   sandbox baseline (`--init --cap-add=NET_ADMIN --dns 1.1.1.1 …`). **Nothing else** —
   no `--privileged`, no socket mount, no `--device`, no `--security-opt`.
3. **Inner daemon.** A normal **rootful** `dockerd` runs inside (Sysbox handles the
   userns), using the native **`overlayfs`** storage driver (containerd snapshotter) —
   no `fuse-overlayfs`, no `/dev/fuse`. The agent uses the default
   `/var/run/docker.sock` (no `DOCKER_HOST` juggling). The entrypoint must start it after
   `nft`/`squid`, gated behind the opt-in flag.
4. **Proxy → squid.** Because `dockerd` shares the sandbox's network namespace, it reaches
   squid directly at `127.0.0.1:3128`. Configure the inner `daemon.json`:
   ```json
   { "proxies": { "http-proxy": "http://127.0.0.1:3128",
                  "https-proxy": "http://127.0.0.1:3128",
                  "no-proxy": "localhost,127.0.0.1,::1" },
     "features": { "containerd-snapshotter": true } }
   ```
   The `proxies` block is required for **buildkit** to use the proxy — with only the
   process-env `HTTP(S)_PROXY`, `docker run` pulls worked but `docker build` did a direct
   DNS lookup that nftables dropped.
5. **Registry allow-list.** Add **`.cloudfront.docker.com`** (Docker Hub's blob CDN) to
   the squid allow-list, in addition to `registry-1.docker.io`, `auth.docker.io`,
   `index.docker.io`. (The manifest/token hosts alone are insufficient; blobs 403'd on
   the un-listed CDN. Note: the CDN is **CloudFront**, not the `cloudflare.docker.com`
   host some docs cite.)
6. **Egress containment (a required hardening change).** Nested-container traffic is
   masqueraded and **forwarded**, so it bypasses the hardening's `output`-only nftables
   rule — confirmed: a nested container reached the internet directly. **Fix:** add a
   `forward` chain to the sealed `inet egress` table:
   ```
   chain forward {
       type filter hook forward priority 0; policy drop;
       ct state established,related accept;
   }
   ```
   This drops routed nested→uplink egress while leaving same-network
   container-to-container (L2-bridged, bypasses the forward hook) and the daemon's own
   proxied pulls (loopback/`output`) unaffected. Nested containers that need external
   network must be pointed at the proxy explicitly.

## What was tested (evidence)

- **Hardening intact under `sysbox-runc`:** the entrypoint's `nft -f` loaded and `squid`
  started with no errors; `sshd` serves. The sandbox's own direct egress stays blocked by
  the `output` `skuid proxy` rule.
- **Docker works:** `docker run` (alpine), `docker build` (buildkit), and
  `docker compose up` all succeeded, with pulls riding squid (verified in the access log:
  `CONNECT registry-1.docker.io/auth.docker.io/…cloudfront.docker.com`).
- **Egress containment:** before the `forward` rule, a nested container reached
  `https://1.1.1.1` directly (**gap**); after, it was **dropped**, while daemon pulls and
  container-to-container still worked.
- **Testcontainers pattern:** `postgres:16-alpine` (10 layers) pulled through squid,
  reported **ready** (`pg_isready`) in ~2s, a client container connected **by name** on a
  user network (`select 'tc-conn-ok'`), and the **Ryuk** reaper started with the
  docker-socket bind-mount and began "client processing".
- **Storage:** native `overlayfs` (containerd snapshotter), cgroup v2.

## Required codebase changes (input to the follow-up design)

- **VM provisioning:** install Sysbox in the Lima VM and make it persist (colima provision
  hook); document the `colima start` requirements.
- **Backend (`internal/backend/colima`):** thread the opt-in flag → add
  `--runtime=sysbox-runc` to the `Create`/`RunEphemeral` argv (hermetic argv tests, per
  the repo's plan/execution split).
- **Config (`internal/kit`):** the opt-in `docker: true` flag + validation (as in the
  original design §A).
- **Image (`cove-image`):** bake the Docker **engine** (dockerd/containerd/runc + CLI +
  compose/buildx) — rootful, *not* the rootless-extras path the old design assumed.
- **Entrypoint (hardening):** gated on the flag, start rootful `dockerd` after
  `nft`/`squid`, write the `daemon.json` proxy block, and configure a BuildKit GC size
  cap for the bounded cache.
- **Hardening egress:** add the `forward`-chain drop (scoped to docker-enabled kits) and
  add `.cloudfront.docker.com` to the allow-list guidance for Docker Hub.

## Open items / caveats

- **Persistence not tested.** Sysbox install must survive `colima stop/start` — handle via
  a provision hook; verify in the follow-up.
- **`forward` rule drops cross-*network* container routing** (multi-network compose that
  routes between bridges). Fine for typical single-network testcontainers; document, and
  widen the rule only if a real case needs it.
- **BuildKit GC size cap** was not exercised here (the rootless spike established the
  config shape); confirm it bounds the persistent cache volume in the follow-up.
- **Sysbox as a dependency:** a new host-side runtime with its own CVE surface — the
  follow-up design needs a threat-model pass (it trades the "no extra runtime" property
  for real isolation, a net improvement over the `CAP_SYS_ADMIN` alternative).
- The "kernel headers not found" install warning is benign (only matters for workloads
  compiling kernel modules inside a Sysbox container).

## Next step

Full **Sysbox re-scope design** for COV-114 (config + backend + VM provisioning +
entrypoint + egress `forward` rule + testing), then re-draft parts 2–5 (COV-116…119)
against this recipe.
