//go:build integration

package intercompg

import (
	"context"
	"io"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
)

// MigrateUpTo applies the migrations up to version v: a database as an
// older Jam left it (e.g. the legacy log before the cutover, v = 3).
func MigrateUpTo(ctx context.Context, pool *pgxpool.Pool, v int) error {
	return (&Legacy{pool: pool, log: slog.New(slog.NewTextHandler(io.Discard, nil))}).migrateUpTo(ctx, v)
}
