// Package intercompg is the Postgres backend for intercom.Store. It queries Postgres
// directly (no in-memory mirror) and shares a control-plane *pgxpool.Pool. It
// keeps pgx out of the stdlib-only intercom core.
package intercompg

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
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aethons-tools/cove/internal/intercom"
)

// migrateAdvisoryLock is distinct from the control-plane store's lock so the two
// migrators sharing one database never block each other incorrectly.
const migrateAdvisoryLock = 0x696e746572636f6d // "intercom"

// Legacy is a Postgres-backed intercom.Legacy over a shared pool.
type Legacy struct {
	pool *pgxpool.Pool
	log  *slog.Logger
}

var _ intercom.LegacyStore = (*Legacy)(nil)

// NewLegacy applies the embedded migrations (idempotent, advisory-locked) and returns
// a ready store. It does not own the pool; Close is a no-op.
func NewLegacy(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger) (*Legacy, error) {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	s := &Legacy{pool: pool, log: log}
	if err := s.migrate(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

// Close is a no-op: the pool is owned by the control-plane store.
func (s *Legacy) Close() error { return nil }

func (s *Legacy) Append(m intercom.LegacySquawk) (intercom.LegacySquawk, error) {
	m, err := intercom.PrepareLegacy(m)
	if err != nil {
		return intercom.LegacySquawk{}, err
	}
	toJSON, err := json.Marshal(m.To)
	if err != nil {
		return intercom.LegacySquawk{}, err
	}
	err = pgx.BeginFunc(context.Background(), s.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(context.Background(),
			`INSERT INTO legacy_squawks (id, from_kind, from_ref, body, at, project, reply_to, "to", content_type)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING seq`,
			m.ID, m.From.Kind, m.From.Ref, m.Body, m.At, m.Project, m.ReplyTo, toJSON, m.ContentType).Scan(&m.Seq); err != nil {
			return err
		}
		for _, t := range m.To {
			// Duplicate targets in To must not abort the append (parity with the
			// file backend); the full To, duplicates included, is still preserved
			// in the "to" JSONB column above.
			if _, err := tx.Exec(context.Background(),
				`INSERT INTO legacy_squawk_recipients (squawk_id, kind, ref) VALUES ($1,$2,$3)
				 ON CONFLICT (squawk_id, kind, ref) DO NOTHING`,
				m.ID, t.Kind, t.Ref); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return intercom.LegacySquawk{}, fmt.Errorf("intercompg: append: %w", err)
	}
	return m, nil
}

func (s *Legacy) ReadInbox(t intercom.Target) []intercom.LegacySquawk {
	return s.query(
		`SELECT m.seq, m.id, m.from_kind, m.from_ref, m.body, m.at, m.project, m.reply_to, m."to", m.content_type
		 FROM legacy_squawks m JOIN legacy_squawk_recipients r ON r.squawk_id = m.id
		 WHERE r.kind = $1 AND r.ref = $2 ORDER BY m.seq`, t.Kind, t.Ref)
}

func (s *Legacy) ReadThread(rootID string) []intercom.LegacySquawk {
	return s.query(
		`SELECT seq, id, from_kind, from_ref, body, at, project, reply_to, "to", content_type
		 FROM legacy_squawks WHERE id = $1 OR reply_to = $1 ORDER BY seq`, rootID)
}

func (s *Legacy) List(f intercom.LegacyFilter) []intercom.LegacySquawk {
	// Zero Since/Until are unbounded; pass them as conditional predicates.
	// ORDER BY seq is the append-order key (see Store.ListSince doc): ids are
	// opaque identifiers, not comparable across namespaces, so ordering must
	// never rely on lexical id order.
	return s.query(
		`SELECT seq, id, from_kind, from_ref, body, at, project, reply_to, "to", content_type
		 FROM legacy_squawks
		 WHERE ($1 = '' OR project = $1)
		   AND ($2::timestamptz IS NULL OR at >= $2)
		   AND ($3::timestamptz IS NULL OR at < $3)
		 ORDER BY seq`,
		f.Project, nullTime(f.Since), nullTime(f.Until))
}

func (s *Legacy) SeenIDs(prefix string) []string {
	rows, err := s.pool.Query(context.Background(),
		`SELECT id FROM legacy_squawks WHERE id LIKE $1 ORDER BY id`, likePrefix(prefix))
	if err != nil {
		s.log.Error("intercompg: SeenIDs query", "error", err.Error())
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			s.log.Error("intercompg: SeenIDs scan", "error", err.Error())
			return nil
		}
		out = append(out, id)
	}
	// pgx v5 can end Next() early on a mid-stream failure without a Scan error;
	// the error only surfaces via rows.Err(). A truncated dedupe set must never
	// be treated as authoritative (it would cause duplicate message
	// re-ingestion), so return nil rather than the partial slice.
	if err := rows.Err(); err != nil {
		s.log.Error("intercompg: SeenIDs rows", "error", err.Error())
		return nil
	}
	return out
}

func (s *Legacy) ListSince(afterSeq int64, limit int) []intercom.LegacySquawk {
	sql := `SELECT seq, id, from_kind, from_ref, body, at, project, reply_to, "to", content_type
	        FROM legacy_squawks WHERE seq > $1 ORDER BY seq`
	args := []any{afterSeq}
	if limit > 0 {
		sql += ` LIMIT $2`
		args = append(args, limit)
	}
	return s.query(sql, args...)
}

func (s *Legacy) ReadInboxSince(t intercom.Target, afterSeq int64, limit int) []intercom.LegacySquawk {
	sql := `SELECT m.seq, m.id, m.from_kind, m.from_ref, m.body, m.at, m.project, m.reply_to, m."to", m.content_type
	        FROM legacy_squawks m JOIN legacy_squawk_recipients r ON r.squawk_id = m.id
	        WHERE r.kind = $1 AND r.ref = $2 AND m.seq > $3 ORDER BY m.seq`
	args := []any{t.Kind, t.Ref, afterSeq}
	if limit > 0 {
		sql += ` LIMIT $4`
		args = append(args, limit)
	}
	return s.query(sql, args...)
}

func (s *Legacy) ReadInboxBefore(t intercom.Target, beforeSeq int64, limit int) []intercom.LegacySquawk {
	// nearest-below beforeSeq: order DESC + LIMIT, then reverse to ascending.
	sql := `SELECT m.seq, m.id, m.from_kind, m.from_ref, m.body, m.at, m.project, m.reply_to, m."to", m.content_type
	        FROM legacy_squawks m JOIN legacy_squawk_recipients r ON r.squawk_id = m.id
	        WHERE r.kind = $1 AND r.ref = $2`
	args := []any{t.Kind, t.Ref}
	if beforeSeq > 0 {
		sql += ` AND m.seq < $3`
		args = append(args, beforeSeq)
	}
	sql += ` ORDER BY m.seq DESC`
	if limit > 0 {
		sql += fmt.Sprintf(" LIMIT $%d", len(args)+1)
		args = append(args, limit)
	}
	out := s.query(sql, args...)
	// reverse to ascending.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// SeqOf returns the append-order Seq assigned to the message with the given
// id, or (0, false) if no such message exists.
func (s *Legacy) SeqOf(id string) (int64, bool) {
	var seq int64
	err := s.pool.QueryRow(context.Background(),
		`SELECT seq FROM legacy_squawks WHERE id = $1`, id).Scan(&seq)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, false
		}
		s.log.Error("intercompg: SeqOf query", "error", err.Error())
		return 0, false
	}
	return seq, true
}

// TailSeq returns the last-appended message's Seq, or (0, false) when the log
// is empty.
func (s *Legacy) TailSeq() (int64, bool) {
	var seq int64
	err := s.pool.QueryRow(context.Background(),
		`SELECT seq FROM legacy_squawks ORDER BY seq DESC LIMIT 1`).Scan(&seq)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, false
		}
		s.log.Error("intercompg: TailSeq query", "error", err.Error())
		return 0, false
	}
	return seq, true
}

