//go:build integration

package jam_test

import (
	"context"
	"os"
	"testing"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/storetest"
)

// TestPostgresStoreConformance runs the shared Store conformance suite against a
// real Postgres. Set JAM_TEST_POSTGRES_DSN (e.g.
// "host=localhost port=5432 dbname=jam user=jam password=jam sslmode=disable").
func TestPostgresStoreConformance(t *testing.T) {
	dsn := os.Getenv("JAM_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set JAM_TEST_POSTGRES_DSN to run the Postgres store integration tests")
	}
	storetest.RunConformance(t, func(t *testing.T) jam.Store {
		s, err := jam.NewPostgresStore(context.Background(), dsn, nil)
		if err != nil {
			t.Fatalf("NewPostgresStore: %v", err)
		}
		t.Cleanup(s.Close)
		if err := s.TruncateAllForTest(context.Background()); err != nil {
			t.Fatalf("TruncateAllForTest: %v", err)
		}
		return s
	})
}

// TestPostgresStoreFailsClosedOnBadDSN asserts the fail-closed startup invariant:
// an unreachable DSN yields an error, not a usable store.
func TestPostgresStoreFailsClosedOnBadDSN(t *testing.T) {
	if os.Getenv("JAM_TEST_POSTGRES_DSN") == "" {
		t.Skip("integration only")
	}
	if _, err := jam.NewPostgresStore(context.Background(),
		"host=127.0.0.1 port=1 dbname=nope user=nope password=x sslmode=disable connect_timeout=1", nil); err == nil {
		t.Fatal("NewPostgresStore must fail closed on an unreachable DSN")
	}
}

// TestPostgresProjectIDBackfill: a project row written before projects had ids
// gets one minted and persisted at load, and later loads keep that same id.
func TestPostgresProjectIDBackfill(t *testing.T) {
	dsn := os.Getenv("JAM_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set JAM_TEST_POSTGRES_DSN to run the Postgres store integration tests")
	}
	ctx := context.Background()
	open := func() *jam.PostgresStore {
		t.Helper()
		s, err := jam.NewPostgresStore(ctx, dsn, nil)
		if err != nil {
			t.Fatalf("NewPostgresStore: %v", err)
		}
		t.Cleanup(s.Close)
		return s
	}
	s := open()
	if err := s.TruncateAllForTest(ctx); err != nil {
		t.Fatalf("TruncateAllForTest: %v", err)
	}
	if _, err := s.Pool().Exec(ctx, `INSERT INTO projects (name, doc) VALUES ('legacy', '{"name":"legacy","roster":{}}')`); err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}
	p, ok := open().GetProject("legacy")
	if !ok || p.ID.Kind() != ident.Project {
		t.Fatalf("backfilled project = %+v, %v", p, ok)
	}
	again := open()
	if q, _ := again.GetProject("legacy"); q.ID != p.ID {
		t.Fatalf("id changed across loads: %q → %q", p.ID, q.ID)
	}
	if e, ok := again.Resolve(p.ID); !ok || e.Name != "legacy" {
		t.Fatalf("Resolve = %+v, %v", e, ok)
	}
}
