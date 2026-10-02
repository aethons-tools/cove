// Package sessionpg is the Postgres backend for sessionevents.Store. It shares
// the control-plane *pgxpool.Pool and owns its own migrations.
package sessionpg

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

	"github.com/aethons-tools/cove/internal/jam/sessionevents"
)

// migrateAdvisoryLock is distinct from the other stores' locks so migrators
// sharing one database never block each other incorrectly.
const migrateAdvisoryLock = 0x73657373696f6e65 // "sessione"

// Store is a Postgres-backed sessionevents.Store over a shared pool.
type Store struct {
	pool *pgxpool.Pool
	log  *slog.Logger
}

var _ sessionevents.Store = (*Store)(nil)

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

const insertSQL = `INSERT INTO session_events (actor_id, stream_id, seq, kind, gap_from, gap_to, turn,
  observed_at, received_at, truncated_bytes, project, role, unit, owner, session_kind, raised_at,
  type, subtype, tool_name, claude_session_id, cost_usd, input_tokens, output_tokens, duration_ms, is_error,
  raw, raw_text)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27)
ON CONFLICT (actor_id, stream_id, seq) DO NOTHING`

// sanitizeEventText makes every free-text column value of ev storable in a
// Postgres text column (see sanitizeText). Raw is handled separately. It
// reports whether anything changed.
func sanitizeEventText(ev sessionevents.Event) (sessionevents.Event, bool) {
	changed := false
	for _, p := range []*string{
		&ev.ActorID, &ev.StreamID,
		&ev.Stamp.Project, &ev.Stamp.Role, &ev.Stamp.Unit, &ev.Stamp.Owner, &ev.Stamp.SessionKind,
		&ev.Index.Type, &ev.Index.Subtype, &ev.Index.ToolName, &ev.Index.ClaudeSessionID,
	} {
		out, c := sanitizeText(*p)
		*p = out
		changed = changed || c
	}
	return ev, changed
}

func (s *Store) insert(ev sessionevents.Event, raw, rawText any) error {
	ev, changed := sanitizeEventText(ev)
	if changed {
		s.log.Warn("sessionpg: text columns sanitized (invalid UTF-8 or NUL bytes replaced)", "actor", ev.ActorID, "seq", ev.Seq)
	}
	_, err := s.pool.Exec(context.Background(), insertSQL,
		ev.ActorID, ev.StreamID, int64(ev.Seq), ev.Kind, int64(ev.GapFrom), int64(ev.GapTo), int32(ev.Turn),
		ev.ObservedAt, ev.ReceivedAt, int64(ev.TruncatedBytes),
		ev.Stamp.Project, ev.Stamp.Role, ev.Stamp.Unit, ev.Stamp.Owner, ev.Stamp.SessionKind, ev.Stamp.RaisedAt,
		ev.Index.Type, ev.Index.Subtype, ev.Index.ToolName, ev.Index.ClaudeSessionID,
		ev.Index.CostUSD, ev.Index.InputTokens, ev.Index.OutputTokens, ev.Index.DurationMS, ev.Index.IsError,
		raw, rawText)
	return err
}

// sanitizeText makes t storable in a Postgres text column: invalid UTF-8
// becomes U+FFFD and NUL bytes (which text cannot hold) become U+FFFD too.
// It reports whether anything changed.
func sanitizeText(t string) (string, bool) {
	out := strings.ToValidUTF8(t, "\uFFFD")
	out, nul := replaceNUL(out)
	return out, nul || out != t
}

// replaceNUL swaps NUL bytes for U+FFFD, reporting whether any were found.
func replaceNUL(t string) (string, bool) {
	if !strings.ContainsRune(t, 0) {
		return t, false
	}
	return strings.ReplaceAll(t, "\x00", "\uFFFD"), true
}

// textSafe makes a raw line storable in a text column (lossy only for invalid
// UTF-8 and NUL bytes; logged by actor and seq, never content).
func (s *Store) textSafe(ev sessionevents.Event) string {
	t, changed := sanitizeText(string(ev.Raw))
	if changed {
		s.log.Warn("sessionpg: raw_text sanitized (invalid UTF-8 or NUL bytes replaced)", "actor", ev.ActorID, "seq", ev.Seq)
	}
	return t
}

// isDataException reports whether err is a Postgres class 22 (data exception)
// rejection, e.g. 22P05 (\u0000), 22P02 (lone surrogate), 22021 (bad UTF-8).
func isDataException(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && strings.HasPrefix(pgErr.Code, "22")
}

func (s *Store) Append(ev sessionevents.Event) error {
	if len(ev.Raw) > 0 && json.Valid(ev.Raw) {
		err := s.insert(ev, string(ev.Raw), nil)
		// Go's json.Valid accepts inputs jsonb rejects with several class 22
		// codes — keep the line as sanitized text rather than lose the event.
		if isDataException(err) {
			return s.insert(ev, nil, s.textSafe(ev))
		}
		return err
	}
	if len(ev.Raw) == 0 {
		return s.insert(ev, nil, nil)
	}
	return s.insert(ev, nil, s.textSafe(ev))
}

