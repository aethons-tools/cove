//go:build integration

package jam_test

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/storetest"
	"github.com/aethons-tools/cove/internal/kittree"
	"github.com/aethons-tools/cove/internal/kittree/kittreetest"
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
		`INSERT INTO participants (id, kind) VALUES ('prj_01j9q3aaaaaaaaaaaaaaaaaaaa', 'prj'), ('prj_01j9q3bbbbbbbbbbbbbbbbbbbb', 'prj')`,
		`INSERT INTO projects (name, id, doc) VALUES ('acme', 'prj_01j9q3aaaaaaaaaaaaaaaaaaaa', '{"name":"acme","id":"prj_01j9q3aaaaaaaaaaaaaaaaaaaa","roster":{"humans":[
			{"name":"alice","handle":"@alice","login":"auth0|a","delivery":[{"service":"discord","address":"inbox-a","user_id":"111"}]}]}}')`,
		`INSERT INTO projects (name, id, doc) VALUES ('beta', 'prj_01j9q3bbbbbbbbbbbbbbbbbbbb', '{"name":"beta","id":"prj_01j9q3bbbbbbbbbbbbbbbbbbbb","roster":{"humans":[{"name":"alice"}]}}')`,
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
		`INSERT INTO participants (id, kind) VALUES ('prj_01j9q3aaaaaaaaaaaaaaaaaaaa', 'prj')`,
		`INSERT INTO projects (name, id, doc) VALUES ('acme', 'prj_01j9q3aaaaaaaaaaaaaaaaaaaa', '{"name":"acme","id":"prj_01j9q3aaaaaaaaaaaaaaaaaaaa","roster":{},"chat_service":"discord"}')`,
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
	if got, ok := open().GetChannel(rooms[0].ID); !ok || got.Status != jam.StatusArchived {
		t.Fatalf("a removed project's rooms are archived: %+v, %v", got, ok)
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

// TestPostgresShareGrantsReload proves share grants survive a reload and that
// removals persist.
func TestPostgresShareGrantsReload(t *testing.T) {
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
		t.Fatal(err)
	}
	u, err := s.CreateUser(jam.User{Name: "carol"})
	if err != nil {
		t.Fatal(err)
	}
	exp := time.Unix(2_000_000, 0).UTC()
	g := jam.ShareGrant{SessionID: "ses_1", UserID: u.ID, Rights: []string{"preview", "ssh"}, ExpiresAt: exp, CreatedAt: exp.Add(-time.Hour), GrantedBy: "op:local"}
	if err := s.PutShareGrant(g); err != nil {
		t.Fatal(err)
	}
	again := open()
	if got := again.ShareGrants("ses_1"); len(got) != 1 || !reflect.DeepEqual(got[0], g) {
		t.Fatalf("reload: grants = %+v", got)
	}
	if err := again.RemoveShareGrantsOf("ses_1"); err != nil {
		t.Fatal(err)
	}
	if got := open().ShareGrants("ses_1"); len(got) != 0 {
		t.Fatalf("reload after remove: grants = %+v", got)
	}
}

// TestPostgresPushSubscriptionsReload proves push subscriptions survive a
// reload, that eviction and removal persist, and that RemoveUser drops them.
func TestPostgresPushSubscriptionsReload(t *testing.T) {
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
		t.Fatal(err)
	}
	u, err := s.CreateUser(jam.User{Name: "carol"})
	if err != nil {
		t.Fatal(err)
	}
	v, err := s.CreateUser(jam.User{Name: "dave"})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Unix(2_000_000, 0).UTC()
	p := jam.PushSubscription{Endpoint: "https://push/1", UserID: u.ID, P256dh: "k", Auth: "a", UserAgent: "ua", CreatedAt: at, LastOKAt: at}
	if err := s.PutPushSubscription(p); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < jam.MaxPushSubscriptionsPerUser; i++ { // pushes p out
		q := p
		q.Endpoint = fmt.Sprintf("https://push/more/%02d", i)
		q.CreatedAt = at.Add(time.Duration(i+1) * time.Minute)
		if err := s.PutPushSubscription(q); err != nil {
			t.Fatal(err)
		}
	}
	d := p
	d.Endpoint, d.UserID = "https://push/dave", v.ID
	if err := s.PutPushSubscription(d); err != nil {
		t.Fatal(err)
	}
	touched := at.Add(time.Hour)
	if err := s.TouchPushSubscription(d.Endpoint, touched); err != nil {
		t.Fatal(err)
	}
	again := open()
	if got := again.PushSubscriptions(u.ID); len(got) != jam.MaxPushSubscriptionsPerUser || got[0].Endpoint != "https://push/more/00" {
		t.Fatalf("reload: %d subs, first %+v", len(got), got[:1])
	}
	d.LastOKAt = touched
	if got := again.PushSubscriptions(v.ID); len(got) != 1 || !reflect.DeepEqual(got[0], d) {
		t.Fatalf("reload: dave = %+v", got)
	}
	if err := again.RemoveUser(u.ID); err != nil {
		t.Fatal(err)
	}
	third := open()
	if len(third.PushSubscriptions(u.ID)) != 0 || len(third.PushSubscriptions(v.ID)) != 1 {
		t.Fatal("reload after RemoveUser: wrong rows")
	}
	// A config write that tombstones a user drops their subscriptions too.
	tomb, _ := third.GetUser(v.ID)
	tomb.Status, tomb.Logins, tomb.OIDC = jam.StatusRemoved, nil, nil
	if err := third.ApplyConfig(jam.ConfigWrite{Users: []jam.User{tomb}}); err != nil {
		t.Fatal(err)
	}
	if len(third.PushSubscriptions(v.ID)) != 0 || len(open().PushSubscriptions(v.ID)) != 0 {
		t.Fatal("config-write user removal kept subscriptions")
	}
}

