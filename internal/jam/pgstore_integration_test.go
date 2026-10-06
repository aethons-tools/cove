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

	// An older binary rewrote the doc without "id": the column's id wins.
	if _, err := again.Pool().Exec(ctx, `UPDATE projects SET doc = doc - 'id' WHERE name = 'legacy'`); err != nil {
		t.Fatalf("strip doc id: %v", err)
	}
	if q, _ := open().GetProject("legacy"); q.ID != p.ID {
		t.Fatalf("doc without id re-minted: %q, want the column's %q", q.ID, p.ID)
	}
}

// TestPostgresHumansMigration: project docs written before the registry carry
// roster humans; the first load migrates them into users, memberships,
// accounts and legacy aliases, clears the docs, and marks it done — so a
// later load changes nothing and serves the same roster view.
func TestPostgresHumansMigration(t *testing.T) {
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
	for _, stmt := range []string{
		`DELETE FROM jam_settings WHERE key = 'roster_schema'`,
		`INSERT INTO projects (name, doc) VALUES ('acme', '{"name":"acme","roster":{"humans":[
			{"name":"alice","handle":"@alice","login":"auth0|a","delivery":[{"service":"discord","address":"inbox-a","user_id":"111"}]}]}}')`,
		`INSERT INTO projects (name, doc) VALUES ('beta', '{"name":"beta","roster":{"humans":[{"name":"alice"}]}}')`,
	} {
		if _, err := s.Pool().Exec(ctx, stmt); err != nil {
			t.Fatalf("seed %q: %v", stmt, err)
		}
	}

	m := open()
	id, ok := m.LookupName(ident.User, "alice")
	if !ok || len(m.ListUsers()) != 1 {
		t.Fatalf("users = %+v", m.ListUsers())
	}
	r, _ := m.GetRoster("acme")
	if len(r.Humans) != 1 || r.Humans[0].Login != "auth0|a" || r.Humans[0].Handle != "@alice" {
		t.Fatalf("acme roster = %+v", r.Humans)
	}
	if d, ok := r.Humans[0].DeliveryFor("discord"); !ok || d.Address != "inbox-a" || d.UserID != "111" {
		t.Fatalf("acme delivery = %+v, %v", d, ok)
	}
	if got, ok := m.LegacyHumanAlias("beta", "alice"); !ok || got != id {
		t.Fatalf("beta alias = %q, %v", got, ok)
	}
	var stored int
	if err := m.Pool().QueryRow(ctx, `SELECT count(*) FROM projects WHERE jsonb_array_length(coalesce(doc->'roster'->'humans', '[]')) > 0`).Scan(&stored); err != nil || stored != 0 {
		t.Fatalf("project docs still holding humans = %d, %v", stored, err)
	}

	again := open()
	if got, _ := again.LookupName(ident.User, "alice"); got != id || len(again.ListUsers()) != 1 {
		t.Fatalf("reload: alice = %q, users %+v", got, again.ListUsers())
	}
	if r, _ := again.GetRoster("beta"); len(r.Humans) != 1 || r.Humans[0].Name != "alice" {
		t.Fatalf("reload: beta roster = %+v", r.Humans)
	}
	// AddHuman through the reloaded store persists to the registry, not the doc.
	if err := again.AddHuman("beta", jam.Human{Name: "bob", Handle: "@bob"}); err != nil {
		t.Fatal(err)
	}
	if r, _ := open().GetRoster("beta"); len(r.Humans) != 2 {
		t.Fatalf("after AddHuman + reload: beta roster = %+v", r.Humans)
	}
}

// TestPostgresChatServiceMigration: a store already at roster_schema 1 (humans
// migrated) whose project still names its chat service by kind is moved to
// the discord connection's id at load, once.
func TestPostgresChatServiceMigration(t *testing.T) {
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
	for _, stmt := range []string{
		`INSERT INTO jam_settings (key, doc) VALUES ('roster_schema', '1') ON CONFLICT (key) DO UPDATE SET doc = EXCLUDED.doc`,
		`INSERT INTO projects (name, doc) VALUES ('acme', '{"name":"acme","roster":{},"chat_service":"discord"}')`,
	} {
		if _, err := s.Pool().Exec(ctx, stmt); err != nil {
			t.Fatalf("seed %q: %v", stmt, err)
		}
	}
	m := open()
	p, _ := m.GetProject("acme")
	if jam.ChatKind(m, p) != "discord" {
		t.Fatalf("chat service = %q, want the discord connection", p.ChatService)
	}
	again := open()
	if q, _ := again.GetProject("acme"); q.ChatService != p.ChatService || len(again.ListConnections()) != 1 {
		t.Fatalf("reload: chat service %q, connections %+v", q.ChatService, again.ListConnections())
	}
}
