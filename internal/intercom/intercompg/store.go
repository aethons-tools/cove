package intercompg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

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
		// An ask event folds under the same lock: its checks see every
		// earlier event, and a refused one stores nothing.
		var ask intercom.Ask
		var isAsk bool
		var getErr, foldErr error
		m, ask, isAsk, foldErr = intercom.FoldAsk(m, func(id string) (intercom.Ask, bool) {
			a, ok, err := getAskTx(ctx, tx, id)
			if err != nil {
				getErr = err
			}
			return a, ok
		})
		if getErr != nil {
			return getErr // a read failure, not a missing ask
		}
		if foldErr != nil {
			return foldErr
		}
		var bound []intercom.Blob
		m, bound, foldErr = intercom.FoldBlobs(m, func(id ident.ID) (intercom.Blob, bool) {
			b, ok, err := getBlobTx(ctx, tx, id)
			if err != nil {
				getErr = err
			}
			return b, ok
		})
		if getErr != nil {
			return getErr
		}
		if foldErr != nil {
			return foldErr
		}
		var parts []byte
		if len(m.Parts) > 0 {
			if parts, err = json.Marshal(m.Parts); err != nil {
				return err
			}
		}
		if err := tx.QueryRow(ctx,
			`INSERT INTO squawks (id, channel_id, from_id, body, at, reply_to, content_type, origin_connection_id, origin_ref, parts)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING seq`,
			m.ID, m.Channel, m.From, m.Body, m.At, m.ReplyTo, m.ContentType, m.Origin, m.OriginRef, parts).Scan(&m.Seq); err != nil {
			return err
		}
		for _, b := range bound {
			if _, err := tx.Exec(ctx, `UPDATE blobs SET state = $2, channel_id = $3, squawk_id = $4 WHERE id = $1`,
				b.ID, b.State, b.Channel, b.Squawk); err != nil {
				return err
			}
		}
		if isAsk {
			ask.LastSeq = m.Seq
			doc, err := json.Marshal(ask)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx,
				`INSERT INTO asks (id, channel_id, asker_id, state, last_seq, doc) VALUES ($1,$2,$3,$4,$5,$6)
				 ON CONFLICT (id) DO UPDATE SET state = EXCLUDED.state, last_seq = EXCLUDED.last_seq, doc = EXCLUDED.doc`,
				ask.ID, ask.Channel, ask.Asker, ask.State, ask.LastSeq, doc); err != nil {
				return err
			}
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

const squawkCols = `s.seq, s.id, s.channel_id, s.from_id, s.body, s.at, s.reply_to, s.content_type, s.origin_connection_id, s.origin_ref, s.parts`

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
		var parts []byte
		if err := rows.Scan(&m.Seq, &m.ID, &ch, &from, &m.Body, &m.At, &m.ReplyTo, &m.ContentType, &origin, &m.OriginRef, &parts); err != nil {
			s.log.Error("intercompg: scan", "error", err.Error())
			return nil
		}
		if len(parts) > 0 {
			if err := json.Unmarshal(parts, &m.Parts); err != nil {
				s.log.Error("intercompg: parts", "id", m.ID, "error", err.Error())
				return nil
			}
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

// getAskTx reads ask id inside tx.
func getAskTx(ctx context.Context, tx pgx.Tx, id string) (intercom.Ask, bool, error) {
	var doc []byte
	err := tx.QueryRow(ctx, `SELECT doc FROM asks WHERE id = $1`, id).Scan(&doc)
	if errors.Is(err, pgx.ErrNoRows) {
		return intercom.Ask{}, false, nil
	}
	if err != nil {
		return intercom.Ask{}, false, err
	}
	var a intercom.Ask
	if err := json.Unmarshal(doc, &a); err != nil {
		return intercom.Ask{}, false, err
	}
	return a, true, nil
}

func (s *Store) GetAsk(id string) (intercom.Ask, bool) {
	l := s.asks(`WHERE id = $1`, id)
	if len(l) != 1 {
		return intercom.Ask{}, false
	}
	return l[0], true
}

func (s *Store) ListAsks(f intercom.AskFilter) []intercom.Ask {
	// Only the filters given, as literal predicates, so the partial index on
	// open asks applies.
	var conds []string
	var args []any
	for _, c := range []struct{ col, v string }{{"channel_id", string(f.Channel)}, {"asker_id", string(f.Asker)}, {"state", string(f.State)}} {
		if c.v != "" {
			args = append(args, c.v)
			conds = append(conds, fmt.Sprintf("%s = $%d", c.col, len(args)))
		}
	}
	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}
	return s.asks(where, args...)
}