// TestPostgresKitSourceReload proves kit version provenance and the tracked
// source survive a reload.
func TestPostgresKitSourceReload(t *testing.T) {
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
		t.Fatal(err)
	}
	at := time.Unix(1_000_000, 0).UTC()
	vsrc := &jam.KitSource{Repo: "https://github.com/acme/kits", Dir: "web", Ref: "main", Commit: strings.Repeat("a", 40)}
	tr, blobs := kittreetest.Tree(t, "kit.yml", "kind: studio\n")
	if _, err := s.PushKitVersion("web", tr.Hash(), jam.KitObjects{Trees: []kittree.Tree{tr}, Blobs: blobs}, jam.KitVersionMeta{PushedAt: at, Source: vsrc}, nil); err != nil {
		t.Fatal(err)
	}
	tracked := &jam.KitSource{Repo: "https://github.com/acme/kits", Dir: "web", Ref: "main"}
	if err := s.SetKitSource("web", tracked); err != nil {
		t.Fatal(err)
	}
	k, ok := open().GetKit("web")
	if !ok {
		t.Fatal("kit missing after reload")
	}
	if got := k.Meta[1]; !got.PushedAt.Equal(at) || !reflect.DeepEqual(got.Source, vsrc) {
		t.Fatalf("reload meta = %+v", got)
	}
	if !reflect.DeepEqual(k.Source, tracked) {
		t.Fatalf("reload source = %+v", k.Source)
	}
}

// TestPostgresPutKitDocsAtomic proves a failing upsert rolls the whole batch
// back. The failure is injected with a NUL byte in the second kit's name:
// Postgres text cannot hold it, so that upsert errors after the first one ran
// inside the transaction (the kits table has no length limit to violate).
func TestPostgresPutKitDocsAtomic(t *testing.T) {
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
		t.Fatal(err)
	}
	if _, err := s.PushKit("existing", `{"kind":"studio"}`); err != nil {
		t.Fatal(err)
	}
	before, _ := s.GetKit("existing")
	changed := jam.Kit{Name: "existing", Current: 1, Versions: map[int]string{1: `{"kind":"studio","prompt":"new"}`}}
	bad := jam.Kit{Name: "bad\x00name", Current: 1, Versions: map[int]string{1: `{"kind":"studio"}`}}
	if err := s.PutKitDocs([]jam.Kit{changed, bad}, jam.KitObjects{}); err == nil {
		t.Fatal("PutKitDocs must fail when an upsert fails")
	}
	for label, st := range map[string]*jam.PostgresStore{"cache": s, "reopen": open()} {
		got, ok := st.GetKit("existing")
		if !ok || !reflect.DeepEqual(got, before) {
			t.Fatalf("%s: existing = %+v ok=%v, want %+v", label, got, ok, before)
		}
		if len(st.ListKits()) != 1 {
			t.Fatalf("%s: kits = %+v", label, st.ListKits())
		}
	}
	// And a good batch persists across reopen.
	ok1 := jam.Kit{Name: "k1", Current: 1, Versions: map[int]string{1: `{"kind":"studio"}`}}
	ok2 := jam.Kit{Name: "k2", Current: 1, Versions: map[int]string{1: `{"kind":"studio"}`}}
	if err := s.PutKitDocs([]jam.Kit{ok1, ok2}, jam.KitObjects{}); err != nil {
		t.Fatal(err)
	}
	re := open()
	for _, n := range []string{"k1", "k2"} {
		if _, ok := re.GetKit(n); !ok {
			t.Fatalf("%s missing after reopen", n)
		}
	}
}

