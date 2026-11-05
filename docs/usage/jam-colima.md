---
summary: The `jam-colima` binary — the Studio Manager for a Colima host, a bare-metal process that dials Jam over TLS and hosts studios in a Colima VM; its install, upgrade from jam-studio-manager, config file and `colima.profile`, token, TLS, reconnect backoff, capacity, labels and image builds.
read_when: You are setting up or running `jam-colima` on a host, upgrading a host from `jam-studio-manager`, editing its `config.yml` (which VM via `colima.profile`), or debugging why it will not connect, declines raises, or rebuilds images.
owns: the `jam-colima` binary's config file and dir (and their move from jam-studio-manager), `run` verb, `colima.profile`, TLS trust, reconnect backoff, capacity gate, container/volume labels and per-host image builds
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
3. **Point the service at `jam-colima run`** (`--config` if you pass one) and restart it.

The config dir moves by itself: at `run`, if `<parent>/jam-colima` is missing and `<parent>/jam-studio-manager` exists, it is renamed (one stderr line). The identity key moves with it, so kit images and state are kept.

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
```

`identity-file` is the SSH key the manager uses to reach its studios (generated on first use); `known-hosts-dir` holds the studio host keys; both default to the manager's own `<dir>`.
`runtime-addr` is what a studio dials (`JAM_RUNTIME_ADDR`); `jam-host` is added to the studio's `/etc/hosts` so that name resolves to the host gateway.
`profile` names the Colima VM studios run in; every docker call is pinned to its docker context, `colima` for `default`, else `colima-<profile>`.
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
