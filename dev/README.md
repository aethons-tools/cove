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
jam …` (e.g. `just jam destination list`). On macOS the wildcard
`listen: ":443"` binds without root; on Linux (or a specific-address bind) a
privileged port needs root — run the built binary under `sudo` directly for that.

### Live reload: `just dev-watch`

`just dev-watch` runs [air](https://github.com/air-verse/air) (version pinned in
the recipe, fetched with `go run`; config in `../.air.toml`). It rebuilds
`at-jam` and restarts `serve` whenever a `.go`, `.html`, `.js` or `.css` file
changes. Templates and htmx are `go:embed`ed, so UI edits rebuild too. Browse
**`http://localhost:8090`** (`/me/`, `/ui/`): air's proxy fronts the admin
listener and reloads the page after each restart.

- **Plain-HTTP admin listener required.** air's proxy speaks plain HTTP to
  `localhost:8081`, so comment out `admin-tls` in `jam.dev.yml` (loopback admin
  may be plain HTTP). The recipe refuses to start while `admin-tls` is set.
  CLI admin URLs then become `http://127.0.0.1:8081`.
- **UI writes need `ui-origins`.** The proxy forwards to Jam with `Host:
  localhost:8081` while the browser's `Origin` is `http://localhost:8090`, so
  the UI's CSRF check refuses writes ("cross-origin request refused") unless
  `jam.dev.yml` lists `ui-origins: [http://localhost:8090]` (as the example
  does). See `ui-origins` in [serve.md](../docs/usage/jam/serve.md).
- **No sudo by default.** Jam runs as you. On macOS a wildcard bind of `:443`
  (`listen: ":443"`, as in `jam.dev.yml.example`) needs no root, but a listen
  bound to a specific address (e.g. the hostname) does. `JAM_WATCH_SUDO=1 just
  dev-watch` runs Jam under sudo instead: the recipe primes `sudo -v`, and sudo
  prompts on the terminal again if its cached login expires.
- **Failed builds** leave the last good binary serving, so Jam stays up.
- **Skip login entirely with `dev-identity`** (recommended for UI work). Set
  `dev-identity: {project: <p>, human: <name>}` in `jam.dev.yml`, and loopback
  requests to `/ui` and `/me` act as that roster human, with no IdP and no
  callbacks. The human needs `--login` (for `/ui` actions like Request) and
  `--oidc` (for `/me`). See `dev-identity` in
  [serve.md](../docs/usage/jam/serve.md).
- **Real `/me` login through the proxy** also works, but the OIDC redirect URI
  is built from the proxied Host. The callback lands on
  `http://localhost:8081/me/auth/callback` (add it to the IdP's allowed
  callbacks); after login, go back to `:8090`. The session cookie is
  host-scoped, so it carries over and survives restarts.
- Edits under `dev/` (including `jam.dev.yml`) don't trigger a restart.
  `JAM_WATCH_CONFIG=path` points the loop at another config.

These settings (plaintext admin, inline password, self-signed cert) are for
local dev only. The production shape — TLS, OIDC operator auth, and
resolver-based credentials — is documented in
[`../docs/usage/jam/serve.md`](../docs/usage/jam/serve.md).
