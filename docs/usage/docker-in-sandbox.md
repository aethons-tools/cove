---
summary: The operator guide to docker-in-sandbox — turning on `docker: true`, the one-time Sysbox VM prerequisite, registry allow-list recipes, how nested-container egress behaves, and the feature's limitations.
read_when: You are enabling Docker inside a sandbox (a kit that runs `docker build` / `docker compose up` / testcontainers) — flipping the flag, installing Sysbox in the colima VM, allow-listing a registry, or debugging a nested container that can't reach the network.
owns: the docker-in-sandbox usage story — the Sysbox VM prerequisite (install + colima provision hook), registry allow-list recipes, nested-container egress behavior, and docker-in-sandbox limitations
prereqs: ../OVERVIEW.md for the sandbox + egress model; at-cove-config.md#docker for the flag's schema
tier: leaf
updated: 2026-08-08
---

# Docker inside the sandbox

Set `docker: true` in a kit and its sandboxes get a **working Docker for testing
workloads** — `docker build`, `docker compose up`, and **testcontainers** all run
*inside* the sandbox — with the egress lock and the rest of the hardening intact.
It runs on the **Sysbox** runtime, which requires a **one-time install in the
colima VM** (below); at-cove's preflight fails fast and points you here if it's
missing.

For *why* this design is safe (no `--privileged`, no host socket) and the full
threat model, see the [Sysbox docker-in-sandbox design](../superpowers/specs/2026-08-08-sysbox-docker-in-sandbox-design.md).

## Enabling it

The single switch is the top-level kit flag — schema and validation live in
[`at-cove-config.md`](at-cove-config.md#docker):

```yaml
# .at-cove/config.yml
docker: true
```

When set, `at-cove install`/`create` provision the sandbox so that:

- the **sandbox container itself** runs under `--runtime=sysbox-runc`, which lets a
  normal **rootful `dockerd`** run inside an *unprivileged* container;
- the inner daemon is exposed on the default socket, so the `agent` user runs
  `docker …` with **no `DOCKER_HOST` to set** (it is in the `docker` group);
- a **persistent, bounded cache volume** (`atcove-{kit}[-{class}]-docker`) is
  mounted at the inner `/var/lib/docker`, so pulled layers and build cache survive
  `recreate` (removed on `destroy`).

A kit without the flag is **byte-for-byte unchanged** — no Sysbox runtime, no
cache volume, no systemd.

## Prerequisite: install Sysbox in the colima VM (one-time)

Sysbox is a **VM-level runtime** — at-cove *detects* it but never installs it. The
colima Lima VM's Docker daemon must register the `sysbox-runc` runtime, and the
install must **survive `colima stop/start`**. Do that with a **colima provision
hook** (a system-mode script re-run on every VM boot), so it is durable rather
than an ephemeral in-VM tweak.

Open the colima config and add a `provision:` entry:

```console
$ colima start --edit
```

```yaml
# in the colima config:
provision:
  - mode: system
    script: |
      #!/usr/bin/env bash
      set -euo pipefail
      command -v sysbox-runc >/dev/null 2>&1 && exit 0   # idempotent: already installed
      arch="$(dpkg --print-architecture)"                 # amd64 | arm64
      ver="0.6.4"   # check https://github.com/nestybox/sysbox/releases for the latest
      apt-get update && apt-get install -y jq
      curl -fsSL -o /tmp/sysbox.deb \
        "https://github.com/nestybox/sysbox/releases/download/v${ver}/sysbox-ce_${ver}-0.linux_${arch}.deb"
      apt-get install -y /tmp/sysbox.deb                   # registers sysbox-runc; starts sysbox{,-mgr,-fs}
```

Saving triggers the VM restart that runs the hook. The `.deb` registers the
`sysbox-runc` runtime and starts the `sysbox`, `sysbox-mgr`, and `sysbox-fs`
services; on a kernel ≥6.3 it uses idmapped mounts (no shiftfs). Confirm with:

```console
$ colima ssh -- docker info -f '{{json .Runtimes}}' | jq 'has("sysbox-runc")'
true
```

If `docker: true` and the runtime is absent, `at-cove` **fails the preflight** with
an actionable message pointing back here — it will not silently fall back.

> A one-off `colima ssh` install works for the current session but is lost on the
> next `colima stop/start`. Use the provision hook so it persists.

## Allow-listing registries

Image pulls ride the sandbox's **squid proxy**, so each registry's domains must be
in the kit's root [`image.allowed-domains`](at-cove-config.md#imageallowed-domains)
— the same additive allow-list every other egress uses
([three additive allow-lists](../OVERVIEW.md#egress-three-additive-allow-lists-session-scoped)).
Both the daemon *and* BuildKit route through squid, so this covers `docker build`
too, not just `docker run` pulls.

The registries need their **blob CDNs**, not just their API hosts — pulls fail
mid-layer otherwise:

```yaml
image:
  allowed-domains:
    # Docker Hub (docker.io)
    - registry-1.docker.io
    - auth.docker.io
    - index.docker.io
    - .cloudfront.docker.com    # the blob CDN (CloudFront) — required for layer downloads
    # GitHub Container Registry (ghcr.io)
    - ghcr.io
    - pkg-containers.githubusercontent.com
```

Add only the registries a kit actually pulls from. When a pull is blocked, the
denial surfaces as a squid `403` naming the host — add it here.

## Nested-container egress

A nested container's traffic is masqueraded and **forwarded** out the uplink, so it
would sidestep the agent's `output`-chain egress lock. An **always-on** `nftables`
`forward`-chain drop closes that gap (accepting only established/related), so:

- **Inter-container and localhost traffic just works** — same-network
  container-to-container is L2-bridged and never traverses the `forward` hook, and
  loopback is unaffected. Typical single-network `docker compose` / testcontainers
  setups need nothing extra.
- **A container that needs the *external* network must go through the proxy.** Point
  standard proxy env at the sandbox's squid, reachable on the Docker bridge gateway
  (`172.17.0.1` on the default bridge):

  ```console
  $ docker run --rm \
      -e HTTP_PROXY=http://172.17.0.1:3128 \
      -e HTTPS_PROXY=http://172.17.0.1:3128 \
      -e NO_PROXY=localhost,127.0.0.1 \
      curlimages/curl -fsS https://ghcr.io > /dev/null
  ```

  The destination host must still be in `image.allowed-domains` — the nested
  container gets the *same* allow-list, with no bypass.

The full egress model is in the
[design spec §E](../superpowers/specs/2026-08-08-sysbox-docker-in-sandbox-design.md#e-egress).

## Limitations

- **No `--privileged` containers** inside the sandbox — Sysbox provides isolation
  host-side; nested workloads run unprivileged.
- **Cross-*network* container routing is dropped.** The `forward` drop also blocks
  routing between two nested bridges (multi-network compose that routes between
  networks). Single-network setups are fine.
- **The cache is bounded, not durable storage.** The `/var/lib/docker` volume is
  BuildKit-GC-capped — treat it as a cache for build layers and base images, not an
  image archive.
- **Requires Sysbox in the colima VM** (above); the preflight guides you if it's
  absent.

See the [design spec §J](../superpowers/specs/2026-08-08-sysbox-docker-in-sandbox-design.md#j-limitations-document-up-front)
for the reasoning behind each.
