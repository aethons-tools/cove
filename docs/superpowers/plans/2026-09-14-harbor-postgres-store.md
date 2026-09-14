# harbor Postgres-backed control-plane Store (Phase 1) — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `PostgresStore` implementing the existing `harbor.Store` interface — Postgres as durable source of truth with an in-memory write-through cache — while keeping `FileStore` as the hermetic/dev backend, selected by serve config.

**Architecture:** Extract the in-memory maps + reads + mutation helpers into a shared `memState`; `FileStore` = `memState` + whole-file `save()`, `PostgresStore` = `memState` + per-op SQL in a txn applied **after commit**. Reads are served from the cache (RWMutex, `RLock`); the DB is loaded once at startup. Document-table schema (key columns + a JSONB `doc` marshaled from the existing structs). No `Store` interface change.

**Tech Stack:** Go, `github.com/jackc/pgx/v5` (`pgxpool`), embedded SQL migrations via `embed.FS`, the existing `internal/secret` credential resolver, GitHub Actions (a new Postgres-service job).

**Spec:** `docs/superpowers/specs/2026-09-14-harbor-postgres-store.md`

## Global Constraints

- **No change to the `harbor.Store` interface** (no `context`/`error` signature changes). Verified: no consumer uses the concrete `*FileStore` type outside `filestore.go`.
- **`internal/msglog` is out of scope and untouched** (that is Phase 2).
- **Secrets never hit disk, argv, or logs.** The DB password is a named credential resolved in memory via `secret.Resolve`; the DSN is assembled in memory, never logged (log host/database only), never written to a file or command line.
- **Invariants:** commit-then-cache (mutate the in-memory map only *after* the txn commits; on DB error return it and leave the cache untouched); reads take `RLock` and never hit the DB; fail-closed startup (failed migration or initial load ⇒ `serve` refuses to start); multi-record changes are one txn.
- **Single-writer preserved.** `version`/`updated_at` columns are written but not enforced — the B hook; do not build multi-writer logic.
- **Tests hermetic by default;** Postgres-backed tests live behind `//go:build integration` and run against `HARBOR_TEST_POSTGRES_DSN`, skipping when unset. Keep `FileStore`-based consumer/handler tests unchanged.
- **pgx is a new dependency.** `go get github.com/jackc/pgx/v5` must go through the sandbox `GOPROXY`; a proxy failure is a kit change (allow the module host), not a transient fault — escalate if it fails, do not hunt for a mirror.
- **Commit trailer — use verbatim on every commit** (do not substitute your own model name):

  ```
  Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_01BtTj5yZ4qoJzyK8o6625mv
  ```

- Branch is `feat/harbor-postgres-store` (the spec is committed there).

## File Structure

- `internal/harbor/storetest/conformance.go` — **new**: exported `RunConformance(t, newStore)` behavioral suite for any `harbor.Store` (Task 1).
- `internal/harbor/store_conformance_test.go` — **new**: runs the suite against `FileStore` (hermetic) (Task 1).
- `internal/harbor/memstate.go` — **new**: the shared in-memory core (Task 2).
- `internal/harbor/filestore.go` — **modify**: refactored onto `memState`, keeps `save()` (Task 2).
- `internal/harbor/pgstore.go` — **new**: `PostgresStore` + `NewPostgresStore` (Task 3).
- `internal/harbor/migrations/0001_init.sql` (+ `migrations.go` embed) — **new**: schema (Task 3).
- `internal/harbor/pgstore_integration_test.go` — **new**, `//go:build integration`: runs the conformance suite against a real Postgres (Task 3).
- `cmd/at-harbor/config.go`, `config_test.go` — **modify**: `store-postgres` block + validation (Task 4).
- `cmd/at-harbor/main.go` — **modify**: backend selection + in-memory DSN assembly (Task 4).
- `.github/workflows/store-integration.yml` — **new**: Postgres-service job (Task 5).
- `docs/usage/harbor/serve.md`, `docs/DEVELOPMENT.md` — **modify** (Task 5).

---

### Task 1: `Store` conformance suite (hermetic, against FileStore)

Write the backend-agnostic behavioral contract first, validated against the known-good `FileStore`. It becomes the oracle for `PostgresStore` (Task 3) and a second safety net for the Task 2 refactor.

**Files:**
- Create: `internal/harbor/storetest/conformance.go`
- Create: `internal/harbor/store_conformance_test.go`