func (s *Store) HighWater(actorID, streamID string) (uint64, error) {
	var hw int64
	err := s.pool.QueryRow(context.Background(),
		`SELECT COALESCE(MAX(seq), 0) FROM session_events WHERE actor_id=$1 AND stream_id=$2`, actorID, streamID).Scan(&hw)
	return uint64(hw), err
}

const selectCols = `actor_id, stream_id, seq, kind, gap_from, gap_to, turn, observed_at, received_at, truncated_bytes,
  project, role, unit, owner, session_kind, raised_at, type, subtype, tool_name, claude_session_id,
  cost_usd, input_tokens, output_tokens, duration_ms, is_error, raw::text, raw_text`

func (s *Store) List(f sessionevents.Filter) ([]sessionevents.Event, error) {
	q := `SELECT ` + selectCols + ` FROM session_events WHERE actor_id=$1 AND stream_id=$2 AND seq > $3 ORDER BY seq`
	args := []any{f.ActorID, f.StreamID, int64(f.AfterSeq)}
	if f.Limit > 0 {
		q += ` LIMIT $4`
		args = append(args, f.Limit)
	}
	rows, err := s.pool.Query(context.Background(), q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []sessionevents.Event
	for rows.Next() {
		var e sessionevents.Event
		var seq, gf, gt, trunc int64
		var turn int32
		var raw, rawText *string
		if err := rows.Scan(&e.ActorID, &e.StreamID, &seq, &e.Kind, &gf, &gt, &turn, &e.ObservedAt, &e.ReceivedAt, &trunc,
			&e.Stamp.Project, &e.Stamp.Role, &e.Stamp.Unit, &e.Stamp.Owner, &e.Stamp.SessionKind, &e.Stamp.RaisedAt,
			&e.Index.Type, &e.Index.Subtype, &e.Index.ToolName, &e.Index.ClaudeSessionID,
			&e.Index.CostUSD, &e.Index.InputTokens, &e.Index.OutputTokens, &e.Index.DurationMS, &e.Index.IsError,
			&raw, &rawText); err != nil {
			return nil, err
		}
		e.Seq, e.GapFrom, e.GapTo, e.TruncatedBytes, e.Turn = uint64(seq), uint64(gf), uint64(gt), uint64(trunc), uint32(turn)
		switch {
		case raw != nil:
			e.Raw = []byte(*raw)
		case rawText != nil:
			e.Raw = []byte(*rawText)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) Streams(actorID string) ([]sessionevents.StreamInfo, error) {
	rows, err := s.pool.Query(context.Background(), `SELECT stream_id, MIN(received_at), MAX(received_at), MAX(seq), COUNT(*)
FROM session_events WHERE actor_id=$1 GROUP BY stream_id ORDER BY MIN(received_at) DESC`, actorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []sessionevents.StreamInfo
	for rows.Next() {
		var si sessionevents.StreamInfo
		var last int64
		if err := rows.Scan(&si.StreamID, &si.FirstAt, &si.LastAt, &last, &si.Events); err != nil {
			return nil, err
		}
		si.LastSeq = uint64(last)
		out = append(out, si)
	}
	return out, rows.Err()
}

func (s *Store) DeleteBefore(t time.Time) (int, error) {
	tag, err := s.pool.Exec(context.Background(), `DELETE FROM session_events WHERE received_at < $1`, t)
	return int(tag.RowsAffected()), err
}

func (s *Store) migrate(ctx context.Context) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(migrateAdvisoryLock)); err != nil {
			return fmt.Errorf("sessionpg: advisory lock: %w", err)
		}
		if _, err := tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS session_events_schema_migrations (version int PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
			return fmt.Errorf("sessionpg: create session_events_schema_migrations: %w", err)
		}
		applied := map[int]bool{}
		rows, err := tx.Query(ctx, `SELECT version FROM session_events_schema_migrations`)
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
				return fmt.Errorf("sessionpg: bad migration name %q", name)
			}
			ver, err := strconv.Atoi(name[:i])
			if err != nil {
				return fmt.Errorf("sessionpg: bad migration version in %q: %w", name, err)
			}
			if applied[ver] {
				continue
			}
			sqlText, err := migrationFiles.ReadFile("migrations/" + name)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, string(sqlText)); err != nil {
				return fmt.Errorf("sessionpg: apply %s: %w", name, err)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO session_events_schema_migrations (version) VALUES ($1)`, ver); err != nil {
				return err
			}
			s.log.Info("sessionpg: applied migration", "version", ver, "file", name)
		}
		return nil
	})
}