// query runs a message SELECT (columns in the fixed order below) and
// reconstructs each Squawk via scanSquawks.
func (s *Legacy) query(sql string, args ...any) []intercom.LegacySquawk {
	rows, err := s.pool.Query(context.Background(), sql, args...)
	if err != nil {
		s.log.Error("intercompg: query", "error", err.Error())
		return nil
	}
	defer rows.Close()
	return s.scanSquawks(rows)
}

// scanSquawks scans each row of a message SELECT (columns in the fixed order:
// seq, id, from_kind, from_ref, body, at, project, reply_to, "to", content_type) and
// reconstructs each Squawk, decoding To from the "to" JSONB column. On a scan
// or decode error it logs and returns nil rather than a partial result. After
// the loop it checks rows.Err(): in pgx v5 a mid-stream failure can end Next()
// early without a Scan error, surfacing only via rows.Err(), so a truncated
// read must not be silently returned as a short success.
func (s *Legacy) scanSquawks(rows pgx.Rows) []intercom.LegacySquawk {
	var out []intercom.LegacySquawk
	for rows.Next() {
		var m intercom.LegacySquawk
		var toJSON []byte
		if err := rows.Scan(&m.Seq, &m.ID, &m.From.Kind, &m.From.Ref, &m.Body, &m.At, &m.Project, &m.ReplyTo, &toJSON, &m.ContentType); err != nil {
			s.log.Error("intercompg: scan", "error", err.Error())
			return nil
		}
		if err := json.Unmarshal(toJSON, &m.To); err != nil {
			s.log.Error("intercompg: decode to", "error", err.Error())
			return nil
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		s.log.Error("intercompg: rows", "error", err.Error())
		return nil
	}
	return out
}

func (s *Legacy) migrate(ctx context.Context) error { return s.migrateUpTo(ctx, 0) }

// migrateUpTo applies the migrations up to version upTo (0 = all).
func (s *Legacy) migrateUpTo(ctx context.Context, upTo int) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(migrateAdvisoryLock)); err != nil {
			return fmt.Errorf("intercompg: advisory lock: %w", err)
		}
		if _, err := tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS intercom_schema_migrations (version int PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
			return fmt.Errorf("intercompg: create intercom_schema_migrations: %w", err)
		}
		applied := map[int]bool{}
		rows, err := tx.Query(ctx, `SELECT version FROM intercom_schema_migrations`)
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
				return fmt.Errorf("intercompg: bad migration name %q", name)
			}
			ver, err := strconv.Atoi(name[:i])
			if err != nil {
				return fmt.Errorf("intercompg: bad migration version in %q: %w", name, err)
			}
			if applied[ver] || (upTo > 0 && ver > upTo) {
				continue
			}
			sqlText, err := migrationFiles.ReadFile("migrations/" + name)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, string(sqlText)); err != nil {
				return fmt.Errorf("intercompg: apply %s: %w", name, err)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO intercom_schema_migrations (version) VALUES ($1)`, ver); err != nil {
				return err
			}
			s.log.Info("intercompg: applied migration", "version", ver, "file", name)
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
