package adminui_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
