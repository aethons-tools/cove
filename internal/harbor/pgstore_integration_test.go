//go:build integration

package harbor_test

import (
	"context"
	"os"
	"testing"

	"github.com/aethons-tools/cove/internal/harbor"
	"github.com/aethons-tools/cove/internal/harbor/storetest"
)

// TestPostgresStoreConformance runs the shared Store conformance suite against a
// real Postgres. Set HARBOR_TEST_POSTGRES_DSN (e.g.
// "host=localhost port=5432 dbname=harbor user=harbor password=harbor sslmode=disable").
func TestPostgresStoreConformance(t *testing.T) {
	dsn := os.Getenv("HARBOR_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set HARBOR_TEST_POSTGRES_DSN to run the Postgres store integration tests")
	}
	storetest.RunConformance(t, func(t *testing.T) harbor.Store {
		s, err := harbor.NewPostgresStore(context.Background(), dsn, nil)
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
	if os.Getenv("HARBOR_TEST_POSTGRES_DSN") == "" {
		t.Skip("integration only")
	}
	if _, err := harbor.NewPostgresStore(context.Background(),
		"host=127.0.0.1 port=1 dbname=nope user=nope password=x sslmode=disable connect_timeout=1", nil); err == nil {
		t.Fatal("NewPostgresStore must fail closed on an unreachable DSN")
	}
}
