package harbor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ids.json")
	s, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	a := Actor{ID: "spider-18", TokenHash: HashToken("tok"), Grants: []Grant{{Project: "ACME", Role: "guest"}}}
	if err := s.AddActor(a); err != nil {
		t.Fatalf("AddActor: %v", err)
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
	if err := s2.RemoveActor("spider-18"); err != nil {
		t.Fatalf("RemoveActor: %v", err)
	}
	if _, ok := s2.Lookup(HashToken("tok")); ok {
		t.Fatal("actor still present after RemoveActor")
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
	if err := s.AddActor(Actor{ID: "spider-18", TokenHash: HashToken("tok"), Grants: []Grant{{Project: "ACME", Role: "guest"}}}); err != nil {
		t.Fatalf("AddActor: %v", err)
	}
	// Reload from disk: both collections persist.
	s2, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got, ok := s2.Match("/git/acme/api.git/info/refs"); !ok || got.Name != "git" {
		t.Fatalf("Match after reload = %+v, %v", got, ok)
	}
	if len(s2.ListDestinations()) != 1 || len(s2.ListActors()) != 1 {
		t.Fatalf("lists: dests=%d actors=%d", len(s2.ListDestinations()), len(s2.ListActors()))
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
	if a, ok := s.Lookup(HashToken("tok")); !ok || a.ID != "old-one" {
		t.Fatalf("legacy identity not migrated: %+v, %v", a, ok)
	}
}

func TestFileStoreRoleAndActorCRUD(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.json")
	fs, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	if err := fs.PutRole("acme", Role{Name: "guest", Scope: Scope{Destinations: []string{"anthropic"}, Repos: []string{"acme/*"}}}); err != nil {
		t.Fatalf("PutRole: %v", err)
	}
	if r, ok := fs.GetRole("acme", "guest"); !ok || r.Scope.Destinations[0] != "anthropic" {
		t.Fatalf("GetRole = %+v, %v", r, ok)
	}
	if err := fs.AddActor(Actor{ID: "spider-18", TokenHash: "h1", Grants: []Grant{{Project: "acme", Role: "guest"}}}); err != nil {
		t.Fatalf("AddActor: %v", err)
	}
	if err := fs.AddActor(Actor{ID: "spider-18", TokenHash: "h2"}); err == nil {
		t.Fatal("AddActor should reject a duplicate id")
	}
	if err := fs.AddGrant("spider-18", Grant{Project: "beta", Role: "review"}); err != nil {
		t.Fatalf("AddGrant: %v", err)
	}
	a, ok := fs.Lookup("h1")
	if !ok || len(a.Grants) != 2 {
		t.Fatalf("after AddGrant, actor = %+v", a)
	}
	if err := fs.RemoveGrant("spider-18", "beta", "review"); err != nil {
		t.Fatalf("RemoveGrant: %v", err)
	}
	if a, _ := fs.Lookup("h1"); len(a.Grants) != 1 {
		t.Fatalf("after RemoveGrant, grants = %+v", a.Grants)
	}
	if projs := fs.ListProjects(); len(projs) == 0 {
		t.Fatal("ListProjects should include acme")
	}
}

func TestFileStoreMigratesV2Identities(t *testing.T) {
	// A v2 file: flat identities carrying inline scope. Migration must preserve
	// each actor's effective scope exactly via a synthesized role (+ override on
	// a scope that differs from the synthesized role for the same project/role).
	path := filepath.Join(t.TempDir(), "store.json")
	v2 := `{
	  "identities": {
	    "hashA": {"id":"a","token_hash":"hashA","project":"acme","role":"guest","destinations":["anthropic","git"],"repos":["acme/*"]},
	    "hashB": {"id":"b","token_hash":"hashB","project":"acme","role":"guest","destinations":["anthropic"],"repos":["acme/api"]}
	  },
	  "destinations": {}
	}`
	if err := os.WriteFile(path, []byte(v2), 0o600); err != nil {
		t.Fatal(err)
	}
	fs, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	// A role named (acme,guest) now exists.
	if _, ok := fs.GetRole("acme", "guest"); !ok {
		t.Fatal("migration should synthesize role (acme,guest)")
	}
	// Both actors resolve to their ORIGINAL effective scope.
	check := func(hash string, wantDests, wantRepos []string) {
		a, ok := fs.Lookup(hash)
		if !ok || len(a.Grants) != 1 {
			t.Fatalf("actor %s = %+v", hash, a)
		}
		r, _ := fs.GetRole(a.Grants[0].Project, a.Grants[0].Role)
		got := EffectiveScope(a.Grants[0], r)
		if strings.Join(got.Destinations, ",") != strings.Join(wantDests, ",") ||
			strings.Join(got.Repos, ",") != strings.Join(wantRepos, ",") {
			t.Fatalf("actor %s effective scope = %+v, want dests=%v repos=%v", hash, got, wantDests, wantRepos)
		}
	}
	check("hashA", []string{"anthropic", "git"}, []string{"acme/*"})
	check("hashB", []string{"anthropic"}, []string{"acme/api"})
}

func TestFileStoreMigratesV2IdentitiesPreservesEmptyLegacyScope(t *testing.T) {
	// Two legacy identities share (acme, guest) and both name the same
	// destinations, but one has an empty (nil/omitted) repos list — a legacy
	// deny-all-repos identity — while the other has a real repo scope. Whichever
	// identity map-iteration picks first becomes the synthesized role; the other
	// gets an Override. Migration must express the *exact* original scope in that
	// override, not something EffectiveScope will treat as "inherit the role".
	path := filepath.Join(t.TempDir(), "store.json")
	v2 := `{
	  "identities": {
	    "hashA": {"id":"a","token_hash":"hashA","project":"acme","role":"guest","destinations":["git"],"repos":["acme/*"]},
	    "hashB": {"id":"b","token_hash":"hashB","project":"acme","role":"guest","destinations":["git"]}
	  },
	  "destinations": {}
	}`
	if err := os.WriteFile(path, []byte(v2), 0o600); err != nil {
		t.Fatal(err)
	}
	fs, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	// Look up by token hash (not by "which one defined the role") since map
	// iteration order is nondeterministic — the assertion must hold either way.
	effectiveRepos := func(hash string) []string {
		a, ok := fs.Lookup(hash)
		if !ok || len(a.Grants) != 1 {
			t.Fatalf("actor %s = %+v, %v", hash, a, ok)
		}
		r, _ := fs.GetRole(a.Grants[0].Project, a.Grants[0].Role)
		return EffectiveScope(a.Grants[0], r).Repos
	}
	if got := effectiveRepos("hashA"); strings.Join(got, ",") != "acme/*" {
		t.Fatalf("hashA effective repos = %v, want [acme/*]", got)
	}
	// hashB's legacy repos were empty (deny-all); migration must not let it
	// inherit hashA's ["acme/*"] via a nil override field.
	if got := effectiveRepos("hashB"); len(got) != 0 {
		t.Fatalf("hashB effective repos = %v, want empty (deny-all), not inherited from role", got)
	}
}