// asks runs a SELECT over the asks read-model, by last_seq; on any error it
// logs and returns nil.
func (s *Store) asks(where string, args ...any) []intercom.Ask {
	rows, err := s.pool.Query(context.Background(), `SELECT doc FROM asks `+where+` ORDER BY last_seq`, args...)
	if err != nil {
		s.log.Error("intercompg: asks", "error", err.Error())
		return nil
	}
	defer rows.Close()
	var out []intercom.Ask
	for rows.Next() {
		var doc []byte
		var a intercom.Ask
		if err := rows.Scan(&doc); err != nil || json.Unmarshal(doc, &a) != nil {
			s.log.Error("intercompg: asks scan")
			return nil
		}
		out = append(out, a)
	}
	if rows.Err() != nil {
		return nil
	}
	return out
}

const blobCols = `id, uploader_id, state, size, type, name, sha256, channel_id, squawk_id, created_at`

func scanBlob(row pgx.Row) (intercom.Blob, error) {
	var b intercom.Blob
	var id, up, ch string
	err := row.Scan(&id, &up, &b.State, &b.Size, &b.Type, &b.Name, &b.SHA256, &ch, &b.Squawk, &b.Created)
	b.ID, b.Uploader, b.Channel = ident.ID(id), ident.ID(up), ident.ID(ch)
	return b, err
}

