# dev

Assets for running a local dev instance of Jam.

- **`docker-compose.yml`** — a local PostgreSQL 17 for Jam.
- **`jam.dev.yml`** — a sample `at-jam serve` config wired to that DB.

## Postgres

`docker-compose.yml` raises PostgreSQL 17 matching Jam's CI/integration
conventions (`.github/workflows/store-integration.yml`): database/user/password
all `jam`, with `sslmode=disable` for local use. The host port is **15432**
(mapped to the container's 5432) to avoid clashing with any other local
Postgres. Data persists in the `pgdata` named volume. Jam applies its
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

## Running `at-jam serve`

`jam.dev.yml` is a ready-to-edit dev serve config pointed at the Postgres
above, with an inline dev DB password and loopback admin API. The broker
listener is always TLS and binds `jam.local.aethons.tools` (which DNS already
resolves to 127.0.0.1, so no `/etc/hosts` entry is needed). Generate a
self-signed cert for that name (`just dev-cert`, written to the gitignored
`dev/tls/`):

```sh
just dev-cert          # writes dev/tls/{cert,key}.pem (CN=jam.local.aethons.tools)
just dev-up
just build             # (re)build dist/<os>-<arch>/at-jam
just dev-serve         # runs the built at-jam serve --config dev/jam.dev.yml
```

`just dev-serve` runs the **last-built** `dist/<os>-<arch>/at-jam` (run `just
build` first — it does not rebuild), forwarding extra args (e.g. `just dev-serve
--config path/to/other.yml`). For arbitrary subcommands there is also `just
jam …` (e.g. `just jam destination list`). Binding a privileged port such
as `:443` needs root — run the built binary under `sudo` directly for that.

These settings (plaintext admin, inline password, self-signed cert) are for
local dev only. The production shape — TLS, OIDC operator auth, and
resolver-based credentials — is documented in
[`../docs/usage/harbor/serve.md`](../docs/usage/harbor/serve.md).
