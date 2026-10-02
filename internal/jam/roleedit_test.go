package jam

import (
	"errors"
	"net/http"
	"slices"
	"sync"
	"testing"
)

func newRoleStore(t *testing.T) Store {
	t.Helper()
	st, err := NewFileStore(t.TempDir() + "/store.json")
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestWriteStatus(t *testing.T) {
	if got := WriteStatus(&WriteError{Status: http.StatusConflict, Msg: "x"}, 500); got != http.StatusConflict {
		t.Errorf("WriteError status = %d", got)
	}
	if got := WriteStatus(errors.New("disk"), 500); got != 500 {
		t.Errorf("plain error status = %d, want the fallback", got)
	}
}

func TestUpdateRoleMissingIs404(t *testing.T) {
	err := UpdateRole(newRoleStore(t), "acme", "nope", func(*Role) error { return nil })
	if WriteStatus(err, 0) != http.StatusNotFound || err.Error() != "role acme/nope does not exist" {
		t.Fatalf("err = %v", err)
	}
}

func TestUpdateRoleAppliesAndFnErrorAborts(t *testing.T) {
	st := newRoleStore(t)
	must(t, st.PutRole("acme", Role{Name: "w", Scope: Scope{TTL: 1}}))
	must(t, UpdateRole(st, "acme", "w", func(r *Role) error { r.Scope.TTL = 2; return nil }))
	if r, _ := st.GetRole("acme", "w"); r.Scope.TTL != 2 {
		t.Fatalf("ttl = %v, want 2", r.Scope.TTL)
	}
	refuse := &WriteError{Status: http.StatusBadRequest, Msg: "no"}
	if err := UpdateRole(st, "acme", "w", func(r *Role) error { r.Scope.TTL = 3; return refuse }); err != refuse {
		t.Fatalf("err = %v, want the fn's error", err)
	}
	if r, _ := st.GetRole("acme", "w"); r.Scope.TTL != 2 {
		t.Fatalf("ttl = %v, want 2 (refused edit not stored)", r.Scope.TTL)
	}
}

func TestCreateRoleRefusesExisting(t *testing.T) {
	st := newRoleStore(t)
	must(t, CreateRole(st, "acme", Role{Name: "w"}))
	if err := CreateRole(st, "acme", Role{Name: "w"}); WriteStatus(err, 0) != http.StatusConflict {
		t.Fatalf("second create = %v, want 409", err)
	}
}

func TestPutRoleKeepingKeepsStandingAndEgress(t *testing.T) {
	st := newRoleStore(t)
	eg := &EgressPolicy{Domains: []string{"a.com"}}
	stand := []StandingSession{{Name: "n", Prompt: "p"}}
	must(t, st.PutRole("acme", Role{Name: "w", Scope: Scope{Egress: eg}, Allocation: RoleAllocation{Standing: stand}}))
	must(t, PutRoleKeeping(st, "acme", Role{Name: "w", Scope: Scope{TTL: 5}}))
	r, _ := st.GetRole("acme", "w")
	if r.Scope.TTL != 5 || r.Scope.Egress == nil || !slices.Equal(r.Allocation.Standing, stand) {
		t.Fatalf("role = %+v", r)
	}
}

func TestEgressSetAndClear(t *testing.T) {
	st := newRoleStore(t)
	must(t, st.PutRole("acme", Role{Name: "w"}))
	n, err := SetRoleEgress(st, "acme", "w", []string{"B.com", "a.com"})
	must(t, err)
	r, _ := st.GetRole("acme", "w")
	if n != 2 || r.Scope.Egress == nil || len(r.Scope.Egress.Domains) != 2 {
		t.Fatalf("after set: n=%d role=%+v", n, r)
	}
	if _, err := SetRoleEgress(st, "acme", "w", []string{"not a domain!"}); WriteStatus(err, 0) != http.StatusBadRequest {
		t.Fatalf("bad domain = %v, want 400", err)
	}
	must(t, ClearRoleEgress(st, "acme", "w"))
	if r, _ := st.GetRole("acme", "w"); r.Scope.Egress != nil {
		t.Fatalf("egress after clear = %+v", r.Scope.Egress)
	}
}

func TestStandingAddRemove(t *testing.T) {
	st := newRoleStore(t)
	must(t, st.PutRole("acme", Role{Name: "w"}))
	must(t, AddStanding(st, "acme", "w", StandingSession{Name: "n1", Prompt: "p"}))
	for _, tc := range []struct {
		s    StandingSession
		want int
	}{
		{StandingSession{Name: "n1", Prompt: "p"}, http.StatusBadRequest}, // duplicate
		{StandingSession{Name: "", Prompt: "p"}, http.StatusBadRequest},   // missing name
		{StandingSession{Name: "n2"}, http.StatusBadRequest},              // missing prompt
	} {
		if err := AddStanding(st, "acme", "w", tc.s); WriteStatus(err, 0) != tc.want {
			t.Errorf("add %+v = %v, want %d", tc.s, err, tc.want)
		}
	}
	if err := RemoveStanding(st, "acme", "w", "ghost"); WriteStatus(err, 0) != http.StatusNotFound {
		t.Errorf("remove missing = %v, want 404", err)
	}
	must(t, RemoveStanding(st, "acme", "w", "n1"))
	if r, _ := st.GetRole("acme", "w"); r.Allocation.Standing != nil {
		t.Fatalf("standing after remove = %+v, want nil", r.Allocation.Standing)
	}
}

// Concurrent read-modify-writes through the shared lock never drop each other.
func TestConcurrentStandingAddsAllLand(t *testing.T) {
	st := newRoleStore(t)
	must(t, st.PutRole("acme", Role{Name: "w"}))
	var wg sync.WaitGroup
	for _, n := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := AddStanding(st, "acme", "w", StandingSession{Name: n, Prompt: "p"}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if r, _ := st.GetRole("acme", "w"); len(r.Allocation.Standing) != 8 {
		t.Fatalf("standing = %d sessions, want 8", len(r.Allocation.Standing))
	}
}
