---
summary: Running the Jam service — `at-jam serve`, the serve-config YAML (listen, admin-listen, tls/admin-tls, store or store-postgres, credentials), the credential-broker model, managing destinations, and the off-loopback fail-closed rule.
read_when: You are standing up or configuring a Jam service — writing its serve config, wiring the real credentials it brokers, adding the destinations studios reach, or exposing the admin API beyond loopback.
owns: the `at-jam serve` command + serve-config schema (listen/admin-listen/tls/admin-tls/store/store-postgres/credentials), the broker model, the `destination` verb, and the off-loopback exposure guard
prereqs: INDEX.md for the service overview; operators.md for the `operator-auth.oidc` block referenced here
tier: leaf
updated: 2026-09-27
---

# Running Jam (`at-jam serve`)

`at-jam serve --config <file>` runs one process that is both the **broker**
(the reverse proxy studios send Anthropic/git through) and a **loopback admin API**
(the control plane the `at-jam` admin verbs talk to). Both are backed by a
single JSON **store** file. The serve config is bootstrap-only: destinations,
roles, and enrollments are managed at runtime via the admin API, not this file.

```
at-jam serve --config /etc/jam/jam.yml
```

## The serve config

```yaml
listen: ":443"                 # broker listener (coves connect here; TLS in prod)
admin-listen: "127.0.0.1:8081" # admin API listener (operator surface)
ui-hosts:                      # optional; extra Host values the browser UI accepts on loopback
  - jam.local.example       # a custom name that DNS-binds to 127.0.0.1
tls:                           # broker server cert (required for a real :443)
  cert: /etc/jam/tls/fullchain.pem
  key:  /etc/jam/tls/privkey.pem
admin-tls:                     # optional; admin-API cert. Falls back to tls: if unset
  cert: /etc/jam/tls/admin-fullchain.pem
  key:  /etc/jam/tls/admin-privkey.pem
store: /var/lib/jam/store.json   # the live data file (identities, roles, kits, destinations)
credentials:                        # the REAL downstream secrets Jam injects
  anthropic-key:
    command: ["at-mint", "anthropic", "--audience", "…"]   # a resolver run on the host
  git-pat:
    value: "ghp_…"                                         # or a literal value
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
    install-manifest: /etc/jam/install.json  # → the pre-built image (Image + ImageDigest)
    runtime-addr: jam.example.com:443        # what a raised cove dials (AT_JAM_RUNTIME_ADDR)
    jam-host: jam.example.com                # added to the cove's /etc/hosts; connector base host
    identity-file: /var/lib/jam/at-cove/id_ed25519  # SSH key matching the image's baked authorized_keys
    known-hosts-dir: /var/lib/jam/known_hosts.d
    dns: []
    docker: false
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
| `listen` | yes | Address the cove-facing endpoint serves on — **both** the broker and the [Attach](coves.md#the-attach-stream) gRPC stream, multiplexed by `content-type`. Use `:443` in production — a sealed studio can only `CONNECT` to 443. |
| `admin-listen` | no | Address the admin API serves on. Omit to run the broker alone. |
| `ui-hosts` | no | Extra `Host` values the browser UI accepts on a **loopback** connection, beyond the loopback literals (`127.0.0.1`/`::1`/`localhost`). Set a custom name that DNS-binds to loopback (e.g. `jam.local.example`); otherwise the UI refuses it as a possible DNS-rebinding attempt. See [ui.md](ui.md#reaching-the-ui). |
| `tls.cert` / `tls.key` | for a real broker | The broker's own server certificate (it serves its own TLS per connector — no MITM CA). |
| `admin-tls.cert` / `admin-tls.key` | no | A separate cert for the admin API; falls back to `tls:` when unset. |
| `store` | yes, unless `store-postgres` is set | Path to the JSON store (created on first write; migrated forward across versions). Used when `store-postgres` is absent. |
| `store-postgres` | no | Selects the Postgres store backend instead of the file `store` (it takes precedence when set). A block of `host`, `port`, `database`, `user`, `sslmode`, and `password-cred`. See [Postgres store backend](#postgres-store-backend-store-postgres) below. |
| `intercom-log` | no | Filesystem path to Jam's durable squawk Log (JSONL). With a Log (file or `store-postgres`), Jam runs the intercom — `/squawks`, wake-on, and (with `runtime.discord`) the Discord relay — with or without a Requisitioner; see [intercom.md](intercom.md#enabling-it). When set, `serve` opens it (creating it on first open) and the admin UI serves the read-only Intercom view at `/ui/intercom`. Unset disables the view. The Log is append-only and single-writer (the serve process); this field only enables the read side — see [ui.md#intercom](ui.md#intercom). |
| `credentials.<name>` | as needed | The real secrets the broker injects, each a `{command: [...]}` resolver or a literal `{value: "..."}`. Referenced by a destination's `cred-name`. Values are resolved on the host, in memory — never written to the store. |
| `operator-auth.oidc` | to gate the admin API | OIDC operator identity — see [operators.md](operators.md). Omitted ⇒ the admin API trusts loopback only. |
| `runtime.lease-ttl` / `runtime.reconcile-interval` | no | Managed-cove supervisor timing (defaults 60s / 30s; reconcile must be < ttl). See [coves.md](coves.md). |
| `runtime.listen` | no | Optional **plaintext** Attach gRPC dev listener (no TLS), for local testing. Omit in production — the Attach gRPC is served on the `:443` mux alongside the broker. |
| `runtime.launcher` | no | Enables the real Colima studio launcher (omit ⇒ a placeholder that records instances without a backend). Requires `install-manifest`, `runtime-addr`, `jam-host` (its pre-rename name is still accepted with a warning — see [renamed-from-harbor.md](renamed-from-harbor.md)); `identity-file`/`known-hosts-dir` default to the at-cove config dir. See the launcher note below. |
| `runtime.requisitioner` | no | Enables the Requisitioner: Jam polls a tracker and raises a managed studio per ready ticket. Requires `role`, `max-concurrent` (>0), and a `linear` block. Its pre-rename key is still accepted with a warning ([renamed-from-harbor.md](renamed-from-harbor.md)). See [requisitioner.md](requisitioner.md). |
| `runtime.discord` | no | Enables the resident Discord relay engine (egress and reply-routing ingress). Requires a non-empty `bot-token` (`command` or `value`, resolved on the host — never logged/injected) and a configured `intercom-log`; no Requisitioner needed. Polls every project whose chat service is `discord`. See [discord.md](discord.md) and [intercom.md](intercom.md#enabling-it). |
| `runtime.wake` | no | Wake-on engine timing: `poll-interval`, `wait-max`, `warm-timeout`. Each field falls back to the matching `runtime.requisitioner` field, then the default. See [intercom.md](intercom.md#waiting-for-a-reply-wake-on). |

### Postgres store backend (`store-postgres`)

By default the control plane lives in the single-file JSON `store`. Setting
`store-postgres` moves it to Postgres — the source of truth — while `serve`
keeps an in-memory read cache (so the broker's hot path never round-trips the
DB) and writes through to Postgres transactionally. This buys a real datastore
for ops (backup/monitoring), transactional durability (no whole-file rewrite),
and headroom to grow. `serve` applies its schema migrations automatically at
startup and **fails closed** if it can't connect, migrate, or load.

```yaml
store-postgres:
  host: db.internal
  port: 5432
  database: jam
  user: jam
  sslmode: verify-full
  password-cred: jam-db      # a name in `credentials:` — never an inline password
