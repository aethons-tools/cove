package adminclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
)

// aliveLauncher is a scripted jam.Launcher for driving the cove routes in
// this package's tests without a real backend. It always reports the instance
// as alive and returns a synthetic location.
type aliveLauncher struct{}

func (aliveLauncher) Raise(context.Context, jam.RaiseSpec, jam.LaunchCreds) (string, error) {
	return "fake", nil
}
func (aliveLauncher) Teardown(context.Context, jam.Instance) error { return nil }
func (aliveLauncher) Probe(context.Context, jam.Instance) (jam.Liveness, error) {
	return jam.LivenessAlive, nil
}
func (aliveLauncher) Pause(context.Context, jam.Instance) error   { return nil }
func (aliveLauncher) Unpause(context.Context, jam.Instance) error { return nil }
func (aliveLauncher) ApplyEgress(context.Context, jam.Instance, *jam.EgressPolicy) error {
	return nil
}
func (aliveLauncher) PrepareKit(context.Context, jam.KitDefinition) (jam.KitStatus, error) {
	return jam.KitStatus{State: jam.KitReady}, nil
}

func newServer(t *testing.T) (*httptest.Server, jam.Store) {
	t.Helper()
	store := jam.NewMemStore()
	if err := store.PutRole(jam.DefaultProject, jam.Role{Name: "guest", Scope: jam.Scope{Destinations: []string{"anthropic"}, TTL: time.Hour}}); err != nil {
		t.Fatal(err)
	}
	sup := jam.NewSupervisor(store, aliveLauncher{}, "holder-test", time.Minute, 30*time.Second, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	h := jam.NewAdminHandler(store, sup, nil, jam.LoopbackAuthenticator{}, func(n string) bool { return n == "git-pat" }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	ts := httptest.NewServer(h) // listens on 127.0.0.1 → passes the loopback authenticator
	t.Cleanup(ts.Close)
	return ts, store
}

// mustCreateProject records project name directly on the server's store: every
// project-scoped write needs its project to exist first.
func mustCreateProject(t *testing.T, store jam.Store, name string) {
	t.Helper()
	if err := store.CreateProject(name); err != nil {
		t.Fatalf("CreateProject(%q): %v", name, err)
	}
}

// TestClientRoundTrip exercises ListDestinations, Enroll and Revoke against a
// real Jam admin handler + MemStore (not just a wire-format mock), proving
// the client's requests actually drive store side effects end to end. Scope
// now comes entirely from the role, so the role must be put before Enroll.
func TestClientRoundTrip(t *testing.T) {
	ts, store := newServer(t)
	c := New(ts.URL, "")

	if err := store.AddDestination(jam.Destination{Name: "git", Route: "/git/", Upstream: "https://github.com", IdentityIn: jam.ApplyBasicPassword, CredName: "git-pat", Apply: jam.ApplyBasicPassword}); err != nil {
		t.Fatalf("AddDestination: %v", err)
	}
	ds, err := c.ListDestinations()
	if err != nil || len(ds) != 1 || ds[0].Name != "git" {
		t.Fatalf("ListDestinations = %+v, %v", ds, err)
	}
	mustCreateProject(t, store, "ACME")
	if err := store.PutRole("ACME", jam.Role{Name: "guest", Scope: jam.Scope{Destinations: []string{"git"}}}); err != nil {
		t.Fatalf("PutRole: %v", err)
	}
	res, err := c.Enroll(EnrollParams{ID: "spider-18", Project: "ACME", Role: "guest"})
	if err != nil || res.Token == "" {
		t.Fatalf("Enroll = %+v, %v", res, err)
	}
	if _, ok := store.Lookup(jam.HashToken(res.Token)); !ok {
		t.Fatal("identity not stored")
	}
	if err := c.Revoke("spider-18"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if _, ok := store.Lookup(jam.HashToken(res.Token)); ok {
		t.Fatal("identity present after Revoke")
	}
}

func TestClientLoginConfig(t *testing.T) {
	store := jam.NewMemStore()
	lc := &jam.OperatorLoginConfig{Issuer: "https://acme.auth0.com/", Audience: "https://jam.acme/api", ClientID: "cid", Scope: "openid"}
	h := jam.NewAdminHandler(store, nil, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, lc, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	ts := httptest.NewServer(h)
	defer ts.Close()

	got, err := New(ts.URL, "").LoginConfig()
	if err != nil {
		t.Fatalf("LoginConfig: %v", err)
	}
	if got != *lc {
		t.Fatalf("LoginConfig = %+v, want %+v", got, *lc)
	}
}

func TestClientSendsBearer(t *testing.T) {
	var gotAuth string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(200)
		w.Write([]byte("[]"))
	}))
	defer ts.Close()
	if _, err := New(ts.URL, "tok-123").ListDestinations(); err != nil {
		t.Fatalf("ListDestinations: %v", err)
	}
	if gotAuth != "Bearer tok-123" {
		t.Fatalf("Authorization = %q, want Bearer tok-123", gotAuth)
	}
}

func TestClientRoleAndGrantRoundTrips(t *testing.T) {
	var gotPath, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.RequestURI()
		switch {
		case r.URL.Path == "/admin/roles" && r.Method == "GET":
			_, _ = w.Write([]byte(`[{"project":"acme","name":"guest","destinations":["anthropic"],"credentials":{"anthropic":"anthropic-sub"},"repos":["acme/*"],"ttl_seconds":3600}]`))
		case r.URL.Path == "/admin/projects":
			_, _ = w.Write([]byte(`["acme"]`))
		case r.URL.Path == "/admin/actors":
			_, _ = w.Write([]byte(`[{"id":"m","grants":[{"project":"acme","role":"guest","destinations":["anthropic"],"repos":["acme/*"]}]}]`))
		default:
			w.WriteHeader(http.StatusCreated)
		}
	}))
	defer srv.Close()
	c := New(srv.URL, "")

	roles, err := c.ListRoles("acme")
	if err != nil || len(roles) != 1 || roles[0].Scope.TTL != time.Hour || roles[0].Scope.Credentials["anthropic"] != "anthropic-sub" {
		t.Fatalf("ListRoles = %+v, %v", roles, err)
	}
	if gotPath != "/admin/roles?project=acme" {
		t.Fatalf("ListRoles path = %s", gotPath)
	}

	projs, err := c.ListProjects()
	if err != nil || len(projs) != 1 || projs[0] != "acme" {
		t.Fatalf("ListProjects = %+v, %v", projs, err)
	}

	roster, err := c.Actors()
	if err != nil {
		t.Fatalf("Roster: %v", err)
	}
	if len(roster) != 1 || roster[0].ID != "m" || len(roster[0].Grants) != 1 {
		t.Fatalf("Roster = %+v", roster)
	}

	if err := c.AddGrant("m", jam.Grant{Project: "beta", Role: "review"}); err != nil {
		t.Fatalf("AddGrant: %v", err)
	}
	if gotMethod != "POST" || gotPath != "/admin/actors/m/grants" {
		t.Fatalf("AddGrant wire = %s %s", gotMethod, gotPath)
	}

	if err := c.RemoveGrant("m", "beta", "review"); err != nil {
		t.Fatalf("RemoveGrant: %v", err)
	}
	if gotMethod != "DELETE" || gotPath != "/admin/actors/m/grants/beta/review" {
		t.Fatalf("RemoveGrant wire = %s %s", gotMethod, gotPath)
	}
}

