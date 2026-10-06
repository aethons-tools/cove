package jam

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// mustCreateProject records each named project on s, failing the test on error.
// Every project-scoped write needs its project to exist first.
func mustCreateProject(t *testing.T, s Store, names ...string) {
	t.Helper()
	for _, n := range names {
		if err := s.CreateProject(n); err != nil {
			t.Fatalf("CreateProject(%q): %v", n, err)
		}
	}
}

func TestMemStoreDestinationsAndMatch(t *testing.T) {
	s := NewMemStore()
	mustCreateProject(t, s, "ACME")
	d := Destination{Name: "git", Route: "/git/", Upstream: "https://github.com", IdentityIn: ApplyBasicPassword, CredName: "git-pat", Apply: ApplyBasicPassword}
	if err := s.AddDestination(d); err != nil {
		t.Fatalf("AddDestination: %v", err)
	}
	if err := s.AddActor(Actor{ID: "spider-18", TokenHash: HashToken("tok"), Grants: []Grant{{Project: "ACME", Role: "guest"}}}); err != nil {
		t.Fatalf("AddActor: %v", err)
	}
	if got, ok := s.Match("/git/acme/api.git/info/refs"); !ok || got.Name != "git" {
		t.Fatalf("Match = %+v, %v", got, ok)
	}
	if len(s.ListDestinations()) != 1 || len(s.ListActors()) != 1 {
		t.Fatalf("lists: dests=%d actors=%d", len(s.ListDestinations()), len(s.ListActors()))
	}
	if err := s.RemoveDestination("git"); err != nil {
		t.Fatalf("RemoveDestination: %v", err)
	}
	if _, ok := s.Match("/git/acme/api.git/info/refs"); ok {
		t.Fatal("destination still matched after removal")
	}
}

func TestMemStoreRoleAndActorCRUD(t *testing.T) {
	fs := NewMemStore()
	mustCreateProject(t, fs, "acme", "beta")
	if err := fs.PutRole("acme", Role{Name: "guest", Scope: Scope{Destinations: []string{"anthropic"}}}); err != nil {
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

func TestMemStoreKitPushPinResolve(t *testing.T) {
	fs := NewMemStore()
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
	fs := NewMemStore()
	// Binding a non-existent kit is rejected (fail closed).
	mustCreateProject(t, fs, "acme")
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

// TestMemStoreGetKitConcurrentSafe is a regression test for a data race: GetKit
// used to return the Kit with its Versions field pointing at the store's live
// map. A caller (the /admin/kits/{name}/versions handler) ranging that map
// outside fs.mu, concurrently with PushKit writing it under fs.mu, is a
// concurrent map iteration/write race. GetKit must deep-copy Versions, exactly
// as ListKits already does, so callers only ever see a private snapshot.
//
// Run with -race: it must fail (not just deadlock/panic on assertions) under
// the old GetKit, and pass under the fixed one.
func TestMemStoreGetKitConcurrentSafe(t *testing.T) {
	fs := NewMemStore()
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

func TestInstanceRegistryCRUD(t *testing.T) {
	fs := NewMemStore()
	inst := Instance{ActorID: "w1", Project: "default", Role: "guest", Phase: PhaseLive,
		Lease: Lease{Holder: "h1", Expiry: time.Unix(500, 0).UTC()}}
	if err := fs.PutInstance(inst); err != nil {
		t.Fatal(err)
	}
	got, ok := fs.GetInstance("w1")
	if !ok || got.Role != "guest" || got.Phase != PhaseLive || got.Lease.Holder != "h1" {
		t.Fatalf("instance wrong: %+v ok=%v", got, ok)
	}
	if n := len(fs.ListInstances()); n != 1 {
		t.Fatalf("ListInstances = %d, want 1", n)
	}
	if err := fs.RemoveInstance("w1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := fs.GetInstance("w1"); ok {
		t.Fatal("instance still present after remove")
	}
	if err := fs.RemoveInstance("w1"); err == nil {
		t.Fatal("expected error removing absent instance")
	}
}

func TestSetEscalationPolicyReplaces(t *testing.T) {
	fs := NewMemStore()
	mustCreateProject(t, fs, "p")
	_ = fs.SetEscalationPolicy("p", "", []EscalationTier{{Targets: []string{"human:a"}, Timeout: time.Minute}})
	_ = fs.SetEscalationPolicy("p", "", []EscalationTier{{Targets: []string{"human:b"}, Timeout: 2 * time.Minute}})
	p, _ := fs.GetProject("p")
	if len(p.Escalation) != 1 || p.Escalation[0].Targets[0] != "human:b" {
		t.Fatalf("expected replace, got %+v", p.Escalation)
	}
}

func TestGetProjectCopiesEscalation(t *testing.T) {
	fs := NewMemStore()
	mustCreateProject(t, fs, "p")
	_ = fs.SetEscalationPolicy("p", "", []EscalationTier{{Targets: []string{"human:a"}, Timeout: time.Minute}})
	p, _ := fs.GetProject("p")
	p.Escalation[0].Targets[0] = "mutated" // must not corrupt the store
	p2, _ := fs.GetProject("p")
	if p2.Escalation[0].Targets[0] != "human:a" {
		t.Fatalf("GetProject leaked a live slice: %v", p2.Escalation[0].Targets)
	}
}

func TestGetProjectDeepCopiesCategoryMap(t *testing.T) {
	fs := NewMemStore()
	mustCreateProject(t, fs, "p")
	_ = fs.SetEscalationPolicy("p", "infra", []EscalationTier{{Targets: []string{"human:a"}, Timeout: time.Minute}})
	p, _ := fs.GetProject("p")
	p.EscalationByCategory["infra"][0].Targets[0] = "mutated" // must not corrupt the store
	p.EscalationByCategory["added"] = nil                     // must not appear in the store
	p2, _ := fs.GetProject("p")
	if p2.EscalationByCategory["infra"][0].Targets[0] != "human:a" {
		t.Fatalf("category chain aliased: %v", p2.EscalationByCategory["infra"][0].Targets)
	}
	if _, ok := p2.EscalationByCategory["added"]; ok {
		t.Fatal("category map aliased (new key leaked into store)")
	}
}
