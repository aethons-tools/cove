# harbor Postgres-backed message log (Phase 2) — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give `internal/msglog` a Postgres backend behind a new `msglog.Store` interface, on a real indexed schema with no in-memory mirror, sharing the Phase-1 Postgres pool. Keep the file log as the hermetic/dev backend; read signatures unchanged.

**Architecture:** A `msglog.Store` interface (Append/ReadInbox/ReadThread/List/SeenIDs/Close). The file `*Log` satisfies it; a new `internal/msglog/msglogpg` subpackage adds a pgx-backed impl that queries Postgres directly (no mirror). `msgport.Engine` depends on the interface. The log backend follows the store backend: `store-postgres` set ⇒ Postgres log on the shared pool, else the file `message-log`.

**Tech Stack:** Go, `github.com/jackc/pgx/v5` (`pgxpool`) confined to `msglogpg`, embedded SQL migrations, the Phase-1 `store-integration` CI Postgres service.

**Spec:** `docs/superpowers/specs/2026-09-14-harbor-msglog-postgres.md`

## Global Constraints

- **`internal/msglog` core stays stdlib-only.** pgx lives **only** in `internal/msglog/msglogpg`. Dependency direction: `msglogpg → msglog`, never the reverse.
- **Approach A — read signatures unchanged.** `ReadInbox`/`ReadThread`/`List` still return `[]Message`; no ctx/pagination. Pagination and retention are Phase 3 — do not add them.
- **No in-memory mirror in the Postgres backend** — reads are indexed queries.
- **Append is one transaction** (the `messages` row + its `message_recipients` rows). id/at assignment + validation go through the shared `msglog.Prepare` so both backends behave identically.
- **Shared pool:** `msglogpg` takes an already-open `*pgxpool.Pool` (the Phase-1 control-plane pool) and **never closes it** (its `Close` is a no-op); the control-plane store owns the pool's lifecycle.
- **Single-writer preserved** (the serve process is the sole appender).
- **Tests hermetic by default;** the Postgres log backend's tests live behind `//go:build integration` against `HARBOR_TEST_POSTGRES_DSN`, skipping when unset. Keep `msgport`/`wakeon`/adminui tests on the file log.
- **Before every commit:** `go build ./...`, `gofmt -l` (must be empty), `go vet`. (CI's `gate` runs `gofmt` — a stray unaligned struct fails it.)
- **Commit trailer — verbatim on every commit** (do not substitute your own model name):

  ```
  Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_01BtTj5yZ4qoJzyK8o6625mv
  ```

- Branch is `feat/harbor-msglog-postgres` (the spec is committed there).

## File Structure

- `internal/msglog/store.go` — **new**: the `Store` interface + the shared `Prepare` helper (Task 1).
- `internal/msglog/log.go` — **modify**: add `SeenIDs`; route `Append` through `Prepare`; assert `*Log` satisfies `Store` (Task 1).
- `internal/msglog/msglogtest/conformance.go` — **new**: the backend-agnostic suite (Task 1).
- `internal/msglog/store_conformance_test.go` — **new**: runs it against the file `Log` (Task 1).
- `internal/msgport/engine.go` — **modify**: `msglog.Store` param + `SeenIDs` (Task 2).
- `internal/msglog/msglogpg/msglogpg.go` — **new**: the Postgres backend (Task 3).
- `internal/msglog/msglogpg/migrations/0001_messages.sql` (+ `migrations.go`) — **new** (Task 3).
- `internal/msglog/msglogpg/msglogpg_integration_test.go` — **new**, `//go:build integration` (Task 3).
- `internal/harbor/pgstore.go` — **modify**: add `Pool()` accessor (Task 4).
- `cmd/at-harbor/main.go` — **modify**: backend selection for the log (Task 4).
- `.github/workflows/store-integration.yml`, `docs/usage/harbor/serve.md`, `docs/usage/harbor/ui.md`, `docs/DEVELOPMENT.md` — **modify** (Task 5).

---

### Task 1: `msglog.Store` interface, `SeenIDs`, shared `Prepare`, conformance suite

**Files:**
- Create: `internal/msglog/store.go`, `internal/msglog/msglogtest/conformance.go`, `internal/msglog/store_conformance_test.go`
- Modify: `internal/msglog/log.go`

**Interfaces:**
- Consumes: existing `Message`, `Target`, `Filter`, the unexported `newID`/`validate`.
- Produces: `msglog.Store` interface; `func msglog.Prepare(Message) (Message, error)`; `func (*Log) SeenIDs(prefix string) []string`; `func msglogtest.RunConformance(t, func(t) msglog.Store)`.

- [ ] **Step 1: Write the conformance suite (fails to compile — Store undefined)**

Create `internal/msglog/msglogtest/conformance.go`:

```go
// Package msglogtest is a backend-agnostic conformance suite for msglog.Store,
// run against both the file Log (hermetic) and the Postgres backend (integration).
package msglogtest

import (
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/msglog"
)

func RunConformance(t *testing.T, newStore func(t *testing.T) msglog.Store) {
	actor := func(r string) msglog.Target { return msglog.Target{Kind: "actor", Ref: r} }
	human := func(r string) msglog.Target { return msglog.Target{Kind: "human", Ref: r} }

	t.Run("append_assigns_id_and_at", func(t *testing.T) {
		s := newStore(t)
		got, err := s.Append(msglog.Message{From: actor("c1"), To: []msglog.Target{human("a")}, Body: "hi"})
		if err != nil {
			t.Fatalf("Append: %v", err)
		}
		if got.ID == "" || got.At.IsZero() {
			t.Fatalf("Append must assign ID and At: %+v", got)
		}
	})

	t.Run("append_validates", func(t *testing.T) {
		s := newStore(t)
		if _, err := s.Append(msglog.Message{From: actor("c1"), To: []msglog.Target{human("a")}}); err == nil {
			t.Fatal("empty body must error")
		}
		if _, err := s.Append(msglog.Message{From: actor("c1"), Body: "x"}); err == nil {
			t.Fatal("empty To must error")
		}
	})

	t.Run("read_inbox_multi_recipient", func(t *testing.T) {
		s := newStore(t)
		if _, err := s.Append(msglog.Message{From: actor("c1"), To: []msglog.Target{actor("a"), human("b")}, Body: "m1"}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Append(msglog.Message{From: actor("c1"), To: []msglog.Target{actor("c")}, Body: "m2"}); err != nil {
			t.Fatal(err)
		}
		if got := s.ReadInbox(actor("a")); len(got) != 1 || got[0].Body != "m1" {
			t.Fatalf("ReadInbox(actor:a) = %+v, want [m1]", got)
		}
		if got := s.ReadInbox(human("b")); len(got) != 1 || got[0].Body != "m1" {
			t.Fatalf("ReadInbox(human:b) = %+v, want [m1]", got)
		}
		if got := s.ReadInbox(actor("z")); len(got) != 0 {
			t.Fatalf("ReadInbox(actor:z) = %+v, want none", got)
		}
		// The returned message reconstructs its full To set.
		if got := s.ReadInbox(actor("a")); len(got[0].To) != 2 {
			t.Fatalf("reconstructed To = %+v, want 2 targets", got[0].To)
		}
	})

	t.Run("read_thread_root_and_direct_replies", func(t *testing.T) {
		s := newStore(t)
		root, _ := s.Append(msglog.Message{From: actor("c1"), To: []msglog.Target{human("a")}, Body: "root"})
		if _, err := s.Append(msglog.Message{From: human("a"), To: []msglog.Target{actor("c1")}, Body: "reply", ReplyTo: root.ID}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Append(msglog.Message{From: actor("c1"), To: []msglog.Target{human("a")}, Body: "unrelated"}); err != nil {
			t.Fatal(err)
		}
		got := s.ReadThread(root.ID)
		if len(got) != 2 || got[0].Body != "root" || got[1].Body != "reply" {
			t.Fatalf("ReadThread = %+v, want [root, reply] in order", got)
		}
	})

	t.Run("list_filter_project_and_time", func(t *testing.T) {
		s := newStore(t)
		t0 := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
		if _, err := s.Append(msglog.Message{From: actor("c1"), To: []msglog.Target{human("a")}, Body: "acme1", Project: "acme", At: t0}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Append(msglog.Message{From: actor("c1"), To: []msglog.Target{human("a")}, Body: "beta1", Project: "beta", At: t0.Add(48 * time.Hour)}); err != nil {
			t.Fatal(err)
		}
		if got := s.List(msglog.Filter{Project: "acme"}); len(got) != 1 || got[0].Body != "acme1" {
			t.Fatalf("List(project=acme) = %+v", got)
		}
		if got := s.List(msglog.Filter{}); len(got) != 2 {
			t.Fatalf("List(all) = %d, want 2", len(got))
		}
		win := s.List(msglog.Filter{Since: t0.Add(24 * time.Hour), Until: t0.Add(72 * time.Hour)})
		if len(win) != 1 || win[0].Body != "beta1" {
			t.Fatalf("List(time window) = %+v, want [beta1]", win)
		}
	})

	t.Run("list_and_reads_are_append_order", func(t *testing.T) {
		s := newStore(t)
		for _, b := range []string{"a", "b", "c"} {
			if _, err := s.Append(msglog.Message{From: actor("c1"), To: []msglog.Target{human("h")}, Body: b}); err != nil {
				t.Fatal(err)
			}
		}
		got := s.List(msglog.Filter{})
		if len(got) != 3 || got[0].Body != "a" || got[2].Body != "c" {
			t.Fatalf("List order = %+v, want a,b,c", got)
		}
	})

	t.Run("seen_ids_by_prefix", func(t *testing.T) {
		s := newStore(t)
		for _, id := range []string{"in:linear:c1", "in:linear:c2", "in:discord:c3"} {
			if _, err := s.Append(msglog.Message{ID: id, From: human("a"), To: []msglog.Target{actor("x")}, Body: "b"}); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := s.Append(msglog.Message{From: actor("x"), To: []msglog.Target{human("a")}, Body: "egress"}); err != nil {
			t.Fatal(err)
		}
		got := s.SeenIDs("in:linear:")
		set := map[string]bool{}
		for _, id := range got {
			set[id] = true
		}
		if len(got) != 2 || !set["in:linear:c1"] || !set["in:linear:c2"] {
			t.Fatalf("SeenIDs(in:linear:) = %+v, want the two linear ids", got)
		}
	})
}
```

- [ ] **Step 2: Add the interface + `Prepare`**

Create `internal/msglog/store.go`:

```go
package msglog

// Store is the message-log capability the consumers depend on. The file *Log
// and the Postgres backend (internal/msglog/msglogpg) both satisfy it.
type Store interface {
	Append(m Message) (Message, error)
	ReadInbox(t Target) []Message
	ReadThread(rootID string) []Message
	List(f Filter) []Message
	// SeenIDs returns the ids of messages whose id starts with prefix, in append
	// order — the bounded query the msgport engine uses to rebuild its ingress
	// dedupe set at startup without materializing the whole log.
	SeenIDs(prefix string) []string
	Close() error
}

// Prepare validates m and assigns an ID and At when unset, returning the message
// ready to persist. Both backends call it so id/at assignment and validation
// live in one place.
func Prepare(m Message) (Message, error) {
	if err := m.validate(); err != nil {
		return Message{}, err
	}
	if m.At.IsZero() {
		m.At = time.Now()
	}
	if m.ID == "" {
		m.ID = newID(m.At)
	}
	return m, nil
}
```

(`store.go` imports `"time"`.)

- [ ] **Step 3: Route `Log.Append` through `Prepare`, add `SeenIDs`, assert satisfaction**

In `internal/msglog/log.go`, change `Append` to use `Prepare` (behavior-preserving):

```go
func (l *Log) Append(m Message) (Message, error) {
	m, err := Prepare(m)
	if err != nil {
		return Message{}, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	line, err := json.Marshal(m)
	if err != nil {
		return Message{}, fmt.Errorf("msglog: marshal: %w", err)
	}
	if _, err := l.f.Write(append(line, '\n')); err != nil {
		return Message{}, fmt.Errorf("msglog: write: %w", err)
	}
	l.msgs = append(l.msgs, m)
	return m, nil
}
```

Add `SeenIDs` (scan the mirror) and a compile-time assertion (put the assertion in `store.go` or `log.go`):

```go
// SeenIDs returns ids with the given prefix, in append order.
func (l *Log) SeenIDs(prefix string) []string {
	var out []string
	for _, m := range l.snapshot() {
		if strings.HasPrefix(m.ID, prefix) {
			out = append(out, m.ID)
		}
	}
	return out
}

var _ Store = (*Log)(nil)
```

Add `"strings"` to `log.go` imports if missing.

- [ ] **Step 4: Runner test against the file log**

Create `internal/msglog/store_conformance_test.go`:

```go
package msglog_test

import (
	"path/filepath"
	"testing"

	"github.com/aethons-tools/cove/internal/msglog"
	"github.com/aethons-tools/cove/internal/msglog/msglogtest"
)

func TestFileLogConformance(t *testing.T) {
	msglogtest.RunConformance(t, func(t *testing.T) msglog.Store {
		lg, err := msglog.Open(filepath.Join(t.TempDir(), "m.jsonl"), nil)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		t.Cleanup(func() { lg.Close() })
		return lg
	})
}
```

- [ ] **Step 5: Run — expect GREEN against the file log**

Run: `go test ./internal/msglog/... 2>&1 | tail -20`
Expected: PASS (the new conformance suite + the existing `log_test`/`read_test`/`message_test`, unchanged). Then `go build ./... && gofmt -l internal/msglog && go vet ./internal/msglog/...`.

- [ ] **Step 6: Commit**

```bash
git add internal/msglog/store.go internal/msglog/log.go internal/msglog/msglogtest/ internal/msglog/store_conformance_test.go
git commit -m "msglog: Store interface, SeenIDs, shared Prepare, conformance suite

$(printf 'Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>\nClaude-Session: https://claude.ai/code/session_01BtTj5yZ4qoJzyK8o6625mv')"
```

---

### Task 2: Point `msgport.Engine` at `msglog.Store` + use `SeenIDs`

**Files:**
- Modify: `internal/msgport/engine.go`

**Interfaces:**
- Consumes: `msglog.Store` + `SeenIDs` (Task 1).
- Produces: `msgport.New(surf Surface, lg msglog.Store, …)` — the `lg` param/field type changes from `*msglog.Log` to `msglog.Store`.

- [ ] **Step 1: Change the field and constructor param type**

In `internal/msgport/engine.go`: change the struct field `lg *msglog.Log` → `lg msglog.Store`, and the `New(..., lg *msglog.Log, ...)` parameter → `lg msglog.Store`.

- [ ] **Step 2: Replace the startup full-log scan with `SeenIDs`**

Replace (engine.go ~line 51-56):

```go
	prefix := "in:" + surf.Service() + ":"
	for _, m := range lg.List(msglog.Filter{}) {
		if strings.HasPrefix(m.ID, prefix) {
			e.seen[m.ID] = true
		}
	}
```

with:

```go
	prefix := "in:" + surf.Service() + ":"
	for _, id := range lg.SeenIDs(prefix) {
		e.seen[id] = true
	}
```

Remove the now-unused `strings` / `msglog` imports **only if** nothing else in the file uses them (the engine still uses `msglog.Message`/`Target` elsewhere, so `msglog` stays; check `strings`).

- [ ] **Step 3: Run the engine tests (they pass a real *msglog.Log, which is a msglog.Store)**

Run: `go test ./internal/msgport/... 2>&1 | tail -20`
Expected: PASS unchanged — `openLog(t)` returns `*msglog.Log`, which satisfies `msglog.Store`; the ingress-dedupe test still sees the two `in:linear:` ids via `SeenIDs`. Then `go build ./... && gofmt -l internal/msgport && go vet ./internal/msgport/...`.

- [ ] **Step 4: Commit**

```bash
git add internal/msgport/engine.go
git commit -m "msgport: engine depends on msglog.Store; SeenIDs replaces the startup scan

$(printf 'Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>\nClaude-Session: https://claude.ai/code/session_01BtTj5yZ4qoJzyK8o6625mv')"
```

---

### Task 3: `msglogpg` Postgres backend + migrations + integration conformance

**Files:**
- Create: `internal/msglog/msglogpg/msglogpg.go`, `internal/msglog/msglogpg/migrations.go`, `internal/msglog/msglogpg/migrations/0001_messages.sql`, `internal/msglog/msglogpg/msglogpg_integration_test.go`

**Interfaces:**
- Consumes: `msglog.Store`, `msglog.Message/Target/Filter`, `msglog.Prepare`; a `*pgxpool.Pool`.
- Produces: `func msglogpg.New(ctx, pool *pgxpool.Pool, log *slog.Logger) (*Store, error)` — `*msglogpg.Store` implements `msglog.Store`.

- [ ] **Step 1: Schema + embed**

Create `internal/msglog/msglogpg/migrations/0001_messages.sql`:

```sql
CREATE TABLE IF NOT EXISTS messages (
    id        text PRIMARY KEY,
    from_kind text NOT NULL,
    from_ref  text NOT NULL,
    body      text NOT NULL,
    at        timestamptz NOT NULL,
    project   text NOT NULL DEFAULT '',
    reply_to  text NOT NULL DEFAULT '',
    "to"      jsonb NOT NULL
);
CREATE TABLE IF NOT EXISTS message_recipients (
    message_id text NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    kind       text NOT NULL,
    ref        text NOT NULL,
    PRIMARY KEY (message_id, kind, ref)
);
CREATE INDEX IF NOT EXISTS idx_recipients_target ON message_recipients (kind, ref, message_id);
CREATE INDEX IF NOT EXISTS idx_messages_reply_to ON messages (reply_to) WHERE reply_to <> '';
CREATE INDEX IF NOT EXISTS idx_messages_project_id ON messages (project, id);
```

Create `internal/msglog/msglogpg/migrations.go`:

```go
package msglogpg

import "embed"

//go:embed migrations/*.sql
var migrationFiles embed.FS
```

- [ ] **Step 2: Implement the backend**

Create `internal/msglog/msglogpg/msglogpg.go`:

```go
// Package msglogpg is the Postgres backend for msglog.Store. It queries Postgres
// directly (no in-memory mirror) and shares a control-plane *pgxpool.Pool. It
// keeps pgx out of the stdlib-only msglog core.
package msglogpg

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aethons-tools/cove/internal/msglog"
)

// migrateAdvisoryLock is distinct from the control-plane store's lock so the two
// migrators sharing one database never block each other incorrectly.
const migrateAdvisoryLock = 0x6d73676c6f67 // "msglog"

// Store is a Postgres-backed msglog.Store over a shared pool.
type Store struct {
	pool *pgxpool.Pool
	log  *slog.Logger
}

var _ msglog.Store = (*Store)(nil)

// New applies the embedded migrations (idempotent, advisory-locked) and returns
// a ready store. It does not own the pool; Close is a no-op.
func New(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger) (*Store, error) {
	if log == nil {
		log = slog.New(slog.NewTextHandler(discard{}, nil))
	}
	s := &Store{pool: pool, log: log}
	if err := s.migrate(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

// Close is a no-op: the pool is owned by the control-plane store.
func (s *Store) Close() error { return nil }

func (s *Store) Append(m msglog.Message) (msglog.Message, error) {
	m, err := msglog.Prepare(m)
	if err != nil {
		return msglog.Message{}, err
	}
	toJSON, err := json.Marshal(m.To)
	if err != nil {
		return msglog.Message{}, err
	}
	err = pgx.BeginFunc(context.Background(), s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(context.Background(),
			`INSERT INTO messages (id, from_kind, from_ref, body, at, project, reply_to, "to")
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
			m.ID, m.From.Kind, m.From.Ref, m.Body, m.At, m.Project, m.ReplyTo, toJSON); err != nil {
			return err
		}
		for _, t := range m.To {
			if _, err := tx.Exec(context.Background(),
				`INSERT INTO message_recipients (message_id, kind, ref) VALUES ($1,$2,$3)`,
				m.ID, t.Kind, t.Ref); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return msglog.Message{}, fmt.Errorf("msglogpg: append: %w", err)
	}
	return m, nil
}