// TestPostgresApplyConfigAtomic: a config write whose last statement fails
// (NUL in a role name) leaves nothing behind, in the cache or after a reopen;
// a good write survives a reopen, marks included.
func TestPostgresApplyConfigAtomic(t *testing.T) {
	dsn := os.Getenv("JAM_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set JAM_TEST_POSTGRES_DSN to run the Postgres store integration tests")
	}
	ctx := context.Background()
	s, err := jam.NewPostgresStore(ctx, dsn, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.TruncateAllForTest(ctx); err != nil {
		t.Fatal(err)
	}
	empty := func(st jam.Store) bool {
		return len(st.ListUsers())+len(st.ListKits())+len(st.ListDestinations())+len(st.ListConfigMarks())+len(st.ListProjects()) == 0
	}
	bad, _, _, pid := storetest.ConfigWriteFixture()
	bad.Roles = append(bad.Roles, jam.ConfigRoleWrite{ProjectID: pid, Role: jam.Role{Name: "bad\x00role"}})
	if err := s.ApplyConfig(bad); err == nil {
		t.Fatal("a NUL in a role name must fail the write")
	}
	if !empty(s) {
		t.Fatal("cache holds a partial write")
	}
	r, err := jam.NewPostgresStore(ctx, dsn, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !empty(r) {
		t.Fatal("database holds a partial write")
	}
	r.Close()

	good, _, _, _ := storetest.ConfigWriteFixture()
	if err := s.ApplyConfig(good); err != nil {
		t.Fatal(err)
	}
	r, err = jam.NewPostgresStore(ctx, dsn, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if marks := r.ListConfigMarks(); len(marks) != 2 {
		t.Fatalf("marks after reopen: %+v", marks)
	}
	if _, ok := r.GetRole("app", "ic"); !ok {
		t.Fatal("role lost on reopen")
	}
	// Tombstoning a project persists the same as the cache: archived
	// channels and the full tombstone, also after a reopen.
	p, _ := r.GetProject("app")
	room, _ := r.ChannelByKey(p.ID, jam.SourceRoom, "ops")
	if err := s.ApplyConfig(jam.ConfigWrite{Projects: []jam.Project{{ID: p.ID, Name: "app", Status: jam.StatusRemoved}}}); err != nil {
		t.Fatal(err)
	}
	r2, err := jam.NewPostgresStore(ctx, dsn, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Close()
	for _, st := range []jam.Store{s, r2} {
		tp, ok := st.GetProject(string(p.ID))
		if !ok || tp.Status != jam.StatusRemoved || tp.ChatService != p.ChatService {
			t.Fatalf("tombstone %+v", tp)
		}
		if c, _ := st.GetChannel(room.ID); c.Status != jam.StatusArchived {
			t.Fatalf("room %+v", c)
		}
	}
}

// TestPostgresConfigSourcePersists: the tracked source survives a reopen,
// whether set directly or by an apply, and a clear is durable.
func TestPostgresConfigSourcePersists(t *testing.T) {
	dsn := os.Getenv("JAM_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set JAM_TEST_POSTGRES_DSN to run the Postgres store integration tests")
	}
	ctx := context.Background()
	s, err := jam.NewPostgresStore(ctx, dsn, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.TruncateAllForTest(ctx); err != nil {
		t.Fatal(err)
	}
	src := jam.ConfigSource{Repo: "https://github.com/acme/jam", Dir: "cfg", Ref: "main", Commit: "c1", AppliedAt: time.Unix(300, 0).UTC()}
	if err := s.ApplyConfig(jam.ConfigWrite{ConfigSource: &src}); err != nil {
		t.Fatal(err)
	}
	r, err := jam.NewPostgresStore(ctx, dsn, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := r.GetConfigSource()
	r.Close()
	if !ok || got.Repo != src.Repo || got.Dir != "cfg" || got.Commit != "c1" || !got.AppliedAt.Equal(src.AppliedAt) {
		t.Fatalf("after reopen: %+v %v", got, ok)
	}
	if err := s.SetConfigSource(nil); err != nil {
		t.Fatal(err)
	}
	r, err = jam.NewPostgresStore(ctx, dsn, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, ok := r.GetConfigSource(); ok {
		t.Fatal("a cleared source came back on reopen")
	}
}

// TestPostgresKitObjectsFailedTxLeavesCache: a kit write whose transaction
// fails after its objects were inserted leaves neither the objects nor the
// docs, in the cache or after a reopen.
func TestPostgresKitObjectsFailedTxLeavesCache(t *testing.T) {
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
		t.Fatal(err)
	}
	tr, blobs := kittreetest.Tree(t, "kit.yml", "kind: studio\n", "run.sh*", "echo\n")
	objs := jam.KitObjects{Trees: []kittree.Tree{tr}, Blobs: blobs}
	good := jam.Kit{Name: "good", Current: 1, Trees: map[int]string{1: tr.Hash()}}
	bad := jam.Kit{Name: "bad\x00name", Current: 1, Trees: map[int]string{1: tr.Hash()}}
	if err := s.PutKitDocs([]jam.Kit{good, bad}, objs); err == nil {
		t.Fatal("PutKitDocs must fail when an upsert fails")
	}
	for label, st := range map[string]*jam.PostgresStore{"cache": s, "reopen": open()} {
		if _, ok := st.KitTree(tr.Hash()); ok {
			t.Fatalf("%s: the tree survived a failed write", label)
		}
		for sha := range blobs {
			if _, ok := st.KitBlob(sha); ok {
				t.Fatalf("%s: a blob survived a failed write", label)
			}
		}
		if _, ok := st.GetKit("good"); ok {
			t.Fatalf("%s: the doc survived a failed write", label)
		}
	}
}

// TestPostgresLoadRefusesCorruptKitObjects: a tree row whose entries do not
// hash to its key, or a blob row whose bytes or size do not match, stops the
// store from opening.
func TestPostgresLoadRefusesCorruptKitObjects(t *testing.T) {
	dsn := os.Getenv("JAM_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set JAM_TEST_POSTGRES_DSN to run the Postgres store integration tests")
	}
	ctx := context.Background()
	s, err := jam.NewPostgresStore(ctx, dsn, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	t.Cleanup(func() { _ = s.TruncateAllForTest(ctx) })
	for _, tc := range []struct{ name, corrupt, want string }{
		{"tree", `UPDATE kit_trees SET entries = '[]'`, "kit_trees"},
		{"blob bytes", `UPDATE kit_blobs SET bytes = 'tampered'::bytea`, "kit_blobs"},
		{"blob size", `UPDATE kit_blobs SET size = size + 1`, "kit_blobs"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := s.TruncateAllForTest(ctx); err != nil {
				t.Fatal(err)
			}
			if _, err := s.PushKit("app", "kind: studio\n"); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Pool().Exec(ctx, tc.corrupt); err != nil {
				t.Fatal(err)
			}
			re, err := jam.NewPostgresStore(ctx, dsn, nil)
			if err == nil {
				re.Close()
				t.Fatal("a corrupt kit object must refuse the open")
			}
			if !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "tampered") {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

// TestPostgresKitDraftSurvivesReopen: a draft row and its loose tree load
// back on open.
func TestPostgresKitDraftSurvivesReopen(t *testing.T) {
	dsn := os.Getenv("JAM_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set JAM_TEST_POSTGRES_DSN to run the Postgres store integration tests")
	}
	ctx := context.Background()
	s, err := jam.NewPostgresStore(ctx, dsn, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if err := s.TruncateAllForTest(ctx); err != nil {
		t.Fatal(err)
	}
	tr, blobs := kittreetest.Tree(t, "kit.yml", "kind: studio\n")
	if _, err := s.PushKitVersion("app", tr.Hash(), jam.KitObjects{Trees: []kittree.Tree{tr}, Blobs: blobs}, jam.KitVersionMeta{PushedAt: time.Unix(10, 0).UTC()}, nil); err != nil {
		t.Fatal(err)
	}
	dt, dblobs, err := kittree.BuildFiles(kittreetest.Files("notes.md", "draft\n"))
	if err != nil {
		t.Fatal(err)
	}
	d := jam.KitDraft{Kit: "app", Base: 1, Tree: dt.Hash(), OpenedAt: time.Unix(20, 0).UTC(), UpdatedAt: time.Unix(30, 0).UTC(), Actor: "local"}
	if err := s.PutKitDraft(d, jam.KitObjects{Trees: []kittree.Tree{dt}, Blobs: dblobs}); err != nil {
		t.Fatal(err)
	}
	again, err := jam.NewPostgresStore(ctx, dsn, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(again.Close)
	got, ok := again.GetKitDraft("app")
	if !ok || got.Tree != d.Tree || got.Base != 1 || got.Actor != "local" || !got.OpenedAt.Equal(d.OpenedAt) || !got.UpdatedAt.Equal(d.UpdatedAt) {
		t.Fatalf("reloaded draft = %+v %v", got, ok)
	}
	if _, ok := again.KitTree(dt.Hash()); !ok {
		t.Fatal("the draft tree was not loaded")
	}
}
