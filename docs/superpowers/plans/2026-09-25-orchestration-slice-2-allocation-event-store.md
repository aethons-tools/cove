# Orchestration Slice 2: the allocation event store (dual-write shadow)

> **For agentic workers:** REQUIRED SUB-SKILL: use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Stand up the **allocation event store** — a per-*(project, role)* event stream with a new **optimistic-concurrency (OCC) append** primitive (`UNIQUE(stream_id, stream_revision)` + expected-revision, which the codebase lacks today) — and have the Allocator **dual-write** `ReservationGranted` events to it as a durable, audited shadow. The **cap decision stays on the registry `LiveCount`** from Slice 1: the ledger is *not yet authoritative*. Behavior-preserving.

**Architecture:** New Postgres-backed package `internal/allocator/allocpg`, mirroring `internal/intercom/intercompg`'s embedded-migrations + advisory-lock idiom (its own lock constant + `alloc_schema_migrations` table, on the shared pool it does not own). The Allocator gains an optional `Recorder`; when Postgres is configured it records a grant per successful raise, when it isn't (file-store dev) it records nothing — identical to Slice 1. Release events and making the ledger authoritative are **Slice 3**, not here.

**Tech Stack:** Go 1.26, `pgx/v5` + `pgxpool`, embedded SQL migrations, `slog`, `just`. Design of record: [`../specs/2026-09-15-orchestration-roles-requisitioner-allocator-supervisor.md`](../specs/2026-09-15-orchestration-roles-requisitioner-allocator-supervisor.md); builds on Slice 1 (merged: `internal/allocator`, `harbor.InstanceCounter`, dispatcher admits via the Allocator).

## Global Constraints