**Interfaces:**
- Consumes: `harbor.Store`, `harbor.NewFileStore`, and the aggregate types (`Actor`, `Grant`, `Role`, `Scope`, `Kit`, `Instance`, `Destination`, `Human`, `Channel`, `EscalationTier`) in `internal/harbor`.
- Produces: `func storetest.RunConformance(t *testing.T, newStore func(t *testing.T) harbor.Store)` — called by Task 3's integration test.

- [ ] **Step 1: Write the conformance suite**

Create `internal/harbor/storetest/conformance.go` in package `storetest`. Provide one exported entry point that runs every case as a subtest against a freshly-constructed store. Cover **every** `harbor.Store` method; use `internal/harbor/filestore_test.go` as the reference for exact expected semantics. Skeleton (fill in the remaining cases following the same pattern — do not leave any interface method unexercised):

```go
// Package storetest is a backend-agnostic conformance suite for harbor.Store.
// Both FileStore (hermetic) and PostgresStore (integration) run it, guaranteeing
// behavioral parity.
package storetest

import (
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/harbor"
)

// RunConformance exercises the full harbor.Store contract. newStore must return
// a fresh, empty store on each call.
func RunConformance(t *testing.T, newStore func(t *testing.T) harbor.Store) {
	t.Run("actors_add_lookup_remove", func(t *testing.T) {
		s := newStore(t)
		a := harbor.Actor{ID: "cove-1", TokenHash: "hash-1"}
		if err := s.AddActor(a); err != nil {
			t.Fatalf("AddActor: %v", err)
		}
		if err := s.AddActor(a); err == nil {
			t.Fatal("AddActor of a duplicate id must error")
		}
		got, ok := s.Lookup("hash-1")
		if !ok || got.ID != "cove-1" {
			t.Fatalf("Lookup = %+v, %v", got, ok)
		}
		if err := s.RemoveActor("cove-1"); err != nil {
			t.Fatalf("RemoveActor: %v", err)
		}
		if _, ok := s.Lookup("hash-1"); ok {
			t.Fatal("actor still present after RemoveActor")
		}
	})

	t.Run("grants_upsert_remove", func(t *testing.T) {
		s := newStore(t)
		if err := s.AddActor(harbor.Actor{ID: "cove-1", TokenHash: "h"}); err != nil {
			t.Fatal(err)
		}
		if err := s.AddGrant("cove-1", harbor.Grant{Project: "acme", Role: "worker"}); err != nil {
			t.Fatalf("AddGrant: %v", err)
		}
		if err := s.AddGrant("absent", harbor.Grant{Project: "acme", Role: "worker"}); err == nil {
			t.Fatal("AddGrant to an absent actor must error")
		}
		a, _ := s.Lookup("h")
		if len(a.Grants) != 1 {
			t.Fatalf("grants = %d, want 1", len(a.Grants))
		}
		if err := s.RemoveGrant("cove-1", "acme", "worker"); err != nil {
			t.Fatalf("RemoveGrant: %v", err)
		}
	})

	t.Run("roles_crud_projects", func(t *testing.T) {
		s := newStore(t)
		if err := s.PutRole("acme", harbor.Role{Name: "worker", Scope: harbor.Scope{Destinations: []string{"anthropic"}, TTL: time.Hour}}); err != nil {
			t.Fatalf("PutRole: %v", err)
		}
		r, ok := s.GetRole("acme", "worker")
		if !ok || len(r.Scope.Destinations) != 1 {
			t.Fatalf("GetRole = %+v, %v", r, ok)
		}
		if got := s.ListProjects(); len(got) != 1 || got[0] != "acme" {
			t.Fatalf("ListProjects = %v", got)
		}
		if got := s.ListRoles("acme"); len(got) != 1 {
			t.Fatalf("ListRoles = %v", got)
		}
		if err := s.RemoveRole("acme", "worker"); err != nil {
			t.Fatalf("RemoveRole: %v", err)
		}
	})

	// (remaining subtests added per the checklist below)
}
```

Then add a real subtest (same assertion style, no placeholder comments left in the shipped file) for **every remaining `Store` method**, using `internal/harbor/filestore_test.go` for the exact expected semantics of each:
- **kits:** `PushKit` (returns an incrementing version), `GetKit`, `KitConfig(name, version)`, `PinKit`, `ListKits`, `RemoveKit`, `RoleReferencingKit` (and `RemoveKit` is blocked while a role references the kit).
- **instances:** `PutInstance` (upsert), `GetInstance`, `ListInstances`, `RemoveInstance`.
- **destinations:** `AddDestination`, `RemoveDestination`, `ListDestinations`, `Match(reqPath)`.
- **roster:** `AddHuman`/`AddChannel` (upsert by name), `RemoveHuman`/`RemoveChannel`, `GetProject`, `GetRoster`, `SetEscalationPolicy(project, category, tiers)`.

