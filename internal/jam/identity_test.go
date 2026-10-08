package jam

import (
	"testing"
	"time"
)

func newMemStoreT(t *testing.T) *MemStore {
	t.Helper()
	st := NewMemStore()
	return st
}

func TestMintTokenIsUniqueAndHashable(t *testing.T) {
	a, err := MintToken()
	if err != nil {
		t.Fatalf("MintToken: %v", err)
	}
	b, _ := MintToken()
	if a == b {
		t.Fatal("two mints returned the same token")
	}
	if len(a) < 40 {
		t.Fatalf("token too short: %d chars", len(a))
	}
	if HashToken(a) == a {
		t.Fatal("hash equals raw token")
	}
	if HashToken(a) != HashToken(a) {
		t.Fatal("hash not stable for same input")
	}
}

func TestValidateIdentity(t *testing.T) {
	ok := []OIDCIdentity{{Issuer: "https://accounts.google.com", Subject: "alice-sub"}}
	if err := ValidateIdentity(ok); err != nil {
		t.Fatalf("ValidateIdentity(%+v) = %v, want nil", ok, err)
	}
	for _, bad := range [][]OIDCIdentity{
		{{Issuer: "", Subject: "x"}},
		{{Issuer: "x", Subject: ""}},
		{{Issuer: "", Subject: ""}},
	} {
		if err := ValidateIdentity(bad); err == nil {
			t.Fatalf("ValidateIdentity(%+v) = nil, want error", bad)
		}
	}
}

func TestRoleAllocationPersonalIdleDefaults(t *testing.T) {
	idle, nag, reclaim := RoleAllocation{}.PersonalIdle()
	if idle != 4*time.Hour || nag != 24*time.Hour || reclaim != 0 {
		t.Fatalf("defaults = %v, %v, %v; want 4h, 24h, 0", idle, nag, reclaim)
	}
	idle, nag, reclaim = RoleAllocation{IdleAfter: time.Hour, NagEvery: 2 * time.Hour, ReclaimAfter: 72 * time.Hour}.PersonalIdle()
	if idle != time.Hour || nag != 2*time.Hour || reclaim != 72*time.Hour {
		t.Fatalf("set = %v, %v, %v; want 1h, 2h, 72h", idle, nag, reclaim)
	}
}