func (s *Store) ReadInbox(t msglog.Target) []msglog.Message {
	return s.query(
		`SELECT m.id, m.from_kind, m.from_ref, m.body, m.at, m.project, m.reply_to, m."to"
		 FROM messages m JOIN message_recipients r ON r.message_id = m.id
		 WHERE r.kind = $1 AND r.ref = $2 ORDER BY m.id`, t.Kind, t.Ref)
}

func (s *Store) ReadThread(rootID string) []msglog.Message {
	return s.query(
		`SELECT id, from_kind, from_ref, body, at, project, reply_to, "to"
		 FROM messages WHERE id = $1 OR reply_to = $1 ORDER BY id`, rootID)
}

func (s *Store) List(f msglog.Filter) []msglog.Message {
	// Zero Since/Until are unbounded; pass them as conditional predicates.
	return s.query(
		`SELECT id, from_kind, from_ref, body, at, project, reply_to, "to"
		 FROM messages
		 WHERE ($1 = '' OR project = $1)
		   AND ($2::timestamptz IS NULL OR at >= $2)
		   AND ($3::timestamptz IS NULL OR at < $3)
		 ORDER BY id`,
		f.Project, nullTime(f.Since), nullTime(f.Until))
}

func (s *Store) SeenIDs(prefix string) []string {
	rows, err := s.pool.Query(context.Background(),
		`SELECT id FROM messages WHERE id LIKE $1 ORDER BY id`, likePrefix(prefix))
	if err != nil {
		s.log.Error("msglogpg: SeenIDs query", "error", err.Error())
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			s.log.Error("msglogpg: SeenIDs scan", "error", err.Error())
			return out
		}
		out = append(out, id)
	}
	return out
}

