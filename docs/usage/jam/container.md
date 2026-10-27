---
summary: Running `jam serve` in a container — the `ghcr.io/aethons-tools/jam` image and its tags, what is and isn't inside it, the opt-in dev compose service, its ports, admin access, volumes, where `command:` resolvers run, pointing Studio Managers at it, and version lockstep.
read_when: You want to run Jam from its container image (the dev compose `jam` service or your own), build or pull the image, reach a containerized Jam's admin API, decide what must persist, or point Studio Managers and studios at it.
owns: the Jam container image (name, tags, platforms, contents, build), the container serve config's shape, the compose `jam` service and its `just` recipes, the container's ports/admin access/volumes, and what a launcher-free serve needs at runtime
prereqs: serve.md for the serve config; studio-managers.md for how managers connect
tier: leaf
updated: 2026-10-27
---

# Jam in a container

Jam can run as a container because Jam drives no Docker itself: remote [Studio Managers](studio-managers.md) next to Colima dial in and host the studios.
The image is `ghcr.io/aethons-tools/jam`. It runs only `jam serve`, as a non-root user, with no docker, ssh or git binary.

## The image

| Tag | Built by | Platforms |
|-----|----------|-----------|
| `<N>-<MMDD>` | release CI on a push to `main` (the release version, the same as the binaries' `jam version`) | `linux/amd64`, `linux/arm64` |
| `latest` | the same publish | the same |
| `jam:dev` | `just jam-image` locally | the host's arch only (`linux/$(go env GOARCH)`) |

The GHCR package may be private: pulling `ghcr.io/aethons-tools/jam` needs `docker login ghcr.io`.

Pull requests build an amd64 image and smoke it with `docker run --rm <image> version`; they never publish.

The image is built from already-built release binaries, not compiled in Docker.
`just jam-image` runs `scripts/build.sh linux/<arch>`, then `scripts/jam-image.sh`.
The script takes `--dist DIR` (default `dist`), `--platforms LIST`, `--tag T` (repeatable, default `jam:dev`) and `--push`.
`docker buildx` is a prerequisite (with Homebrew docker: `brew install docker-buildx`).
Without `--push` it loads one platform into the local daemon. More than one platform needs `--push` and a docker-container buildx builder.

What is inside (`images/jam/Dockerfile`):

- base `alpine:3.22` plus `ca-certificates`, needed for outbound HTTPS (OIDC, Anthropic, git hosts, trackers);
- `/usr/local/bin/jam` (which is also the minter, `jam mint`);
- user `jam`, uid/gid `10001`, home `/var/lib/jam` (`HOME` is set to it);
- `VOLUME /var/lib/jam`, `EXPOSE 8443 8081`;
- `ENTRYPOINT ["jam"]`, `CMD ["serve","--config","/etc/jam/jam.yml"]`.

Why nothing else is needed: `jam serve` runs no external programs except two kinds of [credential](credentials.md) resolvers.
`command:` entries are exec'd directly, with no shell. `mint:` entries run `jam mint`, which ships in the image.
Git fetches (kit and config sources) are in-process go-git. Jam never needs a docker socket, SSH keys, `host-gateway` or loopback access to studios.

## Running it with the dev compose

The `jam` service in `dev/docker-compose.yml` is opt-in (profile `jam`), beside the dev Postgres and Garage ([dev/README.md](../../../dev/README.md)).

1. Make the secrets directory, `dev/local/secrets` (git-ignored; set `JAM_SECRETS_DIR` to the **absolute** path of another, since a relative one resolves against `dev/`). It holds `credentials.yml` and `tls/{cert,key}.pem`.
2. `cp dev/examples/credentials.container.yml.example dev/local/secrets/credentials.yml`, and `cp dev/examples/jam.container.yml.example dev/jam.container.yml` (git-ignored). Fill in the OIDC placeholders.
3. TLS: `just dev-cert` writes `dev/tls/{cert,key}.pem` (and makes the key `0644`; see below); copy them into `dev/local/secrets/tls/`. A real run needs a cert studios trust ([the cert gotcha](dispatch-runbook.md#gotchas-each-one-cost-a-real-debugging-loop)); put that one there instead.
4. `just jam-image`, or set `JAM_IMAGE=ghcr.io/aethons-tools/jam:<N>-<MMDD>` to run a release.
5. `just dev-jam-up` checks those files exist, then starts Postgres, Garage and Jam (`docker compose --profile jam up -d`).
6. `just dev-jam-logs` follows Jam's log.

The container config differs from a host config in these ways:

- `runtime.studio-managers: true`, the only way studios run;
- `store-postgres.host: postgres` and `blobs.endpoint: http://garage:3900`, the compose service names;
- `credentials-file: /etc/jam/secrets/credentials.yml`;
- TLS files under `/etc/jam/secrets/tls/`.

Every path in it is inside the container.

Jam runs as uid `10001`, and the mounts are read-only, so every mounted secret file must be readable by that uid.
On Colima a `0600` host file is not, and serve exits at startup.
Do not symlink a Let's Encrypt `live/` dir into the secrets directory: its files are relative symlinks into `../../archive` that do not resolve in the mount, and `privkey.pem` is `0600`.
Copy the files dereferenced instead: `cp -L .../fullchain.pem DIR/tls/cert.pem` and `cp -L .../privkey.pem DIR/tls/key.pem`, then make `key.pem` readable by uid 10001 (dev: `chmod 644`, or `chown 10001`).
The same applies to a `0600` `credentials.yml`.

## Ports

| In the container | Published on the host | Why |
|------------------|-----------------------|-----|
| `listen: ":8443"` | `443:8443` | The cove-facing TLS listener: the broker, Attach, and manager Connect. Jam runs non-root, so it listens on 8443 and compose publishes the standard 443. |
| `admin-listen: ":8081"` | `127.0.0.1:8081:8081` | The admin API, on jam's default admin port, so an in-container CLI needs no `--admin-url`. It is published only on host loopback. |

## Admin access

`admin-listen: ":8081"` is not a loopback bind, so serve refuses to start without `admin-tls`/`tls` plus `operator-auth.oidc` ([the exposure rule](serve.md#exposing-the-admin-api-fail-closed)).
Serve discovers the OIDC issuer at startup, so the issuer must be reachable from the container.

- **From the host:** use OIDC, as for any remote admin ([operators.md](operators.md)), e.g. `jam login --admin-url https://jam.local.aethons.tools:8081`.
  A docker port-forward reaches Jam from the bridge gateway, not from loopback, so loopback trust never applies to the published port.
- **Loopback CLI:** `just dev-jam <verb>` runs `docker compose exec jam jam <verb>`, for example `just dev-jam manager list`.
  That runs in the container and reaches `http://127.0.0.1:8081` there.
  Arguments that name files must be paths inside the container.

Keep `dev-identity` and loopback trust out of the container config: the published ports must stay behind OIDC.

## Volumes: what must persist

| Mount | Holds | Persist? |
|-------|-------|----------|
| the Postgres volume (`pgdata` in dev) | the control plane: roster, kits, studios, managers, the message log | yes, this is Jam's state ([backup.md](backup.md)) |
| `jamstate:/var/lib/jam` | `$HOME`, so the default [`state-dir`](serve.md#upgrading-relay-state) (relay cursors, markers, receipts), plus the [pool](pool.md) store if one is used | yes |
| `./jam.container.yml:/etc/jam/jam.yml:ro` | the serve config | your source of truth; keep it |
| `${JAM_SECRETS_DIR:-./local/secrets}:/etc/jam/secrets:ro` | `credentials.yml`, `tls/cert.pem`, `tls/key.pem` | your source of truth; keep it |

A `pool.store` path must be inside `/var/lib/jam`. Anywhere else, it is lost when the container is recreated.

## `command:` resolvers run in the container

A credential `command:` runs inside the container, which has no docker, ssh, git or gh, and not on the host.
Use `value:`, `mint:`, or a command over a mounted file, such as `["cat", "/run/secrets/x"]`.

## Pointing Studio Managers at it

A manager on the same host reaches the container at the host's published 443.
In dev, both its `jam-addr` and `colima.runtime-addr` are `jam.local.aethons.tools:443`, and `colima.jam-host` is `jam.local.aethons.tools` ([manager config](../studio-manager.md#config)).
Studios still reach the same address through the `--add-host <jam-host>:host-gateway` mapping.
Register each manager with `manager add` ([studio-managers.md](studio-managers.md)).

## Version lockstep

Jam refuses a manager whose version differs from its own ([How a manager connects](studio-managers.md#how-a-manager-connects)).
The image tag is the release version, so run `ghcr.io/aethons-tools/jam:<N>-<MMDD>` with managers on that same release.
Locally, `just jam-image` and `just build` stamp the same `git describe` version when run from the same checkout.

A live run of this setup, end to end, is [studio-manager-live-test.md](studio-manager-live-test.md).
