package main

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/aethons-tools/cove/internal/allocator"
	"github.com/aethons-tools/cove/internal/harbor"
)

func newPolicyStore(t *testing.T) *harbor.FileStore {
	t.Helper()
	st, err := harbor.NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// The roster Role's max-ephemeral wins over the dispatcher's max-concurrent
// fallback, and is read live (a later edit is seen on the next grant).
func TestRosterPolicy_RoleWinsOverFallback(t *testing.T) {
	st := newPolicyStore(t)
	if err := st.PutRole("acme", harbor.Role{Name: "worker", Allocation: harbor.RoleAllocation{MaxEphemeral: 7}}); err != nil {
		t.Fatal(err)
	}
	p := rosterPolicy{store: st, fallback: allocator.StaticPolicy{{Project: "acme", Role: "worker"}: {MaxEphemeral: 2}}}
	if pol, ok := p.Policy("acme", "worker"); !ok || pol.MaxEphemeral != 7 {
		t.Fatalf("Policy = %+v,%v; want roster value 7", pol, ok)
	}
	if err := st.PutRole("acme", harbor.Role{Name: "worker", Allocation: harbor.RoleAllocation{MaxEphemeral: 9}}); err != nil {
		t.Fatal(err)
	}
	if pol, ok := p.Policy("acme", "worker"); !ok || pol.MaxEphemeral != 9 {
		t.Fatalf("Policy after edit = %+v,%v; want live roster value 9", pol, ok)
	}
}

// A role that sets no max-ephemeral uses the dispatcher's fallback.
func TestRosterPolicy_UnsetRoleUsesFallback(t *testing.T) {
	st := newPolicyStore(t)
	if err := st.PutRole("acme", harbor.Role{Name: "worker"}); err != nil {
		t.Fatal(err)
	}
	p := rosterPolicy{store: st, fallback: allocator.StaticPolicy{{Project: "acme", Role: "worker"}: {MaxEphemeral: 2}}}
	if pol, ok := p.Policy("acme", "worker"); !ok || pol.MaxEphemeral != 2 {
		t.Fatalf("Policy = %+v,%v; want fallback 2", pol, ok)
	}
}

// The role's personal caps pass through to the policy, alongside the
// dispatcher's ephemeral fallback when the role sets no max-ephemeral.
func TestRosterPolicy_PersonalCaps(t *testing.T) {
	st := newPolicyStore(t)
	if err := st.PutRole("acme", harbor.Role{Name: "worker", Allocation: harbor.RoleAllocation{MaxPersonal: 3, MaxPersonalPerOwner: 1}}); err != nil {
		t.Fatal(err)
	}
	p := rosterPolicy{store: st, fallback: allocator.StaticPolicy{{Project: "acme", Role: "worker"}: {MaxEphemeral: 2}}}
	want := allocator.Policy{MaxEphemeral: 2, MaxPersonal: 3, MaxPersonalPerOwner: 1}
	if pol, ok := p.Policy("acme", "worker"); !ok || !reflect.DeepEqual(pol, want) {
		t.Fatalf("Policy = %+v,%v; want %+v", pol, ok, want)
	}
	// With no dispatcher fallback (harbor serving without a dispatcher), a role
	// with only personal caps still has a policy — its ephemeral cap is 0.
	p = rosterPolicy{store: st}
	want = allocator.Policy{MaxPersonal: 3, MaxPersonalPerOwner: 1}
	if pol, ok := p.Policy("acme", "worker"); !ok || !reflect.DeepEqual(pol, want) {
		t.Fatalf("Policy (no fallback) = %+v,%v; want %+v", pol, ok, want)
	}
}

// An unknown role with no fallback has no policy (the Allocator fails closed).
func TestRosterPolicy_NoRoleNoFallback(t *testing.T) {
	p := rosterPolicy{store: newPolicyStore(t), fallback: allocator.StaticPolicy{{Project: "acme", Role: "worker"}: {MaxEphemeral: 2}}}
	if pol, ok := p.Policy("acme", "reviewer"); ok {
		t.Fatalf("Policy = %+v,%v; want none", pol, ok)
	}
}

// Without a dispatcher, the Allocator's policy has no ephemeral fallback (the
// roster alone decides); with one, the dispatcher's max-concurrent seeds the
// fallback for its own (project, role), with an empty project normalized to
// the default so grants and releases share one stream.
func TestNewRosterPolicy_FallbackOnlyWithDispatcher(t *testing.T) {
	st := newPolicyStore(t)
	if p := newRosterPolicy(st, nil); len(p.fallback) != 0 {
		t.Fatalf("no dispatcher: fallback = %+v, want empty", p.fallback)
	}
	p := newRosterPolicy(st, &dispatcherConfig{Role: "worker", MaxConcurrent: 2})
	if pol, ok := p.Policy(harbor.DefaultProject, "worker"); !ok || pol.MaxEphemeral != 2 {
		t.Fatalf("dispatcher fallback = %+v,%v; want max-ephemeral 2 on %s/worker", pol, ok, harbor.DefaultProject)
	}
}

// The role's declared standing names pass through to the policy, and a role
// that declares only standing sessions still has a policy.
func TestRosterPolicy_StandingNames(t *testing.T) {
	st := newPolicyStore(t)
	if err := st.PutRole("acme", harbor.Role{Name: "reviewer", Allocation: harbor.RoleAllocation{Standing: []harbor.StandingSession{{Name: "alice-bot", Prompt: "p"}, {Name: "bob-bot", Prompt: "q"}}}}); err != nil {
		t.Fatal(err)
	}
	p := rosterPolicy{store: st}
	want := allocator.Policy{StandingNames: []string{"alice-bot", "bob-bot"}}
	if pol, ok := p.Policy("acme", "reviewer"); !ok || !reflect.DeepEqual(pol, want) {
		t.Fatalf("Policy = %+v,%v; want %+v", pol, ok, want)
	}
}
