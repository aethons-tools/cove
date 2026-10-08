// Package conditionpg is the Postgres Persister for internal/jam/condition. It
// shares the control-plane pool and owns its own migrations (the allocpg
// pattern), so the condition package stays free of pgx.
package conditionpg

import (
	"context"
	"encoding/json"
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

	"github.com/aethons-tools/cove/internal/jam/condition"
)

// migrateAdvisoryLock is distinct from the jam, intercom and alloc locks.
const migrateAdvisoryLock = 0x617474656e74 // "attent"

// Store persists condition occurrences in attention_conditions.
type Store struct {
	pool *pgxpool.Pool
	log  *slog.Logger
}

var _ condition.Persister = (*Store)(nil)

// New applies the embedded migrations and returns a ready Store. It does not own the pool.
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

// Save upserts one occurrence by (key, since).
func (s *Store) Save(ctx context.Context, c condition.Condition) error {
	doc, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("conditionpg: marshal: %w", err)
	}
	_, err = s.pool.Exec(ctx,
		`INSERT INTO attention_conditions (key, since, doc, resolved_at) VALUES ($1, $2, $3, $4)
		 ON CONFLICT (key, since) DO UPDATE SET doc = EXCLUDED.doc, resolved_at = EXCLUDED.resolved_at, updated_at = now()`,
		c.Key, c.Since, doc, c.ResolvedAt)
	if err != nil {
		return fmt.Errorf("conditionpg: save %s: %w", c.Key, err)
	}
	return nil
}

// Load returns every open occurrence and those resolved at or after resolvedSince.
func (s *Store) Load(ctx context.Context, resolvedSince time.Time) ([]condition.Condition, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT doc FROM attention_conditions WHERE resolved_at IS NULL OR resolved_at >= $1`, resolvedSince)
	if err != nil {
		return nil, fmt.Errorf("conditionpg: load: %w", err)
	}
	defer rows.Close()
	var out []condition.Condition
	for rows.Next() {
		var doc []byte
		if err := rows.Scan(&doc); err != nil {
			return nil, err
		}
		var c condition.Condition
		if err := json.Unmarshal(doc, &c); err != nil {
			return nil, fmt.Errorf("conditionpg: decode: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Prune deletes occurrences resolved before resolvedBefore.
func (s *Store) Prune(ctx context.Context, resolvedBefore time.Time) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM attention_conditions WHERE resolved_at < $1`, resolvedBefore)
	return err
}

// migrate: copied from allocpg (see Step 4 intro), table attention_schema_migrations.
func (s *Store) migrate(ctx context.Context) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(migrateAdvisoryLock)); err != nil {
			return fmt.Errorf("conditionpg: advisory lock: %w", err)
		}
		if _, err := tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS attention_schema_migrations (version int PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
			return fmt.Errorf("conditionpg: create attention_schema_migrations: %w", err)
		}
		applied := map[int]bool{}
		rows, err := tx.Query(ctx, `SELECT version FROM attention_schema_migrations`)
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
				return fmt.Errorf("conditionpg: bad migration name %q", name)
			}
			ver, err := strconv.Atoi(name[:i])
			if err != nil {
				return fmt.Errorf("conditionpg: bad migration version in %q: %w", name, err)
			}
			if applied[ver] {
				continue
			}
			sqlText, err := migrationFiles.ReadFile("migrations/" + name)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, string(sqlText)); err != nil {
				return fmt.Errorf("conditionpg: apply %s: %w", name, err)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO attention_schema_migrations (version) VALUES ($1)`, ver); err != nil {
				return err
			}
			s.log.Info("conditionpg: applied migration", "version", ver, "file", name)
		}
		return nil
	})
}
