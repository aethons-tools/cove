package harbor

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFileStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ids.json")
	s, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	id := Identity{ID: "spider-18", TokenHash: HashToken("tok"), Project: "ACME", Role: "guest", Destinations: []string{"anthropic"}}
	if err := s.Add(id); err != nil {
		t.Fatalf("Add: %v", err)
	}
	// Reload from disk to prove persistence.
	s2, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	got, ok := s2.Lookup(HashToken("tok"))
	if !ok || got.ID != "spider-18" {
		t.Fatalf("Lookup after reload = %+v, %v", got, ok)
	}
	if err := s2.Remove("spider-18"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, ok := s2.Lookup(HashToken("tok")); ok {
		t.Fatal("identity still present after Remove")
	}
}

func TestFileStoreDestinationsAndMatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.json")
	s, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	d := Destination{Name: "git", Route: "/git/", Upstream: "https://github.com", IdentityIn: ApplyBasicPassword, CredName: "git-pat", Apply: ApplyBasicPassword, RepoScoped: true}
	if err := s.AddDestination(d); err != nil {
		t.Fatalf("AddDestination: %v", err)
	}
	if err := s.Add(Identity{ID: "spider-18", TokenHash: HashToken("tok"), Destinations: []string{"git"}}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	// Reload from disk: both collections persist.
	s2, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got, ok := s2.Match("/git/acme/api.git/info/refs"); !ok || got.Name != "git" {
		t.Fatalf("Match after reload = %+v, %v", got, ok)
	}
	if len(s2.ListDestinations()) != 1 || len(s2.ListIdentities()) != 1 {
		t.Fatalf("lists: dests=%d ids=%d", len(s2.ListDestinations()), len(s2.ListIdentities()))
	}
	if err := s2.RemoveDestination("git"); err != nil {
		t.Fatalf("RemoveDestination: %v", err)
	}
	if _, ok := s2.Match("/git/acme/api.git/info/refs"); ok {
		t.Fatal("destination still matched after removal")
	}
}

func TestFileStoreMigratesLegacyIdentityMap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.json")
	// v1 on-disk format: a bare map[tokenHash]Identity
	legacy := `{"` + HashToken("tok") + `":{"id":"old-one","token_hash":"` + HashToken("tok") + `"}}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore (legacy): %v", err)
	}
	if id, ok := s.Lookup(HashToken("tok")); !ok || id.ID != "old-one" {
		t.Fatalf("legacy identity not migrated: %+v, %v", id, ok)
	}
}
