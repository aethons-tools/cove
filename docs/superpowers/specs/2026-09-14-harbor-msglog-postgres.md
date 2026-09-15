# harbor: Postgres-backed message log (Phase 2)

**Status:** design approved (approach A), pre-plan
**Issue:** move `internal/msglog` off its single JSONL file onto Postgres — Phase 2 of the store migration (Phase 1 = the control-plane `Store`, shipped in #192).
**Drivers:** **C** scale/queryability (the log grows unbounded; stop holding it all in RAM; indexed inbox/thread/time lookups), plus **D** operational consistency (the log shares the Phase-1 Postgres datastore) and **A** durability. **B** multi-node is still out of scope.
**Foundation:** Phase 1 (`store-postgres` serve config + the shared Postgres database, pgx/v5); the `msgport` engine, `wakeon`, and the admin Messages view that now consume the log.

## Summary

Give `internal/msglog` a **Postgres backend behind a new `msglog.Store` interface**, on a **real indexed `messages` schema**, with **no in-memory mirror** — reads become indexed Postgres queries, so the log no longer has to fit in RAM. The existing file log is retained as the hermetic/dev backend and satisfies the same interface. The read method **signatures are unchanged** (Approach A): they still return full `[]Message`; pagination/cursor reads and retention/compaction are an explicit **Phase 3**. The message-log backend follows the store backend: when `store-postgres` is configured the log uses that same database; otherwise the file `message-log` path is used as today.

## 1. The `msglog.Store` interface

Introduce, in `internal/msglog`, the interface the consumers depend on:

```go
type Store interface {
    Append(m Message) (Message, error)
    ReadInbox(t Target) []Message
    ReadThread(rootID string) []Message
    List(f Filter) []Message
    Close() error
}
```

- The current file type **`*Log` already satisfies it** (its methods match). `Open` keeps returning `*Log`.
- **`msgport.Engine` changes from concrete `*msglog.Log` to `msglog.Store`** (its constructor field/param). The other consumers already use narrower interfaces (`wakeon.Inbox` = `ReadInbox`; adminui `MessageReader` = `List`; harbor's appender) and are unaffected.
- The engine's one-time startup **full-log scan** (`lg.List(Filter{})` to rebuild its "seen ingress IDs" set, `engine.go:52`) is replaced by a **bounded prefix lookup**: add `SeenIDs(prefix string) []string` to `Store` (file: scan the mirror; Postgres: `SELECT id FROM messages WHERE id LIKE prefix || '%'`), so startup never materializes the whole log. This is the only interface addition beyond the current methods.

## 2. Package placement (keep the core stdlib-only)

`internal/msglog` stays **stdlib-only** (its original tenet — no kit/grpc/pgx in the envelope package). The Postgres implementation lives in a **subpackage `internal/msglog/msglogpg`** that imports pgx and the `msglog` types and satisfies `msglog.Store`:

```go
func New(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger) (*Store, error) // *msglogpg.Store implements msglog.Store
```

It takes an already-open `*pgxpool.Pool` so it can **share the Phase-1 control-plane pool/database** (driver D — one datastore, one connection pool). The interface + types are owned by `msglog`; the dependency direction is `msglogpg → msglog`, never the reverse.

## 3. Schema (real columns + indexes; no mirror)

One `messages` row per envelope + a `message_recipients` child table that is the **inbox index**, both written in one transaction on append:

```sql
CREATE TABLE messages (
    id        text PRIMARY KEY,          -- time-sortable; append order == id order
    from_kind text NOT NULL,
    from_ref  text NOT NULL,
    body      text NOT NULL,
    at        timestamptz NOT NULL,
    project   text NOT NULL DEFAULT '',
    reply_to  text NOT NULL DEFAULT '',
    "to"      jsonb NOT NULL             -- the []Target, for Message reconstruction on read
);
CREATE TABLE message_recipients (        -- pure index over messages."to"
    message_id text NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    kind       text NOT NULL,
    ref        text NOT NULL,
    PRIMARY KEY (message_id, kind, ref)
);
CREATE INDEX idx_recipients_target ON message_recipients (kind, ref, message_id);
CREATE INDEX idx_messages_reply_to ON messages (reply_to) WHERE reply_to <> '';
CREATE INDEX idx_messages_project_id ON messages (project, id);
```

- **`to` is stored as JSONB on the row** (source of truth for reconstructing `Message.To`) and **denormalized into `message_recipients`** on append (the queryable inbox index). Scalars are real columns for the project/time/thread queries **C** wants.
- Ordering is by `id` everywhere (append order == lexical id order, by construction), so no separate sequence is needed.

**Reads → queries (returning full `[]Message`, no mirror):**
- `ReadInbox(t)`: `SELECT m.* FROM messages m JOIN message_recipients r ON r.message_id = m.id WHERE r.kind=$1 AND r.ref=$2 ORDER BY m.id` (index `idx_recipients_target`).
- `ReadThread(rootID)`: `WHERE id=$1 OR reply_to=$1 ORDER BY id`.
- `List(f)`: `WHERE ($1='' OR project=$1) AND (at >= since) AND (at < until) ORDER BY id` (index `idx_messages_project_id`).
- Each row reconstructs `Message` directly (From from columns, To from the `"to"` JSONB) — no N+1 recipient fetch.

**Append:** one txn — insert the `messages` row, then the `message_recipients` rows for each `To`. Returns the stored `Message` (id/at assigned by the existing `msglog` logic before the write, unchanged).

## 4. Migrations & lifecycle

`msglogpg` embeds its own SQL migrations and applies them at `New` under a **distinct advisory lock** and its **own bookkeeping table** (`msglog_schema_migrations`), so it never collides with the control-plane store's migrator even though they share the database. Fails closed (a connect/migrate error aborts). No in-memory load step (there is no mirror). `Close` is a no-op on the shared pool (the pool is owned and closed by the control-plane store wiring).

## 5. Wiring & config

The message-log backend **follows the store backend**:
- `store-postgres` configured ⇒ the log is Postgres-backed on that same pool/database (created by `msglogpg` migrations). The `message-log:` file path is ignored in this mode.
- otherwise ⇒ the file log at `message-log:` (unchanged from today).

`cmd/at-harbor` builds a `msglog.Store` accordingly and passes it where `*msglog.Log` flows today (the engine, wake-on, the appender, the admin view), preserving the existing typed-nil-safety handling. Reusing the Phase-1 pool means **no new config** — one datastore, driver **D**.

Two consequences to note:
- **Sharing the pool needs a small Phase-1 touch:** the control-plane `PostgresStore` currently owns an unexported `*pgxpool.Pool`. Expose it (an accessor, e.g. `Pool() *pgxpool.Pool`, or construct the pool in `main` and pass it into both `NewPostgresStore` and `msglogpg.New`). `msglogpg` never closes the shared pool — the control-plane store's `Close` owns it.
- **Under `store-postgres` the message log is effectively always-on** (its tables are created and the Messages view + dual-write shadow are live), rather than opt-in via a path as in file mode. That is a deliberate simplification for driver **D**; a decouple/off toggle is deferred to Phase 3. Consumers that do real work off the log (the `msgport` engine, `wakeon`) remain separately gated by their own config, so auto-on does not start them.

## 6. Tests

- **A shared `msglog.Store` conformance suite** (a new `internal/msglog/msglogtest` package, mirroring Phase 1's `storetest`): append + `ReadInbox`/`ReadThread`/`List`/`SeenIDs` behavior, multi-recipient inbox, thread root+replies, filter by project/time, append order. Run against the **file `Log` hermetically** (default `go test`) and against **`msglogpg` behind the `integration` tag** on a real Postgres (`HARBOR_TEST_POSTGRES_DSN`), so the two backends stay behavior-identical.
- The existing `internal/msglog` file-log tests stay green unchanged (the interface + `SeenIDs` are additive).
- `msgport`/`wakeon`/adminui tests keep using the file log (in-memory, hermetic) — unaffected by the interface change beyond the engine's param type.
- **CI:** extend the Phase-1 `store-integration` workflow to also run `go test -tags integration ./internal/msglog/...` against its Postgres service.

## 7. Boundaries & non-goals

- **Approach A scope:** durable, indexed, mirror-free, **signatures unchanged**. **Deferred to Phase 3:** paginated/cursor reads (ctx + limits) across the API, retention/compaction/rotation, and admin-UI pagination. `List(Filter{})` and a large inbox still materialize their full result set — acceptable now (indexed, and the steady-state RAM win is dropping the always-resident mirror), addressed by Phase 3 pagination.
- **No data migration:** consistent with Phase 1, switching to the Postgres log starts empty (the log is event data; there is no importer). The old JSONL file is left in place.
- **B / multi-node** stays out; the single-writer model holds (the serve process is the sole appender).
- `internal/msglog` core stays stdlib-only; pgx is confined to `msglogpg`.
- No change to the `Message`/`Target`/`Filter`/`Classify` types or id/`Classify` semantics.

## 8. Docs (same change)

- `docs/usage/harbor/serve.md` — the message-log backend follows `store-postgres` (Postgres, shared DB) vs. the file `message-log` path; note "starts empty, no data migration".
- `docs/usage/harbor/ui.md#messages` — one line that the Messages view is served from Postgres when `store-postgres` is set (behavior unchanged; still no pagination — a Phase-3 note).
- `docs/DEVELOPMENT.md` — the `store-integration` job now also covers `internal/msglog/...`.
- Route via docs-author; verify with docs-audit.
