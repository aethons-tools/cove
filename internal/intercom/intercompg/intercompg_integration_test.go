//go:build integration

package intercompg_test

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/intercom/intercompg"
	"github.com/aethons-tools/cove/internal/intercom/intercomtest"
)

func TestPostgresLogConformance(t *testing.T) {
	dsn := os.Getenv("HARBOR_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set HARBOR_TEST_POSTGRES_DSN to run the Postgres squawk log integration tests")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	intercomtest.RunConformance(t, func(t *testing.T) intercom.Store {
		s, err := intercompg.New(context.Background(), pool, nil)
		if err != nil {
			t.Fatalf("intercompg.New: %v", err)
		}
		if _, err := pool.Exec(context.Background(), `TRUNCATE squawks, squawk_recipients`); err != nil {
			t.Fatalf("truncate: %v", err)
		}
		return s
	})
}
