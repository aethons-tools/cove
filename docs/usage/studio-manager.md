---
summary: The `at-studio-manager` binary — a bare-metal process next to Colima that dials Jam over TLS and hosts studios; its install, config file, token, TLS, reconnect backoff, capacity, labels and image builds.
read_when: You are setting up or running `at-studio-manager` on a host, editing its `config.yml`, or debugging why it will not connect, declines raises, or rebuilds images.
owns: the `at-studio-manager` binary's config file, `run` verb, TLS trust, reconnect backoff, capacity gate, container/volume labels and per-host image builds
prereqs: jam/studio-managers.md for registering the manager and what Jam does with it
tier: leaf
updated: 2026-10-27
---

# at-studio-manager

`at-studio-manager` runs on a host with Colima, dials *into* Jam over TLS and raises studios there on Jam's behalf.
Registration, the token's auth, and disconnect/reconnect/revoke behaviour on Jam's side are in [Studio Managers](jam/studio-managers.md); this doc is the manager host's side.

## Install and run

`install.sh` and `at-jam update` install it next to `at-jam`; the first `at-jam update` from an older `at-jam` (whose embedded installer predates this binary) skips it, so run the update once more or use `install.sh`.
Its version must equal Jam's exactly, otherwise Jam refuses the stream.
An update replaces the binary, not the running process: restart `at-studio-manager run` after `at-jam update` or `install.sh`, or Jam keeps refusing the old process.

1. On Jam, `at-jam manager add <name>`, and save the printed token in `token-file` (whitespace is trimmed; the token is never logged).
2. Write the config below.
3. `at-studio-manager run [--config <path>]` serves until SIGINT or SIGTERM.

The token file is reread at each connect, so after `at-jam manager rotate` ([verbs](jam/studio-managers.md)) writing the new token to `token-file` is enough; the manager reconnects with it, no restart.

`at-studio-manager colima setup-docker|check-docker` are the same verbs as `at-jam colima ...`, owned by [docker-in-sandbox.md](docker-in-sandbox.md).

## Config

Default path `<dir>/config.yml`, where `<dir>` is `$XDG_CONFIG_HOME/at-studio-manager`, else `~/.config/at-studio-manager`.
The file is strict: unknown keys are rejected.

```yaml
name: mac-studio-1            # required; the registered manager name; not "local"
jam-addr: jam.example.com:443 # required; host:port of Jam's cove-facing TLS listener
server-name: ""               # TLS name override; default: the jam-addr host
ca-file: ""                   # PEM added to the system roots
token-file: ""                # default <dir>/token
capacity: 0                   # max studios; 0 = unlimited
log-level: info               # debug|info|warn|error
colima:
  runtime-addr: jam.example.com:443  # required; the Jam address studios dial
  jam-host: jam.example.com          # required; the Jam host name studios resolve
  docker-context: ""                 # default colima
  docker: false                      # Sysbox docker-in-sandbox
  dns: []
  identity-file: ""                  # default <dir>/id_ed25519 (generated on first use)
  known-hosts-dir: ""                # default <dir>/known_hosts.d
```

`identity-file` and `known-hosts-dir` default to the manager's own `<dir>`, not `local`'s at-cove directory.
The `colima` fields mean what the same fields of `runtime.launcher` mean for Jam ([serve.md](jam/serve.md)).
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

Containers carry `harbor.cove`, `harbor.cove.actor=<id>` and `harbor.cove.manager=<name>`.
State volumes carry `harbor.cove.manager` too, and their `harbor.cove.jam` value is `<runtime-addr>#<name>`.
These scope every list and sweep to one manager, so `local` and a manager (or two managers) can share one Colima daemon.
A manager restart leaves its studios running and re-reports them from these labels on reconnect.

## Names and moving between managers

A remote manager's containers are `atcove.<manager>.cove-<id>` and its volumes derive from that (`-agent-data`, `-workspace`, `-docker`); `local` keeps `atcove-cove-<id>`.
Local names start `atcove-` and a manager's `atcove.`, and a manager name holds no `.`, so no two managers' names (or `local`'s) can collide on a shared daemon.
So a standing session that moves from `local` to a manager on a shared daemon starts fresh: no state carries over.

## Image builds

Kit images are tagged with the kit digest and a fingerprint that includes the launcher's identity key.
A manager whose `identity-file` differs from `local`'s therefore builds its own images, even on a shared daemon.
