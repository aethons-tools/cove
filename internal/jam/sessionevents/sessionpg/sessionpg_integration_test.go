//go:build integration

package sessionpg_test

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aethons-tools/cove/internal/jam/sessionevents"
	"github.com/aethons-tools/cove/internal/jam/sessionevents/sessioneventstest"
	"github.com/aethons-tools/cove/internal/jam/sessionevents/sessionpg"
)

func TestPostgresSessionEventsConformance(t *testing.T) {
	dsn := os.Getenv("JAM_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set JAM_TEST_POSTGRES_DSN to run the Postgres session-events integration tests")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	sessioneventstest.RunConformance(t, func(t *testing.T) sessionevents.Store {
		s, err := sessionpg.New(context.Background(), pool, nil)
		if err != nil {
			t.Fatalf("sessionpg.New: %v", err)
		}
		if _, err := pool.Exec(context.Background(), `TRUNCATE session_events`); err != nil {
			t.Fatalf("truncate: %v", err)
		}
		return s
	})
}