- **Behavior-preserving & dual-write.** The admission decision is unchanged (registry `LiveCount` vs `StaticBudget`). The event store is a **shadow**: recording a grant is best-effort audit; a record failure is logged, never fatal to a raise. The ledger does **not** drive the cap in this slice.
- **Postgres-gated.** The store exists only when `store-postgres` is configured (a shared `*pgxpool.Pool` is available). With the file store there is no pool ⇒ the Allocator's `Recorder` is nil ⇒ it records nothing (exactly Slice 1). Mirrors how the intercom log follows the store backend.
- **Mirror `intercompg`, don't reinvent the migration runner.** Copy `internal/intercom/intercompg`'s `migrate()` (advisory-locked, embed `migrations/*.sql`, `*_schema_migrations` bookkeeping) verbatim, changing only the package, the advisory-lock constant, and the table name. Use a **distinct advisory-lock constant** `0x616c6c6f63 // "alloc"` (distinct from `0x686172626f72` harbor and `0x696e746572636f6d` intercom) and table `alloc_schema_migrations`. The store must **not** own/close the pool (its `Close()` is a no-op; the control-plane store owns the pool).
- **Record grants only** in this slice (one event per real raise — coves are coarse). `ReservationRequested`/`Denied`/`Withdrawn`/`Released` are defined-but-deferred (Released lands in Slice 3); do **not** append on the deny path (it would spam the poll loop).
- **CI coverage (do not skip — recurring blind spot):** the new package's integration test must actually run in CI. Add `./internal/allocator/...` to the `just integration-harbor` recipe **and** to `.github/workflows/store-integration.yml`'s test step, or the pg tests silently never run.
- **TDD**, hermetic unit tests + a pg-gated `//go:build integration` test (needs `just dev-up` + `HARBOR_TEST_POSTGRES_DSN`, like `intercompg`'s). End each commit message with:
  ```
  Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_01BtTj5yZ4qoJzyK8o6625mv
  ```

---

## Task 1: Allocation event types + the Allocator's `Recorder` seam

**Files:**
- Modify: `internal/allocator/allocator.go` (add `Event`, `Kind`, `Recorder`; a `recorder` field; `RecordGrant`)
- Modify: `internal/allocator/allocator_test.go` (add `RecordGrant` tests)
- Modify existing `New` callers in the same commit if the signature changes (see below).

**Interfaces:**
- Produces: `type Kind string` + `const KindReservationGranted Kind = "reservation_granted"`; `type Event struct{ Category, Project, Role string; Kind Kind; ReservationID string }`; `type Recorder interface { Record(ctx context.Context, ev Event) error }`; `func (*Allocator) RecordGrant(ctx, project, role, reservationID string) error`.
- Decision: extend `New` to `New(counter Counter, budget Budget, recorder Recorder) *Allocator` — `recorder` may be nil. (Update the three existing `New(...)` call sites: `cmd/at-harbor/main.go` and the two in `allocator_test.go`/`dispatcher_test.go`. Passing `nil` preserves Slice-1 behavior.)

- [ ] **Step 1: Write the failing tests** (append to `allocator_test.go`)

```go
type fakeRecorder struct{ events []Event }

func (r *fakeRecorder) Record(_ context.Context, ev Event) error {
	r.events = append(r.events, ev)
	return nil
}

func TestRecordGrant_AppendsEvent(t *testing.T) {
	rec := &fakeRecorder{}
	a := New(fakeCounter{n: 0}, StaticBudget{{Project: "acme", Role: "worker"}: 3}, rec)
	if err := a.RecordGrant(context.Background(), "acme", "worker", "cove-AET-1"); err != nil {
		t.Fatal(err)
	}
	if len(rec.events) != 1 {
		t.Fatalf("recorded %d events, want 1", len(rec.events))
	}
	got := rec.events[0]
	if got.Kind != KindReservationGranted || got.Project != "acme" || got.Role != "worker" || got.ReservationID != "cove-AET-1" || got.Category != "acme" {
		t.Fatalf("unexpected event: %+v", got)
	}
}

func TestRecordGrant_NilRecorder_NoOp(t *testing.T) {
	a := New(fakeCounter{n: 0}, StaticBudget{{Project: "acme", Role: "worker"}: 3}, nil)
	if err := a.RecordGrant(context.Background(), "acme", "worker", "cove-AET-1"); err != nil {
		t.Fatalf("nil recorder should be a no-op, got %v", err)
	}
}
```

(Also update the Slice-1 `Admit` tests' `New(...)` calls to pass a third `nil` arg.)

- [ ] **Step 2: Run, verify fail** — `go test ./internal/allocator/` → FAIL (`New` arity, `undefined: Recorder`).

- [ ] **Step 3: Implement** (add to `allocator.go`; `import "context"`)

```go
// Kind is an allocation event type.
type Kind string

const KindReservationGranted Kind = "reservation_granted"

// Event is one allocation event. Category is the project (the grouping/shard
// axis); the stream is keyed by (Project, Role). Revision/seq/timestamp are
// assigned by the store on append.
type Event struct {
	Category      string
	Project, Role string
	Kind          Kind
	ReservationID string
}

// Recorder persists allocation events (the durable reservation ledger). Optional:
// a nil Recorder (file-store dev, no Postgres) means the Allocator records nothing,
// exactly as slice 1. Implemented by internal/allocator/allocpg.
type Recorder interface {
	Record(ctx context.Context, ev Event) error
}
```

Add `recorder Recorder` to the `Allocator` struct; set it in `New` (new third param). Then:

```go
// RecordGrant durably records that a session was granted for (project, role) — a
// best-effort shadow write (the cap is still the registry count in this slice). A
// nil Recorder is a no-op.
func (a *Allocator) RecordGrant(ctx context.Context, project, role, reservationID string) error {
	if a.recorder == nil {
		return nil
	}
	return a.recorder.Record(ctx, Event{
		Category:      project,
		Project:       project,
		Role:          role,
		Kind:          KindReservationGranted,
		ReservationID: reservationID,
	})
}
```

Update `cmd/at-harbor/main.go`'s `allocator.New(...)` call to pass `nil` for now (Task 4 replaces it with the real store).

- [ ] **Step 4: Run, verify pass** — `go test ./internal/allocator/ ./internal/dispatcher/` and `go build ./...` → green.

- [ ] **Step 5: Commit** — `allocator: Event/Recorder seam + RecordGrant (slice 2)`

---

## Task 2: The `allocpg` Postgres event store with the OCC append

**Files:**
- Create: `internal/allocator/allocpg/allocpg.go`
- Create: `internal/allocator/allocpg/migrations.go` (embed)
- Create: `internal/allocator/allocpg/migrations/0001_alloc_events.sql`
- Create: `internal/allocator/allocpg/allocpg_integration_test.go` (`//go:build integration`)

**Interfaces:**
- Consumes: `allocator.Event`, `*pgxpool.Pool`.
- Produces: `func New(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger) (*Store, error)`; `func (*Store) Record(ctx context.Context, ev allocator.Event) error` (satisfies `allocator.Recorder`); `func (*Store) Close() error` (no-op); test helper `func (*Store) events(ctx, streamID string) ([]row, error)`; `var ErrConflictExhausted error`.

- [ ] **Step 1: Write the migration** `migrations/0001_alloc_events.sql`

```sql
CREATE TABLE alloc_events (
    global_seq      BIGSERIAL PRIMARY KEY,
    category        TEXT NOT NULL,
    stream_id       TEXT NOT NULL,
    stream_revision BIGINT NOT NULL,
    kind            TEXT NOT NULL,
    reservation_id  TEXT NOT NULL DEFAULT '',
    at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    data            JSONB NOT NULL DEFAULT '{}',
    UNIQUE (stream_id, stream_revision)
);
CREATE INDEX idx_alloc_events_stream ON alloc_events (stream_id, stream_revision);
CREATE INDEX idx_alloc_events_category ON alloc_events (category, global_seq);
```

`global_seq` is the cheap global order (observation only); `UNIQUE(stream_id, stream_revision)` is the OCC gate.

- [ ] **Step 2: `migrations.go`** — copy `internal/intercom/intercompg/migrations.go` verbatim, changing only `package intercompg` → `package allocpg`:

```go
package allocpg

import "embed"

//go:embed migrations/*.sql
var migrationFiles embed.FS
```

- [ ] **Step 3: Write the failing integration test** (`allocpg_integration_test.go`, first line `//go:build integration`)

Mirror `intercompg_integration_test.go`'s harness (read `HARBOR_TEST_POSTGRES_DSN`, skip if unset, open a pool, `TRUNCATE alloc_events` between cases). Cover:

```go
// New applies migrations; the table exists and Record appends sequential revisions.
func TestAllocpg_RecordAppendsSequentialRevisions(t *testing.T) {
	st := newTestStore(t) // helper: pool from DSN + New(ctx, pool, log)
	ev := allocator.Event{Category: "acme", Project: "acme", Role: "worker", Kind: allocator.KindReservationGranted, ReservationID: "cove-AET-1"}
	for i := 0; i < 3; i++ {
		if err := st.Record(context.Background(), ev); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := st.events(context.Background(), "acme/worker")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[0].revision != 1 || rows[2].revision != 3 {
		t.Fatalf("revisions not 1..3: %+v", rows)
	}
}

// The UNIQUE(stream_id, stream_revision) gate rejects a duplicate revision — the
// OCC primitive. A raw duplicate INSERT must fail with a unique violation.
func TestAllocpg_DuplicateRevisionRejected(t *testing.T) {
	st := newTestStore(t)
	ins := func(rev int64) error {
		_, err := st.pool.Exec(context.Background(),
			`INSERT INTO alloc_events (category, stream_id, stream_revision, kind) VALUES ($1,$2,$3,$4)`,
			"acme", "acme/worker", rev, "reservation_granted")
		return err
	}
	if err := ins(1); err != nil {
		t.Fatal(err)
	}
	if err := ins(1); !isUniqueViolation(err) {
		t.Fatalf("expected unique violation on duplicate revision, got %v", err)
	}
}
```

- [ ] **Step 4: Run, verify fail** — `just dev-up`; `export HARBOR_TEST_POSTGRES_DSN="host=localhost port=15432 dbname=harbor user=harbor password=harbor sslmode=disable"`; `go test -tags integration ./internal/allocator/allocpg/` → FAIL (package/undefined).

- [ ] **Step 5: Implement `allocpg.go`**

Mirror `intercompg`'s `New`/`migrate` (advisory lock `0x616c6c6f63`, table `alloc_schema_migrations`, `Close()` no-op). Then the OCC append:

```go
const migrateAdvisoryLock = 0x616c6c6f63 // "alloc" — distinct from harbor/intercom locks

const maxAppendRetries = 5

// ErrConflictExhausted means the OCC append lost the version race maxAppendRetries
// times running — pathological under single-instance serial writes.
var ErrConflictExhausted = errors.New("allocpg: append conflict retries exhausted")

// Record appends one allocation event to the (project, role) stream using an OCC
// append: read the current head revision, insert at head+1, and on a
// UNIQUE(stream_id, stream_revision) violation re-read and retry. Correctness is
// the constraint, not read freshness — a stale head only costs a retry.
func (s *Store) Record(ctx context.Context, ev allocator.Event) error {
	streamID := ev.Project + "/" + ev.Role
	data, err := json.Marshal(map[string]string{}) // slice 2: no extra payload yet
	if err != nil {
		return fmt.Errorf("allocpg: marshal: %w", err)
	}
	for attempt := 0; attempt < maxAppendRetries; attempt++ {
		var head int64
		if err := s.pool.QueryRow(ctx,
			`SELECT COALESCE(MAX(stream_revision), 0) FROM alloc_events WHERE stream_id = $1`,
			streamID).Scan(&head); err != nil {
			return fmt.Errorf("allocpg: head: %w", err)
		}
		_, err := s.pool.Exec(ctx,
			`INSERT INTO alloc_events (category, stream_id, stream_revision, kind, reservation_id, data)
			 VALUES ($1,$2,$3,$4,$5,$6)`,
			ev.Category, streamID, head+1, string(ev.Kind), ev.ReservationID, data)
		if err == nil {
			return nil
		}
		if isUniqueViolation(err) {
			continue // lost the version race — re-read head and retry
		}
		return fmt.Errorf("allocpg: append: %w", err)
	}
	return ErrConflictExhausted
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
```

Add the `events(ctx, streamID)` test helper (SELECT ordered by `stream_revision`, returning a small `row{revision int64; kind, reservationID string}`).

- [ ] **Step 6: Run, verify pass** — `go test -tags integration ./internal/allocator/allocpg/` → PASS; `go build ./...` → green.

- [ ] **Step 7: Commit** — `allocpg: allocation event store with OCC append (slice 2)`

---

## Task 3: Dispatcher records a grant after each successful raise

**Files:**
- Modify: `internal/dispatcher/dispatcher.go` (extend `Admitter`; call `RecordGrant` post-raise, best-effort)
- Modify: `internal/dispatcher/dispatcher_test.go`

**Interfaces:**
- Consumes: `allocator.Allocator.RecordGrant`.
- Produces: extended `Admitter`:
  ```go
  type Admitter interface {
  	Admit(project, role string) bool
  	RecordGrant(ctx context.Context, project, role, reservationID string) error
  }
  ```

- [ ] **Step 1: Failing test** — extend the dispatcher's fake admitter with `RecordGrant` (recording calls), and assert that after a successful raise the dispatcher calls `RecordGrant` with the issue's actorID; and that a **raise failure** records nothing.

```go
func TestTick_RecordsGrantAfterSuccessfulRaise(t *testing.T) {
	tr := &fakeTracker{ready: []scheduler.Issue{{ID: "1", Identifier: "AET-1", DispatchLabeled: true}}}
	rz := &fakeRaiser{}
	adm := &fakeAdmitter{allow: 1}
	d := New(tr, rz, &fakeRegistry{}, adm, Config{Role: "worker", Project: "acme", PollInterval: time.Minute}, slog.Default())
	d.tick(context.Background())
	if len(adm.granted) != 1 || adm.granted[0] != "cove-AET-1" {
		t.Fatalf("RecordGrant calls = %v, want [cove-AET-1]", adm.granted)
	}
}
```

- [ ] **Step 2: Run, verify fail** — FAIL (fake lacks `RecordGrant` / not called).

- [ ] **Step 3: Implement** — add `RecordGrant` to `Admitter`; in `tick`, after the successful-raise log line, call it best-effort:

```go
	d.log.Info("dispatcher: raised cove", "issue", iss.Identifier, "actor", actorID)
	if err := d.admitter.RecordGrant(ctx, d.cfg.Project, d.cfg.Role, actorID); err != nil {
		d.log.Warn("dispatcher: record grant failed (shadow, non-fatal)", "actor", actorID, "err", err.Error())
	}
```

Update `fakeAdmitter` to implement `RecordGrant` (append to a `granted []string`).

- [ ] **Step 4: Run, verify pass** — `go test ./internal/dispatcher/` → green.

- [ ] **Step 5: Commit** — `dispatcher: record a grant after a successful raise (slice 2)`

---

## Task 4: Wire the store in `main.go` + extend integration coverage

**Files:**
- Modify: `cmd/at-harbor/main.go`
- Modify: `justfile` (the `integration-harbor` recipe)
- Modify: `.github/workflows/store-integration.yml` (the go-test step's package list)

- [ ] **Step 1:** In `cmd/at-harbor/main.go`, where the Allocator is built (Slice-1 site), construct the store when the pool exists and pass it as the recorder:

```go
var rec allocator.Recorder // nil ⇒ Allocator records nothing (file-store dev), as slice 1
if pgPool != nil {
	as, err := allocpg.New(context.Background(), pgPool, log)
	if err != nil {
		fmt.Fprintln(stderr, "at-harbor:", err)
		return 1
	}
	rec = as
}
budget := allocator.StaticBudget{{Project: dc.Project, Role: dc.Role}: dc.MaxConcurrent}
alloc := allocator.New(harbor.InstanceCounter{Store: st}, budget, rec)
```

Add the `github.com/aethons-tools/cove/internal/allocator/allocpg` import.

- [ ] **Step 2:** Extend `just integration-harbor` to cover the new package:

```
integration-harbor:
    go test -tags integration ./cmd/at-harbor/... ./internal/harbor/... ./internal/allocator/...
```

- [ ] **Step 3:** In `.github/workflows/store-integration.yml`, add `./internal/allocator/...` to the same `go test -tags integration ...` step so the pg tests run in CI. (Recurring blind spot: CI workflow files are not caught by code-level checks — verify the exact command line and update it.)

- [ ] **Step 4:** `go build ./...` → compiles.

- [ ] **Step 5: Commit** — `at-harbor: wire the allocation event store (pg-gated) + CI coverage (slice 2)`

---

## Task 5: Verification gate

- [ ] `just test` → green (hermetic; allocator + dispatcher).
- [ ] `just dev-up`; `export HARBOR_TEST_POSTGRES_DSN=...`; `just integration-harbor` → green (allocpg migrations apply, OCC append + UNIQUE gate verified). `just dev-down` after.
- [ ] `go build ./...` (or `GOPROXY=direct … go build ./...`) → compiles.
- [ ] `just lint` → green.
- [ ] **Behavior check:** with the file store (no pool) the Allocator's recorder is nil and nothing is recorded — the dispatcher behaves exactly as Slice 1. With Postgres, a `reservation_granted` row appears per raise; the cap is still the registry count.

## Self-review

- **Spec coverage:** implements the re-scoped Slice 2 (durable event store + OCC append primitive, dual-write shadow, pg-gated) from the design's roadmap; the ledger is deliberately **not** authoritative and **release** is deferred to Slice 3 — see the split recorded on the design PR.
- **Placeholder scan:** none — concrete SQL, Go, and commands throughout.
- **Type consistency:** `Event`, `Kind`, `KindReservationGranted`, `Recorder`, `RecordGrant`, `allocpg.New`/`Record`, extended `Admitter` are used identically across tasks; `*allocpg.Store` satisfies `allocator.Recorder`; `allocpg` imports `allocator` (no cycle — `allocator` never imports `allocpg`).
- **Ambiguity:** grants only are recorded (not deny/requested) — explicit; release is Slice 3.
- **CI blind spot:** Task 4 updates both the justfile recipe and the GitHub workflow so the integration test actually runs.

## Next (Slice 3, not this plan)

Release + cutover: the Supervisor emits Session-terminal events (or the Allocator observes teardowns) → append `ReservationReleased`; then flip the cap from the registry count to the ledger fold (`Granted − Released`) + snapshot, and retire the `InstanceCounter` path — making the event store authoritative.
