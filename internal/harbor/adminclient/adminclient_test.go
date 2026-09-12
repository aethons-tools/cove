package adminclient

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/harbor"
)

// aliveLauncher is a scripted harbor.Launcher for driving the cove routes in
// this package's tests without a real backend. It always reports the instance
// as alive and returns a synthetic location.
type aliveLauncher struct{}

func (aliveLauncher) Raise(context.Context, harbor.RaiseSpec) (string, error) { return "fake", nil }
func (aliveLauncher) Teardown(context.Context, harbor.Instance) error          { return nil }
func (aliveLauncher) Probe(context.Context, harbor.Instance) (harbor.Liveness, error) {
	return harbor.LivenessAlive, nil
}

func newServer(t *testing.T) (*httptest.Server, harbor.Store) {
	t.Helper()
	store, err := harbor.NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutRole(harbor.DefaultProject, harbor.Role{Name: "guest", Scope: harbor.Scope{Destinations: []string{"anthropic"}, TTL: time.Hour}}); err != nil {
		t.Fatal(err)
	}
	sup := harbor.NewSupervisor(store, aliveLauncher{}, "holder-test", time.Minute, 30*time.Second, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	h := harbor.NewAdminHandler(store, sup, harbor.LoopbackAuthenticator{}, func(n string) bool { return n == "git-pat" }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ts := httptest.NewServer(h) // listens on 127.0.0.1 → passes the loopback authenticator
	t.Cleanup(ts.Close)
	return ts, store
}

// TestClientRoundTrip exercises AddDestination, Enroll and Revoke against a
// real harbor admin handler + FileStore (not just a wire-format mock), proving
// the client's requests actually drive store side effects end to end. Scope
// now comes entirely from the role, so the role must be put before Enroll.
func TestClientRoundTrip(t *testing.T) {
	ts, store := newServer(t)
	c := New(ts.URL, "")

	if err := c.AddDestination(harbor.Destination{Name: "git", Route: "/git/", Upstream: "https://github.com", IdentityIn: harbor.ApplyBasicPassword, CredName: "git-pat", Apply: harbor.ApplyBasicPassword, RepoScoped: true}); err != nil {
		t.Fatalf("AddDestination: %v", err)
	}
	ds, err := c.ListDestinations()
	if err != nil || len(ds) != 1 || ds[0].Name != "git" {
		t.Fatalf("ListDestinations = %+v, %v", ds, err)
	}
	if err := store.PutRole("ACME", harbor.Role{Name: "guest", Scope: harbor.Scope{Destinations: []string{"git"}, Repos: []string{"acme/*"}}}); err != nil {
		t.Fatalf("PutRole: %v", err)
	}
	res, err := c.Enroll(EnrollParams{ID: "spider-18", Project: "ACME", Role: "guest"})
	if err != nil || res.Token == "" {
		t.Fatalf("Enroll = %+v, %v", res, err)
	}
	if _, ok := store.Lookup(harbor.HashToken(res.Token)); !ok {
		t.Fatal("identity not stored")
	}
	if err := c.Revoke("spider-18"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if _, ok := store.Lookup(harbor.HashToken(res.Token)); ok {
		t.Fatal("identity present after Revoke")
	}
}

func TestClientAddDestinationRejected(t *testing.T) {
	ts, _ := newServer(t)
	c := New(ts.URL, "")
	err := c.AddDestination(harbor.Destination{Name: "bad", Route: "/bad/", Upstream: "https://x", IdentityIn: harbor.ApplyBearer, CredName: "nope", Apply: harbor.ApplyBearer})
	if err == nil {
		t.Fatal("expected error for unresolvable cred_name")
	}
}

func TestClientLoginConfig(t *testing.T) {
	store, err := harbor.NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	if err != nil {
		t.Fatal(err)
	}
	lc := &harbor.OperatorLoginConfig{Issuer: "https://acme.auth0.com/", Audience: "https://harbor.acme/api", ClientID: "cid", Scope: "openid"}
	h := harbor.NewAdminHandler(store, nil, harbor.LoopbackAuthenticator{}, func(string) bool { return true }, lc, slog.New(slog.NewTextHandler(io.Discard, nil)))
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
	var gotPath, gotMethod, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.RequestURI()
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		switch {
		case r.URL.Path == "/admin/roles" && r.Method == "GET":
			_, _ = w.Write([]byte(`[{"project":"acme","name":"guest","destinations":["anthropic"],"repos":["acme/*"],"ttl_seconds":3600}]`))
		case r.URL.Path == "/admin/projects":
			_, _ = w.Write([]byte(`["acme"]`))
		case r.URL.Path == "/admin/roster":
			_, _ = w.Write([]byte(`[{"id":"m","grants":[{"project":"acme","role":"guest","destinations":["anthropic"],"repos":["acme/*"]}]}]`))
		default:
			w.WriteHeader(http.StatusCreated)
		}
	}))
	defer srv.Close()
	c := New(srv.URL, "")

	if err := c.PutRole("acme", harbor.Role{Name: "guest", Scope: harbor.Scope{Destinations: []string{"anthropic"}, Repos: []string{"acme/*"}, TTL: time.Hour}}); err != nil {
		t.Fatalf("PutRole: %v", err)
	}
	if gotMethod != "POST" || gotPath != "/admin/roles" || !strings.Contains(gotBody, `"ttl_seconds":3600`) {
		t.Fatalf("PutRole wire = %s %s %s", gotMethod, gotPath, gotBody)
	}
	roles, err := c.ListRoles("acme")
	if err != nil || len(roles) != 1 || roles[0].Scope.TTL != time.Hour {
		t.Fatalf("ListRoles = %+v, %v", roles, err)
	}
	if gotPath != "/admin/roles?project=acme" {
		t.Fatalf("ListRoles path = %s", gotPath)
	}

	projs, err := c.ListProjects()
	if err != nil || len(projs) != 1 || projs[0] != "acme" {
		t.Fatalf("ListProjects = %+v, %v", projs, err)
	}

	if err := c.RemoveRole("acme", "guest"); err != nil {
		t.Fatalf("RemoveRole: %v", err)
	}
	if gotMethod != "DELETE" || gotPath != "/admin/roles/acme/guest" {
		t.Fatalf("RemoveRole wire = %s %s", gotMethod, gotPath)
	}

	roster, err := c.Roster()
	if err != nil {
		t.Fatalf("Roster: %v", err)
	}
	if len(roster) != 1 || roster[0].ID != "m" || len(roster[0].Grants) != 1 {
		t.Fatalf("Roster = %+v", roster)
	}

	if err := c.AddGrant("m", harbor.Grant{Project: "beta", Role: "review"}); err != nil {
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
	var gotMethod, gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.RequestURI()
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		switch {
		case r.URL.Path == "/admin/kits" && r.Method == "POST":
			_, _ = w.Write([]byte(`{"name":"web","version":3}`))
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

	v, err := c.PushKit("web", "name: web\n")
	if err != nil || v != 3 {
		t.Fatalf("PushKit = %d, %v (body=%s)", v, err, gotBody)
	}
	if gotMethod != "POST" || gotPath != "/admin/kits" || !strings.Contains(gotBody, `"config":"name: web\n"`) {
		t.Fatalf("push wire = %s %s %s", gotMethod, gotPath, gotBody)
	}
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
	if err := c.PinKit("web", 1); err != nil {
		t.Fatalf("PinKit: %v", err)
	}
	if err := c.RemoveKit("web"); err != nil {
		t.Fatalf("RemoveKit: %v", err)
	}
}

func TestClientPutRoleCarriesKit(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()
	if err := New(srv.URL, "").PutRole("acme", harbor.Role{Name: "impl", Kit: "builder"}); err != nil {
		t.Fatalf("PutRole: %v", err)
	}
	if !strings.Contains(gotBody, `"kit":"builder"`) {
		t.Fatalf("PutRole body missing kit: %s", gotBody)
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
	if len(coves) != 1 || coves[0].ID != "w1" {
		t.Fatalf("list = %+v", coves)
	}
	if err := c.ReportCoveStatus("w1", "blocked"); err != nil {
		t.Fatal(err)
	}
	if inst, _ := store.GetInstance("w1"); inst.Activity != harbor.ActivityBlocked {
		t.Fatalf("activity = %s", inst.Activity)
	}
	if err := c.TeardownCove("w1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.GetInstance("w1"); ok {
		t.Fatal("instance present after teardown")
	}
}