// query runs a message SELECT (columns in the fixed order below) and
// reconstructs each Message. Read errors are logged and yield the rows gathered
// so far (reads are non-fatal, matching the file backend's in-memory scan which
// cannot error).
func (s *Store) query(sql string, args ...any) []msglog.Message {
	rows, err := s.pool.Query(context.Background(), sql, args...)
	if err != nil {
		s.log.Error("msglogpg: query", "error", err.Error())
		return nil
	}
	defer rows.Close()
	var out []msglog.Message
	for rows.Next() {
		var m msglog.Message
		var toJSON []byte
		if err := rows.Scan(&m.ID, &m.From.Kind, &m.From.Ref, &m.Body, &m.At, &m.Project, &m.ReplyTo, &toJSON); err != nil {
			s.log.Error("msglogpg: scan", "error", err.Error())
			return out
		}
		if err := json.Unmarshal(toJSON, &m.To); err != nil {
			s.log.Error("msglogpg: decode to", "error", err.Error())
			return out
		}
		out = append(out, m)
	}
	return out
}

func (s *Store) migrate(ctx context.Context) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(migrateAdvisoryLock)); err != nil {
			return fmt.Errorf("msglogpg: advisory lock: %w", err)
		}
		if _, err := tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS msglog_schema_migrations (version int PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
			return fmt.Errorf("msglogpg: create msglog_schema_migrations: %w", err)
		}
		applied := map[int]bool{}
		rows, err := tx.Query(ctx, `SELECT version FROM msglog_schema_migrations`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var v int
			if err := rows.Scan(&v); err != nil {
				rows.Close()
				return err
			}
			applied[v] = true
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		entries, err := fs.ReadDir(migrationFiles, "migrations")
		if err != nil {
			return err
		}
		var names []string
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
				names = append(names, e.Name())
			}
		}
		sort.Strings(names)
		for _, name := range names {
			i := strings.IndexByte(name, '_')
			if i <= 0 {
				return fmt.Errorf("msglogpg: bad migration name %q", name)
			}
			ver, err := strconv.Atoi(name[:i])
			if err != nil {
				return fmt.Errorf("msglogpg: bad migration version in %q: %w", name, err)
			}
			if applied[ver] {
				continue
			}
			sqlText, err := migrationFiles.ReadFile("migrations/" + name)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, string(sqlText)); err != nil {
				return fmt.Errorf("msglogpg: apply %s: %w", name, err)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO msglog_schema_migrations (version) VALUES ($1)`, ver); err != nil {
				return err
			}
			s.log.Info("msglogpg: applied migration", "version", ver, "file", name)
		}
		return nil
	})
}

// likePrefix escapes LIKE metacharacters in prefix and appends '%'.
func likePrefix(prefix string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(prefix) + "%"
}

// nullTime maps a zero time.Time to a nil arg so the "IS NULL" predicate treats
// it as unbounded.
func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }
```

(Add `"time"` to the `msglogpg.go` imports for `nullTime`. `SeenIDs`' `LIKE $1` needs no explicit `ESCAPE` — backslash is the default LIKE escape, and `likePrefix` escapes `\`, `%`, `_`.)

- [ ] **Step 3: Integration conformance test**

Create `internal/msglog/msglogpg/msglogpg_integration_test.go`:

```go
//go:build integration

package msglogpg_test

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aethons-tools/cove/internal/msglog"
	"github.com/aethons-tools/cove/internal/msglog/msglogpg"
	"github.com/aethons-tools/cove/internal/msglog/msglogtest"
)

func TestPostgresLogConformance(t *testing.T) {
	dsn := os.Getenv("HARBOR_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set HARBOR_TEST_POSTGRES_DSN to run the Postgres message-log integration tests")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	msglogtest.RunConformance(t, func(t *testing.T) msglog.Store {
		s, err := msglogpg.New(context.Background(), pool, nil)
		if err != nil {
			t.Fatalf("msglogpg.New: %v", err)
		}
		if _, err := pool.Exec(context.Background(), `TRUNCATE messages, message_recipients`); err != nil {
			t.Fatalf("truncate: %v", err)
		}
		return s
	})
}
```

- [ ] **Step 4: Verify (compile locally; runs in CI)**

Run:
```
go get github.com/jackc/pgx/v5  # already in go.mod from Phase 1; no-op / ensures require
go build ./... && go build -tags integration ./...
gofmt -l internal/msglog/msglogpg && go vet ./internal/msglog/... && go vet -tags integration ./internal/msglog/...
go test ./internal/msglog/...   # hermetic file-log conformance still green
```
Expected: all compile; hermetic suite passes. The Postgres integration test can't run in the sandbox (no Postgres) — report it as compile-verified locally, executed in CI (Task 5). If `HARBOR_TEST_POSTGRES_DSN` is reachable, run `go test -tags integration ./internal/msglog/msglogpg/ -run TestPostgresLogConformance` and report.

- [ ] **Step 5: Commit**

```bash
git add internal/msglog/msglogpg/ go.mod go.sum
git commit -m "msglogpg: Postgres backend for msglog.Store (indexed, no mirror)

$(printf 'Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>\nClaude-Session: https://claude.ai/code/session_01BtTj5yZ4qoJzyK8o6625mv')"
```

---

### Task 4: Expose the control-plane pool + wire log-backend selection

**Files:**
- Modify: `internal/harbor/pgstore.go` (add `Pool()`)
- Modify: `cmd/at-harbor/main.go`

**Interfaces:**
- Consumes: `harbor.PostgresStore.Pool()`; `msglogpg.New`; `msglog.Open`; `msglog.Store`.
- Produces: a `msglog.Store` selected by backend, wired where `*msglog.Log` flowed.

- [ ] **Step 1: Expose the pool (Phase-1 touch)**

In `internal/harbor/pgstore.go`, add:

```go
// Pool returns the underlying connection pool so colocated subsystems (e.g. the
// Postgres message log) can share this store's database. The pool's lifecycle is
// owned by the store; callers must not close it.
func (s *PostgresStore) Pool() *pgxpool.Pool { return s.pool }
```

- [ ] **Step 2: Capture the pool in the store-selection branch**

In `cmd/at-harbor/main.go`, in the Phase-1 store-selection block, remember the pool when Postgres is chosen. Where it builds `ps, err := harbor.NewPostgresStore(...)`, after `st = ps` add a package-scoped-to-func variable:

```go
	var pgPool *pgxpool.Pool   // non-nil ⇒ Postgres backend; shared with the message log
	...
	if pc := cfg.StorePostgres; pc != nil {
		...
		st = ps
		pgPool = ps.Pool()
		...
	}
```

Add the import `"github.com/jackc/pgx/v5/pgxpool"` to `cmd/at-harbor/main.go`.

- [ ] **Step 3: Select the log backend**

Replace the existing message-log block:

```go
	var messageLog *msglog.Log
	if cfg.MessageLog != "" {
		ml, err := msglog.Open(cfg.MessageLog, log)
		if err != nil {
			fmt.Fprintln(stderr, "at-harbor: message-log:", err)
			return 1
		}
		defer ml.Close()
		messageLog = ml
		log.Info("harbor message log", "path", cfg.MessageLog)
	}
```

with backend selection (note the variable is now the interface type):

```go
	// Message-log backend follows the store backend: Postgres (shared pool) when
	// store-postgres is set, else the file log at message-log. nil ⇒ disabled.
	var messageLog msglog.Store
	switch {
	case pgPool != nil:
		ml, err := msglogpg.New(context.Background(), pgPool, log)
		if err != nil {
			fmt.Fprintln(stderr, "at-harbor: message-log (postgres):", err)
			return 1
		}
		messageLog = ml // Close is a no-op; the store owns the pool
		log.Info("harbor message log: postgres (shared control-plane database)")
	case cfg.MessageLog != "":
		ml, err := msglog.Open(cfg.MessageLog, log)
		if err != nil {
			fmt.Fprintln(stderr, "at-harbor: message-log:", err)
			return 1
		}
		defer ml.Close()
		messageLog = ml
		log.Info("harbor message log: file", "path", cfg.MessageLog)
	}
```

Add the import `"github.com/aethons-tools/cove/internal/msglog/msglogpg"`. The downstream wiring (`msgH`, `inbox`) already guards on `messageLog != nil`; with `messageLog` now an interface only ever assigned a genuinely non-nil value, those guards remain correct (the typed-nil trap the old comments describe no longer applies, since we never box a typed-nil concrete — leave the guards as-is; they are still correct and cheap). `harbor.NewMessagesHandler` and `wakeon.Inbox` accept the `*msglog.Log` today via interfaces they define; confirm they accept `msglog.Store` values — they take their own narrow interfaces (`Appender`, `Inbox`) that `msglog.Store` satisfies structurally, so passing `messageLog` works. If a signature is concretely `*msglog.Log`, widen it to the narrow interface it needs (it should already be an interface).

- [ ] **Step 4: Verify**

Run: `go build ./... && gofmt -l cmd/at-harbor internal/harbor && go vet ./cmd/at-harbor/ ./internal/harbor/ && just test 2>&1 | grep -E "FAIL" || echo ok`
Expected: builds; gofmt clean; vet clean; full hermetic suite green (file-log path unaffected).

- [ ] **Step 5: Commit**

```bash
git add internal/harbor/pgstore.go cmd/at-harbor/main.go
git commit -m "harbor: share the control-plane pool; select the message-log backend

$(printf 'Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>\nClaude-Session: https://claude.ai/code/session_01BtTj5yZ4qoJzyK8o6625mv')"
```

---

### Task 5: CI + docs

**Files:**
- Modify: `.github/workflows/store-integration.yml`, `docs/usage/harbor/serve.md`, `docs/usage/harbor/ui.md`, `docs/DEVELOPMENT.md`

**Interfaces:** Consumes the integration tests from Task 3.

- [ ] **Step 1: Extend the CI job to cover msglog**

In `.github/workflows/store-integration.yml`, change the test step to include the msglog packages:

```yaml
      - name: store integration tests
        env:
          HARBOR_TEST_POSTGRES_DSN: "host=localhost port=5432 dbname=harbor user=harbor password=harbor sslmode=disable"
        run: go test -tags integration ./internal/harbor/... ./internal/msglog/...
```

- [ ] **Step 2: Docs**

- `docs/usage/harbor/serve.md` — in the `store-postgres` section, add that the **message log follows the store backend**: with `store-postgres` set, the log is Postgres-backed on the same database (its tables auto-created; the `message-log:` path is ignored); otherwise the file `message-log` path is used. Note "starts empty, no data migration" and that the Postgres log is effectively always-on under `store-postgres`.
- `docs/usage/harbor/ui.md#messages` — one line: when `store-postgres` is set, the Messages view is served from Postgres (behavior unchanged; still a full snapshot — pagination is a later phase).
- `docs/DEVELOPMENT.md` — update the `store-integration` job description to say it now also runs `./internal/msglog/...`, and the local integration command to `go test -tags integration ./internal/harbor/... ./internal/msglog/...`.
- Run the **docs-audit** checker: `python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs --index OVERVIEW.md`; ensure your edits add **no net-new** errors/warnings (diff against a clean baseline as in Phase 1). Fix any your changes introduce (e.g. use anchors that survive `slugify`: lowercase, punctuation-stripped, spaces→single hyphen — avoid em-dashes in headings you link to).

- [ ] **Step 3: Verify**

Run: `go build ./... && just test 2>&1 | tail -3` (hermetic green), and confirm the workflow YAML has no tabs and parses.

- [ ] **Step 4: Commit**

```bash
git add .github/workflows/store-integration.yml docs/usage/harbor/serve.md docs/usage/harbor/ui.md docs/DEVELOPMENT.md
git commit -m "ci+docs: cover msglog Postgres backend; document the log backend selection

$(printf 'Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>\nClaude-Session: https://claude.ai/code/session_01BtTj5yZ4qoJzyK8o6625mv')"
```

---

## Final verification

- [ ] `just test` — full hermetic suite green (file-log conformance + all existing).
- [ ] `go build ./... && go build -tags integration ./...` — both compile.
- [ ] `gofmt -l` over changed files empty; `go vet ./...` clean.
- [ ] Skim the diff: `internal/msglog` core imports only stdlib (pgx only in `msglogpg`); no read-signature changes; `msglogpg` never calls `pool.Close`; append is one txn; the engine no longer scans the whole log.
- [ ] Open a PR against `main` (branch `feat/harbor-msglog-postgres`) with the attribution block; note the `store-integration` job now covers the Postgres message log (the first real execution of the msglogpg SQL).
