// Package allocpg is the Postgres-backed allocation event store. It appends
// allocator.Event records to a per-(project, role) stream using an
// optimistic-concurrency (OCC) append (UNIQUE(stream_id, stream_revision) +
// expected-revision), satisfying allocator.Recorder. It shares the control-plane
// *pgxpool.Pool and keeps pgx out of the stdlib-only allocator core.
package allocpg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aethons-tools/cove/internal/allocator"
)

// migrateAdvisoryLock is distinct from the harbor (0x686172626f72) and intercom
// (0x696e746572636f6d) locks so the migrators sharing one database never block
// each other incorrectly.
const migrateAdvisoryLock = 0x616c6c6f63 // "alloc"

// maxAppendRetries bounds the OCC append's version-race retries. Under
// single-instance serial writes a conflict is pathological.
const maxAppendRetries = 5

// ErrConflictExhausted means the OCC append lost the version race maxAppendRetries
// times running — pathological under single-instance serial writes.
var ErrConflictExhausted = errors.New("allocpg: append conflict retries exhausted")

// Store is a Postgres-backed allocation event store over a shared pool.
type Store struct {
	pool *pgxpool.Pool
	log  *slog.Logger
}

var _ allocator.Recorder = (*Store)(nil)

// New applies the embedded migrations (idempotent, advisory-locked) and returns
// a ready store. It does not own the pool; Close is a no-op.
func New(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger) (*Store, error) {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	s := &Store{pool: pool, log: log}
	if err := s.migrate(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

// Close is a no-op: the pool is owned by the control-plane store.
func (s *Store) Close() error { return nil }

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

// Outstanding returns the live reservation count for a (project, role) stream —
// granted minus released. This is the ledger fold the cap will use once the
// cutover (Slice 4) makes the store authoritative.
func (s *Store) Outstanding(ctx context.Context, project, role string) (int, error) {
	streamID := project + "/" + role
	var n int
	if err := s.pool.QueryRow(ctx,
		`SELECT
		   COUNT(*) FILTER (WHERE kind = $2) - COUNT(*) FILTER (WHERE kind = $3)
		 FROM alloc_events WHERE stream_id = $1`,
		streamID, string(allocator.KindReservationGranted), string(allocator.KindReservationReleased),
	).Scan(&n); err != nil {
		return 0, fmt.Errorf("allocpg: outstanding: %w", err)
	}
	return n, nil
}

// isUniqueViolation reports whether err is a Postgres unique-constraint violation
// (SQLSTATE 23505) — the OCC append's expected-revision collision.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func (s *Store) migrate(ctx context.Context) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(migrateAdvisoryLock)); err != nil {
			return fmt.Errorf("allocpg: advisory lock: %w", err)
		}
		if _, err := tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS alloc_schema_migrations (version int PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
			return fmt.Errorf("allocpg: create alloc_schema_migrations: %w", err)
		}
		applied := map[int]bool{}
		rows, err := tx.Query(ctx, `SELECT version FROM alloc_schema_migrations`)
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
				return fmt.Errorf("allocpg: bad migration name %q", name)
			}
			ver, err := strconv.Atoi(name[:i])
			if err != nil {
				return fmt.Errorf("allocpg: bad migration version in %q: %w", name, err)
			}
			if applied[ver] {
				continue
			}
			sqlText, err := migrationFiles.ReadFile("migrations/" + name)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, string(sqlText)); err != nil {
				return fmt.Errorf("allocpg: apply %s: %w", name, err)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO alloc_schema_migrations (version) VALUES ($1)`, ver); err != nil {
				return err
			}
			s.log.Info("allocpg: applied migration", "version", ver, "file", name)
		}
		return nil
	})
}
