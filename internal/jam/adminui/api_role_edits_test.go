package adminui_test

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam/adminui"
)

// richRoleCred knows the rich role's credentials, and credKnown's.
func richRoleCred(n string) bool { return credKnown(n) || n == "git-default" || n == "anth-key" }

// A scope edit replaces destinations (with their credentials), addressing
// and TTL, and keeps the allocation and egress; an unknown credential, a bad
// TTL or destinations field, an unknown kit or a missing role is refused.
func TestRoleAPIScopeEdit(t *testing.T) {
	e := editOn(t, editSetup{seed: seedRichRole(t), cred: richRoleCred})
	h := e.H
	before, _ := e.Store().GetRole("acme", "review")
	const path = "/app/admin/api/projects/acme/roles/review/scope"
	var res adminui.RoleResult
	if rec := apiDo(t, h, "POST", path, map[string]string{"destinations": "git=other-pat", "addressing": "human:bob,\nchannel:eng", "ttl": "7200", "kit": ""}, &res); rec.Code != 200 {
		t.Fatalf("scope = %d %s", rec.Code, rec.Body)
	}
	r, _ := e.Store().GetRole("acme", "review")
	if !reflect.DeepEqual(r.Scope.Destinations, []string{"git"}) || r.Scope.Credentials["git"] != "other-pat" ||
		!reflect.DeepEqual(r.Scope.Addressing, []string{"human:bob", "channel:eng"}) || r.Scope.TTL != 2*time.Hour { // a bare integer is seconds
		t.Fatalf("scope = %+v", r.Scope)
	}
	if !reflect.DeepEqual(r.Allocation, before.Allocation) || !reflect.DeepEqual(r.Scope.Egress, before.Scope.Egress) {
		t.Errorf("a scope edit changed allocation or egress")
	}
	if res.Role.TTL != "2h" {
		t.Errorf("answered role = %+v", res.Role)
	}
	for name, tc := range map[string]struct {
		body map[string]string
		want string
	}{
		"unknown cred": {map[string]string{"destinations": "git=nope"}, ""},
		"bad dests":    {map[string]string{"destinations": "git="}, ""},
		"bad ttl":      {map[string]string{"ttl": "soon"}, `TTL must be a duration like \"30m\" or \"1h30m\"`},
		"negative ttl": {map[string]string{"ttl": "-5m"}, "TTL must be a duration"},
		"unknown kit":  {map[string]string{"kit": "ghost"}, `kit \"ghost\" does not exist`},
		"unknown spec": {map[string]string{"modelSpec": "ghost"}, "ghost"},
	} {
		rec := apiDo(t, h, "POST", path, tc.body, nil)
		if rec.Code != 400 || !strings.Contains(rec.Body.String(), tc.want) {
			t.Errorf("%s = %d %s, want 400 %q", name, rec.Code, rec.Body, tc.want)
		}
	}
	if after, _ := e.Store().GetRole("acme", "review"); !reflect.DeepEqual(after.Scope, r.Scope) {
		t.Errorf("a refused edit changed the scope: %+v", after.Scope)
	}
	wantErr(t, apiDo(t, h, "POST", "/app/admin/api/projects/acme/roles/ghost/scope", map[string]string{}, nil), 404, "not_found")
}

// An allocation edit sets every limit and keeps the scope and the standing
// sessions; a negative count or a bad duration is refused.
func TestRoleAPIAllocationEdit(t *testing.T) {
	e := editOn(t, editSetup{seed: seedRichRole(t), cred: richRoleCred})
	h := e.H
	before, _ := e.Store().GetRole("acme", "review")
	const path = "/app/admin/api/projects/acme/roles/review/allocation"
	if rec := apiDo(t, h, "POST", path, map[string]string{"maxEphemeral": "3", "maxPersonal": "1", "maxPersonalPerOwner": "",
		"idleAfter": "30m", "nagEvery": "", "reclaimAfter": "72h"}, nil); rec.Code != 200 {
		t.Fatalf("allocation = %d %s", rec.Code, rec.Body)
	}
	r, _ := e.Store().GetRole("acme", "review")
	a := r.Allocation
	if a.MaxEphemeral != 3 || a.MaxPersonal != 1 || a.MaxPersonalPerOwner != 0 || a.IdleAfter != 30*time.Minute || a.NagEvery != 0 || a.ReclaimAfter != 72*time.Hour {
		t.Fatalf("allocation = %+v", a)
	}
	if !reflect.DeepEqual(a.Standing, before.Allocation.Standing) || !reflect.DeepEqual(r.Scope, before.Scope) {
		t.Errorf("an allocation edit changed scope or standing")
	}
	for _, bad := range []map[string]string{{"maxPersonal": "-1"}, {"maxEphemeral": "x"}, {"idleAfter": "later"}, {"reclaimAfter": "-1h"}} {
		wantErr(t, apiDo(t, h, "POST", path, bad, nil), 400, "bad_request")
	}
	if after, _ := e.Store().GetRole("acme", "review"); !reflect.DeepEqual(after.Allocation, a) {
		t.Errorf("a refused edit changed the allocation: %+v", after.Allocation)
	}
}
