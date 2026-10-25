package jam

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// PutKitDraft writes objs and the draft row in one transaction; the cache
// changes only after commit.
func (s *PostgresStore) PutKitDraft(d KitDraft, objs KitObjects) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := checkKitDraft(s.memState, d, objs); err != nil {
		return err
	}
	if err := s.registryTx("PutKitDraft", func(ctx context.Context, tx pgx.Tx) error {
		if err := insertKitObjectsTx(ctx, tx, objs); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO kit_drafts (name, base, tree, opened_at, updated_at, actor) VALUES ($1,$2,$3,$4,$5,$6)
			 ON CONFLICT (name) DO UPDATE SET base = EXCLUDED.base, tree = EXCLUDED.tree, opened_at = EXCLUDED.opened_at,
			 updated_at = EXCLUDED.updated_at, actor = EXCLUDED.actor`,
			d.Kit, d.Base, d.Tree, d.OpenedAt, d.UpdatedAt, d.Actor)
		return err
	}); err != nil {
		return err
	}
	s.applyKitObjects(objs)
	s.applyPutKitDraft(d)
	return nil
}

func (s *PostgresStore) RemoveKitDraft(kit string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.kitDrafts[kit]; !ok {
		return fmt.Errorf("kit %s draft: %w", kit, ErrNotFound)
	}
	if err := s.exec("RemoveKitDraft", `DELETE FROM kit_drafts WHERE name = $1`, kit); err != nil {
		return err
	}
	s.applyRemoveKitDraft(kit)
	return nil
}

// loadKitDrafts reads every draft row into the cache (load, before the store
// is shared). Times come back in UTC.
func (s *PostgresStore) loadKitDrafts(ctx context.Context) error {
	rows, err := s.pool.Query(ctx, `SELECT name, base, tree, opened_at, updated_at, actor FROM kit_drafts`)
	if err != nil {
		return fmt.Errorf("pgstore: load kit_drafts: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var d KitDraft
		if err := rows.Scan(&d.Kit, &d.Base, &d.Tree, &d.OpenedAt, &d.UpdatedAt, &d.Actor); err != nil {
			return err
		}
		d.OpenedAt, d.UpdatedAt = d.OpenedAt.UTC(), d.UpdatedAt.UTC()
		s.kitDrafts[d.Kit] = d
	}
	return rows.Err()
}
