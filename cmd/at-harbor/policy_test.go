package main

import (
	"path/filepath"
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

// An unknown role with no fallback has no policy (the Allocator fails closed).
func TestRosterPolicy_NoRoleNoFallback(t *testing.T) {
	p := rosterPolicy{store: newPolicyStore(t), fallback: allocator.StaticPolicy{{Project: "acme", Role: "worker"}: {MaxEphemeral: 2}}}
	if pol, ok := p.Policy("acme", "reviewer"); ok {
		t.Fatalf("Policy = %+v,%v; want none", pol, ok)
	}
}
