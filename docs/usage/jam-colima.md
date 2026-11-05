---
summary: The `jam-colima` binary — the Studio Manager for a Colima host, a bare-metal process that dials Jam over TLS and hosts studios in a Colima VM; its install, upgrade from jam-studio-manager, config file and `colima.profile`, token, TLS, reconnect backoff, capacity, the host memory/disk it reports, automatic disk cleanup, labels and image builds.
read_when: You are setting up or running `jam-colima` on a host, upgrading a host from `jam-studio-manager`, editing its `config.yml` (which VM via `colima.profile`, `cleanup` thresholds), or debugging why it will not connect, declines raises (capacity, disk full), removed or rebuilds images, or reports odd memory/disk figures to Jam.
owns: the `jam-colima` binary's config file and dir (and their move from jam-studio-manager), `run` verb, `colima.profile`, TLS trust, reconnect backoff, capacity gate, how host stats are measured, the disk cleanup policy and disk-full gate, container/volume labels and per-host image builds
prereqs: jam/studio-managers.md for registering the manager and what Jam does with it
tier: leaf
updated: 2026-11-05
---

# jam-colima

`jam-colima` is the Studio Manager for a Colima host: it runs next to Colima, dials *into* Jam over TLS and raises studios in a Colima VM on Jam's behalf.
Registration, the token's auth, and disconnect/reconnect/revoke behaviour on Jam's side are in [Studio Managers](jam/studio-managers.md); this doc is the manager host's side.

## Install and run

