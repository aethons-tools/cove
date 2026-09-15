# dev

Assets for running a local dev instance of harbor.

- **`docker-compose.yml`** — a local PostgreSQL 17 for harbor.
- **`harbor.dev.yml`** — a sample `at-harbor serve` config wired to that DB.

## Postgres

`docker-compose.yml` raises PostgreSQL 17 matching harbor's CI/integration
conventions (`.github/workflows/store-integration.yml`): database/user/password
all `harbor`, with `sslmode=disable` for local use. The host port is **15432**
(mapped to the container's 5432) to avoid clashing with any other local
Postgres. Data persists in the `pgdata` named volume. harbor applies its
embedded migrations automatically on startup, so no init SQL lives here.

```sh
just dev-up            # start (docker compose up -d)
just dev-down          # stop, keeping data
just dev-down -v       # stop and wipe the data volume
```

Run the harbor integration tests against it:

```sh
export HARBOR_TEST_POSTGRES_DSN="host=localhost port=15432 dbname=harbor user=harbor password=harbor sslmode=disable"
just integration-harbor
```

## Running `at-harbor serve`

`harbor.dev.yml` is a ready-to-edit dev serve config pointed at the Postgres
above, with an inline dev DB password and loopback admin API. The broker
listener is always TLS and binds `harbor.local.aethons.tools` (which DNS already
resolves to 127.0.0.1, so no `/etc/hosts` entry is needed). Generate a
self-signed cert for that name (`just dev-cert`, written to the gitignored
`dev/tls/`):

```sh
just dev-cert          # writes dev/tls/{cert,key}.pem (CN=harbor.local.aethons.tools)
just dev-up
at-harbor serve --config dev/harbor.dev.yml
```

These settings (plaintext admin, inline password, self-signed cert) are for
local dev only. The production shape — TLS, OIDC operator auth, and
resolver-based credentials — is documented in
[`../docs/usage/harbor/serve.md`](../docs/usage/harbor/serve.md).
