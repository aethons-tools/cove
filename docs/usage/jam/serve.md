---
summary: Running the Jam service — `at-jam serve`, the serve-config YAML (listen, admin-listen, tls/admin-tls, store-postgres, state-dir, credentials, the subscription account pool), the credential-broker model, managing destinations, and the off-loopback fail-closed rule.
read_when: You are standing up or configuring a Jam service — writing its serve config, wiring the real credentials it brokers, enabling the subscription-OAuth account pool, adding the destinations studios reach, or exposing the admin API beyond loopback.
owns: the `at-jam serve` command + serve-config schema (listen/admin-listen/tls/admin-tls/store-postgres/state-dir/removed storage keys/credentials/pool), the broker model, the subscription account pool + `pool` verb, the `destination` verb, and the off-loopback exposure guard
prereqs: INDEX.md for the service overview; operators.md for the `operator-auth.oidc` block referenced here
tier: leaf
updated: 2026-10-08
---

# Running Jam (`at-jam serve`)

`at-jam serve --config <file>` runs one process that is both the **broker**
(the reverse proxy studios send Anthropic/git through) and a **loopback admin API**
(the control plane the `at-jam` admin verbs talk to). Both are backed by a
single **Postgres** store (`store-postgres`, required). The serve config is bootstrap-only: destinations,
roles, and enrollments are managed at runtime via the admin API, not this file.

```
at-jam serve --config /etc/jam/jam.yml
```

## The serve config

```yaml
display-name: Aethon           # optional; the admin UI's title bar reads "Aethon Jam"
listen: ":443"                 # broker listener (coves connect here; TLS in prod)
admin-listen: "127.0.0.1:8081" # admin API listener (operator surface)
ui-hosts:                      # optional; extra Host values the browser UI accepts on loopback
  - jam.local.example       # a custom name that DNS-binds to 127.0.0.1
ui-origins:                    # optional; extra origins the UI's write (CSRF) check accepts
  - http://localhost:8090      # e.g. the `just dev-watch` live-reload proxy
tls:                           # broker server cert (required for a real :443)
  cert: /etc/jam/tls/fullchain.pem
  key:  /etc/jam/tls/privkey.pem
admin-tls:                     # optional; admin-API cert. Falls back to tls: if unset
  cert: /etc/jam/tls/admin-fullchain.pem
  key:  /etc/jam/tls/admin-privkey.pem
store-postgres:                  # REQUIRED — the live data (identities, roles, kits, destinations, squawks, session events)
  host: db.internal
  database: jam
  user: jam
  sslmode: verify-full
  password-cred: jam-db
state-dir: /var/lib/jam/state    # optional; relay cursors/markers/receipts (default shown in the key table)
credentials-file: /home/jam/.config/at-jam/credentials.yml  # optional; the XDG default shown
credentials:                        # name-only DEMANDS — strategies live in the credentials file
  anthropic-key:
  git-pat:
  discord-bot:
  linear-bot:
pool:                               # optional — subscription-OAuth account pool (see below)
  store: /var/lib/jam/pool.json     # host-side pool file (accounts + identity→account bindings)
  cred-name: anthropic-sub          # the anthropic destination cred this pool serves
  refresh-interval: 5m              # optional (default 5m)
  refresh-margin: 15m               # optional (default 15m) — refresh when within this of expiry
operator-auth:                      # see operators.md — omit for loopback-only admin
  oidc:
    issuer:   https://YOUR_TENANT.us.auth0.com/
    audience: https://jam.example.com/admin
    require-scope: jam:admin
    device-client-id: "…"
    device-scope: "openid profile"
runtime:                            # optional — supervisor lease/reconcile timing
  lease-ttl: 60s
  reconcile-interval: 30s           # must be < lease-ttl
  listen: "127.0.0.1:9090"          # OPTIONAL plaintext Attach gRPC dev listener; prod uses the :443 mux
  launcher:                         # optional — enables the real Colima cove launcher
    runtime-addr: jam.example.com:443        # what a raised cove dials (AT_JAM_RUNTIME_ADDR)
    jam-host: jam.example.com                # added to the cove's /etc/hosts; connector base host
    identity-file: /var/lib/jam/at-cove/id_ed25519  # SSH key matching the image's baked authorized_keys
    known-hosts-dir: /var/lib/jam/known_hosts.d
    dns: []
    docker: false
    docker-context: colima                   # optional — the colima instance studios run in (colima-<profile>)
  discord:
    connection: discord-main        # an `at-jam connection` of kind discord
  requisitioner:
    connection: linear-acme         # an `at-jam connection` of kind linear
  wake:                             # optional — wake-on engine timing (see intercom.md)
    wait-max: 24h
```

