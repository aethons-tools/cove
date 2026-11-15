package jam

import (
	"context"
	"fmt"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/jackc/pgx/v5"
)

func (s *PostgresStore) PutRailOrder(user ident.ID, section RailSection, ids []string) error {
	if err := checkRailSection(section); err != nil {
		return err
	}
	if ids == nil {
		ids = []string{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.registryTx("PutRailOrder", func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO rail_orders (user_id, section, ids) VALUES ($1, $2, $3)
			ON CONFLICT (user_id, section) DO UPDATE SET ids = EXCLUDED.ids, updated_at = now()`, user, section, ids)
		return err
	}); err != nil {
		return err
	}
	s.putRailOrderLocked(user, section, ids)
	return nil
}

func (s *PostgresStore) loadRailOrders(ctx context.Context) error {
	rows, err := s.pool.Query(ctx, `SELECT user_id, section, ids FROM rail_orders`)
	if err != nil {
		return fmt.Errorf("pgstore: load rail_orders: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var user, section string
		var ids []string
		if err := rows.Scan(&user, &section, &ids); err != nil {
			return err
		}
		s.putRailOrderLocked(ident.ID(user), RailSection(section), ids)
	}
	return rows.Err()
}
