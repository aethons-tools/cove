//go:build integration

package jam_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
)

// pgImportStore opens a truncated real-Postgres store (same harness as
// TestPostgresStoreConformance) or skips when no DSN is configured.
func pgImportStore(t *testing.T) (*jam.PostgresStore, string) {
	t.Helper()
	dsn := os.Getenv("JAM_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set JAM_TEST_POSTGRES_DSN to run the Postgres store integration tests")
	}
	s, err := jam.NewPostgresStore(context.Background(), dsn, nil)
	if err != nil {
		t.Fatalf("NewPostgresStore: %v", err)
	}
	t.Cleanup(s.Close)
	if err := s.TruncateAllForTest(context.Background()); err != nil {
		t.Fatalf("TruncateAllForTest: %v", err)
	}
	return s, dsn
}

func importSnapshot(t *testing.T) jam.ConfigSnapshot {
	t.Helper()
	fs := jam.NewMemStore()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(fs.PutRole(jam.DefaultProject, jam.Role{Name: "guest", Scope: jam.Scope{Destinations: []string{"anthropic"}}}))
	must(fs.AddActor(jam.Actor{ID: "spider-18", TokenHash: jam.HashToken("tok"), Grants: []jam.Grant{{Project: jam.DefaultProject, Role: "guest"}}}))
	_, err := fs.PushKit("base", "image: x")
	must(err)
	must(fs.AddDestination(jam.Destination{Name: "anthropic", Route: "/v1", Upstream: "https://api"}))
	must(fs.AddHuman(jam.DefaultProject, jam.Human{Name: "alice", Handle: "@alice"}))
	return fs.ExportConfig()
}

func TestPGImportConfigRoundTrip(t *testing.T) {
	snap := importSnapshot(t)
	pg, dsn := pgImportStore(t)
	if err := pg.ImportConfig(snap); err != nil {
		t.Fatalf("ImportConfig: %v", err)
	}
	if a, ok := pg.Lookup(jam.HashToken("tok")); !ok || a.ID != "spider-18" {
		t.Fatalf("Lookup after import = %+v ok=%v", a, ok)
	}
	// Durable: a fresh store over the same DB reloads the imported config.
	pg2, err := jam.NewPostgresStore(context.Background(), dsn, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer pg2.Close()
	if _, ok := pg2.GetRole(jam.DefaultProject, "guest"); !ok {
		t.Fatal("role not durable after reload")
	}
	if a, ok := pg2.Lookup(jam.HashToken("tok")); !ok || a.ID != "spider-18" {
		t.Fatalf("actor not durable after reload: %+v ok=%v", a, ok)
	}
}

func TestPGImportConfigRefusesWhenNotEmpty(t *testing.T) {
	snap := importSnapshot(t)
	pg, _ := pgImportStore(t)
	if err := pg.AddDestination(jam.Destination{Name: "d", Route: "/r", Upstream: "u"}); err != nil {
		t.Fatal(err)
	}
	if err := pg.ImportConfig(snap); !errors.Is(err, jam.ErrConfigNotEmpty) {
		t.Fatalf("err = %v, want ErrConfigNotEmpty", err)
	}
}
