package adminclient

import (
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/aethons-tools/cove/internal/harbor"
)

func newServer(t *testing.T) (*httptest.Server, harbor.Store) {
	t.Helper()
	store, err := harbor.NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	if err != nil {
		t.Fatal(err)
	}
	h := harbor.NewAdminHandler(store, harbor.LoopbackAuthenticator{}, func(n string) bool { return n == "git-pat" }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ts := httptest.NewServer(h) // listens on 127.0.0.1 → passes the loopback authenticator
	t.Cleanup(ts.Close)
	return ts, store
}

func TestClientRoundTrip(t *testing.T) {
	ts, store := newServer(t)
	c := New(ts.URL)

	if err := c.AddDestination(harbor.Destination{Name: "git", Route: "/git/", Upstream: "https://github.com", IdentityIn: harbor.ApplyBasicPassword, CredName: "git-pat", Apply: harbor.ApplyBasicPassword, RepoScoped: true}); err != nil {
		t.Fatalf("AddDestination: %v", err)
	}
	ds, err := c.ListDestinations()
	if err != nil || len(ds) != 1 || ds[0].Name != "git" {
		t.Fatalf("ListDestinations = %+v, %v", ds, err)
	}
	res, err := c.Enroll(EnrollParams{ID: "spider-18", Project: "ACME", Role: "guest", Destinations: []string{"git"}, Repos: []string{"acme/*"}})
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
	c := New(ts.URL)
	err := c.AddDestination(harbor.Destination{Name: "bad", Route: "/bad/", Upstream: "https://x", IdentityIn: harbor.ApplyBearer, CredName: "nope", Apply: harbor.ApplyBearer})
	if err == nil {
		t.Fatal("expected error for unresolvable cred_name")
	}
}
