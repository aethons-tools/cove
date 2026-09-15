//go:build integration

package msglogpg_test

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aethons-tools/cove/internal/msglog"
	"github.com/aethons-tools/cove/internal/msglog/msglogpg"
	"github.com/aethons-tools/cove/internal/msglog/msglogtest"
)

func TestPostgresLogConformance(t *testing.T) {
	dsn := os.Getenv("HARBOR_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set HARBOR_TEST_POSTGRES_DSN to run the Postgres message-log integration tests")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	msglogtest.RunConformance(t, func(t *testing.T) msglog.Store {
		s, err := msglogpg.New(context.Background(), pool, nil)
		if err != nil {
			t.Fatalf("msglogpg.New: %v", err)
		}
		if _, err := pool.Exec(context.Background(), `TRUNCATE messages, message_recipients`); err != nil {
			t.Fatalf("truncate: %v", err)
		}
		return s
	})
}