Plus one cross-cutting case (this is the backend-agnostic check for the **commit-then-cache** invariant):
- **`failed_write_leaves_cache_unchanged`:** provoke a write error (e.g. `AddActor` with a duplicate id) and assert the store's readable state is exactly what it was before the failed call — no partial/phantom entry.

- [ ] **Step 2: Run the suite against FileStore**

Create `internal/harbor/store_conformance_test.go` (package `harbor_test`):

```go
package harbor_test

import (
	"testing"

	"github.com/aethons-tools/cove/internal/harbor"
	"github.com/aethons-tools/cove/internal/harbor/storetest"
)

func TestFileStoreConformance(t *testing.T) {
	storetest.RunConformance(t, func(t *testing.T) harbor.Store {
		s, err := harbor.NewFileStore(t.TempDir() + "/store.json")
		if err != nil {
			t.Fatalf("NewFileStore: %v", err)
		}
		return s
	})
}
```

- [ ] **Step 3: Run and verify GREEN against FileStore**

Run: `go test ./internal/harbor/ -run TestFileStoreConformance -v 2>&1 | tail -30`
Expected: PASS — every subtest green (FileStore already implements the contract). If a subtest fails, the assertion is wrong (mismatched against `filestore_test.go`), not FileStore — fix the assertion.

- [ ] **Step 4: Commit**

```bash
git add internal/harbor/storetest/ internal/harbor/store_conformance_test.go
git commit -m "harbor: Store conformance suite (validated against FileStore)

$(printf 'Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>\nClaude-Session: https://claude.ai/code/session_01BtTj5yZ4qoJzyK8o6625mv')"
```

---

### Task 2: Extract the shared `memState` core (behavior-preserving refactor)

Move the in-memory maps + all read methods + pure mutation helpers out of `FileStore` into an unexported `memState`, so a second backend can reuse them. **This is a refactor — no behavior changes.** The oracle is the two suites that must stay green unchanged: `internal/harbor/filestore_test.go` and `TestFileStoreConformance` (Task 1).

**Files:**
- Create: `internal/harbor/memstate.go`
- Modify: `internal/harbor/filestore.go`

**Interfaces:**
- Consumes: existing `FileStore` internals.
- Produces (for Task 3): `memState` with — an `sync.RWMutex`; the maps (`roles`, `actors`, `dests`, `kits`, `instances`, `projects`); read methods delegating from the `Store` reads; and pure in-memory mutation helpers used post-commit. Exact helper set to expose (used by `PostgresStore`): one per mutating `Store` method, applying only the in-memory change (no persistence), e.g. `func (m *memState) applyAddActor(a Actor)`, `applyRemoveActor(id string)`, `applyAddGrant`, `applyPutRole`, … mirroring each `Store` mutator.
- **Locking discipline (important — `sync.RWMutex` is not re-entrant):** each public read method takes `RLock`; each `apply*` and each mutator takes no lock (the caller holds `Lock`). Where a mutator needs to *read* while holding the write lock (e.g. `RemoveKit` must check `RoleReferencingKit` before deleting), factor the read into a **lock-free internal helper** (`func (m *memState) roleReferencingKit(name string) (project, role string, ok bool)`) that the public `RoleReferencingKit` wraps under `RLock` and that mutators call directly. Never call a public `RLock` read method from inside a `Lock`-held mutator (self-deadlock).

- [ ] **Step 1: Read the whole current file**

Read `internal/harbor/filestore.go` end to end. Note every method, the map fields, `save()`, and whether reads currently lock.

- [ ] **Step 2: Introduce `memState`**

Create `internal/harbor/memstate.go`. Define:

```go
package harbor

import "sync"

// memState is the in-memory representation of the control plane, shared by
// FileStore and PostgresStore. It owns the maps, the read methods, and pure
// in-memory mutation helpers (apply*). Persistence is the embedding store's job.
type memState struct {
	mu        sync.RWMutex
	roles     map[string]map[string]Role
	actors    map[string]Actor       // keyed by TokenHash
	dests     map[string]Destination // keyed by Name
	kits      map[string]Kit         // keyed by Name
	instances map[string]Instance    // keyed by ActorID
	projects  map[string]Project     // keyed by Name
}

func newMemState() *memState {
	return &memState{
		roles: map[string]map[string]Role{}, actors: map[string]Actor{},
		dests: map[string]Destination{}, kits: map[string]Kit{},
		instances: map[string]Instance{}, projects: map[string]Project{},
	}
}
```

