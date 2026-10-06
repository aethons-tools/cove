package intercompg

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
)

// Store is the Postgres channel log (intercom.Store) over a shared pool. It
// continues the legacy log, which Legacy reads.
type Store struct {
	pool    *pgxpool.Pool
	log     *slog.Logger
	legacy  *Legacy
	cutover int64
}

var _ intercom.Store = (*Store)(nil)

// New applies the embedded migrations (idempotent, advisory-locked) and
// returns the channel log. It does not own the pool; Close is a no-op.
func New(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger) (*Store, error) {
	legacy, err := NewLegacy(ctx, pool, log)
	if err != nil {
		return nil, err
	}
	s := &Store{pool: pool, log: legacy.log, legacy: legacy}
	if err := pool.QueryRow(ctx, `SELECT value FROM intercom_settings WHERE key = 'cutover_seq'`).Scan(&s.cutover); err != nil {
		return nil, fmt.Errorf("intercompg: read cutover_seq: %w", err)
	}
	return s, nil
}

// Legacy is the frozen legacy log this log continues.
func (s *Store) Legacy() *Legacy { return s.legacy }

func (s *Store) Close() error { return nil }

func (s *Store) CutoverSeq() int64 { return s.cutover }

func (s *Store) Append(m intercom.Squawk, audience []ident.ID) (intercom.Squawk, error) {
	m, err := intercom.Prepare(m)
	if err != nil {
		return intercom.Squawk{}, err
	}
	ctx := context.Background()
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// One append at a time: a seq is taken inside the transaction, so
		// without this a later seq could commit before an earlier one, and a
		// reader advancing past it (egress, a commit cursor, wake-on) would
		// skip the earlier one for good.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(appendAdvisoryLock)); err != nil {
			return err
		}
		var dup bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM legacy_squawks WHERE id = $1)`, m.ID).Scan(&dup); err != nil {
			return err
		}
		if dup {
			return fmt.Errorf("%w: %q (legacy log)", intercom.ErrDuplicateID, m.ID)
		}
		if err := tx.QueryRow(ctx,
			`INSERT INTO squawks (id, channel_id, from_id, body, at, reply_to, content_type, origin_connection_id, origin_ref)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING seq`,
			m.ID, m.Channel, m.From, m.Body, m.At, m.ReplyTo, m.ContentType, m.Origin, m.OriginRef).Scan(&m.Seq); err != nil {
			return err
		}
		if len(audience) == 0 {
			return nil
		}
		ids := make([]string, len(audience))
		for i, p := range audience {
			ids[i] = string(p)
		}
		_, err := tx.Exec(ctx,
			`INSERT INTO squawk_deliveries (participant_id, seq) SELECT DISTINCT p, $2::bigint FROM unnest($1::text[]) AS p`,
			ids, m.Seq)
		return err
	})
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && pgErr.Code == "23505" && pgErr.ConstraintName == "squawks_id_key" {
		err = fmt.Errorf("%w: %q", intercom.ErrDuplicateID, m.ID)
	}
	if err != nil {
		return intercom.Squawk{}, fmt.Errorf("intercompg: append: %w", err)
	}
	return m, nil
}

// appendAdvisoryLock serializes appends (see Append).
const appendAdvisoryLock = 0x696e746170706e64 // "intappnd"

const squawkCols = `s.seq, s.id, s.channel_id, s.from_id, s.body, s.at, s.reply_to, s.content_type, s.origin_connection_id, s.origin_ref`

func (s *Store) InboxSince(p ident.ID, afterSeq int64, limit int) []intercom.Squawk {
	return s.since(`FROM squawk_deliveries d JOIN squawks s ON s.seq = d.seq WHERE d.participant_id = $1`, p, afterSeq, limit)
}

func (s *Store) InboxBefore(p ident.ID, beforeSeq int64, limit int) []intercom.Squawk {
	return s.before(`FROM squawk_deliveries d JOIN squawks s ON s.seq = d.seq WHERE d.participant_id = $1`, p, beforeSeq, limit)
}

func (s *Store) ChannelSince(ch ident.ID, afterSeq int64, limit int) []intercom.Squawk {
	return s.since(`FROM squawks s WHERE s.channel_id = $1`, ch, afterSeq, limit)
}

func (s *Store) ChannelBefore(ch ident.ID, beforeSeq int64, limit int) []intercom.Squawk {
	return s.before(`FROM squawks s WHERE s.channel_id = $1`, ch, beforeSeq, limit)
}

func (s *Store) ListSince(afterSeq int64, limit int) []intercom.Squawk {
	sql := `SELECT ` + squawkCols + ` FROM squawks s WHERE s.seq > $1 ORDER BY s.seq`
	args := []any{afterSeq}
	if limit > 0 {
		sql += ` LIMIT $2`
		args = append(args, limit)
	}
	return s.query(sql, args...)
}

func (s *Store) ReadThread(rootID string) []intercom.Squawk {
	return s.query(`SELECT `+squawkCols+` FROM squawks s WHERE s.id = $1 OR s.reply_to = $1 ORDER BY s.seq`, rootID)
}

// since reads rows of from (which binds $1 to key) after a seq, ascending.
func (s *Store) since(from string, key any, afterSeq int64, limit int) []intercom.Squawk {
	sql := `SELECT ` + squawkCols + ` ` + from + ` AND s.seq > $2 ORDER BY s.seq`
	args := []any{key, afterSeq}
	if limit > 0 {
		sql += ` LIMIT $3`
		args = append(args, limit)
	}
	return s.query(sql, args...)
}

// before reads the rows of from nearest below a seq (<= 0: the end),
// returned ascending.
func (s *Store) before(from string, key any, beforeSeq int64, limit int) []intercom.Squawk {
	sql := `SELECT ` + squawkCols + ` ` + from
	args := []any{key}
	if beforeSeq > 0 {
		sql += ` AND s.seq < $2`
		args = append(args, beforeSeq)
	}
	sql += ` ORDER BY s.seq DESC`
	if limit > 0 {
		sql += fmt.Sprintf(` LIMIT $%d`, len(args)+1)
		args = append(args, limit)
	}
	out := s.query(sql, args...)
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func (s *Store) InboxChannels(p ident.ID) []intercom.ChannelStat {
	rows, err := s.pool.Query(context.Background(),
		`SELECT s.channel_id, max(s.seq) FROM squawk_deliveries d JOIN squawks s ON s.seq = d.seq
		 WHERE d.participant_id = $1 GROUP BY s.channel_id ORDER BY s.channel_id`, p)
	if err != nil {
		s.log.Error("intercompg: InboxChannels query", "error", err.Error())
		return nil
	}
	defer rows.Close()
	var out []intercom.ChannelStat
	for rows.Next() {
		var ch string
		var st intercom.ChannelStat
		if err := rows.Scan(&ch, &st.LastSeq); err != nil {
			s.log.Error("intercompg: InboxChannels scan", "error", err.Error())
			return nil
		}
		st.Channel = ident.ID(ch)
		out = append(out, st)
	}
	if err := rows.Err(); err != nil {
		s.log.Error("intercompg: InboxChannels rows", "error", err.Error())
		return nil
	}
	return out
}

func (s *Store) SeqOf(id string) (int64, bool) {
	var seq int64
	err := s.pool.QueryRow(context.Background(), `SELECT seq FROM squawks WHERE id = $1`, id).Scan(&seq)
	switch {
	case err == nil:
		return seq, true
	case !errors.Is(err, pgx.ErrNoRows):
		s.log.Error("intercompg: SeqOf query", "error", err.Error())
		return 0, false
	}
	return s.legacy.SeqOf(id)
}

func (s *Store) SeenIDs(prefix string) []string {
	legacy, err := s.legacy.seenIDs(prefix)
	if err != nil {
		s.log.Error("intercompg: SeenIDs (legacy)", "error", err.Error())
		return nil // a partial dedupe set must never be authoritative
	}
	rows, err := s.pool.Query(context.Background(), `SELECT id FROM squawks WHERE id LIKE $1 ORDER BY seq`, likePrefix(prefix))
	if err != nil {
		s.log.Error("intercompg: SeenIDs query", "error", err.Error())
		return nil
	}
	defer rows.Close()
	out := legacy
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			s.log.Error("intercompg: SeenIDs scan", "error", err.Error())
			return nil
		}
		out = append(out, id)
	}
	// A truncated dedupe set must never be authoritative (it would re-ingest).
	if err := rows.Err(); err != nil {
		s.log.Error("intercompg: SeenIDs rows", "error", err.Error())
		return nil
	}
	return out
}

func (s *Store) TailSeq() (int64, bool) {
	var seq int64
	err := s.pool.QueryRow(context.Background(), `SELECT seq FROM squawks ORDER BY seq DESC LIMIT 1`).Scan(&seq)
	switch {
	case err == nil:
		return seq, true
	case !errors.Is(err, pgx.ErrNoRows):
		s.log.Error("intercompg: TailSeq query", "error", err.Error())
		return 0, false
	}
	return s.legacy.TailSeq()
}

// query runs a SELECT of squawkCols; on any error it logs and returns nil,
// never a partial result.
func (s *Store) query(sql string, args ...any) []intercom.Squawk {
	rows, err := s.pool.Query(context.Background(), sql, args...)
	if err != nil {
		s.log.Error("intercompg: query", "error", err.Error())
		return nil
	}
	defer rows.Close()
	var out []intercom.Squawk
	for rows.Next() {
		var m intercom.Squawk
		var ch, from, origin string
		if err := rows.Scan(&m.Seq, &m.ID, &ch, &from, &m.Body, &m.At, &m.ReplyTo, &m.ContentType, &origin, &m.OriginRef); err != nil {
			s.log.Error("intercompg: scan", "error", err.Error())
			return nil
		}
		m.Channel, m.From, m.Origin = ident.ID(ch), ident.ID(from), ident.ID(origin)
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		s.log.Error("intercompg: rows", "error", err.Error())
		return nil
	}
	return out
}
