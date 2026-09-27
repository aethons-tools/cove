package jam

import (
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

// PUT stores the normalized list and keeps every other role field; GET reports
// it as managed; DELETE reverts to the kit default (nil).
func TestAdminEgressSetShowClear(t *testing.T) {
	h, store := newTestAdmin(t)
	orig := putStandingRole(t, store)

	var view EgressView
	getJSON(t, h, "/admin/roles/acme/reviewer/egress", &view)
	if view.Managed || view.Domains == nil || len(view.Domains) != 0 {
		t.Fatalf("unset policy = %+v, want managed=false domains=[]", view)
	}

	if rec := doJSON(t, h, "PUT", "/admin/roles/acme/reviewer/egress", EgressPolicy{Domains: []string{"B.org", "a.com", ".b.org"}}); rec.Code != http.StatusNoContent {
		t.Fatalf("set = %d, body=%s", rec.Code, rec.Body.String())
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
	if rec := doJSON(t, h, "PUT", "/admin/roles/acme/reviewer/egress", EgressPolicy{}); rec.Code != http.StatusNoContent {
		t.Fatalf("set empty = %d, body=%s", rec.Code, rec.Body.String())
	}
	got, _ = store.GetRole("acme", "reviewer")
	if got.Scope.Egress == nil || len(got.Scope.Egress.Domains) != 0 {
		t.Fatalf("empty policy = %+v, want set and empty", got.Scope.Egress)
	}
	getJSON(t, h, "/admin/roles/acme/reviewer/egress", &view)
	if !view.Managed || len(view.Domains) != 0 {
		t.Fatalf("empty view = %+v", view)
	}

	if rec := doReq(t, h, "DELETE", "/admin/roles/acme/reviewer/egress", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("clear = %d, body=%s", rec.Code, rec.Body.String())
	}
	got, _ = store.GetRole("acme", "reviewer")
	if !reflect.DeepEqual(got, orig) {
		t.Fatalf("role after clear = %+v, want %+v", got, orig)
	}
}

func TestAdminEgressValidation(t *testing.T) {
	h, store := newTestAdmin(t)
	putStandingRole(t, store)

	rec := doJSON(t, h, "PUT", "/admin/roles/acme/reviewer/egress", EgressPolicy{Domains: []string{"ok.com", "https://evil.com"}})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "https://evil.com") {
		t.Fatalf("bad domain = %d %q, want 400 naming it", rec.Code, rec.Body.String())
	}
	if got, _ := store.GetRole("acme", "reviewer"); got.Scope.Egress != nil {
		t.Fatalf("a rejected set must not store anything: %+v", got.Scope.Egress)
	}
	for _, c := range []struct{ method, path string }{
		{"GET", "/admin/roles/acme/nobody/egress"},
		{"PUT", "/admin/roles/acme/nobody/egress"},
		{"DELETE", "/admin/roles/acme/nobody/egress"},
	} {
		var rec = doJSON(t, h, c.method, c.path, EgressPolicy{Domains: []string{"a.com"}})
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s %s = %d, want 404", c.method, c.path, rec.Code)
		}
	}
}

// Re-putting a role (`role add` again) keeps its egress policy: it is managed
// only by the egress endpoints.
func TestAdminRolePutKeepsEgress(t *testing.T) {
	h, store := newTestAdmin(t)
	putStandingRole(t, store)
	if rec := doJSON(t, h, "PUT", "/admin/roles/acme/reviewer/egress", EgressPolicy{Domains: []string{"a.com"}}); rec.Code != http.StatusNoContent {
		t.Fatalf("set = %d", rec.Code)
	}
	if rec := doJSON(t, h, "POST", "/admin/roles", RoleBody{Project: "acme", Name: "reviewer", MaxEphemeral: 5}); rec.Code != http.StatusCreated {
		t.Fatalf("re-put role = %d", rec.Code)
	}
	got, _ := store.GetRole("acme", "reviewer")
	if got.Allocation.MaxEphemeral != 5 || got.Scope.Egress == nil || !reflect.DeepEqual(got.Scope.Egress.Domains, []string{"a.com"}) {
		t.Fatalf("role after re-put = %+v, want max-ephemeral 5 and egress kept", got)
	}
}

// GET /admin/roles reports each role's egress policy (nil = kit default).
func TestAdminRoleListCarriesEgress(t *testing.T) {
	h, store := newTestAdmin(t)
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