```

The DB password is **never inline**: `password-cred` names a `credentials:`
entry, resolved on the host in memory when `serve` assembles the connection
string — it is never written to disk, put on a command line, or logged (the
startup log names only the host and database). `store-postgres` takes precedence
over `store` when both are present.

For a local Postgres to develop against (matching this schema and the CI
integration setup), see [`dev/`](../../../dev/README.md) — a `docker compose`
that raises a `postgres:17` on `localhost:15432` with database/user `jam`,
plus a sample dev serve config (`dev/jam.dev.yml`) wired to it.

**No data migration (Phase 1).** Switching an existing deployment from the file
`store` to `store-postgres` starts with an **empty control plane** — there is
no importer. Re-declare actors/roles/kits/destinations via the admin CLI or UI
after switching; a deployment needing its roster preserved should stay on the
file backend until it re-enrolls.

**The squawk Log follows the store backend.** `store-postgres` also makes the
durable squawk Log ([`intercom-log`](#the-serve-config) above,
[ui.md#intercom](ui.md#intercom)) Postgres-backed, on the same database and
pool (tables auto-created; `intercom-log:` is ignored) — effectively always-on.
Without `store-postgres`, the file `intercom-log` path is used as before.
Either way, switching backends **starts empty** — no data migration.

**The allocation event store follows the store backend too, and with
`store-postgres` it is now AUTHORITATIVE for the cap.** Jam always runs an
Allocator (its capacity authority), with or without a Requisitioner. With
`store-postgres`, it admits each raise through an
**atomic optimistic-concurrency grant** on an allocation event store on the same
database and pool (its `alloc_events` table is auto-created): a single conditional
append that writes a `reservation_granted` *iff* the stream's outstanding count
(`granted − released`) is below budget, gated by `UNIQUE(stream_id,
stream_revision)`. The grant happens **before** the raise (it reserves the slot,
so admission cannot overshoot the budget under concurrency), and a
`reservation_released` is written on teardown **and** as compensation if the
claim/prompt/raise fails after a grant. The cap is therefore per-normalized-`(project,
role)` `Outstanding`, not a global instance count. **Without `store-postgres`
(file backend)** there is no ledger, so the Allocator falls back to the **registry
live instance count vs budget** (the earlier global behavior) and releases are
no-ops. A crash *between* a grant and the raise leaves a dangling
`reservation_granted` (a leaked slot); with Postgres a resident **reconcile sweep**
reclaims it, so the ledger self-heals. Every minute the Allocator releases each
outstanding reservation (net `granted − released > 0`) whose latest grant is older
than a ~5-minute grace window **and** whose actor has no live instance — the grace
window keeps an in-flight raise (instance not yet in the registry) from being
swept. The sweep is best-effort (a per-reservation release failure is logged and
retried on the next tick) and Postgres-only (no ledger ⇒ no sweep). The cap stays
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
Personal sessions **require** `store-postgres`: the file backend has no
fallback for them.

### The launcher (`runtime.launcher`)

With a `launcher` block, `at-jam studio raise` starts a **real** studio on the Colima
backend from the pre-built image named by `install-manifest` (the frozen
`install.json` an `at-cove install` produced — its `Image` + `ImageDigest`),
injects the connector + prompt over SSH, and starts `cove-master` in it. The studio
dials Jam's Attach stream at `runtime-addr` (`jam.host:443`) through its own
squid proxy; `jam-host` is added to the studio's `/etc/hosts` so that name resolves
to the host gateway.

**Deployment constraint:** Jam must be given the **same SSH key** that
`at-cove install` baked into the image's `authorized_keys` — point `identity-file`
at that private key (it defaults to `~/.config/at-cove/id_ed25519`, the at-cove
default). A key Jam generates fresh would not be authorized by the image. Jam
must also reach the Colima backend (run it where `docker`/Colima is available). Omit
the whole block to keep the placeholder launcher (dev/tests).

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
  --identity-in basic-password --cred-name git-pat --apply basic-password --repo-scoped
at-jam destination list
at-jam destination rm <name>
at-jam destination import <file.yaml>   # bulk add from a YAML with a `destinations:` list
```

- `--identity-in` / `--apply` are one of `bearer | basic-password | x-api-key` —
  how the studio presents its identity, and how Jam applies the real credential.
- `--repo-scoped` marks a git-style destination whose path is `<route>/<owner>/<repo>/…`,
  so a role's `repos` globs can scope it.
- `--cred-name` must resolve to a `credentials:` entry in the serve config
  (validated at add time).

These admin verbs take the standard client flags (`--app`/`--admin-url`/`--token`);
see [operators.md](operators.md).

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
