package harbor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
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

func TestFileStoreKitPushPinResolve(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.json")
	fs, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	v1, err := fs.PushKit("web", "name: web\nversion: one\n")
	if err != nil || v1 != 1 {
		t.Fatalf("PushKit v1 = %d, %v", v1, err)
	}
	v2, err := fs.PushKit("web", "name: web\nversion: two\n")
	if err != nil || v2 != 2 {
		t.Fatalf("PushKit v2 = %d, %v", v2, err)
	}
	// Current resolves to the latest push.
	if cfg, ok := fs.KitConfig("web", 0); !ok || cfg != "name: web\nversion: two\n" {
		t.Fatalf("Current config = %q, %v", cfg, ok)
	}
	// A specific version is addressable.
	if cfg, ok := fs.KitConfig("web", 1); !ok || cfg != "name: web\nversion: one\n" {
		t.Fatalf("v1 config = %q, %v", cfg, ok)
	}
	// Pin rolls Current back.
	if err := fs.PinKit("web", 1); err != nil {
		t.Fatalf("PinKit: %v", err)
	}
	if cfg, _ := fs.KitConfig("web", 0); cfg != "name: web\nversion: one\n" {
		t.Fatalf("after pin, Current = %q", cfg)
	}
	// Pin to an absent version fails.
	if err := fs.PinKit("web", 99); err == nil {
		t.Fatal("PinKit to absent version should fail")
	}
	if k, ok := fs.GetKit("web"); !ok || k.Current != 1 || len(k.Versions) != 2 {
		t.Fatalf("GetKit = %+v, %v", k, ok)
	}
	if err := fs.RemoveKit("nope"); err == nil {
		t.Fatal("RemoveKit of absent kit should fail")
	}
	// Pushing again after pinning back to v1 must mint v3, not reuse v2 (the
	// next version number is max(existing)+1, not Current+1) — and v2's config
	// must still be intact.
	v3, err := fs.PushKit("web", "name: web\nversion: three\n")
	if err != nil || v3 != 3 {
		t.Fatalf("PushKit after pin = %d, %v, want 3", v3, err)
	}
	if cfg, ok := fs.KitConfig("web", 2); !ok || cfg != "name: web\nversion: two\n" {
		t.Fatalf("v2 config after pin+push = %q, %v, want unchanged", cfg, ok)
	}
}

func TestPutRoleValidatesKitExists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.json")
	fs, _ := NewFileStore(path)
	// Binding a non-existent kit is rejected (fail closed).
	if err := fs.PutRole("acme", Role{Name: "impl", Kit: "ghost"}); err == nil {
		t.Fatal("PutRole with a non-existent kit should fail")
	}
	// After the kit exists, the binding persists and RoleReferencingKit finds it.
	if _, err := fs.PushKit("builder", "name: builder\n"); err != nil {
		t.Fatalf("PushKit: %v", err)
	}
	if err := fs.PutRole("acme", Role{Name: "impl", Kit: "builder"}); err != nil {
		t.Fatalf("PutRole with existing kit: %v", err)
	}
	if r, ok := fs.GetRole("acme", "impl"); !ok || r.Kit != "builder" {
		t.Fatalf("role.Kit = %+v, %v", r, ok)
	}
	proj, role, ok := fs.RoleReferencingKit("builder")
	if !ok || proj != "acme" || role != "impl" {
		t.Fatalf("RoleReferencingKit = %q/%q/%v", proj, role, ok)
	}
	// A role with no kit is always allowed.
	if err := fs.PutRole("acme", Role{Name: "free"}); err != nil {
		t.Fatalf("PutRole with empty kit: %v", err)
	}
}

func TestFileStoreV3LoadsWithEmptyKitRegistry(t *testing.T) {
	// A v3 file (roles/actors/destinations, no "kits" key) loads with an empty
	// kit registry and roles whose Kit is "".
	path := filepath.Join(t.TempDir(), "store.json")
	v3 := `{
	  "roles": {"acme": {"guest": {"name":"guest","scope":{"destinations":["anthropic"],"repos":null,"ttl":0}}}},
	  "actors": {},
	  "destinations": {}
	}`
	if err := os.WriteFile(path, []byte(v3), 0o600); err != nil {
		t.Fatal(err)
	}
	fs, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	if len(fs.ListKits()) != 0 {
		t.Fatalf("expected empty kit registry, got %d", len(fs.ListKits()))
	}
	if r, ok := fs.GetRole("acme", "guest"); !ok || r.Kit != "" {
		t.Fatalf("migrated role = %+v, %v", r, ok)
	}
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

// TestFileStoreGetKitConcurrentSafe is a regression test for a data race: GetKit
// used to return the Kit with its Versions field pointing at the store's live
// map. A caller (the /admin/kits/{name}/versions handler) ranging that map
// outside fs.mu, concurrently with PushKit writing it under fs.mu, is a
// concurrent map iteration/write race. GetKit must deep-copy Versions, exactly
// as ListKits already does, so callers only ever see a private snapshot.
//
// Run with -race: it must fail (not just deadlock/panic on assertions) under
// the old GetKit, and pass under the fixed one.
func TestFileStoreGetKitConcurrentSafe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.json")
	fs, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	if _, err := fs.PushKit("k", "name: k\nversion: 0\n"); err != nil {
		t.Fatalf("PushKit initial: %v", err)
	}

	const iterations = 50
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			if _, err := fs.PushKit("k", fmt.Sprintf("name: k\nversion: %d\n", i)); err != nil {
				t.Errorf("PushKit: %v", err)
				return
			}
		}
	}()

	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			k, ok := fs.GetKit("k")
			if !ok {
				t.Errorf("GetKit: kit %q not found", "k")
				return
			}
			// Range the returned Versions map outside any lock, exactly as the
			// admin /versions handler does — this is what raced against
			// PushKit's write to the live map before GetKit deep-copied it.
			for range k.Versions {
			}
		}
	}()

	wg.Wait()
}

func TestInstanceRegistryRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.json")
	fs, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	inst := Instance{ActorID: "w1", Project: "default", Role: "guest", Phase: PhaseLive,
		Lease: Lease{Holder: "h1", Expiry: time.Unix(500, 0).UTC()}}
	if err := fs.PutInstance(inst); err != nil {
		t.Fatal(err)
	}
	// Reload from disk — durability.
	fs2, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := fs2.GetInstance("w1")
	if !ok || got.Role != "guest" || got.Phase != PhaseLive || got.Lease.Holder != "h1" {
		t.Fatalf("reloaded instance wrong: %+v ok=%v", got, ok)
	}
	if n := len(fs2.ListInstances()); n != 1 {
		t.Fatalf("ListInstances = %d, want 1", n)
	}
	if err := fs2.RemoveInstance("w1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := fs2.GetInstance("w1"); ok {
		t.Fatal("instance still present after remove")
	}
	if err := fs2.RemoveInstance("w1"); err == nil {
		t.Fatal("expected error removing absent instance")
	}
}

func TestStoreLoadsV4FileWithoutInstances(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.json")
	// A v4 file: roles + actors + kits, NO "instances" key.
	v4 := `{"roles":{"default":{"guest":{"name":"guest","scope":{"destinations":["a"],"repos":[],"ttl":0}}}},` +
		`"actors":{},"destinations":{},"kits":{}}`
	if err := os.WriteFile(path, []byte(v4), 0o600); err != nil {
		t.Fatal(err)
	}
	fs, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("v4 file must load additively, got %v", err)
	}
	if n := len(fs.ListInstances()); n != 0 {
		t.Fatalf("expected empty instance registry, got %d", n)
	}
	if _, ok := fs.GetRole("default", "guest"); !ok {
		t.Fatal("v4 role lost on load")
	}
}

func TestRosterRoundTripAndPersist(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "store.json")
	fs, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := fs.AddHuman("acme", Human{Name: "alice", Handle: "alice.h"}); err != nil {
		t.Fatal(err)
	}
	if err := fs.AddChannel("acme", Channel{Name: "eng-help", Service: "linear", Ref: "ACME-1"}); err != nil {
		t.Fatal(err)
	}
	// reload from disk
	fs2, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	rr, ok := fs2.GetRoster("acme")
	if !ok || len(rr.Humans) != 1 || rr.Humans[0].Handle != "alice.h" || len(rr.Channels) != 1 || rr.Channels[0].Ref != "ACME-1" {
		t.Fatalf("roster not persisted: %+v ok=%v", rr, ok)
	}
	// a project with a roster shows up in ListProjects even without roles
	found := false
	for _, p := range fs2.ListProjects() {
		if p == "acme" {
			found = true
		}
	}
	if !found {
		t.Fatalf("ListProjects missing acme: %v", fs2.ListProjects())
	}
}

func TestAddHumanUpsertsByName(t *testing.T) {
	fs, _ := NewFileStore(filepath.Join(t.TempDir(), "s.json"))
	_ = fs.AddHuman("p", Human{Name: "a", Handle: "old"})
	_ = fs.AddHuman("p", Human{Name: "a", Handle: "new"})
	rr, _ := fs.GetRoster("p")
	if len(rr.Humans) != 1 || rr.Humans[0].Handle != "new" {
		t.Fatalf("expected upsert to new handle, got %+v", rr.Humans)
	}
}

func TestMigrationLeavesEmptyRoster(t *testing.T) {
	// A pre-existing v4 store with no "projects" key loads with empty rosters.
	dir := t.TempDir()
	path := filepath.Join(dir, "s.json")
	if err := os.WriteFile(path, []byte(`{"roles":{},"actors":{"h":{"id":"a","token_hash":"h"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	fs, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := fs.GetRoster("acme"); ok {
		t.Fatal("expected no roster for absent project")
	}
}