The cove-facing `listen:`/`tls:` endpoint serves **both** the HTTP broker and the
[Attach](coves.md#the-attach-stream) gRPC stream on the one :443 TLS port: Jam
terminates TLS once, then multiplexes the decrypted stream by `content-type`
(`application/grpc` → the Attach server, everything else → the broker). This is
why a hardened studio — whose sealed egress only permits `CONNECT … :443` — can
reach the Attach stream at all. `runtime.listen` is now only an **optional plaintext dev listener** (no TLS, for local testing), not the production path.

| Key | Required | Purpose |
|-----|----------|---------|
| `display-name` | no | Names this Jam in the [admin UI](ui.md): the title bar and tab title read `<name> Jam` and the rail's Jam entry `<name>`. One line of printable text, at most 64 characters, trimmed; anything else fails serve at startup. Unset, the UI says "Jam". Don't end it in "Jam" — the title bar adds that. |
| `listen` | yes | Address the cove-facing endpoint serves on — **both** the broker and the [Attach](coves.md#the-attach-stream) gRPC stream, multiplexed by `content-type`. Use `:443` in production — a sealed studio can only `CONNECT` to 443. |
| `admin-listen` | no | Address the admin API serves on. Omit to run the broker alone. |
| `ui-hosts` | no | Extra `Host` values the browser UI accepts on a **loopback** connection, beyond the loopback literals (`127.0.0.1`/`::1`/`localhost`). Set a custom name that DNS-binds to loopback (e.g. `jam.local.example`); otherwise the UI refuses it as a possible DNS-rebinding attempt. See [ui.md](ui.md#reaching-the-ui). |
| `ui-origins` | no | Extra exact origins (`scheme://host[:port]`, no path) the browser UI's write checks (`/ui` and `/me`) accept, besides the request's own `Host`. For a proxy that fronts the admin listener on another address, such as the `just dev-watch` live-reload proxy (`http://localhost:8090`, see [`dev/README.md`](../../../dev/README.md)). Matching is exact: another port or scheme is another origin. Malformed entries fail serve at startup. |
| `dev-identity` | no | **Dev only.** `{user}` (a user's name or id; the old `{project, human}` still works, with a deprecation warning, as exactly the user that roster human became in the migration): loopback browser requests to `/ui` and `/me` act as that user with **no login**. `/ui` uses the user's first login, falling back to `local` if they have none. `/me` uses their first OIDC identity, so the user needs one (`at-jam user oidc`) and a project membership. A real login session still wins. It never applies off loopback, and the `Host` check still runs. `/me` is mounted even without browser OIDC. serve refuses it unless `admin-listen` is loopback, and logs a `DEV IDENTITY ACTIVE` warning at startup. Never set it in production. |
| `tls.cert` / `tls.key` | for a real broker | The broker's own server certificate (it serves its own TLS per connector — no MITM CA). |
| `admin-tls.cert` / `admin-tls.key` | no | A separate cert for the admin API; falls back to `tls:` when unset. |
| `store-postgres` | **yes** | The Postgres store: control plane, squawk Log, session events, allocation ledger. A block of `host`, `port`, `database`, `user`, `sslmode`, and `password-cred`. `serve` refuses to start without it. See [Postgres store](#postgres-store-store-postgres) below. |
| `state-dir` | no | Directory for the relay engines' small local files: `relay-cursors.json`, `relay-markers.json`, `relay-receipts.json`. Default `$XDG_STATE_HOME/at-jam`, else `~/.local/state/at-jam`; created `0700` when a relay needs it. Must resolve to an **absolute** path: with `HOME` unset, a relative `XDG_STATE_HOME`, or a relative `state-dir`, `serve` fails at startup rather than write under the working directory. See [Upgrading relay state](#upgrading-relay-state). |
| `session-events-retention` | no | How long session events are kept (`<N>d` or a Go duration; empty keeps forever); see [session-events.md](session-events.md). |
| `credentials-file` | no | Path to the protected file that supplies the demanded credentials. Default `${XDG_CONFIG_HOME:-~/.config}/at-jam/credentials.yml`. See [credentials.md](credentials.md). |
| `credentials.<name>` | as needed | The credentials the broker injects, **named only** (an empty entry); strategies live in the credentials file — see [credentials.md](credentials.md). Referenced by a destination's `cred-name`. Values are resolved on the host, in memory — never written to the store. A demanded name the file doesn't supply aborts `serve`. |
| `pool` | no | Enables the [subscription-OAuth account pool](pool.md): the anthropic destination's credential is resolved from a pool of subscription accounts by cove identity, coves launch in subscription mode, and a background refresher rotates pool tokens. Requires `pool.store` (the path to the pool JSON file) and `cred-name`; `refresh-interval`/`refresh-margin` default to 5m/15m and `token-url`/`client-id`/`scope` default to the probed Claude Code constants. Absent ⇒ the anthropic destination keeps its configured credential and coves launch in API-key mode. |
| `metrics` | no | Serves the operator-attention exposition at `/metrics` on the broker listener: `token-cred` (required) names a demanded credential holding the Prometheus scrape token; `alertmanager-url` (optional) is linked from the admin UI's Health tab. Unset ⇒ no `/metrics`. See [monitoring.md](monitoring.md). |
| `operator-auth.oidc` | to gate the admin API | OIDC operator identity — see [operators.md](operators.md). Omitted ⇒ the admin API trusts loopback only. |
| `runtime.lease-ttl` / `runtime.reconcile-interval` | no | Managed-cove supervisor timing (defaults 60s / 30s; reconcile must be < ttl). See [coves.md](coves.md). |
| `runtime.listen` | no | Optional **plaintext** Attach gRPC dev listener (no TLS), for local testing. Omit in production — the Attach gRPC is served on the `:443` mux alongside the broker. |
| `runtime.launcher` | no | Enables the real Colima studio launcher (omit ⇒ a placeholder that records instances without a backend). Requires `runtime-addr` and `jam-host` (its pre-rename name is still accepted with a warning — see [renamed-from-harbor.md](renamed-from-harbor.md)); `identity-file`/`known-hosts-dir` default to the at-cove config dir. Optional `docker-context` (default `colima`) selects the colima instance — see the launcher note below. The cove's kit comes from the [studio-kit registry](kits.md) (default seeded at start). `install-manifest` was removed — see [studio-kit-migration.md](studio-kit-migration.md). See the launcher note below. |
| `runtime.requisitioner` | no | Enables the Requisitioner: Jam polls a tracker and raises a managed studio per ready ticket. Requires `role`, `max-concurrent` (>0), a `linear` block, and `connection`: a **connection** of kind `linear` (`at-jam connection add --kind linear --name linear-acme --cred linear-bot`), whose credential must be demanded in `credentials:`. Serve refuses to start on an unknown connection, another kind, or an undemanded credential. The deprecated `tracker-token-cred: <credential>` still works (with a warning) by binding the implicit connection named `linear`. Its pre-rename key is still accepted with a warning ([renamed-from-harbor.md](renamed-from-harbor.md)). See [requisitioner.md](requisitioner.md). |
| `runtime.discord` | no | Enables the resident Discord relay engine (egress and reply-routing ingress). Requires `connection`: a connection of kind `discord` whose credential (the bot token) is demanded in `credentials:`, resolved on the host — never logged/injected; the deprecated `bot-token-cred` binds the implicit connection named `discord`. No Requisitioner needed. Polls every project whose chat service is `discord`. See [discord.md](discord.md) and [intercom.md](intercom.md#enabling-it). |
| `runtime.wake` | no | Wake-on engine timing: `poll-interval`, `wait-max`, `warm-timeout`; and `session-wake-limit`, the agent-to-agent loop breaker (default 8; `0` = session posts never wake sessions; negative refused). Each field falls back to the matching `runtime.requisitioner` field, then the default. See [intercom.md](intercom.md#waiting-for-a-reply-wake-on). |
| `runtime.escalation-poll-interval` | no | How often the escalation engine checks waiting sessions (default 30s; falls back to `runtime.requisitioner.escalation-poll-interval`). The engine runs with or without a Requisitioner. See [escalation.md](escalation.md). |

### Postgres store (`store-postgres`)

The control plane lives in Postgres — the source of truth — while `serve`
keeps an in-memory read cache (so the broker's hot path never round-trips the
DB) and writes through to Postgres transactionally. `serve` applies its schema
migrations automatically at startup and **fails closed** if it can't connect,
migrate, or load. The intercom's channel log ([ui.md#intercom](ui.md#intercom)), session
events, and the allocation ledger live in the same database and pool (tables
auto-created), so the intercom and session events are always on.

### Upgrading to project ids

The release with intercom 1b-2a keys roles by project id (jam migration
`0013`) and, at first start, rewrites grants, sessions and allocation-ledger
events that name a project (or a personal session's owner) by name to name it
by id; session events gain `project_id`/`owner_id`, and `relay-cursors.json`
is re-keyed by project id (the old file is kept as `relay-cursors.json.bak`).
Migration `0014` keys projects by id, so a removed project leaves a tombstone
and a project can be renamed ([projects.md](projects.md)).
A Jam that skipped the 2026-10-06 releases must start one of them first
(they mint project ids): `0013` refuses to run while a project has none. Back
up the database first: an older Jam can't read it (roles lose their name
column), so the only way back is restoring that backup.

### Upgrading to the channel log

The release with intercom slice 2b moves the intercom onto the channel log
(intercompg migration `0004`, under an advisory lock, at first start). **Stop
every older Jam first** — an older one would keep writing the renamed legacy
tables — and don't roll back past it. The earlier log stays as read-only
history; what this changes for agents, relays and `/me` is in
[intercom.md](intercom.md#enabling-it).

### Removed keys

`store`, `intercom-log`, and `session-events-dir` were removed: setting any of
them is a **hard startup error** naming the key. Operators already on
`store-postgres` just **delete the leftover line**: those keys were ignored
there, so no migration is needed. To move a file-backed Jam, run
`at-jam export` with the old version and `at-jam import` into a Postgres Jam
([backup.md](backup.md)). Squawk history and session events in the old files
are **not** migrated.

### Upgrading relay state

The relay files `relay-cursors.json`, `relay-markers.json`, and
`relay-receipts.json` used to live in the directory of `store:` when it was set,
otherwise in **serve's working directory** (e.g. the systemd
`WorkingDirectory`). On upgrade, move them from there into `state-dir` (or point
`state-dir` at that directory). If you do not:

- Egress marks re-seed to the Log tail: nothing is redelivered, but squawks
  appended-but-undelivered at shutdown and pending retries are dropped.
- Linear ingress starts from "now", so replies posted during the upgrade
  downtime are never ingested.
- Discord ingress re-reads recent messages (deduped).
- Discord replies to posts made before the upgrade no longer route to their
  studio (receipts lost).

```yaml
store-postgres:
  host: db.internal
  port: 5432
  database: jam
  user: jam
  sslmode: verify-full
  password-cred: jam-db      # a name in `credentials:` — never an inline password
```

No credential is ever inline — the DB password included: `password-cred` names a `credentials:`
entry (supplied by the [credentials file](credentials.md)), resolved on the host in memory when `serve` assembles the connection
string — it is never written to disk, put on a command line, or logged (the
startup log names only the host and database).

For a local Postgres to develop against (matching this schema and the CI
integration setup), see [`dev/`](../../../dev/README.md) — a `docker compose`
that raises a `postgres:18` on `localhost:15432` with database/user `jam`,
plus a sample dev serve config (`dev/jam.dev.yml`) wired to it.

**The allocation event store is AUTHORITATIVE for the cap.** Jam always runs an
Allocator (its capacity authority), with or without a Requisitioner. It admits each raise through an
**atomic optimistic-concurrency grant** on an allocation event store on the same
database and pool (its `alloc_events` table is auto-created): a single conditional
append that writes a `reservation_granted` *iff* the stream's outstanding count
(`granted − released`) is below budget, gated by `UNIQUE(stream_id,
stream_revision)`. The grant happens **before** the raise (it reserves the slot,
so admission cannot overshoot the budget under concurrency), and a
`reservation_released` is written on teardown **and** as compensation if the
claim/prompt/raise fails after a grant. The cap is therefore per-normalized-`(project,
role)` `Outstanding`, not a global instance count. `serve` always has the ledger. (A ledger-less Allocator, used only in tests, falls back to the **registry
live instance count vs budget**.) A crash *between* a grant and the raise leaves a dangling
`reservation_granted` (a leaked slot); a resident **reconcile sweep**
reclaims it, so the ledger self-heals. Every minute the Allocator releases each
outstanding reservation (net `granted − released > 0`) whose latest grant is older
than a ~5-minute grace window **and** whose actor has no live instance — the grace
window keeps an in-flight raise (instance not yet in the registry) from being
swept. The sweep is best-effort (a per-reservation release failure is logged and
retried on the next tick) and needs the ledger. The cap stays
≤ budget throughout, so an unreclaimed slot is only a transient availability
nuisance, not a correctness break.

Each reservation event also records its **session kind** (`session_kind`, plus a
standing session's name and a personal session's owner), and the grant counts
outstanding reservations **of the requested kind only**, so kinds never consume
each other's capacity; a release inherits the kind of the reservation's latest
grant. All three kinds are admitted: `ephemeral` (Requisitioner) sessions,
`personal` sessions ([personal-sessions.md](personal-sessions.md)), and
`standing` sessions ([standing-sessions.md](standing-sessions.md#admission)).
Pre-existing rows are tagged `ephemeral`. The reconcile sweep releases leaked
ephemeral and standing reservations, never personal ones. The ephemeral budget is the role's roster `max-ephemeral`, falling
back to the Requisitioner's `max-concurrent` ([roster.md](roster.md#roles)). A
personal grant checks two caps in the same atomic append: the role's
`max-personal` pool and, when set, the owner's `max-personal-per-owner` share.
Personal sessions need the ledger, which `serve` always has.

### The launcher (`runtime.launcher`)

With a `launcher` block, `at-jam studio raise` starts a **real** studio on the Colima
backend from the role's [StudioKit](kits.md) (the seeded `default` when the role
names none), built on demand by the launcher — no pre-built image or
`install.json` is involved. It injects the connector + prompt over SSH and starts
`cove-master` in it. The studio dials Jam's Attach stream at `runtime-addr`
(`jam.host:443`) through its own squid proxy; `jam-host` is added to the studio's
`/etc/hosts` so that name resolves to the host gateway. Jam must reach the Colima
backend (run it where `docker`/Colima is available); `identity-file` is the SSH key
Jam uses to reach the studio (defaults to `~/.config/at-cove/id_ed25519`). Omit the
whole block to keep the placeholder launcher (dev/tests).

`docker-context` picks **which colima instance** the studios run in: every docker
call the launcher makes is pinned to that docker context. It defaults to `colima`,
the default profile's context; `colima start --profile jam-b` creates
`colima-jam-b`, so `docker-context: colima-jam-b` places this Jam's studios (and
their kit images and volumes) in that VM. Use it to give each Jam on one host its
own colima instance. The build/prepare
handshake is in [coves.md](coves.md#the-studiokit-and-its-kit-prepare-protocol).

## The broker model

The broker is **client-addressed, per-connector** — there is no MITM CA to
install. A studio re-addresses Jam per service (`ANTHROPIC_BASE_URL=<base>/anthropic`,
git `insteadOf` → `<base>/git/`), presents only its **identity token**, and Jam:

1. authenticates the identity (by token hash) and authorizes the request against
   the actor's role scope (see [roster.md](roster.md)),
2. swaps the identity token for the **real** downstream credential (from
   `credentials:`), and
3. re-originates the request upstream.

The conscious trade: Jam sees the plaintext of brokered traffic (the client
sent it there on purpose), in exchange for collapsing N cove-side secrets into one
defended Jam.

## Destinations

A **destination** is one brokered upstream: a path prefix on the broker that maps
to an upstream base URL plus how the identity arrives and how the real credential
is applied. Destinations are managed at runtime via the admin API:

```
at-jam destination add \
  --name anthropic --route /anthropic/ --upstream https://api.anthropic.com \
  --identity-in x-api-key --cred-name anthropic-key --apply x-api-key
at-jam destination add \
  --name git --route /git/ --upstream https://github.com \
  --identity-in basic-password --cred-name git-pat --apply basic-password
at-jam destination list
at-jam destination rm <name>
at-jam destination import <file.yaml>   # bulk add from a YAML with a `destinations:` list
```

- `--identity-in` / `--apply` are a preset, `bearer | basic-password | x-api-key | raw` —
  how the studio presents its identity, and how Jam applies the real credential;
  other headers take a custom spec via `import` — see [header-specs.md](header-specs.md).
  The identity header is always stripped before forwarding, so a Jam identity
  token never reaches the upstream.
- `--oauth-beta` was **removed** (an error, as is a write carrying the old
  `oauth_beta` field): the broker now adds that beta for every
  [pool](pool.md#the-oauth-beta) credential.
- `--env KEY=TEMPLATE` (repeatable) and `--git` declare what a studio must set
  to use the destination — see [connector.md](connector.md), which also covers
  the `gh` (GitHub API) destinations.
- `--allow-path PATTERN` (repeatable) limits what the broker forwards: a
  [`path.Match`](https://pkg.go.dev/path#Match) pattern over the path *after* the
  route (`*` never crosses `/`; a path with `..` or `//` never matches). Any other
  path is refused **403** before the credential is resolved. No patterns means
  any path. Use it when the injected credential reaches more than the cove
  needs. [vertex.md](vertex.md) uses it to limit a GCP token to Claude model calls.
- `--note` (≤ 300 bytes) is a usage hint shown to sessions granted the destination —
  see [connector.md](connector.md#notes-for-sessions).
- `--cred-name` must resolve to a `credentials:` entry in the serve config —
  or, when the [pool](pool.md) is enabled, the pool's `cred-name` (which the
  pool resolves by identity, not from `credentials:`). Validated at add time.
  It is the destination's **default** credential: a role may map the
  destination to a different one ([roster.md](roster.md)).
- **No repo policy.** Jam does not scope a git destination by `owner/repo`; the
  injected credential's own scope is the boundary. Use a fine-grained PAT per
  project (one `credentials:` entry each) and map it per role.
  **Upgrading from a repo-scoped Jam widens access:** a role that relied on
  `--repos` to narrow a broad PAT gets that PAT's full reach — re-scope the
  credentials before upgrading. Stored `repo_scoped`/`repos` keys are ignored.

These admin verbs take the standard client flags (`--app`/`--admin-url`/`--token`);
see [operators.md](operators.md).

## The subscription account pool (`pool:`)

The optional `pool:` block runs cove `claude` as a **pooled subscription
principal** (cheaper, model-entitled) instead of the federated `anthropic-key`
credential: the anthropic destination flips to `bearer`, each cove is seeded a
dummy subscription credential whose access token is its own identity, and the
broker injects a pooled account's real token per request. See
[pool.md](pool.md) for the mechanism, the `at-jam pool` verb, broker-owned token
refresh, and the egress it needs.

## Exposing the admin API (fail-closed)

A loopback `admin-listen` stays plain HTTP. The admin API **refuses to bind
off-loopback unless both** a TLS cert (`admin-tls` or `tls`) **and**
`operator-auth.oidc` are configured — an unauthenticated, plaintext control plane
can never be exposed to the network by accident. To administer a Jam from
another machine, give it a routable `admin-listen`, TLS, and OIDC, then point
clients at its `https://` admin URL ([operators.md](operators.md)).

For the studio side of the broker connection — the `jam:` kit config, auto-enroll,
and reaching a host-run Jam — see [`../at-cove-config.md#jam`](../at-cove-config.md).
For the broker's threat model and boundary rationale, follow the design-history
pointer in [INDEX.md](INDEX.md).
