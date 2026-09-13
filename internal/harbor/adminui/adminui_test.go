package adminui_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/harbor"
	"github.com/aethons-tools/cove/internal/harbor/adminui"
)

func newStore(t *testing.T) harbor.Store {
	t.Helper()
	st, err := harbor.NewFileStore(t.TempDir() + "/store.json")
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	return st
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestIndexRenders(t *testing.T) {
	h := adminui.Handler(newStore(t))
	rec := get(t, h, "/ui/")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /ui/ = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "harbor") {
		t.Errorf("index body missing title marker; got:\n%s", rec.Body.String())
	}
}

func seedCove(t *testing.T, store harbor.Store) {
	t.Helper()
	if err := store.PutInstance(harbor.Instance{
		ActorID: "spider-9", Project: "acme", Role: "worker", Unit: "COV-1",
		Phase: harbor.PhaseLive, Activity: harbor.ActivityRunning,
		Lease:    harbor.Lease{Holder: "harbor-a"},
		RaisedAt: time.Now(), LastSeen: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCovesFullPage(t *testing.T) {
	store := newStore(t)
	seedCove(t, store)
	rec := get(t, adminui.Handler(store), "/ui/coves")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /ui/coves = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"<nav", `id="coves"`, "spider-9", "live", "running", `hx-trigger="every 3s"`} {
		if !strings.Contains(body, want) {
			t.Errorf("full page missing %q", want)
		}
	}
}

func TestCovesFragment(t *testing.T) {
	store := newStore(t)
	seedCove(t, store)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ui/coves", nil)
	req.Header.Set("HX-Request", "true")
	adminui.Handler(store).ServeHTTP(rec, req)
	body := rec.Body.String()
	if !strings.Contains(body, `id="coves"`) || !strings.Contains(body, "spider-9") {
		t.Errorf("fragment missing table/row; got:\n%s", body)
	}
	if strings.Contains(body, "<nav") || strings.Contains(body, "<html") {
		t.Errorf("fragment must not include page chrome; got:\n%s", body)
	}
}

func TestCovesNoSecretLeak(t *testing.T) {
	store := newStore(t)
	if err := store.PutInstance(harbor.Instance{
		ActorID: "spider-9", Project: "acme", Role: "worker",
		Phase: harbor.PhaseLive, LaunchSecretHash: "SECRET-HASH-XYZ",
		Lease: harbor.Lease{Holder: "h"}, RaisedAt: time.Now(), LastSeen: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	rec := get(t, adminui.Handler(store), "/ui/coves")
	if strings.Contains(rec.Body.String(), "SECRET-HASH-XYZ") {
		t.Error("cove view leaked the launch-secret hash")
	}
}

func TestStaticHtmxServed(t *testing.T) {
	h := adminui.Handler(newStore(t))
	rec := get(t, h, "/ui/static/htmx.min.js")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET htmx.min.js = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/javascript") {
		t.Errorf("htmx Content-Type = %q, want text/javascript*", ct)
	}
	if rec.Body.Len() == 0 {
		t.Error("htmx asset body is empty")
	}
}