Move the map fields off `FileStore` and embed `*memState`:

```go
type FileStore struct {
	path string
	*memState
}
```

- [ ] **Step 3: Move reads and split each mutator into apply* + persist**

Move every **read** method (`Lookup`, `ListActors`, `GetRole`, `ListRoles`, `ListProjects`, `GetKit`, `KitConfig`, `ListKits`, `RoleReferencingKit`, `GetInstance`, `ListInstances`, `ListDestinations`, `Match`, `GetProject`, `GetRoster`) onto `*memState`, taking `m.mu.RLock()` (preserve existing copy-on-read semantics exactly).

For every **mutator**, split the in-memory change into an unexported `apply*` method on `*memState` (no locking inside — the caller holds the write lock; no persistence), and keep the `FileStore` method as: `m.mu.Lock(); validate; apply*; err := fs.save(); m.mu.Unlock(); return err`. Example:

```go
// memstate.go
func (m *memState) applyAddActor(a Actor) { m.actors[a.TokenHash] = a }

// filestore.go
func (fs *FileStore) AddActor(a Actor) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	for _, rec := range fs.actors {
		if rec.ID == a.ID {
			return fmt.Errorf("actor %q already exists", a.ID)
		}
	}
	fs.applyAddActor(a)
	return fs.save()
}
```

Keep `save()` on `FileStore` (whole-file rewrite, unchanged), reading the maps through the embedded `memState`. `NewFileStore` calls `newMemState()` then loads the file into the maps as today.

- [ ] **Step 4: Verify both suites stay GREEN, unchanged**

Run: `go test ./internal/harbor/ 2>&1 | tail -20`
Expected: `filestore_test.go` AND `TestFileStoreConformance` pass with **no test edits**. Then `go build ./... && go vet ./internal/harbor/` clean.
If a test needed editing to pass, the refactor changed behavior — revert that and preserve the original behavior instead.

- [ ] **Step 5: Commit**

```bash
git add internal/harbor/memstate.go internal/harbor/filestore.go
git commit -m "harbor: extract shared memState core from FileStore (no behavior change)

$(printf 'Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>\nClaude-Session: https://claude.ai/code/session_01BtTj5yZ4qoJzyK8o6625mv')"
```

---

### Task 3: `PostgresStore` + embedded migrations

**Files:**
- Create: `internal/harbor/pgstore.go`, `internal/harbor/migrations.go`, `internal/harbor/migrations/0001_init.sql`
- Create: `internal/harbor/pgstore_integration_test.go` (`//go:build integration`)
- Modify: `go.mod`/`go.sum` (add pgx)

**Interfaces:**
- Consumes: `*memState` + its `apply*` helpers and read methods (Task 2); the aggregate types.
- Produces: `func NewPostgresStore(ctx context.Context, dsn string, log *slog.Logger) (*PostgresStore, error)` returning a `harbor.Store`; a `Close()` method.

- [ ] **Step 1: Add the dependency**

Run: `go get github.com/jackc/pgx/v5@latest`
Expected: resolves via GOPROXY. If it fails with a proxy/connection error, STOP and report BLOCKED — the module host must be added to the sandbox allow-list (a kit change); do not seek a mirror.

- [ ] **Step 2: Write the schema + embed**

Create `internal/harbor/migrations/0001_init.sql` — the six document tables, each `key + doc jsonb + version bigint NOT NULL DEFAULT 1 + updated_at timestamptz NOT NULL DEFAULT now()`:

```sql
CREATE TABLE IF NOT EXISTS actors (
    token_hash text PRIMARY KEY,
    id         text NOT NULL UNIQUE,
    doc        jsonb NOT NULL,
    version    bigint NOT NULL DEFAULT 1,
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS roles (
    project text NOT NULL, name text NOT NULL,
    doc jsonb NOT NULL, version bigint NOT NULL DEFAULT 1, updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (project, name)
);
CREATE TABLE IF NOT EXISTS kits         (name text PRIMARY KEY, doc jsonb NOT NULL, version bigint NOT NULL DEFAULT 1, updated_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE IF NOT EXISTS instances    (actor_id text PRIMARY KEY, doc jsonb NOT NULL, version bigint NOT NULL DEFAULT 1, updated_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE IF NOT EXISTS destinations (name text PRIMARY KEY, doc jsonb NOT NULL, version bigint NOT NULL DEFAULT 1, updated_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE IF NOT EXISTS projects     (name text PRIMARY KEY, doc jsonb NOT NULL, version bigint NOT NULL DEFAULT 1, updated_at timestamptz NOT NULL DEFAULT now());
```

