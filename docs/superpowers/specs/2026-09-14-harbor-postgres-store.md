# harbor: Postgres-backed control-plane Store (Phase 1)

**Status:** design approved, pre-plan
**Issue:** move harbor's backing store off single-file JSON onto Postgres. **Phase 1 = the control-plane `Store` only.** The message log (`internal/msglog`) is a documented **Phase 2**, separately specced.
**Drivers (in priority order):** **D** operational consistency (one datastore for backup/monitoring/access-control), **A** durability/correctness (transactions; no whole-file rewrite; crash-safe), **C** scale/queryability (mostly a Phase-2 / log concern). **B** multi-node/HA is explicitly *out of scope now* but must not be foreclosed.
**Foundation:** the existing `harbor.Store` interface + `FileStore` (`internal/harbor/filestore.go`); harbor's credential-resolver secret model; the serve config (`cmd/at-harbor/config.go`).

## Summary

Add a **`PostgresStore` implementing the existing `harbor.Store` interface**, with Postgres as the durable source of truth and an in-memory **write-through cache** for reads. `FileStore` is **retained** as the default dev/test backend; the backend is chosen by serve config. No `Store` interface change, so no consumer churn. This delivers **D** and **A** now, keeps the suite hermetic, keeps the single-writer model, and leaves **B** as a later evolution (a `version` column is written but not yet enforced).

## 1. Write-through model

- **Postgres is the source of truth.** At startup the store `SELECT`s all rows into in-memory maps; **every read is served from memory** (RWMutex; reads take `RLock`) — the broker's `Lookup` hot path never touches the DB.
- **Commit-then-cache:** every mutation runs as one Postgres transaction and updates the in-memory map **only after the txn commits**. On any DB error, return it and leave the cache untouched.
- **Fail-closed startup:** if migrations or the initial load fail, `serve` refuses to start.
- A crash cannot corrupt state (Postgres owns durability); restart reloads from Postgres.

## 2. Code organization — shared in-memory core

Extract the current in-memory maps + all read methods + the pure in-memory mutation helpers into an unexported **`memState`** (RWMutex-guarded, in `internal/harbor`).

- `FileStore` = `memState` + `save()` (whole-file rewrite). **Behavior-preserving refactor**, protected by the existing `filestore_test.go` as the safety net.
- `PostgresStore` = `memState` + per-operation SQL in a txn, applying the shared in-memory helper **post-commit**.

Reads and in-memory mutation logic are shared; only *how one change is persisted* differs. Both satisfy `harbor.Store` unchanged.

- **Placement:** `memState`, `FileStore`, and `PostgresStore` all live in package `internal/harbor` (they share the unexported `memState`, so a subpackage cannot host `PostgresStore`). Consequence: **pgx becomes a dependency of `internal/harbor`** — acceptable (harbor is the service core), noted so it is a conscious cost, not a review surprise.
- **Construction:** a single `harbor.OpenStore(...)`-style constructor picks the backend from config and returns `harbor.Store`; the serve wiring (`cmd/at-harbor/main.go`) calls it instead of `NewFileStore` directly. **Verify no consumer depends on the concrete `*FileStore` type** (all should use the `harbor.Store` interface — a plan pre-check); "no consumer churn" holds only if that is true.

## 3. Schema — document tables (key columns + JSONB body)

One table per aggregate; a real key column (or composite key) plus a `doc jsonb` holding the Go struct marshaled with its **current json tags** (so mapping is `json.Marshal`/`Unmarshal` — no hand-written column mapping, and **no schema churn when a struct gains a field**).

```
actors(token_hash text PK, id text NOT NULL UNIQUE, doc jsonb NOT NULL, version bigint NOT NULL DEFAULT 1, updated_at timestamptz NOT NULL DEFAULT now())
roles(project text, name text, doc jsonb NOT NULL, version …, updated_at …, PRIMARY KEY(project,name))
kits(name text PK, doc jsonb NOT NULL, version …, updated_at …)          -- versions live inside doc
instances(actor_id text PK, doc jsonb NOT NULL, version …, updated_at …)
destinations(name text PK, doc jsonb NOT NULL, version …, updated_at …)
projects(name text PK, doc jsonb NOT NULL, version …, updated_at …)        -- roster + escalation inside doc
```

