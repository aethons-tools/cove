package jam

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aethons-tools/cove/internal/ident"
)

func (s *PostgresStore) PutAppToken(t AppToken) ([]ident.ID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	evicted, err := s.prepareAppToken(t)
	if err != nil {
		return nil, err
	}
	if err := s.registryTx("PutAppToken", func(ctx context.Context, tx pgx.Tx) error {
		if len(evicted) > 0 {
			if _, err := tx.Exec(ctx, `DELETE FROM app_tokens WHERE id = ANY($1)`, idStrings(evicted)); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx,
			`INSERT INTO app_tokens (id, user_id, token_hash, label, operator_id, created_at, last_used_at, expires_at)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
			t.ID, t.UserID, t.Hash, t.Label, t.OperatorID, t.CreatedAt, t.LastUsedAt, t.ExpiresAt)
		return err
	}); err != nil {
		return nil, err
	}
	s.applyPutAppToken(t, evicted)
	return evicted, nil
}

func (s *PostgresStore) DeleteUserAppToken(user, id ident.ID) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.appTokens[id]; !ok || t.UserID != user {
		return false, nil
	}
	if err := s.registryTx("DeleteUserAppToken", func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM app_tokens WHERE id = $1 AND user_id = $2`, id, user)
		return err
	}); err != nil {
		return false, err
	}
	s.applyDeleteAppToken(id)
	return true, nil
}

func (s *PostgresStore) TouchAppToken(id ident.ID, lastUsed, expires time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.appTokens[id]
	if !ok {
		return nil
	}
	if err := s.registryTx("TouchAppToken", func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE app_tokens SET last_used_at = $2, expires_at = $3 WHERE id = $1`, id, lastUsed, expires)
		return err
	}); err != nil {
		return err
	}
	t.LastUsedAt, t.ExpiresAt = lastUsed, expires
	s.appTokens[id] = t
	return nil
}

func (s *PostgresStore) DeleteExpiredAppTokens(now time.Time) ([]ident.ID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	gone := s.expiredAppTokens(now)
	if len(gone) == 0 {
		return nil, nil
	}
	if err := s.registryTx("DeleteExpiredAppTokens", func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM app_tokens WHERE id = ANY($1)`, idStrings(gone))
		return err
	}); err != nil {
		return nil, err
	}
	for _, id := range gone {
		s.applyDeleteAppToken(id)
	}
	return gone, nil
}

// loadAppTokens fills the app-token maps. load's part; no lock.
func (s *PostgresStore) loadAppTokens(ctx context.Context) error {
	rows, err := s.pool.Query(ctx, `SELECT id, user_id, token_hash, label, operator_id, created_at, last_used_at, expires_at FROM app_tokens`)
	if err != nil {
		return fmt.Errorf("pgstore: load app_tokens: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var t AppToken
		if err := rows.Scan(&t.ID, &t.UserID, &t.Hash, &t.Label, &t.OperatorID, &t.CreatedAt, &t.LastUsedAt, &t.ExpiresAt); err != nil {
			return err
		}
		t.CreatedAt, t.LastUsedAt, t.ExpiresAt = t.CreatedAt.UTC(), t.LastUsedAt.UTC(), t.ExpiresAt.UTC()
		s.applyPutAppToken(t, nil)
	}
	return rows.Err()
}

// idStrings converts ids for a text[] parameter.
func idStrings(ids []ident.ID) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = string(id)
	}
	return out
}