// TestClientRosterAndAddressing exercises users + members, rooms and
// role Addressing round-trips against a real Jam
// admin handler + MemStore (not just a wire-format mock).
func TestClientRosterAndAddressing(t *testing.T) {
	ts, store := newServer(t)
	mustCreateProject(t, store, "acme")
	c := New(ts.URL, "")

	alice, err := store.CreateUser(jam.User{Name: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	acme, _ := store.GetProject("acme")
	if err := store.PutMembership(jam.Membership{ProjectID: acme.ID, UserID: alice.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateConnection(jam.Connection{Kind: "linear", Name: "linear"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := jam.PutRoom(store, "acme", jam.RoomBody{Name: "eng-help", Ref: "ACME-1"}); err != nil {
		t.Fatalf("PutRoom: %v", err)
	}
	if rooms, err := c.ListRooms("acme"); err != nil || len(rooms) != 1 || rooms[0].Name != "eng-help" || rooms[0].Connection != "linear" {
		t.Fatalf("ListRooms = %+v, %v", rooms, err)
	}
	if ms, err := c.ListMembers("acme"); err != nil || len(ms) != 1 || ms[0].User != "alice" {
		t.Fatalf("ListMembers = %+v, %v", ms, err)
	}

	if err := jam.RemoveRoom(store, "acme", "eng-help"); err != nil {
		t.Fatalf("RemoveRoom: %v", err)
	}
	if rooms, err := c.ListRooms("acme"); err != nil || len(rooms) != 0 {
		t.Fatalf("rooms after removal = %+v, %v", rooms, err)
	}

	// A role's Scope.Addressing round-trips via ListRoles.
	if err := store.PutRole("acme", jam.Role{Name: "impl", Scope: jam.Scope{Addressing: []string{"human:*", "channel:eng-help"}}}); err != nil {
		t.Fatalf("PutRole: %v", err)
	}
	roles, err := c.ListRoles("acme")
	if err != nil {
		t.Fatalf("ListRoles: %v", err)
	}
	var got []string
	for _, r := range roles {
		if r.Name == "impl" {
			got = r.Scope.Addressing
		}
	}
	if len(got) != 2 || got[0] != "human:*" || got[1] != "channel:eng-help" {
		t.Fatalf("role addressing = %+v, roles=%+v", got, roles)
	}
	// A role's Allocation round-trips via ListRoles.
	if err := store.PutRole("acme", jam.Role{Name: "worker", Allocation: jam.RoleAllocation{MaxEphemeral: 4, MaxPersonal: 3, MaxPersonalPerOwner: 1, IdleAfter: time.Hour, NagEvery: 2 * time.Hour, ReclaimAfter: 72 * time.Hour}}); err != nil {
		t.Fatalf("PutRole: %v", err)
	}
	roles, err = c.ListRoles("acme")
	if err != nil {
		t.Fatalf("ListRoles: %v", err)
	}
	var alloc jam.RoleAllocation
	for _, r := range roles {
		if r.Name == "worker" {
			alloc = r.Allocation
		}
	}
	if want := (jam.RoleAllocation{MaxEphemeral: 4, MaxPersonal: 3, MaxPersonalPerOwner: 1, IdleAfter: time.Hour, NagEvery: 2 * time.Hour, ReclaimAfter: 72 * time.Hour}); !reflect.DeepEqual(alloc, want) {
		t.Fatalf("role allocation = %+v, want %+v; roles=%+v", alloc, want, roles)
	}
}

// TestClientEscalationPolicy exercises GetEscalationPolicy (seeded through the store)
// against a real Jam admin handler + MemStore.
func TestClientEscalationPolicy(t *testing.T) {
	ts, store := newServer(t)
	mustCreateProject(t, store, "acme")
	c := New(ts.URL, "")

	tiers := []jam.EscalationTier{
		{Targets: []string{"human:alice", "human:bob"}, Timeout: 15 * time.Minute},
		{Targets: []string{"human:carol"}, Timeout: time.Hour},
	}
	if err := store.SetEscalationPolicy("acme", "", tiers); err != nil {
		t.Fatalf("SetEscalationPolicy: %v", err)
	}
	v, err := c.GetEscalationPolicy("acme")
	if err != nil {
		t.Fatalf("GetEscalationPolicy: %v", err)
	}
	got := v.Default
	if len(got) != 2 || got[0].Targets[0] != "human:alice" || got[0].Targets[1] != "human:bob" || got[0].Timeout != 15*time.Minute {
		t.Fatalf("tier 0 = %+v", got[0])
	}
	if got[1].Targets[0] != "human:carol" || got[1].Timeout != time.Hour {
		t.Fatalf("tier 1 = %+v", got[1])
	}
}

// TestClientEscalationCategory exercises GetEscalationPolicy (seeded through the store)
// with a category, asserting the view carries both the default and the category
// chain without disturbing each other.
func TestClientEscalationCategory(t *testing.T) {
	ts, store := newServer(t)
	mustCreateProject(t, store, "acme")
	c := New(ts.URL, "")

	def := []jam.EscalationTier{{Targets: []string{"human:oncall"}, Timeout: 30 * time.Minute}}
	infra := []jam.EscalationTier{{Targets: []string{"human:sre"}, Timeout: 10 * time.Minute}}
	if err := store.SetEscalationPolicy("acme", "", def); err != nil {
		t.Fatalf("SetEscalationPolicy(default): %v", err)
	}
	if err := store.SetEscalationPolicy("acme", "infra", infra); err != nil {
		t.Fatalf("SetEscalationPolicy(infra): %v", err)
	}
	v, err := c.GetEscalationPolicy("acme")
	if err != nil {
		t.Fatalf("GetEscalationPolicy: %v", err)
	}
	if len(v.Default) != 1 || v.Default[0].Targets[0] != "human:oncall" {
		t.Fatalf("default chain = %+v", v.Default)
	}
	got := v.ByCategory["infra"]
	if len(got) != 1 || got[0].Targets[0] != "human:sre" || got[0].Timeout != 10*time.Minute {
		t.Fatalf("infra chain = %+v", v.ByCategory)
	}
}

// TestClientChatService exercises GetChatService (seeded through the store; set,
// get, clear) against a real Jam admin handler + MemStore.
func TestClientChatService(t *testing.T) {
	ts, store := newServer(t)
	mustCreateProject(t, store, "acme")
	c := New(ts.URL, "")

	if err := store.SetChatService("acme", "discord"); err != nil {
		t.Fatalf("SetChatService: %v", err)
	}
	svc, err := c.GetChatService("acme")
	if err != nil {
		t.Fatalf("GetChatService: %v", err)
	}
	if svc != "discord" {
		t.Fatalf("GetChatService = %q, want discord", svc)
	}

	if err := store.SetChatService("acme", ""); err != nil {
		t.Fatalf("SetChatService (clear): %v", err)
	}
	svc, err = c.GetChatService("acme")
	if err != nil {
		t.Fatalf("GetChatService after clear: %v", err)
	}
	if svc != "" {
		t.Fatalf("GetChatService after clear = %q, want empty", svc)
	}
}

// TestClientUsersMembersAccounts round-trips the registry routes: a user
// with a login, a member with delivery, an account linked and unlinked.
func TestClientUsersMembersAccounts(t *testing.T) {
	ts, store := newServer(t)
	mustCreateProject(t, store, "acme")
	if _, err := store.CreateConnection(jam.Connection{Kind: "discord", Name: "discord"}); err != nil {
		t.Fatal(err)
	}
	c := New(ts.URL, "")
	u, err := store.CreateUser(jam.User{Name: "david", Logins: []string{"auth0|d"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SetUserOIDC("david", []jam.OIDCIdentity{{Issuer: "i", Subject: "s"}}); err != nil {
		t.Fatalf("SetUserOIDC: %v", err)
	}
	if err := store.SetUserLogins(u.ID, nil); err != nil {
		t.Fatal(err)
	}
	acme, _ := store.GetProject("acme")
	if err := store.PutMembership(jam.Membership{ProjectID: acme.ID, UserID: u.ID, Delivery: []jam.DeliveryProfile{{Service: "discord", Address: "chan-9"}}}); err != nil {
		t.Fatal(err)
	}
	ms, err := c.ListMembers("acme")
	if err != nil || len(ms) != 1 || ms[0].Delivery[0].Address != "chan-9" {
		t.Fatalf("ListMembers = %+v, %v", ms, err)
	}
	a, err := c.AddAccount(jam.AccountBody{Connection: "discord", ServiceUID: "123", User: "david"})
	if err != nil || a.User != "david" {
		t.Fatalf("AddAccount = %+v, %v", a, err)
	}
	if err := c.UnlinkAccount(string(a.ID)); err != nil {
		t.Fatalf("UnlinkAccount: %v", err)
	}
	if err := c.LinkAccount(string(a.ID), "david"); err != nil {
		t.Fatalf("LinkAccount: %v", err)
	}
	got, err := c.GetUser("david")
	if err != nil || len(got.Accounts) != 1 || len(got.OIDC) != 1 || len(got.Logins) != 0 || got.Projects[0] != "acme" {
		t.Fatalf("GetUser = %+v, %v", got, err)
	}
	if conns, err := c.ListConnections(); err != nil || len(conns) != 1 {
		t.Fatalf("ListConnections = %+v, %v", conns, err)
	}
	if accs, err := c.ListAccounts("discord"); err != nil || len(accs) != 1 {
		t.Fatalf("ListAccounts = %+v, %v", accs, err)
	}
	if users, err := c.ListUsers(); err != nil || len(users) != 1 {
		t.Fatalf("ListUsers = %+v, %v", users, err)
	}
}

// TestClientEnrollBodyIsTrimmed proves Enroll's wire body carries only
// id/project/role/overrides — no inline destinations/repos/ttl_seconds, since
// scope now comes entirely from the role.
func TestClientEnrollBodyIsTrimmed(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","token":"tok"}`))
	}))
	defer srv.Close()
	c := New(srv.URL, "")

	if _, err := c.Enroll(EnrollParams{ID: "x", Project: "acme", Role: "guest"}); err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	for _, field := range []string{"destinations", "repos", "ttl_seconds"} {
		if strings.Contains(gotBody, field) {
			t.Fatalf("Enroll body still contains %q: %s", field, gotBody)
		}
	}
}

func TestClientKitRoundTrips(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.RequestURI()
		switch {
		case r.URL.Path == "/admin/kits" && r.Method == "GET":
			_, _ = w.Write([]byte(`[{"name":"web","current":3,"versions":3}]`))
		case r.URL.Path == "/admin/kits/web/versions":
			_, _ = w.Write([]byte(`[1,2,3]`))
		case r.URL.Path == "/admin/kits/web":
			_, _ = w.Write([]byte(`{"name":"web","version":2,"config":"name: web\n"}`))
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer srv.Close()
	c := New(srv.URL, "")

	if kits, err := c.ListKits(); err != nil || len(kits) != 1 || kits[0].Current != 3 {
		t.Fatalf("ListKits = %+v, %v", kits, err)
	}
	if got, err := c.GetKit("web", 2); err != nil || got.Version != 2 {
		t.Fatalf("GetKit = %+v, %v", got, err)
	}
	if gotPath != "/admin/kits/web?version=2" {
		t.Fatalf("GetKit path = %s", gotPath)
	}
	if vers, err := c.KitVersions("web"); err != nil || len(vers) != 3 {
		t.Fatalf("KitVersions = %+v, %v", vers, err)
	}
}

func TestCoveClientRoundTrip(t *testing.T) {
	srv, store := newServer(t) // existing helper: httptest server over a real admin handler
	defer srv.Close()
	// newServer must build the handler WITH a supervisor for cove routes — see note below.
	c := New(srv.URL, "")

	res, err := c.RaiseCove(CoveRaiseParams{ID: "w1", Role: "guest", Unit: "AET-3"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Token == "" || res.Phase != "live" {
		t.Fatalf("raise result = %+v", res)
	}
	coves, err := c.ListCoves()
	if err != nil {
		t.Fatal(err)
	}
	if len(coves) != 1 || coves[0].ID != res.ID || coves[0].Name != "w1" {
		t.Fatalf("list = %+v", coves)
	}
	if err := c.ReportCoveStatus("w1", "blocked"); err != nil {
		t.Fatal(err)
	}
	if inst, _ := store.GetInstance(res.ID); inst.Activity != jam.ActivityBlocked {
		t.Fatalf("activity = %s", inst.Activity)
	}
	if err := c.TeardownCove("w1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.GetInstance("w1"); ok {
		t.Fatal("instance present after teardown")
	}
}

// grantAll is a jam.SessionAllocator that admits every personal request.
type grantAll struct{ releases int }

func (*grantAll) GrantPersonal(context.Context, string, string, string, string) (bool, error) {
	return true, nil
}
func (*grantAll) GrantEphemeral(context.Context, string, string, string) (bool, error) {
	return true, nil
}
func (g *grantAll) RecordRelease(context.Context, string, string, string) error {
	g.releases++
	return nil
}

// TestClientPersonalSessionRoundTrip requests, lists and releases a personal
// session through the client against a real admin handler. The loopback
// operator is "local", so the roster human is linked to that login.
func TestClientPersonalSessionRoundTrip(t *testing.T) {
	store := jam.NewMemStore()
	mustCreateProject(t, store, "acme")
	if err := store.PutRole("acme", jam.Role{Name: "pair", Scope: jam.Scope{Destinations: []string{"anthropic"}, TTL: time.Hour}}); err != nil {
		t.Fatal(err)
	}
	// A personal session is delivered over Discord: the project's chat service
	// and the owner's delivery profile must both be set.
	if err := jam.AddPerson(store, "acme", jam.Human{Name: "alice", Handle: "@alice", Login: "local", Delivery: []jam.DeliveryProfile{{Service: "discord", Address: "111"}}}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetChatService("acme", "discord"); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	sup := jam.NewSupervisor(store, aliveLauncher{}, "holder-test", time.Minute, 30*time.Second, nil, log)
	h := jam.NewAdminHandler(store, sup, &grantAll{}, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, log, nil)
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	c := New(ts.URL, "")

	res, err := c.RequestPersonalSession(jam.PersonalSessionBody{Project: "acme", Role: "pair", Prompt: "help me"})
	if err != nil {
		t.Fatalf("RequestPersonalSession: %v", err)
	}
	if !strings.HasPrefix(res.ID, "ses_") || res.Owner != "alice" {
		t.Fatalf("result = %+v", res)
	}
	list, err := c.ListPersonalSessions("acme")
	if err != nil || len(list) != 1 || list[0].ID != res.ID {
		t.Fatalf("ListPersonalSessions = %+v, %v", list, err)
	}
	if err := c.ReleasePersonalSession(res.ID); err != nil {
		t.Fatalf("ReleasePersonalSession: %v", err)
	}
	if list, err := c.ListPersonalSessions("acme"); err != nil || len(list) != 0 {
		t.Fatalf("after release = %+v, %v", list, err)
	}
	if err := c.ReleasePersonalSession(res.ID); err == nil {
		t.Fatal("releasing a released session must error (404)")
	}
}

// TestClientListStanding lists the standing sessions declared through the
// store (they change only in git).
func TestClientListStanding(t *testing.T) {
	ts, store := newServer(t)
	c := New(ts.URL, "")

	if err := jam.AddStanding(store, jam.DefaultProject, "guest", jam.StandingSession{Name: "alice-bot", Prompt: "review PRs"}); err != nil {
		t.Fatalf("AddStanding: %v", err)
	}
	list, err := c.ListStanding(jam.DefaultProject, "guest")
	if err != nil || len(list) != 1 || list[0] != (jam.StandingStatus{StandingSession: jam.StandingSession{Name: "alice-bot", Prompt: "review PRs"}}) {
		t.Fatalf("ListStanding = %+v, %v", list, err)
	}
	if _, err := c.ResetStanding(jam.DefaultProject, "guest", "nobody"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("reset of an undeclared name = %v, want ErrNotFound", err)
	}
	if _, err := c.ListStanding(jam.DefaultProject, "nobody"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown role = %v, want ErrNotFound", err)
	}
}

// resetterFunc adapts a func to jam.StandingResetter.
type resetterFunc func() error

func (f resetterFunc) ResetStanding(context.Context, string, string, string) error { return f() }

// TestClientResetStanding: a done reset is Pending=false, one the reconciler
// is still finishing Pending=true with its reason; the declaration is kept.
func TestClientResetStanding(t *testing.T) {
	store := jam.NewMemStore()
	if err := store.PutRole(jam.DefaultProject, jam.Role{Name: "guest", Allocation: jam.RoleAllocation{Standing: []jam.StandingSession{{Name: "bot", Prompt: "p"}}}}); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	sup := jam.NewSupervisor(store, aliveLauncher{}, "holder-test", time.Minute, 30*time.Second, nil, log)
	var resetErr error
	sup.SetStandingResetter(resetterFunc(func() error { return resetErr }))
	ts := httptest.NewServer(jam.NewAdminHandler(store, sup, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, log, nil))
	t.Cleanup(ts.Close)
	c := New(ts.URL, "")

	if res, err := c.ResetStanding(jam.DefaultProject, "guest", "bot"); err != nil || res.Pending {
		t.Fatalf("ResetStanding = %+v, %v", res, err)
	}
	resetErr = errors.New("volume in use")
	if res, err := c.ResetStanding(jam.DefaultProject, "guest", "bot"); err != nil || !res.Pending || !strings.Contains(res.Reason, "volume in use") {
		t.Fatalf("pending ResetStanding = %+v, %v", res, err)
	}
	if list, _ := c.ListStanding(jam.DefaultProject, "guest"); len(list) != 1 {
		t.Fatalf("reset must keep the declaration; list = %+v", list)
	}
}

// queueUpgrader is a jam.StandingUpgrader queue that records the last force,
// fails with err, and reports state for every name.
type queueUpgrader struct {
	force *bool
	err   error
	state string
}

func (q *queueUpgrader) QueueUpgrade(_, _, _ string, force bool) error {
	if q.err != nil {
		return q.err
	}
	q.force, q.state = &force, jam.UpgradeQueued
	return nil
}

func (q *queueUpgrader) UpgradeState(string, string, string) string { return q.state }

// TestClientUpgradeStanding: an upgrade is queued (Pending, State queued) with
// the force flag reaching the server; a pending reset is ErrConflict; an
// undeclared name is ErrNotFound; ListStanding carries the upgrade state.
func TestClientUpgradeStanding(t *testing.T) {
	store := jam.NewMemStore()
	if err := store.PutRole(jam.DefaultProject, jam.Role{Name: "guest", Allocation: jam.RoleAllocation{Standing: []jam.StandingSession{{Name: "bot", Prompt: "p"}}}}); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	sup := jam.NewSupervisor(store, aliveLauncher{}, "holder-test", time.Minute, 30*time.Second, nil, log)
	q := &queueUpgrader{}
	sup.SetStandingUpgrader(q)
	jam.SeedStandingSession(store, jam.DefaultProject, "guest", "bot") // a session to upgrade (down)
	ts := httptest.NewServer(jam.NewAdminHandler(store, sup, nil, jam.LoopbackAuthenticator{}, func(string) bool { return true }, nil, log, nil))
	t.Cleanup(ts.Close)
	c := New(ts.URL, "")

	if res, err := c.UpgradeStanding(jam.DefaultProject, "guest", "bot", false); err != nil || !res.Pending || res.State != jam.UpgradeQueued || q.force == nil || *q.force {
		t.Fatalf("UpgradeStanding = %+v, %v", res, err)
	}
	if _, err := c.UpgradeStanding(jam.DefaultProject, "guest", "bot", true); err != nil || !*q.force {
		t.Fatalf("forced UpgradeStanding: %v (force reached server: %v)", err, *q.force)
	}
	if list, err := c.ListStanding(jam.DefaultProject, "guest"); err != nil || len(list) != 1 || list[0].Upgrade != jam.UpgradeQueued {
		t.Fatalf("ListStanding = %+v, %v", list, err)
	}
	q.err = fmt.Errorf("%w: x", jam.ErrStandingResetPending)
	if _, err := c.UpgradeStanding(jam.DefaultProject, "guest", "bot", false); !errors.Is(err, ErrConflict) {
		t.Fatalf("during a pending reset = %v, want ErrConflict", err)
	}
	if _, err := c.UpgradeStanding(jam.DefaultProject, "guest", "nobody", false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("undeclared UpgradeStanding = %v, want ErrNotFound", err)
	}
}

// TestClientShowEgress shows a role's egress policy (set through the store)
// through the client; ListRoles carries it.
func TestClientShowEgress(t *testing.T) {
	ts, store := newServer(t)
	c := New(ts.URL, "")

	view, err := c.ShowEgress(jam.DefaultProject, "guest")
	if err != nil || view.Managed || len(view.Domains) != 0 {
		t.Fatalf("ShowEgress (unset) = %+v, %v", view, err)
	}
	if _, err := jam.SetRoleEgress(store, jam.DefaultProject, "guest", []string{"B.org", "a.com"}); err != nil {
		t.Fatalf("SetRoleEgress: %v", err)
	}
	view, err = c.ShowEgress(jam.DefaultProject, "guest")
	if err != nil || !view.Managed || strings.Join(view.Domains, ",") != "a.com,b.org" {
		t.Fatalf("ShowEgress = %+v, %v", view, err)
	}
	roles, err := c.ListRoles(jam.DefaultProject)
	if err != nil || len(roles) != 1 || roles[0].Scope.Egress == nil || len(roles[0].Scope.Egress.Domains) != 2 {
		t.Fatalf("ListRoles egress = %+v, %v", roles, err)
	}
	if _, err := c.ShowEgress(jam.DefaultProject, "nobody"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown role = %v, want ErrNotFound", err)
	}
}

func TestExportImportConfigClient(t *testing.T) {
	want := jam.ConfigSnapshot{Version: jam.ConfigSnapshotVersion, Actors: []jam.Actor{{ID: "a", TokenHash: "h"}}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /admin/config", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(want)
	})
	var imported jam.ConfigSnapshot
	mux.HandleFunc("POST /admin/config", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&imported)
		w.WriteHeader(http.StatusNoContent)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	c := New(ts.URL, "")
	got, err := c.ExportConfig()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Actors) != 1 || got.Actors[0].ID != "a" {
		t.Fatalf("export = %+v", got)
	}
	if err := c.ImportConfig(want); err != nil {
		t.Fatal(err)
	}
	if imported.Actors[0].ID != "a" {
		t.Fatalf("server received %+v", imported)
	}
}

func TestImportConfigConflict(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "import refused: target config is not empty", http.StatusConflict)
	}))
	defer ts.Close()
	err := New(ts.URL, "").ImportConfig(jam.ConfigSnapshot{Version: jam.ConfigSnapshotVersion})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
}

func TestClientContextRoundTrip(t *testing.T) {
	ts, store := newServer(t)
	c := New(ts.URL, "")
	for _, sc := range []struct {
		scope ContextScope
		set   func() error
		clear func() error
	}{
		{ContextScope{Project: jam.DefaultProject, Role: "guest"},
			func() error {
				return jam.SetRoleContextChecked(store, jam.DefaultProject, "guest", jam.ContextBody{Core: "C"})
			},
			func() error { return jam.ClearRoleContext(store, jam.DefaultProject, "guest") }},
		{ContextScope{Project: jam.DefaultProject},
			func() error {
				return jam.SetProjectContextChecked(store, jam.DefaultProject, jam.ContextBody{Core: "C"})
			},
			func() error { return jam.SetProjectContextChecked(store, jam.DefaultProject, jam.ContextBody{}) }},
		{ContextScope{Jam: true},
			func() error { return jam.SetJamContextChecked(store, jam.ContextBody{Core: "C"}) },
			func() error { return jam.SetJamContextChecked(store, jam.ContextBody{}) }},
	} {
		if err := sc.set(); err != nil {
			t.Fatalf("set %+v: %v", sc.scope, err)
		}
		if b, err := c.GetContext(sc.scope); err != nil || b.Core != "C" {
			t.Fatalf("GetContext %+v = %+v, %v", sc.scope, b, err)
		}
		if err := sc.clear(); err != nil {
			t.Fatalf("clear %+v: %v", sc.scope, err)
		}
		if b, _ := c.GetContext(sc.scope); b.Core != "" {
			t.Fatalf("after clear %+v: %+v", sc.scope, b)
		}
	}
}

func TestClientConnections(t *testing.T) {
	ts, store := newServer(t)
	c := New(ts.URL, "")
	conn, err := store.CreateConnection(jam.Connection{Kind: "linear", Name: "linear-acme", CredName: "lin-tok"})
	if err != nil {
		t.Fatal(err)
	}
	if list, err := c.ListConnections(); err != nil || len(list) != 1 || list[0].ID != conn.ID || list[0].CredName != "lin-tok" {
		t.Fatalf("ListConnections = %+v, %v", list, err)
	}
}

// A long placement timeout widens the client's timeout past its default, so a
// slow-but-successful placement is not abandoned client-side.
func TestPlacementTimeoutWidensClientTimeout(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"ses_1"}`))
	}))
	defer ts.Close()
	c := New(ts.URL, "")
	c.httpc.Timeout = 100 * time.Millisecond

	if _, err := c.RaiseCove(CoveRaiseParams{ID: "w1", Role: "guest"}); err == nil {
		t.Fatal("without placement timeout: want the short default to trip")
	}
	if _, err := c.RaiseCove(CoveRaiseParams{ID: "w1", Role: "guest", PlacementTimeout: "1s"}); err != nil {
		t.Fatalf("raise with placement timeout: %v", err)
	}
	if _, err := c.RequestPersonalSession(jam.PersonalSessionBody{Role: "pair", PlacementTimeout: "1s"}); err != nil {
		t.Fatalf("personal session with placement timeout: %v", err)
	}
	if _, err := c.RaiseCove(CoveRaiseParams{ID: "w1", Role: "guest", PlacementTimeout: "soon"}); err == nil {
		t.Fatal("unparsable placement timeout must leave the default")
	}
}
