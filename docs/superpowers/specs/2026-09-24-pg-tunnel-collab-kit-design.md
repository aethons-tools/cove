# pg-tunnel-collab: reach an external Postgres over WSS/443, kit-only

**Status:** design approved (scope + launcher + run-path + prototype simplifications confirmed), pre-plan
**Motivation:** A customer's agent needs to do work against the customer's Postgres, but a cove's egress is HTTPS-only (squid `CONNECT` to :443 for allow-listed domains; nftables drops all other direct egress). Raw TCP to `:5432` cannot pass. Tunnel the PG wire protocol over WSS/443 with `wstunnel` so it rides the *existing* egress path — no change to at-cove core or the sealed hardening layer.
**Touches:** a new example kit only — `kits/pg-tunnel-collab/` (`config.yml`, `image/Dockerfile`, `image/pg-tunnel-up`, `RUNBOOK.md`) plus a docs index pointer. No Go code, no `internal/assemble/hardening/` change.
**Explicitly out of scope:** any change to the sealed egress/hardening layer; a first-class `config.yml` tunnel surface (rejected in favor of a recipe); mTLS auth and supervised/auto-restarted tunneling (named prototype simplifications, below); the autonomous-worker and "both" run paths (this kit targets interactive `chat` only); standing up the tunnel *server* (documented as a reference command, not shipped).

## Problem

The cove egress model is HTTP(S)-only and domain-based, enforced by two independent sealed layers:

- **nftables** (`internal/assemble/hardening/image-files/etc/nftables.conf`): the `agent` user gets no direct network (loopback only, to reach squid); only the `proxy` user (squid) may egress, and only on ports 53/80/443; everything else is `policy drop`.
- **squid** (`.../etc/squid/squid.conf`): `acl SSL_ports port 443` + `http_access deny CONNECT !SSL_ports` — `CONNECT` tunnels are permitted only to **:443**, and only to allow-listed `dstdomain` hostnames.

So a direct dial to `db-host:5432` is dropped by nftables, and a `CONNECT db-host:5432` is denied by squid. Neither "add the DB host to `allowed-domains`" nor "run PG on :443" works: squid still refuses `CONNECT` to any port but 443, `libpq`/pgx don't speak the HTTP `CONNECT` handshake, and the agent can't dial :443 directly (only the `proxy` user can).

The key opening: squid **already** permits `CONNECT host:443` for an allow-listed `host`, without TLS interception (it matches on the CONNECT hostname). So any tool that wraps the PG TCP stream in a WSS connection to a server on **:443** at an allow-listed host fits the existing model with **zero hardening change** — the only widening is one `allowed-domains` entry.

## Design

An example kit, `kits/pg-tunnel-collab/`, modeled on `kits/reference-worker/`. It ships the tunnel **client** and documents the reference **server**. A human-supervised `at-cove chat` collaborator (`db-analyst`) brings the tunnel up with a helper, then works against the customer DB through a local `127.0.0.1:5432`.

Data flow:

```
agent shell (chat session, db-analyst class)
  │  psql "$DATABASE_URL"           # DATABASE_URL → 127.0.0.1:5432
  ▼
127.0.0.1:5432  (wstunnel client, launched by pg-tunnel-up)
  │  WSS, via --http-proxy $HTTPS_PROXY
  ▼
squid  127.0.0.1:3128  →  CONNECT tunnel.example.com:443     # allow-listed dstdomain, :443 → permitted
  ▼
tunnel.example.com:443  (wstunnel server, operator-run, TLS)
  │  --restrict-to db.internal:5432
  ▼
customer Postgres  db.internal:5432
```

### 1. Kit layout

```
kits/pg-tunnel-collab/
  RUNBOOK.md              # fill-in guide, the reference SERVER command, security notes (kit root)
  .at-cove/               # the kit dir at-cove's resolveKit expects under --project-dir
    config.yml            # allow-list widening (session-scoped) + collaborator secrets (declared, not valued)
    image/
      Dockerfile          # FROM cove base; bake pinned wstunnel + the helper
      pg-tunnel-up        # → /usr/local/bin; idempotent client launcher
```

> Note: a kit consumed via `at-cove chat/install --project-dir X` must keep its
> `config.yml` (and sibling `image/` tree) under `X/.at-cove/` — `resolveKit`
> joins `--project-dir` with `.at-cove`. The RUNBOOK lives at the kit root, where
> it is most discoverable and is not read by at-cove.