// getBlobTx reads blob id inside tx, locking its row.
func getBlobTx(ctx context.Context, tx pgx.Tx, id ident.ID) (intercom.Blob, bool, error) {
	b, err := scanBlob(tx.QueryRow(ctx, `SELECT `+blobCols+` FROM blobs WHERE id = $1 FOR UPDATE`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return intercom.Blob{}, false, nil
	}
	return b, err == nil, err
}

func (s *Store) CreateBlob(b intercom.Blob) error {
	_, err := s.pool.Exec(context.Background(),
		`INSERT INTO blobs (`+blobCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,'','',$8)`,
		b.ID, b.Uploader, intercom.BlobPending, b.Size, b.Type, b.Name, b.SHA256, b.Created)
	if err != nil {
		return fmt.Errorf("intercompg: create blob: %w", err)
	}
	return nil
}

func (s *Store) GetBlob(id ident.ID) (intercom.Blob, bool) {
	b, err := scanBlob(s.pool.QueryRow(context.Background(), `SELECT `+blobCols+` FROM blobs WHERE id = $1`, id))
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			s.log.Error("intercompg: get blob", "id", string(id), "error", err.Error())
		}
		return intercom.Blob{}, false
	}
	return b, true
}

func (s *Store) PendingBlobsBefore(t time.Time) []intercom.Blob {
	rows, err := s.pool.Query(context.Background(), `SELECT `+blobCols+` FROM blobs WHERE state = 'pending' AND created_at < $1`, t)
	if err != nil {
		s.log.Error("intercompg: pending blobs", "error", err.Error())
		return nil
	}
	defer rows.Close()
	var out []intercom.Blob
	for rows.Next() {
		b, err := scanBlob(rows)
		if err != nil {
			s.log.Error("intercompg: pending blobs scan", "error", err.Error())
			return nil
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		s.log.Error("intercompg: pending blobs rows", "error", err.Error())
		return nil
	}
	return out
}

func (s *Store) DeleteBlob(id ident.ID) error {
	tag, err := s.pool.Exec(context.Background(), `DELETE FROM blobs WHERE id = $1 AND state = 'pending'`, id)
	if err != nil {
		return fmt.Errorf("intercompg: delete blob: %w", err)
	}
	if tag.RowsAffected() == 0 {
		if _, ok := s.GetBlob(id); ok {
			return fmt.Errorf("%w: %s", intercom.ErrBlobAttached, id)
		}
	}
	return nil
}

func (s *Store) CreateBoard(b intercom.Board) error {
	_, err := s.pool.Exec(context.Background(),
		`INSERT INTO boards (id, channel_id, title, created_by, created_at) VALUES ($1,$2,$3,$4,$5)`,
		b.ID, b.Channel, b.Title, b.CreatedBy, b.Created)
	if err != nil {
		return fmt.Errorf("intercompg: create board: %w", err)
	}
	return nil
}

func (s *Store) boards(where string, arg any) []intercom.Board {
	rows, err := s.pool.Query(context.Background(), `SELECT id, channel_id, title, created_by, created_at, seq FROM boards `+where+` ORDER BY created_at, id`, arg)
	if err != nil {
		s.log.Error("intercompg: boards", "error", err.Error())
		return nil
	}
	defer rows.Close()
	var out []intercom.Board
	for rows.Next() {
		var b intercom.Board
		var id, ch, by string
		if err := rows.Scan(&id, &ch, &b.Title, &by, &b.Created, &b.Seq); err != nil {
			s.log.Error("intercompg: boards scan", "error", err.Error())
			return nil
		}
		b.ID, b.Channel, b.CreatedBy = ident.ID(id), ident.ID(ch), ident.ID(by)
		out = append(out, b)
	}
	return out
}

func (s *Store) GetBoard(id ident.ID) (intercom.Board, bool) {
	l := s.boards(`WHERE id = $1`, string(id))
	if len(l) != 1 {
		return intercom.Board{}, false
	}
	return l[0], true
}

func (s *Store) ListBoards(channel ident.ID) []intercom.Board {
	return s.boards(`WHERE channel_id = $1`, string(channel))
}

// MergeElements applies the merge rule per element in one transaction: the
// board row is locked (serializing merges into it), each element's stored
// row compared, the winners written with the board's next seq.
func (s *Store) MergeElements(board ident.ID, els []intercom.Element) (int64, []intercom.Element, error) {
	if len(els) > intercom.MaxMergeBatch {
		return 0, nil, fmt.Errorf("%w: over %d elements in one merge", intercom.ErrInvalid, intercom.MaxMergeBatch)
	}
	ctx := context.Background()
	var seq int64
	var lost []intercom.Element
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT seq FROM boards WHERE id = $1 FOR UPDATE`, board).Scan(&seq); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return intercom.ErrNoBoard
			}
			return err
		}
		next, accepted := seq+1, false
		for _, e := range els {
			var cur intercom.Element
			err := tx.QueryRow(ctx, `SELECT version, version_nonce, deleted, seq, doc FROM board_elements WHERE board_id = $1 AND element_id = $2`, board, e.ID).
				Scan(&cur.Version, &cur.Nonce, &cur.Deleted, &cur.Seq, (*rawText)(&cur.Doc))
			switch {
			case err == nil:
				cur.ID = e.ID
				if !intercom.Wins(e, cur) {
					lost = append(lost, cur)
					continue
				}
			case !errors.Is(err, pgx.ErrNoRows):
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO board_elements (board_id, element_id, version, version_nonce, deleted, seq, doc)
				VALUES ($1,$2,$3,$4,$5,$6,$7)
				ON CONFLICT (board_id, element_id) DO UPDATE SET version = EXCLUDED.version, version_nonce = EXCLUDED.version_nonce,
					deleted = EXCLUDED.deleted, seq = EXCLUDED.seq, doc = EXCLUDED.doc`,
				board, e.ID, e.Version, e.Nonce, e.Deleted, next, string(e.Doc)); err != nil {
				return err
			}
			accepted = true
		}
		if accepted {
			seq = next
			_, err := tx.Exec(ctx, `UPDATE boards SET seq = $2 WHERE id = $1`, board, seq)
			return err
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, intercom.ErrNoBoard) {
			return 0, nil, err
		}
		return 0, nil, fmt.Errorf("intercompg: merge elements: %w", err)
	}
	return seq, lost, nil
}

func (s *Store) BoardElements(board ident.ID, since int64) ([]intercom.Element, int64) {
	ctx := context.Background()
	var seq int64
	var out []intercom.Element
	// One snapshot for the elements and the seq, so the seq returned covers
	// exactly what was read.
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT seq FROM boards WHERE id = $1`, board).Scan(&seq); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT element_id, version, version_nonce, deleted, seq, doc FROM board_elements WHERE board_id = $1 AND seq > $2 ORDER BY element_id`, board, since)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e intercom.Element
			if err := rows.Scan(&e.ID, &e.Version, &e.Nonce, &e.Deleted, &e.Seq, (*rawText)(&e.Doc)); err != nil {
				return err
			}
			out = append(out, e)
		}
		return rows.Err()
	})
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			s.log.Error("intercompg: board elements", "board", string(board), "error", err.Error())
		}
		return nil, 0
	}
	return out, seq
}

// rawText scans a text column into a json.RawMessage.
type rawText json.RawMessage

func (r *rawText) Scan(src any) error {
	switch v := src.(type) {
	case string:
		*r = rawText(v)
	case []byte:
		*r = append(rawText(nil), v...)
	default:
		return fmt.Errorf("intercompg: doc: unexpected %T", src)
	}
	return nil
}

func (s *Store) BoardsCreatedBy(p ident.ID) []intercom.Board {
	return s.boards(`WHERE created_by = $1`, string(p))
}