Create `internal/harbor/migrations.go`:

```go
package harbor

import "embed"

//go:embed migrations/*.sql
var migrationFiles embed.FS
```

- [ ] **Step 3: Implement `PostgresStore`**

Create `internal/harbor/pgstore.go`. Structure:

```go
package harbor

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresStore is a harbor.Store backed by Postgres (durable source of truth)
// with an in-memory write-through cache (memState) serving all reads. Single
// serve process is the sole writer (Phase 1).
type PostgresStore struct {
	*memState
	pool *pgxpool.Pool
	log  *slog.Logger
}

// NewPostgresStore connects, applies embedded migrations, loads all rows into
// the cache, and returns a ready store. Fails closed on any step.
func NewPostgresStore(ctx context.Context, dsn string, log *slog.Logger) (*PostgresStore, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("pgstore: connect: %w", err)
	}
	s := &PostgresStore{memState: newMemState(), pool: pool, log: log}
	if err := s.migrate(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	if err := s.load(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return s, nil
}

func (s *PostgresStore) Close() { s.pool.Close() }
```

Implement:
- `migrate(ctx)` — a `schema_migrations(version int primary key, applied_at timestamptz default now())` table; take a Postgres advisory lock (`pg_advisory_xact_lock`) inside a txn; read embedded `migrations/*.sql` in lexical order; apply any whose number isn't recorded; record it. Idempotent across restarts.
- `load(ctx)` — `SELECT doc FROM <table>` for each table; `json.Unmarshal` each into the aggregate; populate the maps directly (no lock needed pre-return; the store isn't shared yet). Rebuild `roles` as `project→name→Role` and `projects` from their docs.
- Every **write** method: `mu.Lock()`; run the SQL in a txn via `pgx.BeginFunc`; on commit success call the matching `apply*` helper; `mu.Unlock()`. The doc is `json.Marshal(aggregate)`. Uniform patterns — implement all ~35 following these three representative examples:

```go
func (s *PostgresStore) AddActor(a Actor) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, rec := range s.actors {
		if rec.ID == a.ID {
			return fmt.Errorf("actor %q already exists", a.ID)
		}
	}
	doc, err := json.Marshal(a)
	if err != nil {
		return err
	}
	err = pgx.BeginFunc(context.Background(), s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(),
			`INSERT INTO actors (token_hash, id, doc) VALUES ($1,$2,$3)`,
			a.TokenHash, a.ID, doc)
		return err
	})
	if err != nil {
		return fmt.Errorf("pgstore: AddActor: %w", err)
	}
	s.applyAddActor(a) // cache only after commit
	return nil
}

func (s *PostgresStore) PutRole(project string, r Role) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, err := json.Marshal(r)
	if err != nil {
		return err
	}
	err = pgx.BeginFunc(context.Background(), s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(),
			`INSERT INTO roles (project, name, doc) VALUES ($1,$2,$3)
			 ON CONFLICT (project, name) DO UPDATE SET doc = EXCLUDED.doc, version = roles.version + 1, updated_at = now()`,
			project, r.Name, doc)
		return err
	})
	if err != nil {
		return fmt.Errorf("pgstore: PutRole: %w", err)
	}
	s.applyPutRole(project, r)
	return nil
}

func (s *PostgresStore) RemoveKit(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if project, role, ok := s.roleReferencingKit(name); ok { // lock-free internal helper (we hold Lock)
		return fmt.Errorf("kit %q still referenced by role %s/%s", name, project, role)
	}
	err := pgx.BeginFunc(context.Background(), s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `DELETE FROM kits WHERE name = $1`, name)
		return err
	})
	if err != nil {
		return fmt.Errorf("pgstore: RemoveKit: %w", err)
	}
	s.applyRemoveKit(name)
	return nil
}
```

Table map for the remaining writers: actors/grants → `actors` (grants live inside the actor doc: `AddGrant`/`RemoveGrant` re-marshal the mutated actor and UPSERT its row); roles → `roles`; kits (`PushKit`/`PinKit`/`RemoveKit`) → `kits` (versions inside the doc); instances → `instances`; destinations → `destinations`; humans/channels/escalation (`AddHuman`/`AddChannel`/`RemoveHuman`/`RemoveChannel`/`SetEscalationPolicy`) → the project's `projects` row (re-marshal the mutated Project and UPSERT). Reads are inherited from `memState` — do not reimplement them.

- [ ] **Step 4: Write the integration conformance test**

Create `internal/harbor/pgstore_integration_test.go`:

```go
//go:build integration

package harbor_test

import (
	"context"
	"os"
	"testing"

	"github.com/aethons-tools/cove/internal/harbor"
	"github.com/aethons-tools/cove/internal/harbor/storetest"
)

func TestPostgresStoreConformance(t *testing.T) {
	dsn := os.Getenv("HARBOR_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set HARBOR_TEST_POSTGRES_DSN to run the Postgres store integration tests")
	}
	storetest.RunConformance(t, func(t *testing.T) harbor.Store {
		// Each subtest needs a clean store: use a uniquely-named schema or TRUNCATE
		// all tables before constructing. Simplest: TRUNCATE then NewPostgresStore.
		s, err := harbor.NewPostgresStore(context.Background(), dsn, nil)
		if err != nil {
			t.Fatalf("NewPostgresStore: %v", err)
		}
		t.Cleanup(s.Close)
		if err := s.TruncateAllForTest(context.Background()); err != nil {
			t.Fatalf("truncate: %v", err)
		}
		return s
	})
}
```

Add a test-only `TruncateAllForTest(ctx)` method (guarded by the `integration` tag via a separate `//go:build integration` file `pgstore_testhelpers.go`, or an exported method acceptable for a store — prefer the tag-guarded file) that `TRUNCATE`s all six tables so each subtest starts empty.

Also add, in the same integration file, a **fail-closed startup** case asserting `NewPostgresStore` returns an error (not a usable store) for an unreachable/invalid DSN — the spec's fail-closed invariant:

```go
func TestPostgresStoreFailsClosedOnBadDSN(t *testing.T) {
	if os.Getenv("HARBOR_TEST_POSTGRES_DSN") == "" {
		t.Skip("integration only")
	}
	_, err := harbor.NewPostgresStore(context.Background(),
		"host=127.0.0.1 port=1 dbname=nope user=nope password=x sslmode=disable connect_timeout=1", nil)
	if err == nil {
		t.Fatal("NewPostgresStore must fail closed on an unreachable DSN")
	}
}
```

- [ ] **Step 5: Verify locally (compile + hermetic), note integration runs in CI**

Run:
```
go build ./... && go build -tags integration ./...
go vet ./internal/harbor/ && go vet -tags integration ./internal/harbor/
go test ./internal/harbor/    # hermetic: FileStore conformance + filestore_test still green
```
Expected: all compile and the hermetic suite passes. The Postgres integration test cannot run in the sandbox (no Postgres) — report it as **compile-verified locally, executed in CI (Task 5)**. If `HARBOR_TEST_POSTGRES_DSN` happens to be set/reachable, run `go test -tags integration -run TestPostgresStoreConformance ./internal/harbor/` and report the result.

- [ ] **Step 6: Commit**

```bash
git add internal/harbor/pgstore.go internal/harbor/migrations.go internal/harbor/migrations/ internal/harbor/pgstore_integration_test.go internal/harbor/pgstore_testhelpers.go go.mod go.sum
git commit -m "harbor: PostgresStore — write-through cache over document tables

$(printf 'Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>\nClaude-Session: https://claude.ai/code/session_01BtTj5yZ4qoJzyK8o6625mv')"
```

---

### Task 4: Config, secret resolution & backend selection

**Files:**
- Modify: `cmd/at-harbor/config.go`, `cmd/at-harbor/config_test.go`
- Modify: `cmd/at-harbor/main.go`

**Interfaces:**
- Consumes: `harbor.NewPostgresStore` (Task 3); `harbor.NewFileStore`; `secret.Resolve`; `serveConfig.credSpecs()`.
- Produces: backend selection in the serve composition root.

- [ ] **Step 1: Write failing config tests**

Add to `cmd/at-harbor/config_test.go`:

```go
func TestServeConfigStorePostgres(t *testing.T) {
	y := "store-postgres:\n  host: db\n  port: 5432\n  database: harbor\n  user: harbor\n  sslmode: verify-full\n  password-cred: harbor-db\n"
	var c serveConfig
	if err := yaml.Unmarshal([]byte(y), &c); err != nil {
		t.Fatal(err)
	}
	if c.StorePostgres == nil || c.StorePostgres.Host != "db" || c.StorePostgres.PasswordCred != "harbor-db" {
		t.Fatalf("StorePostgres = %+v", c.StorePostgres)
	}
}

func TestStorePostgresValidation(t *testing.T) {
	// password-cred must resolve to a configured credential.
	c := serveConfig{Credentials: map[string]credSpec{}}
	c.StorePostgres = &storePostgresConfig{Host: "db", Database: "h", User: "u", SSLMode: "require", PasswordCred: "missing"}
	if err := c.validateStorePostgres(); err == nil {
		t.Fatal("expected error when password-cred is not a configured credential")
	}
	c.Credentials["harbor-db"] = credSpec{Command: []string{"echo", "pw"}}
	c.StorePostgres.PasswordCred = "harbor-db"
	if err := c.validateStorePostgres(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
}
```

Extend the "all-known keys" line in `TestUnknownServeKeys` to include `store-postgres: {}`.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./cmd/at-harbor/ -run 'StorePostgres|UnknownServeKeys' 2>&1 | tail -15`
Expected: FAIL — `storePostgresConfig`/`StorePostgres`/`validateStorePostgres` undefined.

- [ ] **Step 3: Add the config type + validation**

In `cmd/at-harbor/config.go`, add the field to `serveConfig` (after `MessageLog`):

```go
	StorePostgres *storePostgresConfig `yaml:"store-postgres"`
```

and the type + validation:

```go
// storePostgresConfig selects the Postgres store backend. When present it takes
// precedence over the file `store`. The password is never inline: password-cred
// names an entry in `credentials`, resolved on the host in memory.
type storePostgresConfig struct {
	Host         string `yaml:"host"`
	Port         int    `yaml:"port"`
	Database     string `yaml:"database"`
	User         string `yaml:"user"`
	SSLMode      string `yaml:"sslmode"`
	PasswordCred string `yaml:"password-cred"`
}

func (c serveConfig) validateStorePostgres() error {
	p := c.StorePostgres
	if p == nil {
		return nil
	}
	for name, v := range map[string]string{"host": p.Host, "database": p.Database, "user": p.User, "sslmode": p.SSLMode, "password-cred": p.PasswordCred} {
		if v == "" {
			return fmt.Errorf("store-postgres.%s is required", name)
		}
	}
	if _, ok := c.Credentials[p.PasswordCred]; !ok {
		return fmt.Errorf("store-postgres.password-cred %q is not a configured credential", p.PasswordCred)
	}
	return nil
}
```

Call `validateStorePostgres()` wherever the serve config is validated at startup (find the existing validation call site next to `validateLauncher`/`validateDispatcher`/`validateAdminExposure` in `main.go` and add it alongside).

- [ ] **Step 4: Select the backend in serve (main.go)**

In `cmd/at-harbor/main.go`, replace the unconditional `st, err := harbor.NewFileStore(cfg.Store)` (line ~975) with backend selection. Resolve the password in memory and assemble the DSN; never log it:

```go
var st harbor.Store
if pc := cfg.StorePostgres; pc != nil {
	resolved, err := secret.Resolve(runner.OS{}, nil, []secret.Spec{specsForStore(cfg, pc.PasswordCred)})
	if err != nil {
		fmt.Fprintln(stderr, "at-harbor: store-postgres password:", err)
		return 1
	}
	dsn := fmt.Sprintf("host=%s port=%d dbname=%s user=%s password=%s sslmode=%s",
		pc.Host, pc.Port, pc.Database, pc.User, resolved[pc.PasswordCred], pc.SSLMode)
	ps, err := harbor.NewPostgresStore(context.Background(), dsn, log)
	if err != nil {
		fmt.Fprintln(stderr, "at-harbor: store-postgres:", err)
		return 1
	}
	defer ps.Close()
	st = ps
	log.Info("harbor store: postgres", "host", pc.Host, "database", pc.Database) // never the password
} else {
	fs, err := harbor.NewFileStore(cfg.Store)
	if err != nil {
		fmt.Fprintln(stderr, "at-harbor:", err)
		return 1
	}
	st = fs
	log.Info("harbor store: file", "path", cfg.Store)
}
```

`credSpecs()` is already computed as `specs` at line ~981 — reuse it: `specsForStore` is just `specs[name]` wrapped as `[]secret.Spec{specs[name]}`; inline it rather than adding a helper if simpler. Note: this refines the spec's wording ("a harbor.OpenStore constructor") — selection lives in the composition root because config + secret resolution live in `cmd`, not `internal/harbor`; `harbor` exposes `NewPostgresStore`/`NewFileStore` only. `st` remains typed `harbor.Store`, so all downstream wiring is unchanged.

- [ ] **Step 5: Verify**

Run: `go test ./cmd/at-harbor/ -run 'StorePostgres|UnknownServeKeys' -v && go build ./... && go vet ./cmd/at-harbor/`
Expected: config tests PASS; build + vet clean. (Selecting the Postgres backend at runtime is exercised by the integration job; the file path stays the default and all hermetic tests are unaffected.)

- [ ] **Step 6: Commit**

```bash
git add cmd/at-harbor/config.go cmd/at-harbor/config_test.go cmd/at-harbor/main.go
git commit -m "harbor: serve config + backend selection for the Postgres store

$(printf 'Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>\nClaude-Session: https://claude.ai/code/session_01BtTj5yZ4qoJzyK8o6625mv')"
```

---

### Task 5: CI Postgres integration job + docs

**Files:**
- Create: `.github/workflows/store-integration.yml`
- Modify: `docs/usage/harbor/serve.md`, `docs/DEVELOPMENT.md`

**Interfaces:**
- Consumes: the `//go:build integration` test from Task 3.
- Produces: an automated PR check that runs the Postgres conformance.

- [ ] **Step 1: Add the CI job**

Create `.github/workflows/store-integration.yml` — a **separate** workflow (do NOT edit `gate.yml`; its `gate` job name is the branch-protection check and must not change). Mirror `gate.yml`'s Go setup; add a Postgres service and run the integration tag:

```yaml
name: store-integration

on:
  pull_request:
    branches: [main]
  push:
    branches: [main]

permissions:
  contents: read

jobs:
  store-integration:
    runs-on: ubuntu-latest
    timeout-minutes: 10
    services:
      postgres:
        image: postgres:17
        env:
          POSTGRES_USER: harbor
          POSTGRES_PASSWORD: harbor
          POSTGRES_DB: harbor
        ports: ["5432:5432"]
        options: >-
          --health-cmd "pg_isready -U harbor" --health-interval 5s
          --health-timeout 5s --health-retries 10
    steps:
      - uses: actions/checkout@v7
      - uses: actions/setup-go@v7
        with:
          go-version-file: go.mod
          cache: true
      - name: store integration tests
        env:
          HARBOR_TEST_POSTGRES_DSN: "host=localhost port=5432 dbname=harbor user=harbor password=harbor sslmode=disable"
        run: go test -tags integration ./internal/harbor/...
```

(Note in the PR description that making this a *required* check is a branch-protection setting the maintainer flips — the workflow only reports.)

- [ ] **Step 2: Docs**

- `docs/usage/harbor/serve.md` — document the `store-postgres:` block (fields, `password-cred` via the credential resolver, precedence over `store:`, backend startup log line) and the boundary: **switching to Postgres starts with an empty control plane — there is no data migration in Phase 1**; deployments needing their roster preserved stay on the file backend until they re-enroll. Link the message-log Phase 2 as future work only if a natural spot exists (don't invent one).
- `docs/DEVELOPMENT.md` — the pgx `GOPROXY` allow-list note, and how to run the integration tests locally: `HARBOR_TEST_POSTGRES_DSN=... go test -tags integration ./internal/harbor/...`.
- Run the **docs-audit** skill/checker over `docs/`; fix anything flagged. No INDEX change expected.

- [ ] **Step 3: Verify**

Run: `go build ./... && just test 2>&1 | tail -5` (hermetic suite still green) and confirm the workflow YAML is valid (`yq . .github/workflows/store-integration.yml >/dev/null` or a syntax check).

- [ ] **Step 4: Commit**

```bash
git add .github/workflows/store-integration.yml docs/usage/harbor/serve.md docs/DEVELOPMENT.md
git commit -m "ci+docs: Postgres store integration job + serve/dev docs

$(printf 'Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>\nClaude-Session: https://claude.ai/code/session_01BtTj5yZ4qoJzyK8o6625mv')"
```

---

## Final verification

- [ ] `just test` — full hermetic suite green (FileStore conformance + all existing tests).
- [ ] `go build ./... && go build -tags integration ./...` — both compile.
- [ ] `just lint` (`STRICT=1 ./scripts/lint.sh`) — clean, incl. `gofmt` and `go vet`.
- [ ] Skim the diff: `harbor.Store` interface unchanged; no consumer uses `*FileStore`/`*PostgresStore` concretely; no secret/DSN/password in any log or on argv; `internal/msglog` untouched; commit-then-cache honored in every PostgresStore writer.
- [ ] Open a PR against `main` (branch `feat/harbor-postgres-store`) with the PR-description attribution block; note the `store-integration` check and that promoting it to a required gate is a branch-protection setting.
