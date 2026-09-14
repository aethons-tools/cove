package adminui_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/harbor"
	"github.com/aethons-tools/cove/internal/harbor/adminui"
)

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
	h := adminui.Handler(store, testLogger())
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
	// The token is in this response body...
	body := rec.Body.String()
	if !strings.Contains(strings.ToLower(body), "token") || len(body) < 20 {
		t.Errorf("enroll response should show the token panel; got:\n%s", body)
	}
	// ...but never appears on the roster afterward.
	roster := get(t, h, "/ui/roster").Body.String()
	if strings.Contains(roster, "spider-1") == false {
		t.Error("roster should list the new actor")
	}
	// (No token value is asserted persisted — Store keeps only the hash.)
}

func TestEnrollRejectsCrossOrigin(t *testing.T) {
	store := newStore(t)
	h := adminui.Handler(store, testLogger())
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

func TestEnrollValidationError(t *testing.T) {
	store := newStore(t)
	h := adminui.Handler(store, testLogger())
	rec := post(t, h, "/ui/enrollments", url.Values{"id": {""}, "role": {"worker"}}) // missing id
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing id = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "id is required") {
		t.Errorf("expected inline error; got:\n%s", rec.Body.String())
	}
}
