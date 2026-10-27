# dev

Assets for running a local dev instance of Jam.

- **`docker-compose.yml`** — a local PostgreSQL 18 and Garage (the attachments blob store) for Jam.
- **`garage.toml`** — Garage's dev config.
- **`examples/`** — templates to copy: `jam.dev.yml.example` (a sample `jam serve` config wired to both, copied to `dev/jam.dev.yml`), and `jam.container.yml.example` + `credentials.container.yml.example` (Jam in a container, below).
- **`local/`** — git-ignored, for your own files (the container's secrets directory by default).

## Postgres

`docker-compose.yml` raises PostgreSQL 18 matching Jam's CI/integration
conventions (`.github/workflows/store-integration.yml`): database/user/password
all `jam`, with `sslmode=disable` for local use. The host port is **15432**
(mapped to the container's 5432) to avoid clashing with any other local
Postgres. Data persists in the `pgdata` named volume, mounted at
`/var/lib/postgresql` (the 18+ image layout; it refuses a mount at the old
`.../data` path). A volume created under Postgres 17 is not upgraded in
place — wipe it with `just dev-down -v` after the bump. Jam applies its
embedded migrations automatically on startup, so no init SQL lives here.

```sh
just dev-up            # start (docker compose up -d)
just dev-down          # stop, keeping data
just dev-down -v       # stop and wipe the data volume
```

Run the Jam integration tests against it:

```sh
export JAM_TEST_POSTGRES_DSN="host=localhost port=15432 dbname=jam user=jam password=jam sslmode=disable"
just integration-jam
```

## Attachments: Garage

The compose file also runs [Garage](https://garagehq.deuxfleurs.fr/), an
S3-compatible store, as the [attachments](../docs/usage/jam/attachments.md)
bucket: one node, S3 API on host port **13900**, data in the `garagemeta` and
`garagedata` volumes. On first start it lays itself out and creates the dev
access key and the `jam-attachments` bucket (`--single-node --default-bucket`,
from the `GARAGE_DEFAULT_*` variables), so there is nothing to set up by hand;
restarts keep them. The sample config's `blobs:` block points at it (region
`garage`, path-style). The key and the RPC secret in the compose file are
dev-only values — never reuse them.

Check it from the host with any S3 client, e.g. with `awscli`:

```sh
AWS_ACCESS_KEY_ID=GKjamdev0000000000000000 \
AWS_SECRET_ACCESS_KEY=jam-dev-secret-not-secret-0000000000000000 \
AWS_DEFAULT_REGION=garage \
aws --endpoint-url http://localhost:13900 s3 ls s3://jam-attachments/blobs/
```

or `docker compose -f dev/docker-compose.yml exec garage /garage bucket info jam-attachments`.

## Running `jam serve`

`jam.dev.yml` (copy it from `examples/jam.dev.yml.example`; it is git-ignored) is a ready-to-edit dev serve config pointed at the Postgres
above, with an inline dev DB password and loopback admin API. The broker
listener is always TLS and binds `jam.local.aethons.tools` (which DNS already
resolves to 127.0.0.1, so no `/etc/hosts` entry is needed). Generate a
self-signed cert for that name (`just dev-cert`, written to the gitignored
`dev/tls/`):

```sh
just dev-cert          # writes dev/tls/{cert,key}.pem (CN=jam.local.aethons.tools)
just dev-up
just build             # (re)build dist/<os>-<arch>/jam
just dev-serve         # runs the built jam serve --config dev/jam.dev.yml
```

`just dev-serve` runs the **last-built** `dist/<os>-<arch>/jam` (run `just
build` first — it does not rebuild), forwarding extra args (e.g. `just dev-serve
--config path/to/other.yml`). For arbitrary subcommands there is also `just
jam …` (e.g. `just jam destination list`). On macOS the wildcard
`listen: ":443"` binds without root; on Linux (or a specific-address bind) a
privileged port needs root — run the built binary under `sudo` directly for that.

**Skip login with `dev-identity`** (handy for UI work): set
`dev-identity: {user: <name>}` in `jam.dev.yml`, and loopback requests to
`/app` act as that user, with no IdP and no callbacks. The user needs a login
(`jam user login`), an OIDC identity (`jam user oidc`) and a project
membership. See `dev-identity` in [serve.md](../docs/usage/jam/serve.md). To
work on the Jam app with hot reload, run `just app-dev` beside `just dev-serve`
([web-app.md](../docs/web-app.md#dev-loop)).

## Jam in a container

The compose file also has an opt-in `jam` service that runs Jam from its image (`just jam-image`, `just dev-jam-up`).
Steps, ports, admin access and volumes are in [container.md](../docs/usage/jam/container.md).

These settings (plaintext admin, inline password, self-signed cert) are for
local dev only. The production shape — TLS, OIDC operator auth, and
resolver-based credentials — is documented in
[`../docs/usage/jam/serve.md`](../docs/usage/jam/serve.md).
