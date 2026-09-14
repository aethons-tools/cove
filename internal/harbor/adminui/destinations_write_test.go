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

// credOK recognizes "known-cred" and rejects everything else.
func credOK(name string) bool { return name == "known-cred" }

func destHandler(t *testing.T, store harbor.Store) http.Handler {
	t.Helper()
	return adminui.Handler(store, testLogger(), nil, credOK, nil)
}

func TestAddDestination(t *testing.T) {
	store := newStore(t)
	h := destHandler(t, store)
	rec := post(t, h, "/ui/destinations", url.Values{
		"name": {"anthropic"}, "route": {"/anthropic/"}, "upstream": {"https://api.anthropic.com"},
		"identity-in": {"x-api-key"}, "cred-name": {"known-cred"}, "apply": {"x-api-key"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("add destination = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	found := false
	for _, d := range store.ListDestinations() {
		if d.Name == "anthropic" {
			found = true
		}
	}
	if !found {
		t.Fatal("destination not added")
	}
}

func TestAddDestinationBadCred400(t *testing.T) {
	store := newStore(t)
	h := destHandler(t, store)
	rec := post(t, h, "/ui/destinations", url.Values{
		"name": {"x"}, "route": {"/x/"}, "upstream": {"https://x"}, "cred-name": {"ghost"},
	})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "cred-name") {
		t.Fatalf("bad cred = %d %q, want 400 mentioning cred-name", rec.Code, rec.Body.String())
	}
	if len(store.ListDestinations()) != 0 {
		t.Error("destination with bad cred must not be added")
	}
}

// TestDestinationWriteCSRF mirrors TestEnrollRejectsCrossOrigin / TestRaiseCoveCSRF:
// a cross-origin POST to /ui/destinations must be refused, and no
// destination must be added.
func TestDestinationWriteCSRF(t *testing.T) {
	store := newStore(t)
	h := destHandler(t, store)
	req := httptest.NewRequest(http.MethodPost, "/ui/destinations", strings.NewReader(
		"name=x&route=%2Fx%2F&upstream=https%3A%2F%2Fx"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://evil.example")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-origin add destination = %d, want 403", rec.Code)
	}
	if len(store.ListDestinations()) != 0 {
		t.Error("no destination should exist after a cross-origin add")
	}
}

// TestAddDestinationNoCredLeak asserts the destinations-table fragment
// returned by a successful add shows only name/route/upstream/repo-scoped,
// never the cred-name value. Uses a dedicated credExists stub (rather than
// the shared credOK, which only accepts "known-cred") so the add succeeds
// with a distinctive cred-name.
func TestAddDestinationNoCredLeak(t *testing.T) {
	const secretCred = "SECRET-CRED-REF"
	store := newStore(t)
	h := adminui.Handler(store, testLogger(), nil, func(name string) bool { return name == secretCred }, nil)
	rec := post(t, h, "/ui/destinations", url.Values{
		"name": {"anthropic"}, "route": {"/anthropic/"}, "upstream": {"https://api.anthropic.com"},
		"identity-in": {"x-api-key"}, "cred-name": {secretCred}, "apply": {"x-api-key"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("add destination = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), secretCred) {
		t.Errorf("add-destination response must not leak cred-name; got:\n%s", rec.Body.String())
	}
}

func TestRemoveDestination(t *testing.T) {
	store := newStore(t)
	if err := store.AddDestination(harbor.Destination{Name: "d1", Route: "/d1/", Upstream: "https://d1"}); err != nil {
		t.Fatal(err)
	}
	h := destHandler(t, store)
	req := httptest.NewRequest(http.MethodDelete, "/ui/destinations/d1", nil)
	req.Header.Set("Origin", "http://"+req.Host)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("remove destination = %d, want 200", rec.Code)
	}
	if len(store.ListDestinations()) != 0 {
		t.Error("destination should be gone after remove")
	}
}