`install.sh` and `jam update` install it next to `jam`. Upgrading a host from a pre-rename release: follow [the rename runbook](jam/renamed-from-at.md#remote-studio-managers); from `jam-studio-manager`: [below](#upgrading-from-jam-studio-manager).
Its version must equal Jam's exactly, otherwise Jam refuses the stream.
An update replaces the binary, not the running process: restart `jam-colima run` after `jam update` or `install.sh`, or Jam keeps refusing the old process.

1. On Jam, `jam manager add <name>`, and save the printed token in `token-file` (whitespace is trimmed; the token is never logged).
2. Write the config below.
3. `jam-colima run [--config <path>]` serves until SIGINT or SIGTERM.

The token file is reread at each connect, so after `jam manager rotate` ([verbs](jam/studio-managers.md)) writing the new token to `token-file` is enough; the manager reconnects with it, no restart.

`jam-colima colima setup-docker|check-docker [--config <path>] [--profile <name>]` prepare the manager's Colima VM for `colima.docker: true`; they are owned by [docker-in-sandbox.md](docker-in-sandbox.md).

## Upgrading from jam-studio-manager

The binary was `jam-studio-manager`; the Studio Manager concept, Jam's `manager` verbs, labels, container names and the stream are unchanged. On each host, after Jam is upgraded:

1. **Install** with `jam update` or the one-command installer ([updating in place](../OVERVIEW.md#installing-the-binaries)). An older `jam update` installs the new `jam`, then fails on the missing `jam-studio-manager`; a second `jam update` (the new `jam`) completes it. `install.sh` (so `jam update` too) notices a leftover `jam-studio-manager` in the install directory; delete it after step 3.
2. **Edit `config.yml`** (still in the old dir): replace `colima.docker-context` with `colima.profile` — a config that still has it is refused. `docker-context: colima` (or none) is the `default` profile; `docker-context: colima-jam-b` is `profile: jam-b`. Rewrite any `token-file`, `ca-file`, `identity-file` or `known-hosts-dir` that names the old dir.
3. **Point the service at `jam-colima run`** and restart it. A `--config` into the old dir must become the same path under `<parent>/jam-colima/`: until it does, `run` reads the moved file and says so on each start.

The config dir moves by itself: at `run` or `colima setup-docker|check-docker`, if `<parent>/jam-colima` is missing and `<parent>/jam-studio-manager` exists, it is renamed (one stderr line). The identity key moves with it, so kit images and state are kept.

## Config

Default path `<dir>/config.yml`, where `<dir>` is `$XDG_CONFIG_HOME/jam-colima`, else `~/.config/jam-colima`.
The file is strict: unknown keys are rejected.

```yaml
name: mac-studio-1            # required; the registered manager name
jam-addr: jam.example.com:443 # required; host:port of Jam's cove-facing TLS listener
server-name: ""               # TLS name override; default: the jam-addr host
ca-file: ""                   # PEM added to the system roots
token-file: ""                # default <dir>/token
capacity: 0                   # max studios; 0 = unlimited
log-level: info               # debug|info|warn|error
colima:
  runtime-addr: jam.example.com:443  # required; the Jam address studios dial
  jam-host: jam.example.com          # required; the Jam host name studios resolve
  profile: default                   # the Colima VM (colima --profile); letters, digits, - _ .
  docker: false                      # Sysbox docker-in-sandbox
  dns: []
  identity-file: ""                  # default <dir>/id_ed25519 (generated on first use)
  known-hosts-dir: ""                # default <dir>/known_hosts.d
cleanup:              # percent of the VM's docker disk; 0 = the default
  high: 85            # a sample at or over it starts a cleanup
  low: 70             # a cleanup stops once under it
  critical: 95        # at or over it raises are declined
  daily: true         # the daily light pass
  disabled: false     # no automatic cleanup (raises are still declined at critical)
```

`identity-file` is the SSH key the manager uses to reach its studios (generated on first use); `known-hosts-dir` holds the studio host keys; both default to the manager's own `<dir>`.
`runtime-addr` is what a studio dials (`JAM_RUNTIME_ADDR`); `jam-host` is added to the studio's `/etc/hosts` so that name resolves to the host gateway.
`profile` names the Colima VM studios run in, read as colima reads `--profile`: `default` and `colima` are the default VM, and a `colima-` prefix is dropped (`colima-jam-b` is `jam-b`).
Every docker call is pinned to its docker context, `colima` for `default`, else `colima-<profile>`.
So `colima start --profile jam-b` and `profile: jam-b` place this manager's studios, kit images and volumes in that VM.
For a Jam in a container, `jam-addr` is the host's published 443 ([pointing managers at it](jam/container.md#pointing-studio-managers-at-it)).

## TLS

The manager connects only to Jam's cove-facing :443, trusting the system roots plus `ca-file`; set `server-name` when the certificate name differs from the `jam-addr` host.
Jam sends its SSH user CA in its Welcome, so the manager host needs no CA config.

## Reconnect

The retry wait starts at 1s and doubles to 30s; it resets once Jam's Welcome arrives.
A refusal (`Unauthenticated`, `PermissionDenied`, or `FailedPrecondition` such as a version mismatch) logs one Error and waits 30s.
After a refusal the backoff stays at 30s until a connect succeeds.

## Capacity

`capacity` bounds the studios this host holds, counting raises in flight.
At the limit a raise is declined with `at capacity (<n>/<cap> studios)` and Jam tries the next manager ([placement](jam/coves.md#managers-and-placement)).

## Host stats

Every 30s (and right after connecting) the manager reports its VM's memory and disk to Jam, which shows them and alerts on a full disk ([status reports](jam/studio-managers.md#status-reports)).
They are measured inside the `colima.profile` VM:

- `colima ssh --profile <p> -- sh -c 'df -kP /var/lib/docker; cat /proc/meminfo'`: memory used is `MemTotal − MemAvailable`; disk is the filesystem holding docker's data dir (df's one data row, whatever its mount point; often `/`), with total = used + available, so the percentage matches df's `Capacity` (root-reserved blocks excluded).
- `docker --context <ctx> system df --format '{{json .}}'`: the Reclaimable of Images, Build Cache and Local Volumes (docker's decimal sizes, `1.2GB` = 1.2×10⁹ bytes). Containers are not counted; they go with their studios.

If either command fails, or the two take over 20s together (a hung `colima ssh`), the report carries no host (one Warn in the manager's log until it recovers, then debug); Jam keeps the previous figures' condition as it was.

## Disk cleanup

Each host stats sample also drives the disk policy; `cleanup` sets its thresholds, which must satisfy `0 < low < high < critical ≤ 100`.
A sample at or over `high` starts a cleanup in the background (one at a time) that runs these steps in order, re-measuring after each and stopping as soon as the disk is under `low`:

1. dangling images (`docker image prune -f`);
2. unused kit images (`jam-kit`, `jam-kit-base`, `jam-kit-draft` tags only), oldest first, one at a time with `docker image rm <id>` (no `-f`), re-measuring after each. Skipped: images a container uses (running or stopped), images younger than 1h (just prepared, about to be raised), and images another repository also names;
3. unused anonymous volumes (`docker volume prune -f --filter label=com.docker.volume.anonymous`);
4. the build cache (`docker builder prune -f`).

Steps 1 and 2 are skipped while this manager has a raise, kit prepare or draft build in flight; while a prune or removal of these runs, a new one waits for it.
Kit base builds are tagged from the start (a temporary `jam-kit-base:building-<hex>`, untagged once the stable tag is on), so no dangling-image prune, this manager's or another's, takes one mid-build.

Every high or critical run starts a 5-minute cooldown. Within it no high run starts; a critical sample still runs at once, unless the run that started the cooldown was itself critical.
With `daily`, a light pass (steps 1 and 3 only, whatever the disk) runs 10 minutes after start, then every 24 hours.
`disabled: true` turns all automatic cleanup off.

While the last sample is at or over `critical`, raises are declined with `disk full (<n>%)`, so Jam places them elsewhere ([placement](jam/coves.md#managers-and-placement)); a sample that fails to measure lifts this. Kit prepares are not declined.

Each run is logged (Info; Warn when a step failed, the rest still running) and reported to Jam at once as the host's last cleanup: when, bytes freed (disk used before minus after) and its reason, `high`, `critical` or `daily`.

Never removed: named volumes (studio state, on any daemon version: the label filter matches only volumes docker 23+ created anonymously), containers, images a container uses, and images under any other name.
On a shared VM, dangling images, anonymous volumes and the build cache are VM-wide garbage, cleaned for everyone; removing an unused kit image may make another manager rebuild it (time, never data).

## Labels and sharing a daemon

Containers carry `jam.studio`, `jam.studio.actor=<id>` and `jam.studio.manager=<name>`.
State volumes carry `jam.studio.manager` too, and their `jam.studio.jam` value is `<runtime-addr>#<name>`.
These scope every list and sweep to one manager, so two managers can share one Colima daemon.
A manager restart leaves its studios running and re-reports them from these labels on reconnect.

## Names and moving between managers

A manager's containers are `jam-studio.<manager>.<id>` and its volumes derive from that (`-agent-data`, `-workspace`, `-docker`).
A manager name holds no `.`, so no two managers' names can collide on a shared daemon.
A standing session that moves to another manager starts fresh: no state carries over.

## Image builds

Kit images are tagged with the kit digest and a fingerprint that includes the manager's identity key.
Two managers with different `identity-file`s therefore build their own images, even on a shared daemon.
