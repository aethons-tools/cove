package jam

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

func (s *PostgresStore) PutSessionColor(actorID, color string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.sessionColors[actorID]; ok {
		return c, nil
	}
	if err := s.registryTx("PutSessionColor", func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO session_colors (actor_id, color) VALUES ($1, $2) ON CONFLICT (actor_id) DO NOTHING`, actorID, color)
		return err
	}); err != nil {
		return "", err
	}
	return s.putSessionColorLocked(actorID, color), nil
}

func (s *PostgresStore) loadSessionColors(ctx context.Context) error {
	rows, err := s.pool.Query(ctx, `SELECT actor_id, color FROM session_colors`)
	if err != nil {
		return fmt.Errorf("pgstore: load session_colors: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, color string
		if err := rows.Scan(&id, &color); err != nil {
			return err
		}
		s.sessionColors[id] = color
	}
	return rows.Err()
}
