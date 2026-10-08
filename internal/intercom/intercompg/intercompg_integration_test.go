//go:build integration

package intercompg_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/intercom/intercompg"
	"github.com/aethons-tools/cove/internal/intercom/intercomtest"
)

// freshPool is a pool on a schema of its own (dropped at cleanup), so each
// case migrates from nothing — the cutover included.
func freshPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("JAM_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set JAM_TEST_POSTGRES_DSN to run the Postgres squawk log integration tests")
	}
	ctx := context.Background()
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	schema := "intercom_test_" + hex.EncodeToString(b)
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(admin.Close)
	if _, err := admin.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), `DROP SCHEMA `+schema+` CASCADE`) })
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestPostgresLegacyLogConformance(t *testing.T) {
	intercomtest.RunLegacyConformance(t, func(t *testing.T) intercom.LegacyStore {
		s, err := intercompg.NewLegacy(context.Background(), freshPool(t), nil)
		if err != nil {
			t.Fatalf("intercompg.NewLegacy: %v", err)
		}
		return s
	})
}

func TestPostgresChannelLogConformance(t *testing.T) {
	intercomtest.RunConformance(t, func(t *testing.T, legacy int) intercomtest.Fixture {
		ctx := context.Background()
		pool := freshPool(t)
		// The legacy log as the last Jam before the cutover left it.
		if err := intercompg.MigrateUpTo(ctx, pool, 3); err != nil {
			t.Fatalf("migrate to 3: %v", err)
		}
		for i := range legacy {
			if _, err := pool.Exec(ctx,
				`INSERT INTO squawks (id, from_kind, from_ref, body, at, project, reply_to, "to")
				 VALUES ($1, 'actor', 'c', $2, now(), 'acme', '', '[{"kind":"human","ref":"a"}]')`,
				fmt.Sprintf("legacy-%d", i), fmt.Sprint(i)); err != nil {
				t.Fatalf("legacy squawk: %v", err)
			}
		}
		// The cutover renames those tables; the legacy reader follows.
		s, err := intercompg.New(ctx, pool, nil)
		if err != nil {
			t.Fatalf("intercompg.New: %v", err)
		}
		return intercomtest.Fixture{Store: s, Legacy: s.Legacy(), LegacyCount: legacy}
	})
}