### 2. `image/pg-tunnel-up` (the launcher)

An idempotent bash helper on `PATH`, invoked by the human/agent before touching the DB. Chosen over a systemd unit because a plain (non-docker) cove's PID 1 is `sshd` with no service manager (`internal/assemble/hardening/image-files/usr/local/bin/entrypoint.sh`); requiring `docker:true` just to supervise the tunnel would be overkill.

Environment (all injected as `db-analyst` secrets — see §3):

| var | example | role |
|---|---|---|
| `PG_TUNNEL_URL` | `wss://tunnel.example.com:443` | wstunnel server endpoint (host must be allow-listed) |
| `PG_TUNNEL_SECRET` | (opaque) | shared HTTP-upgrade path-prefix authenticating to the server |
| `PG_TARGET` | `db.internal:5432` | customer Postgres `host:port` the server forwards to |
| `PG_LOCAL` | `127.0.0.1:5432` (default) | local listen address the agent connects to |

Behavior:

1. If `PG_LOCAL` already accepts a TCP connection → print "already up", exit 0. (Safe to call repeatedly; a second `chat` action never double-launches.)
2. Validate required env vars are present → **fail loud** with a one-line message naming the missing var otherwise.
3. Launch, backgrounded (`nohup … &`, log to `/tmp/pg-tunnel.log`, never the repo):
   ```
   wstunnel client \
     -L "tcp://$PG_LOCAL:$PG_TARGET" \
     --http-upgrade-path-prefix "$PG_TUNNEL_SECRET" \
     --http-proxy "$HTTPS_PROXY" \
     "$PG_TUNNEL_URL"
   ```
   `--http-proxy "$HTTPS_PROXY"` routes the WSS handshake through squid as `CONNECT tunnel.example.com:443` — exactly what the allow-list permits. `$HTTPS_PROXY` is `http://127.0.0.1:3128` in every session.
4. Wait-loop until `PG_LOCAL` accepts (bounded, ~15s). On timeout: **fail loud**, print the tail of `/tmp/pg-tunnel.log`, exit non-zero.

Secrets note: the values reach `pg-tunnel-up` only as session env (injected over SSH into tmpfs, never written to the kit or to disk). `pg-tunnel-up` must not echo them or write them to a file; `PG_TUNNEL_SECRET` appears on the `wstunnel` argv, which is acceptable inside the single-tenant cove (the `agent` user already holds the DB credentials) but must never be logged.

Exact `wstunnel` flag spellings are pinned to the version installed in the Dockerfile (§4); the plan step verifies them against that release.

### 3. `config.yml`

```yaml
name: pg-tunnel-collab

image:
  allowed-domains: []          # root stays minimal; Anthropic is already in the sealed base

collaborators:
  db-analyst:
    allowed-domains:
      - tunnel.example.com      # ← the ONLY egress widening: the wstunnel server host, session-scoped
    secrets:
      PG_TUNNEL_URL:    { description: "wss URL of the wstunnel server, e.g. wss://tunnel.example.com:443" }
      PG_TUNNEL_SECRET: { description: "shared HTTP-upgrade path-prefix authenticating to the wstunnel server" }
      PG_TARGET:        { description: "customer Postgres host:port the server forwards to, e.g. db.internal:5432" }
      DATABASE_URL:     { description: "libpq conn string; dial loopback but verify the real cert — host=db.internal hostaddr=127.0.0.1 port=5432 sslmode=verify-full" }
```

The tunnel host sits under the **collaborator class**, not root `image.allowed-domains`, so it is least-privilege and session-scoped (`collaborators.<class>.allowed-domains` is the per-session delta over the baked root list). Secrets are declared demand-only (name + description); every value is supplied machine-side in `~/.config/at-cove/secrets.yml`. Placing them under `collaborators.db-analyst.secrets` keeps them on the `chat` path only — never injected into any worker/git step.

### 4. `image/Dockerfile`