- Real key columns give DB-enforced uniqueness (the integrity **A** wants). Writes are uniform `INSERT … ON CONFLICT (key) DO UPDATE SET doc = …, version = version + 1, updated_at = now()`; deletes are `DELETE … WHERE key = …`.
- **The actor↔grants relationship stays inside the actor `doc`** (Grants is a field of `Actor`), matching the in-memory model; no separate grants table.
- Reads never query into the JSONB (they come from the cache), so no normalization is paid for. The `version`/`updated_at` columns are unused by Phase-1 logic — they are the **B hook** (future optimistic concurrency / cache invalidation) and cost nothing now.
- Accepted trade-off: no rich relational queryability on the control plane. Data is tiny and already exposed by the admin UI/CLI; **C** targets the log (Phase 2). Generated columns / targeted normalization can be added later without touching the Go read path.

## 4. Config, driver & secrets

- **Driver:** `github.com/jackc/pgx/v5` (`pgxpool`). **New dependency** — the sandbox `GOPROXY` allow-list must permit it; a proxy failure on `go get` is a kit change, not a transient fault (see `docs/DEVELOPMENT.md`).
- **Selection (back-compatible):** keep `store:` (file path) as the default. Add an optional `store-postgres:` block; when present it wins and `store:` is ignored:

  ```yaml
  store: /var/lib/harbor/store.json     # default/dev when store-postgres absent
  store-postgres:
    host: db.internal
    port: 5432
    database: harbor
    user: harbor
    sslmode: verify-full
    password-cred: harbor-db            # a name in the existing `credentials:` map
  ```

- **Secret handling (hard rule):** the DB password is referenced by `password-cred`, resolved by a **host resolver command, in memory only** — exactly the mechanism destinations use for `cred-name`. The DSN is assembled in memory, **never written to disk/argv, never logged**. Config validation fails closed if `password-cred` does not resolve to a configured credential. A startup log line names the backend (host/database only — never the password).
- New serve-config keys (`store-postgres` and its fields) are added to the reflect-derived known-key set automatically; `unknownServeKeys` guards drift.

## 5. Migrations

Embed ordered SQL files via `embed.FS`; apply forward at startup against a `schema_migrations` bookkeeping table, each in a txn, guarded by a Postgres advisory lock so a restart cannot double-apply. No external migration CLI (consistent with harbor self-managing; FileStore already migrates on open). Because the schema is document-tables, migrations are essentially the initial `CREATE TABLE` set and rarely change — struct evolution lives inside the JSONB, not the schema.

## 6. Tests

- **Consumers/handlers** keep using `FileStore` in-memory — unchanged, hermetic, no Docker.
- **Shared `Store` conformance suite:** a table of behavioral tests written against the `harbor.Store` interface, run against **both** backends. `FileStore` runs it hermetically (default `just test`); `PostgresStore` runs it behind `//go:build integration` against a real Postgres from an env DSN (e.g. `HARBOR_TEST_POSTGRES_DSN`), skipping when unset — same shape as the existing real-ssh integration tests. This guarantees backend parity.
- **`memState` refactor** is covered by the existing `filestore_test.go` (behavior-preserving).
- **CI** gains a Postgres service container for the integration job; the default hermetic jobs (`gate`/`atcove`) are untouched.
- Cover: commit-then-cache (a forced DB error leaves the cache unchanged), fail-closed startup (bad DSN / failed load → serve start error), config parse + `password-cred` validation, and the migration apply/idempotency.

## 7. Boundaries & non-goals

- **No data migration in Phase 1.** There is no import/export verb (per decision). Switching a deployment to `store-postgres:` **starts with an empty control plane**; operators re-declare via the existing admin CLI/UI, and Postgres backups are the recovery path thereafter. Deployments needing their current roster preserved stay on the file backend until they re-enroll. (An importer is a clean future add if ever wanted.)
- **Phase 2 (separate spec):** `internal/msglog` → Postgres (append table, indexed queries, pagination, retention) — where **C** actually bites.
- **B / multi-node (deferred, not foreclosed):** the write-through cache + `version`/`updated_at` columns are where that work lands later (LISTEN/NOTIFY or versioned cache invalidation). Phase 1 stays single-writer.
- **Interface unchanged:** no `context`/`error` signature changes to `harbor.Store`.

## 8. Docs (same change)

- `docs/usage/harbor/serve.md` — the `store-postgres:` block, `password-cred`, backend selection/precedence, and the "empty control plane on switch, no data migration" note.
- `docs/DEVELOPMENT.md` — the `GOPROXY` allow-list addition for pgx, and how to run the integration tests against a local/CI Postgres (`HARBOR_TEST_POSTGRES_DSN`).
- Route via docs-author; verify with docs-audit. INDEX unchanged (both docs already listed).
