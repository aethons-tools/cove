package jam

import (
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

// NormalizeEgress lowercases, validates, dedupes, sorts, and drops an entry
// another wildcard in the list already covers.
func TestNormalizeEgress(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"nil is empty", nil, []string{}},
		{"lowercase dedupe sort", []string{"B.org", "a.com", "b.org"}, []string{"a.com", "b.org"}},
		{"wildcard covers apex and subdomains", []string{"x.com", ".x.com", "a.x.com", ".a.x.com", "y.com"}, []string{".x.com", "y.com"}},
		{"exact never covers wildcard", []string{"x.com", ".a.x.com"}, []string{".a.x.com", "x.com"}},
		{"suffix match is label-aligned", []string{".x.com", "notx.com"}, []string{".x.com", "notx.com"}},
		{"surrounding space trimmed", []string{"  a.com "}, []string{"a.com"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := NormalizeEgress(c.in)
			if err != nil {
				t.Fatalf("NormalizeEgress(%v): %v", c.in, err)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("NormalizeEgress(%v) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

// The syntax rule matches the in-box helper's: at least two labels; no scheme,
// port, path, glob or whitespace. The error names the first bad domain.
func TestNormalizeEgressRejects(t *testing.T) {
	for _, bad := range []string{"https://x.com", "*.x.com", "x", "x.com:443", "x.com/p", "a x.com", "-a.x.com", "a..x.com", "x.com.", "", "."} {
		t.Run(bad, func(t *testing.T) {
			_, err := NormalizeEgress([]string{"ok.com", bad, "also-bad"})
			if err == nil {
				t.Fatalf("NormalizeEgress accepted %q", bad)
			}
			if !strings.Contains(err.Error(), `"`+bad+`"`) {
				t.Fatalf("error %q must name the first bad domain %q", err, bad)
			}
		})
	}
}

// SetRoleEgress stores the normalized list and keeps every other role field;
// GET reports it as managed; ClearRoleEgress reverts to the kit default (nil).
func TestRoleEgressSetShowClear(t *testing.T) {
	h, store := newTestAdmin(t)
	orig := putStandingRole(t, store)

	var view EgressView
	getJSON(t, h, "/admin/roles/acme/reviewer/egress", &view)
	if view.Managed || view.Domains == nil || len(view.Domains) != 0 {
		t.Fatalf("unset policy = %+v, want managed=false domains=[]", view)
	}

	if n, err := SetRoleEgress(store, "acme", "reviewer", []string{"B.org", "a.com", ".b.org"}); err != nil || n != 2 {
		t.Fatalf("set = %d, %v", n, err)
	}
	got, _ := store.GetRole("acme", "reviewer")
	keep := orig
	keep.Scope.Egress = &EgressPolicy{Domains: []string{".b.org", "a.com"}}
	if !reflect.DeepEqual(got, keep) {
		t.Fatalf("role after set = %+v, want %+v (other fields kept)", got, keep)
	}
	getJSON(t, h, "/admin/roles/acme/reviewer/egress", &view)
	if !view.Managed || !reflect.DeepEqual(view.Domains, []string{".b.org", "a.com"}) {
		t.Fatalf("view = %+v", view)
	}

	// An empty list is a valid, managed policy (nothing beyond base + infra).
	if _, err := SetRoleEgress(store, "acme", "reviewer", nil); err != nil {
		t.Fatalf("set empty: %v", err)
	}
	got, _ = store.GetRole("acme", "reviewer")
	if got.Scope.Egress == nil || len(got.Scope.Egress.Domains) != 0 {
		t.Fatalf("empty policy = %+v, want set and empty", got.Scope.Egress)
	}
	getJSON(t, h, "/admin/roles/acme/reviewer/egress", &view)
	if !view.Managed || len(view.Domains) != 0 {
		t.Fatalf("empty view = %+v", view)
	}

	if err := ClearRoleEgress(store, "acme", "reviewer"); err != nil {
		t.Fatalf("clear: %v", err)
	}
	got, _ = store.GetRole("acme", "reviewer")
	if !reflect.DeepEqual(got, orig) {
		t.Fatalf("role after clear = %+v, want %+v", got, orig)
	}
}

func TestRoleEgressValidation(t *testing.T) {
	h, store := newTestAdmin(t)
	putStandingRole(t, store)

	_, err := SetRoleEgress(store, "acme", "reviewer", []string{"ok.com", "https://evil.com"})
	var we *WriteError
	if !errors.As(err, &we) || we.Status != http.StatusBadRequest || !strings.Contains(err.Error(), "https://evil.com") {
		t.Fatalf("bad domain = %v, want a 400 naming it", err)
	}
	if got, _ := store.GetRole("acme", "reviewer"); got.Scope.Egress != nil {
		t.Fatalf("a rejected set must not store anything: %+v", got.Scope.Egress)
	}
	if _, err := SetRoleEgress(store, "acme", "nobody", []string{"a.com"}); err == nil || WriteStatus(err, 500) != http.StatusNotFound {
		t.Fatalf("set on a missing role = %v, want 404", err)
	}
	if err := ClearRoleEgress(store, "acme", "nobody"); err == nil || WriteStatus(err, 500) != http.StatusNotFound {
		t.Fatalf("clear on a missing role = %v, want 404", err)
	}
	if rec := doReq(t, h, "GET", "/admin/roles/acme/nobody/egress", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("GET missing role egress = %d, want 404", rec.Code)
	}
}

// Re-putting a role keeps its egress policy: it is managed only by
// SetRoleEgress/ClearRoleEgress.
func TestRolePutKeepsEgress(t *testing.T) {
	_, store := newTestAdmin(t)
	putStandingRole(t, store)
	if _, err := SetRoleEgress(store, "acme", "reviewer", []string{"a.com"}); err != nil {
		t.Fatal(err)
	}
	if err := PutRoleKeeping(store, "acme", Role{Name: "reviewer", Allocation: RoleAllocation{MaxEphemeral: 5}}); err != nil {
		t.Fatalf("re-put role: %v", err)
	}
	got, _ := store.GetRole("acme", "reviewer")
	if got.Allocation.MaxEphemeral != 5 || got.Scope.Egress == nil || !reflect.DeepEqual(got.Scope.Egress.Domains, []string{"a.com"}) {
		t.Fatalf("role after re-put = %+v, want max-ephemeral 5 and egress kept", got)
	}
}

// GET /admin/roles reports each role's egress policy (nil = kit default).
func TestAdminRoleListCarriesEgress(t *testing.T) {
	h, store := newTestAdmin(t)
	mustCreateProject(t, store, "acme")
	if err := store.PutRole("acme", Role{Name: "a", Scope: Scope{Egress: &EgressPolicy{Domains: []string{"x.com"}}}}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRole("acme", Role{Name: "b"}); err != nil {
		t.Fatal(err)
	}
	var list []RoleSummary
	getJSON(t, h, "/admin/roles?project=acme", &list)
	got := map[string]*EgressPolicy{}
	for _, rs := range list {
		got[rs.Name] = rs.Egress
	}
	if got["a"] == nil || !reflect.DeepEqual(got["a"].Domains, []string{"x.com"}) || got["b"] != nil {
		t.Fatalf("summaries egress = a:%+v b:%+v", got["a"], got["b"])
	}
}
