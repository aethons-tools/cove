package adminui_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam/adminui"
)

func del(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodDelete, path, nil)
	req.Header.Set("Origin", "http://"+req.Host)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// credKnown accepts the credentials the rich-role fixture maps.
func credKnown(n string) bool { return n == "git-pat" || n == "other-pat" }

func TestRolePagePrefillsEditForms(t *testing.T) {
	body := get(t, adminui.Handler(seedRichRole(t), testLogger(), nil, nil, credKnown, nil), "/ui/roles/acme/review").Body.String()
	for _, want := range []string{
		`hx-post="/ui/roles/acme/review/scope"`,
		`value="git=git-pat,anthropic"`,
		`value="human:*,channel:eng"`,
		`value="1h30m"`,
		`hx-post="/ui/roles/acme/review/allocation"`,
		`name="nag-every" value="2h"`,
		`hx-post="/ui/roles/acme/review/egress"`,
		`hx-post="/ui/roles/acme/review/standing"`,
		`hx-delete="/ui/roles/acme/review/standing/nightly"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("role page missing %q", want)
		}
	}
}

func TestEditScopeKeepsAllocationAndEgress(t *testing.T) {
	store := seedRichRole(t)
	before, _ := store.GetRole("acme", "review")
	h := adminui.Handler(store, testLogger(), nil, nil, credKnown, nil)
	rec := post(t, h, "/ui/roles/acme/review/scope", url.Values{
		"destinations": {"git=other-pat"}, "addressing": {"human:bob"}, "ttl": {"2h"}, "kit": {""},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("scope edit = %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `id="role"`) || strings.Contains(rec.Body.String(), "<html") {
		t.Errorf("scope edit should return the role fragment")
	}
	r, _ := store.GetRole("acme", "review")
	if !reflect.DeepEqual(r.Scope.Destinations, []string{"git"}) || r.Scope.Credentials["git"] != "other-pat" ||
		!reflect.DeepEqual(r.Scope.Addressing, []string{"human:bob"}) || r.Scope.TTL != 2*time.Hour {
		t.Fatalf("scope = %+v", r.Scope)
	}
	if !reflect.DeepEqual(r.Allocation, before.Allocation) || !reflect.DeepEqual(r.Scope.Egress, before.Scope.Egress) {
		t.Fatalf("scope edit changed allocation/egress: %+v / %+v", r.Allocation, r.Scope.Egress)
	}
}

func TestEditScopeRejects(t *testing.T) {
	h := adminui.Handler(seedRichRole(t), testLogger(), nil, nil, credKnown, nil)
	for name, form := range map[string]url.Values{
		"unknown cred": {"destinations": {"git=nope"}},
		"bad ttl":      {"ttl": {"soon"}},
		"unknown kit":  {"kit": {"ghost"}},
	} {
		if rec := post(t, h, "/ui/roles/acme/review/scope", form); rec.Code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", name, rec.Code)
		}
	}
	if rec := post(t, h, "/ui/roles/acme/ghost/scope", url.Values{}); rec.Code != http.StatusNotFound {
		t.Errorf("missing role = %d, want 404", rec.Code)
	}
}

func TestEditAllocationKeepsScopeAndStanding(t *testing.T) {
	store := seedRichRole(t)
	before, _ := store.GetRole("acme", "review")
	h := adminui.Handler(store, testLogger(), nil, nil, credKnown, nil)
	rec := post(t, h, "/ui/roles/acme/review/allocation", url.Values{
		"max-ephemeral": {"3"}, "max-personal": {"1"}, "max-personal-per-owner": {""},
		"idle-after": {"30m"}, "nag-every": {""}, "reclaim-after": {"72h"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("allocation edit = %d: %s", rec.Code, rec.Body.String())
	}
	r, _ := store.GetRole("acme", "review")
	a := r.Allocation
	if a.MaxEphemeral != 3 || a.MaxPersonal != 1 || a.MaxPersonalPerOwner != 0 ||
		a.IdleAfter != 30*time.Minute || a.NagEvery != 0 || a.ReclaimAfter != 72*time.Hour {
		t.Fatalf("allocation = %+v", a)
	}
	if !reflect.DeepEqual(a.Standing, before.Allocation.Standing) || !reflect.DeepEqual(r.Scope, before.Scope) {
		t.Fatalf("allocation edit changed scope or standing")
	}
	for _, bad := range []url.Values{{"max-personal": {"-1"}}, {"idle-after": {"later"}}} {
		if rec := post(t, h, "/ui/roles/acme/review/allocation", bad); rec.Code != http.StatusBadRequest {
			t.Errorf("%v = %d, want 400", bad, rec.Code)
		}
	}
}

func TestEditEgressSetAndReset(t *testing.T) {
	store := seedRichRole(t)
	h := adminui.Handler(store, testLogger(), nil, nil, credKnown, nil)
	if rec := post(t, h, "/ui/roles/acme/review/egress", url.Values{"domains": {"a.com\n.b.org, c.net"}}); rec.Code != http.StatusOK {
		t.Fatalf("egress set = %d: %s", rec.Code, rec.Body.String())
	}
	r, _ := store.GetRole("acme", "review")
	if r.Scope.Egress == nil || len(r.Scope.Egress.Domains) != 3 {
		t.Fatalf("egress = %+v", r.Scope.Egress)
	}
	if rec := post(t, h, "/ui/roles/acme/review/egress", url.Values{"domains": {"http://x"}}); rec.Code != http.StatusBadRequest {
		t.Errorf("bad domain = %d, want 400", rec.Code)
	}
	if rec := del(t, h, "/ui/roles/acme/review/egress"); rec.Code != http.StatusOK {
		t.Fatalf("egress reset = %d", rec.Code)
	}
	if r, _ := store.GetRole("acme", "review"); r.Scope.Egress != nil {
		t.Fatalf("egress after reset = %+v", r.Scope.Egress)
	}
}

func TestEditStandingAddRemove(t *testing.T) {
	store := seedRichRole(t)
	h := adminui.Handler(store, testLogger(), nil, nil, credKnown, nil)
	if rec := post(t, h, "/ui/roles/acme/review/standing", url.Values{"name": {"weekly"}, "prompt": {"tidy up"}}); rec.Code != http.StatusOK {
		t.Fatalf("standing add = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := post(t, h, "/ui/roles/acme/review/standing", url.Values{"name": {"weekly"}, "prompt": {"again"}}); rec.Code != http.StatusBadRequest {
		t.Errorf("duplicate standing = %d, want 400", rec.Code)
	}
	if rec := del(t, h, "/ui/roles/acme/review/standing/nightly"); rec.Code != http.StatusOK {
		t.Fatalf("standing remove = %d", rec.Code)
	}
	r, _ := store.GetRole("acme", "review")
	if len(r.Allocation.Standing) != 1 || r.Allocation.Standing[0].Name != "weekly" {
		t.Fatalf("standing = %+v", r.Allocation.Standing)
	}
}

// The Roles page form only creates: an existing role is edited on its page.
func TestCreateRoleRedirectsAndRefusesExisting(t *testing.T) {
	store := newStore(t)
	h := adminui.Handler(store, testLogger(), nil, nil, anyCred, nil)
	rec := post(t, h, "/ui/roles", url.Values{"project": {"acme"}, "name": {"w"}})
	if rec.Code != http.StatusOK || rec.Header().Get("HX-Redirect") != "/ui/roles/acme/w" {
		t.Fatalf("create = %d redirect=%q", rec.Code, rec.Header().Get("HX-Redirect"))
	}
	if rec := post(t, h, "/ui/roles", url.Values{"project": {"acme"}, "name": {"w"}}); rec.Code != http.StatusConflict {
		t.Fatalf("re-create = %d, want 409", rec.Code)
	}
}

func TestRoleEditsRefuseCrossOrigin(t *testing.T) {
	h := adminui.Handler(seedRichRole(t), testLogger(), nil, nil, credKnown, nil)
	req := httptest.NewRequest(http.MethodPost, "/ui/roles/acme/review/scope", strings.NewReader("ttl=1h"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://evil.example")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-origin scope edit = %d, want 403", rec.Code)
	}
}
