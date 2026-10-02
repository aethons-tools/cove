package adminui_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminui"
)

// credOK recognizes "known-cred" and rejects everything else.
func credOK(name string) bool { return name == "known-cred" }

func destHandler(t *testing.T, store jam.Store) http.Handler {
	t.Helper()
	return adminui.Handler(store, testLogger(), nil, nil, credOK, nil)
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
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "ghost") {
		t.Fatalf("bad cred = %d %q, want 400 naming the credential", rec.Code, rec.Body.String())
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

// TestAddDestinationShowsCredNameNotValue: credential names are references,
// so the destinations table shows the default credential's name; the resolver
// is only ever asked whether it exists, so no value can reach the page.
func TestAddDestinationShowsCredNameNotValue(t *testing.T) {
	const credRef = "CRED-REF"
	store := newStore(t)
	h := adminui.Handler(store, testLogger(), nil, nil, func(name string) bool { return name == credRef }, nil)
	rec := post(t, h, "/ui/destinations", url.Values{
		"name": {"anthropic"}, "route": {"/anthropic/"}, "upstream": {"https://api.anthropic.com"},
		"identity-in": {"x-api-key"}, "cred-name": {credRef}, "apply": {"x-api-key"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("add destination = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), credRef) {
		t.Errorf("destinations table should show the default credential's name; got:\n%s", rec.Body.String())
	}
}

func TestRemoveDestination(t *testing.T) {
	store := newStore(t)
	if err := store.AddDestination(jam.Destination{Name: "d1", Route: "/d1/", Upstream: "https://d1"}); err != nil {
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