```
ARG COVE_BASE_IMAGE=ghcr.io/aethons-tools/cove-image:latest
FROM ${COVE_BASE_IMAGE}

# Build-time egress is open. Pin the wstunnel release and verify its checksum.
ARG WSTUNNEL_VERSION=<pinned>
ARG WSTUNNEL_SHA256=<pinned>
RUN curl -fsSL -o /tmp/wstunnel.tar.gz \
      "https://github.com/erebe/wstunnel/releases/download/v${WSTUNNEL_VERSION}/wstunnel_${WSTUNNEL_VERSION}_linux_amd64.tar.gz" \
 && echo "${WSTUNNEL_SHA256}  /tmp/wstunnel.tar.gz" | sha256sum -c - \
 && tar -xzf /tmp/wstunnel.tar.gz -C /usr/local/bin wstunnel \
 && chmod +x /usr/local/bin/wstunnel \
 && rm /tmp/wstunnel.tar.gz

COPY pg-tunnel-up /usr/local/bin/pg-tunnel-up
RUN chmod +x /usr/local/bin/pg-tunnel-up
```

`FROM ${COVE_BASE_IMAGE}` keeps the provenance gate satisfied (at-cove injects the blessed base via `--build-arg`). Version + sha256 pin the supply chain; the exact release asset name is resolved in the plan step. `at-task` is not installed (this is a collaborator kit, no worker/git path).

### 5. `RUNBOOK.md`

Documents, as the single source of detail:

- **What the kit does** and the egress rationale (fits the existing squid `CONNECT`-to-:443 model; the sole widening is the tunnel host).
- **The reference server** the operator stands up in front of the customer DB:
  ```
  wstunnel server \
    --restrict-to db.internal:5432 \
    --restrict-http-upgrade-path-prefix <secret> \
    wss://0.0.0.0:443
  ```
  behind TLS on :443, co-located with (or network-adjacent to) the DB.
- **Security notes:** `--restrict-to` prevents an open relay; **end-to-end PG TLS** so the relay never sees plaintext credentials or data — achieved with `host=db.internal hostaddr=127.0.0.1 sslmode=verify-full`, which dials the loopback tunnel entrance but verifies the server certificate against the *real* DB hostname (`hostaddr` sets the address, `host` sets the SNI/verification name). Plain `host=127.0.0.1 sslmode=verify-full` would fail hostname verification, and downgrading to `sslmode=require` to "fix" it would drop the end-to-end guarantee — the `host`/`hostaddr` split is the point. The tunnel host is the only egress widening; rotate `PG_TUNNEL_SECRET`; the agent connects only to `127.0.0.1:5432`.
- **Operating it:** fill the four values in `~/.config/at-cove/secrets.yml`; `at-cove create db-analyst` / `at-cove chat db-analyst`; in-session `pg-tunnel-up && psql "$DATABASE_URL"`.

### 6. Prototype simplifications (intentional, approved)

1. **Auth = shared `--http-upgrade-path-prefix` secret**, not mTLS. Adequate to gate the server; mTLS is a later hardening.
2. **Manual invocation** of `pg-tunnel-up` (idempotent), not a supervised/auto-restarting service. A crashed tunnel is re-established by re-running the helper. Supervision would require the `docker:true`/systemd path.

Both are named here so they are deliberate, not oversights.

## Testing

No Go code, so verification is:

- **`shellcheck`** on `pg-tunnel-up` (clean).
- **Stub-based smoke test:** a fake `wstunnel` on `PATH` plus a throwaway local listener, asserting (a) idempotent re-use when the port is already up, (b) the fail-loud path on missing env, and (c) the fail-loud timeout path when the port never opens. Hermetic — no network, no real wstunnel.
- **Config validation:** an `at-cove … --dry-run` (or equivalent parse/validate path) against the kit, confirming `config.yml` parses and the `db-analyst` allow-list + secret wiring resolves.

## Docs

Per `AGENTS.md`, repo docs are updated in the same change: add a one-row pointer to `kits/pg-tunnel-collab/` wherever kits are indexed (mirroring how `kits/reference-worker/` is referenced), with the kit's `RUNBOOK.md` as the single source of detail. No fact is duplicated between the RUNBOOK and this spec — this spec is the dated design record; the RUNBOOK is the living operator doc.

## Definition of done

- `kits/pg-tunnel-collab/` exists with the four files above; `config.yml` validates.
- `pg-tunnel-up` passes `shellcheck` and the stub-based smoke test.
- `RUNBOOK.md` documents the server command and the security notes.
- Docs index updated in the same change; a single PR against `main`.
- No change under `internal/assemble/hardening/`; no Go/core change.
