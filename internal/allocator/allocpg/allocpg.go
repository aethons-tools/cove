// Package allocpg is the Postgres-backed allocation event store. It appends
// allocator.Event records to a per-(project, role) stream using an
// optimistic-concurrency (OCC) append (UNIQUE(stream_id, stream_revision) +
// expected-revision), satisfying allocator.Ledger. It shares the control-plane
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
	"time"

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

var _ allocator.Ledger = (*Store)(nil)

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
//
// A release takes its session kind, name, and owner from the reservation's latest
// grant in the same stream (falling back to the event's own fields, then
// ephemeral, when there is none), so per-kind counts net correctly and release
// callers — supervisor teardown, the sweep, dispatcher compensation — need not
// know the kind. Any other event records its own SessionKind (empty ⇒ ephemeral).
func (s *Store) Record(ctx context.Context, ev allocator.Event) error {
	streamID := ev.Project + "/" + ev.Role
	data, err := json.Marshal(map[string]string{}) // slice 2: no extra payload yet
	if err != nil {
		return fmt.Errorf("allocpg: marshal: %w", err)
	}
	insert := recordInsert
	args := []any{ev.Category, streamID, 0, string(ev.Kind), ev.ReservationID, data,
		string(sessionKindOrDefault(ev.SessionKind)), ev.Name, ev.Owner}
	if ev.Kind == allocator.KindReservationReleased {
		insert = recordReleaseInsert
		args = append(args, string(allocator.KindReservationGranted))
	}
	for attempt := 0; attempt < maxAppendRetries; attempt++ {
		var head int64
		if err := s.pool.QueryRow(ctx,
			`SELECT COALESCE(MAX(stream_revision), 0) FROM alloc_events WHERE stream_id = $1`,
			streamID).Scan(&head); err != nil {
			return fmt.Errorf("allocpg: head: %w", err)
		}
		args[2] = head + 1
		_, err := s.pool.Exec(ctx, insert, args...)
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

// recordInsert appends a non-release event with its own session fields.
const recordInsert = `INSERT INTO alloc_events (category, stream_id, stream_revision, kind, reservation_id, data,
                          session_kind, session_name, session_owner)
 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`

// recordReleaseInsert appends a release whose session fields are inherited from
// the reservation's latest grant ($10) in the same stream; with no such grant it
// falls back to the event's own fields ($7–$9, kind defaulted to ephemeral).
const recordReleaseInsert = `INSERT INTO alloc_events (category, stream_id, stream_revision, kind, reservation_id, data,
                          session_kind, session_name, session_owner)
 SELECT $1, $2, $3, $4, $5, $6,
        COALESCE(g.session_kind, $7), COALESCE(g.session_name, $8), COALESCE(g.session_owner, $9)
 FROM (SELECT 1) AS one
 LEFT JOIN LATERAL (
     SELECT session_kind, session_name, session_owner FROM alloc_events
     WHERE stream_id = $2 AND reservation_id = $5 AND kind = $10
     ORDER BY stream_revision DESC LIMIT 1
 ) AS g ON true`

// sessionKindOrDefault maps the empty (legacy) session kind to ephemeral.
func sessionKindOrDefault(k allocator.SessionKind) allocator.SessionKind {
	if k == "" {
		return allocator.SessionEphemeral
	}
	return k
}

// Grant atomically appends a ReservationGranted for req at head+1 iff the
// stream's outstanding reservations of req.Kind (granted − released, counting only
// rows of that session kind) are below budget — the OCC admission gate. Grant is
// kind-agnostic: which kinds are admitted is the Allocator's policy. An empty
// req.Kind is ephemeral. A single conditional INSERT … SELECT … WHERE
// (outstanding of kind) < budget enforces the budget, and UNIQUE(stream_id,
// stream_revision) enforces the version — every kind of a (project, role) shares
// one stream, so all appends still serialize on it — so the
// budget check and the append are one atomic step: concurrent grants cannot
// overshoot, because the loser of a revision race retries and re-evaluates the
// budget against the winner's grant. Returns (true, nil) granted, (false, nil)
// over budget (no retry), and ErrConflictExhausted after maxAppendRetries lost
// races.
//
// Known gap (Slice 5): a crash between a successful Grant and the corresponding
// raise/teardown leaves a dangling ReservationGranted (a leaked slot) with no
// compensation. Closing it needs a reconcile sweep (release Granted reservations
// with no live instance); the cap stays ≤ budget, so this is an availability
// nuisance, not a correctness break.
func (s *Store) Grant(ctx context.Context, req allocator.Request, budget int) (bool, error) {
	streamID := req.Project + "/" + req.Role
	kind := string(sessionKindOrDefault(req.Kind))
	data, err := json.Marshal(map[string]string{}) // no extra payload yet
	if err != nil {
		return false, fmt.Errorf("allocpg: marshal: %w", err)
	}
	for attempt := 0; attempt < maxAppendRetries; attempt++ {
		var head int64
		if err := s.pool.QueryRow(ctx,
			`SELECT COALESCE(MAX(stream_revision), 0) FROM alloc_events WHERE stream_id = $1`,
			streamID).Scan(&head); err != nil {
			return false, fmt.Errorf("allocpg: grant head: %w", err)
		}
		tag, err := s.pool.Exec(ctx,
			`INSERT INTO alloc_events (category, stream_id, stream_revision, kind, reservation_id, data,
			                          session_kind, session_name, session_owner)
			 SELECT $1, $2, $3, $4, $5, $6, $9, $10, $11
			 WHERE (SELECT COUNT(*) FILTER (WHERE kind = $4 AND session_kind = $9)
			               - COUNT(*) FILTER (WHERE kind = $7 AND session_kind = $9)
			        FROM alloc_events WHERE stream_id = $2) < $8`,
			req.Project, streamID, head+1, string(allocator.KindReservationGranted), req.ReservationID, data,
			string(allocator.KindReservationReleased), budget, kind, req.Name, req.Owner)
		if err != nil {
			if isUniqueViolation(err) {
				continue // lost the revision race — re-read head and re-evaluate budget
			}
			return false, fmt.Errorf("allocpg: grant: %w", err)
		}
		if tag.RowsAffected() == 1 {
			return true, nil
		}
		return false, nil // 0 rows inserted: at/over budget
	}
	return false, ErrConflictExhausted
}

// Outstanding returns the live reservation count for a (project, role) stream —
// granted minus released, across all session kinds. This is the ledger fold the cap will use once the
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

// OutstandingReservations returns the reservations still holding a slot — net
// granted−released > 0 per reservation — whose most recent grant is older than
// olderThan (the reconcile sweep's grace window, so an in-flight raise is not
// swept). Reservation ids recur across dispatch cycles, so this is a net count,
// not a "has no released row" test: a reservation released then re-granted is
// outstanding again. Category is the project and stream_id is "project/role", so
// the role is stream_id with the "category/" prefix trimmed. Each reservation's
// session kind, name, and owner come from its latest grant.
func (s *Store) OutstandingReservations(ctx context.Context, olderThan time.Time) ([]allocator.Reservation, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT category, stream_id, reservation_id,
		        (array_agg(session_kind  ORDER BY stream_revision DESC) FILTER (WHERE kind = $1))[1],
		        (array_agg(session_name  ORDER BY stream_revision DESC) FILTER (WHERE kind = $1))[1],
		        (array_agg(session_owner ORDER BY stream_revision DESC) FILTER (WHERE kind = $1))[1]
		 FROM alloc_events
		 GROUP BY category, stream_id, reservation_id
		 HAVING COUNT(*) FILTER (WHERE kind = $1) > COUNT(*) FILTER (WHERE kind = $2)
		    AND MAX(at) FILTER (WHERE kind = $1) < $3`,
		string(allocator.KindReservationGranted), string(allocator.KindReservationReleased), olderThan)
	if err != nil {
		return nil, fmt.Errorf("allocpg: outstanding reservations: %w", err)
	}
	defer rows.Close()
	var out []allocator.Reservation
	for rows.Next() {
		var category, streamID, resID, kind, name, owner string
		if err := rows.Scan(&category, &streamID, &resID, &kind, &name, &owner); err != nil {
			return nil, err
		}
		out = append(out, allocator.Reservation{
			Project:       category,
			Role:          strings.TrimPrefix(streamID, category+"/"),
			ReservationID: resID,
			SessionKind:   allocator.SessionKind(kind),
			Name:          name,
			Owner:         owner,
		})
	}
	return out, rows.Err()
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
