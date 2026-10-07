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
// later load changes nothing and serves the same members.
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
	alice, ok := jam.MemberByLogin(m, "acme", "auth0|a")
	if !ok || alice.User.ID != id || alice.Handle != "@alice" || alice.DiscordUID != "111" {
		t.Fatalf("acme alice = %+v, %v", alice, ok)
	}
	if d, ok := alice.Inbox("discord"); !ok || d != "inbox-a" {
		t.Fatalf("acme inbox = %q, %v", d, ok)
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
	if ms := (jam.ProjectMembers{Store: again}).Members("beta"); len(ms) != 1 || ms[0].User.Name != "alice" {
		t.Fatalf("reload: beta members = %+v", ms)
	}
	// AddPerson through the reloaded store persists to the registry.
	if err := jam.AddPerson(again, "beta", jam.Human{Name: "bob", Handle: "@bob"}); err != nil {
		t.Fatal(err)
	}
	if ms := (jam.ProjectMembers{Store: open()}).Members("beta"); len(ms) != 2 {
		t.Fatalf("after AddPerson + reload: beta members = %+v", ms)
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

// TestPostgresPolicyRefsMigration: a store at roster_schema 2 whose policy
// still names people "human:<name>" and whose personal session has no owner
// id is moved to user ids at load (step 3), once.
func TestPostgresPolicyRefsMigration(t *testing.T) {
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
	if err := s.CreateProject("acme"); err != nil {
		t.Fatal(err)
	}
	if err := jam.AddPerson(s, "acme", jam.Human{Name: "alice"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutRole("acme", jam.Role{Name: "impl", Scope: jam.Scope{Addressing: []string{"human:alice", "human:*"}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutInstance(jam.Instance{ActorID: "p1", Project: "acme", Role: "impl", Owner: "alice", SessionKind: jam.SessionKindPersonal}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool().Exec(ctx, `UPDATE jam_settings SET doc = '2' WHERE key = 'roster_schema'`); err != nil {
		t.Fatal(err)
	}
	alice, _ := s.LookupName(ident.User, "alice")

	m := open()
	if r, _ := m.GetRole("acme", "impl"); len(r.Scope.Addressing) != 2 || r.Scope.Addressing[0] != "user:"+string(alice) || r.Scope.Addressing[1] != "user:*" {
		t.Fatalf("addressing = %v", r.Scope.Addressing)
	}
	if inst, _ := m.GetInstance("p1"); inst.OwnerID != alice {
		t.Fatalf("owner id = %q, want %q", inst.OwnerID, alice)
	}
	if r, _ := open().GetRole("acme", "impl"); r.Scope.Addressing[0] != "user:"+string(alice) {
		t.Fatalf("reload: addressing = %v", r.Scope.Addressing)
	}
}

// TestPostgresRoomsMigration: a store at roster_schema 4 whose project doc
// still holds roster channels gets them as rooms at load, once; the doc drops
// them, and the rooms and a reload agree.
func TestPostgresRoomsMigration(t *testing.T) {
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
	if err := s.CreateProject("acme"); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`UPDATE jam_settings SET doc = '4' WHERE key = 'roster_schema'`,
		`UPDATE projects SET doc = jsonb_set(doc, '{roster}', '{"channels":[{"name":"eng","service":"linear","ref":"ACME-1"}]}') WHERE name = 'acme'`,
	} {
		if _, err := s.Pool().Exec(ctx, stmt); err != nil {
			t.Fatalf("seed %q: %v", stmt, err)
		}
	}
	m := open()
	p, _ := m.GetProject("acme")
	if got := jam.ListRooms(m, p); len(got) != 1 || got[0].Name != "eng" || got[0].Kind != "linear" || got[0].Ref != "ACME-1" {
		t.Fatalf("rooms = %+v", got)
	}
	rooms := m.ListChannels(p.ID, jam.SourceRoom)
	if len(rooms) != 1 {
		t.Fatalf("rooms = %+v", rooms)
	}
	var stored int
	if err := m.Pool().QueryRow(ctx, `SELECT count(*) FROM projects WHERE jsonb_array_length(coalesce(doc->'roster'->'channels', '[]')) > 0`).Scan(&stored); err != nil || stored != 0 {
		t.Fatalf("project docs still holding channels = %d, %v", stored, err)
	}
	again := open()
	if got := again.ListChannels(p.ID, jam.SourceRoom); len(got) != 1 || got[0].ID != rooms[0].ID {
		t.Fatalf("reload: rooms = %+v", got)
	}
	if err := again.JoinChannel(rooms[0].ID, "standing-acme-impl-spider", 4); err != nil {
		t.Fatalf("join a grandfathered session: %v", err)
	}
	if got := open().ChannelMembers(rooms[0].ID); len(got) != 1 {
		t.Fatalf("reload: members = %+v", got)
	}
	// A membership that ended at seq 0 stays ended across a reload.
	if err := again.LeaveChannel(rooms[0].ID, "standing-acme-impl-spider", 0); err != nil {
		t.Fatal(err)
	}
	if got := open().ChannelMembers(rooms[0].ID); len(got) != 0 {
		t.Fatalf("reload after leave: members = %+v", got)
	}
	if err := again.RemoveProject("acme"); err != nil {
		t.Fatalf("RemoveProject with rooms: %v", err)
	}
	if _, ok := open().GetChannel(rooms[0].ID); ok {
		t.Fatal("a removed project's rooms go with it")
	}
}

// TestPostgresProjectRefsMigration: a store from before 1b-2a — roles keyed
// by project name, grants and instances naming projects by name — loads with
// roles keyed by project id (migration 0013) and grants and instances naming
// projects by id (registry step 6), once.
func TestPostgresProjectRefsMigration(t *testing.T) {
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
	if err := s.CreateProject("acme"); err != nil {
		t.Fatal(err)
	}
	if err := s.PutRole("acme", jam.Role{Name: "impl"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddActor(jam.Actor{ID: "a1", TokenHash: "h1", Grants: []jam.Grant{{Project: "acme", Role: "impl"}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutInstance(jam.Instance{ActorID: "i1", Project: "acme", Role: "impl"}); err != nil {
		t.Fatal(err)
	}
	acme, _ := s.GetProject("acme")
	// Put the database back the way an older Jam left it.
	for _, stmt := range []string{
		`ALTER TABLE roles DROP CONSTRAINT roles_pkey`,
		`ALTER TABLE roles ADD COLUMN project text`,
		`UPDATE roles SET project = (SELECT name FROM projects WHERE id = roles.project_id)`,
		`ALTER TABLE roles DROP COLUMN project_id`,
		`ALTER TABLE roles ALTER COLUMN project SET NOT NULL`,
		`ALTER TABLE roles ADD PRIMARY KEY (project, name)`,
		`ALTER TABLE roles ADD CONSTRAINT roles_project_fkey FOREIGN KEY (project) REFERENCES projects (name)`,
		`DELETE FROM schema_migrations WHERE version = 13`,
		`UPDATE actors SET doc = jsonb_set(doc, '{grants,0,project}', '"acme"')`,
		`UPDATE instances SET doc = jsonb_set(doc, '{project}', '"acme"')`,
		`UPDATE jam_settings SET doc = '5' WHERE key = 'roster_schema'`,
	} {
		if _, err := s.Pool().Exec(ctx, stmt); err != nil {
			t.Fatalf("seed %q: %v", stmt, err)
		}
	}

	m := open()
	if _, ok := m.GetRole("acme", "impl"); !ok {
		t.Fatal("role lost")
	}
	if _, ok := m.GetRole(string(acme.ID), "impl"); !ok {
		t.Fatal("role not found by project id")
	}
	if a, ok := m.Lookup("h1"); !ok || a.Grants[0].Project != string(acme.ID) {
		t.Fatalf("grant = %+v", a.Grants)
	}
	if i, ok := m.GetInstance("i1"); !ok || i.Project != string(acme.ID) {
		t.Fatalf("instance = %+v", i)
	}
	var stored string
	if err := m.Pool().QueryRow(ctx, `SELECT doc->>'project' FROM instances WHERE actor_id = 'i1'`).Scan(&stored); err != nil || stored != string(acme.ID) {
		t.Fatalf("stored instance project = %q, %v", stored, err)
	}
	if again := open(); func() bool { _, ok := again.GetRole("acme", "impl"); return !ok }() {
		t.Fatal("reload lost the role")
	}
}
