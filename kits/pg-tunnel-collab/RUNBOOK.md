# pg-tunnel-collab RUNBOOK

An **example** collaborator kit: an `at-cove chat` session (`db-analyst` class)
reaches a customer's external Postgres by tunneling the PG wire protocol over
**WSS/443** with [`wstunnel`](https://github.com/erebe/wstunnel), through the
cove's existing squid `CONNECT`-to-:443 egress. No change to at-cove core or the
sealed hardening layer — the only egress widening is the tunnel server's hostname.

## How it fits the egress model

A cove's agent has no direct network; all egress goes through squid, which permits
`CONNECT` only to **:443** and only to allow-listed hostnames. `wstunnel` wraps the
Postgres TCP stream in a WSS connection to a server on :443, so it rides that exact
path. Allow-listing the tunnel host (below) is the whole change.

```
psql "$DATABASE_URL"  ->  127.0.0.1:5432 (wstunnel client)
   -> squid CONNECT tunnel.example.com:443  ->  wstunnel server  ->  customer Postgres
```

## The server side (you run this, in front of the customer DB)

On a host that terminates TLS on :443 and can reach the customer Postgres:

```bash
wstunnel server \
  --restrict-to db.internal:5432 \
  --restrict-http-upgrade-path-prefix "$PG_TUNNEL_SECRET" \
  wss://0.0.0.0:443
```

- `--restrict-to` stops the server being an open relay — it may forward only to the DB.
- `--restrict-http-upgrade-path-prefix` requires the shared secret the client sends.
- Terminate TLS on :443 (front it with your own cert / a TLS proxy as needed).

## Supply the secrets (machine-side, out of source control)

In `~/.config/at-cove/secrets.yml`, provide values for the four `db-analyst`
secrets declared in `config.yml` (see the repo's secrets doc for the file format):

| secret | example |
|---|---|
| `PG_TUNNEL_URL` | `wss://tunnel.example.com:443` |
| `PG_TUNNEL_SECRET` | a long random string (also on the server's `--restrict-http-upgrade-path-prefix`) |
| `PG_TARGET` | `db.internal:5432` |
| `DATABASE_URL` | `host=db.internal hostaddr=127.0.0.1 port=5432 user=… dbname=… sslmode=verify-full` |

**End-to-end TLS — read this.** `DATABASE_URL` uses `hostaddr=127.0.0.1` to dial the
local tunnel entrance but `host=db.internal` so libpq verifies the Postgres server
certificate against the *real* DB hostname. This keeps `sslmode=verify-full` working
through the tunnel, so the relay never sees plaintext credentials or data. Plain
`host=127.0.0.1 sslmode=verify-full` fails hostname verification — do **not** "fix"
that by downgrading to `sslmode=require`; use the `host`/`hostaddr` split instead.
Also point the tunnel host in `config.yml` (`collaborators.db-analyst.allowed-domains`)
at your real server, and rotate `PG_TUNNEL_SECRET` periodically.

## Run it

```bash
at-cove install --project-dir kits/pg-tunnel-collab   # build the image
at-cove chat db-analyst --project-dir kits/pg-tunnel-collab
# then, inside the session:
pg-tunnel-up            # brings the tunnel up (idempotent; safe to re-run)
psql "$DATABASE_URL"    # -> 127.0.0.1:5432 -> tunnel -> customer Postgres
```

## Prototype simplifications

- **Auth** is a shared HTTP-upgrade path-prefix secret, not mTLS.
- **Launch** is manual (`pg-tunnel-up`, idempotent), not a supervised service —
  a plain cove has no service manager. Re-run the helper if the tunnel drops.

## Test the launcher

```bash
bash kits/pg-tunnel-collab/.at-cove/image/pg-tunnel-up.test.sh   # hermetic, no network
shellcheck kits/pg-tunnel-collab/.at-cove/image/pg-tunnel-up
```
