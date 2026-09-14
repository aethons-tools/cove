package adminui_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/harbor"
	"github.com/aethons-tools/cove/internal/harbor/adminui"
)

// tokenPattern matches the <code>...</code> element the enroll-result
// template (templates/roster.html) renders the one-time identity token
// into: {{define "enroll-result"}} ... <code>{{.Token}}</code> ... {{end}}.
var tokenPattern = regexp.MustCompile(`<code>([^<]+)</code>`)

// post issues a form POST with a matching Origin (passes the CSRF check).
func post(t *testing.T, h http.Handler, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://"+req.Host)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestEnrollCreatesActorAndShowsTokenOnce(t *testing.T) {
	store := newStore(t)
	if err := store.PutRole("acme", harbor.Role{Name: "worker"}); err != nil {
		t.Fatal(err)
	}
	h := adminui.Handler(store, testLogger(), nil, anyCred)
	rec := post(t, h, "/ui/enrollments", url.Values{"id": {"spider-1"}, "project": {"acme"}, "role": {"worker"}})
	if rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
		t.Fatalf("enroll = %d, want 200/201", rec.Code)
	}
	// The actor now exists.
	found := false
	for _, a := range store.ListActors() {
		if a.ID == "spider-1" {
			found = true
		}
	}
	if !found {
		t.Fatal("actor spider-1 was not created")
	}
	// The real one-time identity token is in this response body...
	body := rec.Body.String()
	m := tokenPattern.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("enroll response should show the token panel; got:\n%s", body)
	}
	token := m[1]
	if len(token) < 20 {
		t.Fatalf("captured token looks too short to be real: %q", token)
	}
	// ...but never appears on the roster afterward.
	roster := get(t, h, "/ui/roster").Body.String()
	if !strings.Contains(roster, "spider-1") {
		t.Error("roster should list the new actor")
	}
	if strings.Contains(roster, token) {
		t.Error("roster must never contain the one-time identity token")
	}
	// (No token value is asserted persisted — Store keeps only the hash.)
}

func TestEnrollRejectsCrossOrigin(t *testing.T) {
	store := newStore(t)
	h := adminui.Handler(store, testLogger(), nil, anyCred)
	req := httptest.NewRequest(http.MethodPost, "/ui/enrollments", strings.NewReader("id=x&role=worker"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://evil.example")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-origin enroll = %d, want 403", rec.Code)
	}
	// No Origin and no Referer → also refused (fail-closed).
	req2 := httptest.NewRequest(http.MethodPost, "/ui/enrollments", strings.NewReader("id=x&role=worker"))
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusForbidden {
		t.Fatalf("no-Origin enroll = %d, want 403", rec2.Code)
	}
}

// findActor scans the store's roster for an actor by id.
func findActor(store harbor.Store, id string) (harbor.Actor, bool) {
	for _, a := range store.ListActors() {
		if a.ID == id {
			return a, true
		}
	}
	return harbor.Actor{}, false
}

func TestRevokeActor(t *testing.T) {
	store := newStore(t)
	if err := store.AddActor(harbor.Actor{ID: "spider-2", TokenHash: "h"}); err != nil {
		t.Fatal(err)
	}
	h := adminui.Handler(store, testLogger(), nil, anyCred)
	req := httptest.NewRequest(http.MethodDelete, "/ui/enrollments/spider-2", nil)
	req.Header.Set("Origin", "http://"+req.Host)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("revoke = %d, want 200", rec.Code)
	}
	if _, ok := findActor(store, "spider-2"); ok {
		t.Error("actor should be gone after revoke")
	}
	if strings.Contains(rec.Body.String(), "spider-2") {
		t.Error("returned roster fragment should not list the revoked actor")
	}
}

func TestCreateAndDeleteRole(t *testing.T) {
	store := newStore(t)
	h := adminui.Handler(store, testLogger(), nil, anyCred)

	rec := post(t, h, "/ui/roles", url.Values{"project": {"acme"}, "name": {"review"}, "destinations": {"git"}, "ttl-seconds": {"3600"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("create role = %d, want 200", rec.Code)
	}
	if _, ok := store.GetRole("acme", "review"); !ok {
		t.Fatal("role acme/review not created")
	}

	// kit that doesn't exist → 400 inline error
	bad := post(t, h, "/ui/roles", url.Values{"project": {"acme"}, "name": {"x"}, "kit": {"ghost"}})
	if bad.Code != http.StatusBadRequest || !strings.Contains(bad.Body.String(), "kit") {
		t.Fatalf("bad kit = %d %q, want 400 mentioning kit", bad.Code, bad.Body.String())
	}

	del := httptest.NewRequest(http.MethodDelete, "/ui/roles/acme/review", nil)
	del.Header.Set("Origin", "http://"+del.Host)
	drec := httptest.NewRecorder()
	h.ServeHTTP(drec, del)
	if drec.Code != http.StatusOK {
		t.Fatalf("delete role = %d, want 200", drec.Code)
	}
	if _, ok := store.GetRole("acme", "review"); ok {
		t.Error("role should be gone after delete")
	}
}

func TestAddAndRemoveGrant(t *testing.T) {
	store := newStore(t)
	if err := store.PutRole("acme", harbor.Role{Name: "worker", Scope: harbor.Scope{Destinations: []string{"git"}}}); err != nil {
		t.Fatal(err)
	}
	if err := store.AddActor(harbor.Actor{ID: "spider-3", TokenHash: "h"}); err != nil {
		t.Fatal(err)
	}
	h := adminui.Handler(store, testLogger(), nil, anyCred)

	rec := post(t, h, "/ui/actors/spider-3/grants", url.Values{"project": {"acme"}, "role": {"worker"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("add grant = %d, want 200", rec.Code)
	}
	a, _ := findActor(store, "spider-3")
	if len(a.Grants) != 1 || a.Grants[0].Role != "worker" {
		t.Fatalf("grants = %+v, want one worker grant", a.Grants)
	}

	del := httptest.NewRequest(http.MethodDelete, "/ui/actors/spider-3/grants/acme/worker", nil)
	del.Header.Set("Origin", "http://"+del.Host)
	drec := httptest.NewRecorder()
	h.ServeHTTP(drec, del)
	if drec.Code != http.StatusOK {
		t.Fatalf("remove grant = %d, want 200", drec.Code)
	}
	a2, _ := findActor(store, "spider-3")
	if len(a2.Grants) != 0 {
		t.Errorf("grants should be empty after remove, got %+v", a2.Grants)
	}
}

func TestEnrollValidationError(t *testing.T) {
	store := newStore(t)
	h := adminui.Handler(store, testLogger(), nil, anyCred)
	rec := post(t, h, "/ui/enrollments", url.Values{"id": {""}, "role": {"worker"}}) // missing id
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing id = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "id is required") {
		t.Errorf("expected inline error; got:\n%s", rec.Body.String())
	}
}
