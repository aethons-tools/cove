package adminui_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam/adminui"
	"github.com/aethons-tools/cove/internal/jam/appui"
	"github.com/aethons-tools/cove/internal/jam/uiassets"
)

// Old admin pages (and the preview's next/ paths) redirect to their new
// homes in the app, the query kept (admin SPA cutover).
func TestRedirectOld(t *testing.T) {
	h := adminui.Handler(newStore(t), testLogger(), nil, nil, anyCred, nil)
	for from, to := range map[string]string{
		"/app/admin/users":                            "/app/admin/config/users",
		"/app/admin/users/u?x=1":                      "/app/admin/config/users/u?x=1",
		"/app/admin/projects":                         "/app/admin/config/projects",
		"/app/admin/projects/p/escalation":            "/app/admin/config/projects/p/escalation",
		"/app/admin/projects/p/roles/r":               "/app/admin/config/projects/p/agents/r",
		"/app/admin/projects/p/roles/r/setup":         "/app/admin/config/projects/p/agents/r",
		"/app/admin/agents/a/setup":                   "/app/admin/agents/a",
		"/app/admin/agents/a?project=p":               "/app/admin/agents/a",
		"/app/admin/kits/k/draft":                     "/app/admin/config/kits/k",
		"/app/admin/destinations/d":                   "/app/admin/config/destinations/d",
		"/app/admin/model-specs/m":                    "/app/admin/config/model-specs/m",
		"/app/admin/intercom?participant=x":           "/app/admin/health/intercom?participant=x",
		"/app/admin/config":                           "/app/admin/config/tracking",
		"/app/admin/specs":                            "/app/admin/config/kits",
		"/app/admin/roles":                            "/app/admin/",
		"/app/admin/roles/p/r":                        "/app/admin/config/projects/p/agents/r",
		"/app/admin/next/":                            "/app/admin/",
		"/app/admin/next/config/users?x=1":            "/app/admin/config/users?x=1",
		"/app/admin/next/agents/a/session?stream=s":   "/app/admin/agents/a/session?stream=s",
		"/app/admin/kits/k/draft?file=kit.yml&diff=1": "/app/admin/config/kits/k?ddiff=1&dfile=kit.yml",
		"/app/admin/coves":                            "/app/admin/agents",
		"/app/admin/coves/c":                          "/app/admin/agents/c",
		"/app/admin/coves/c/session":                  "/app/admin/agents/c/session",
		"/app/admin/actors":                           "/app/admin/agents",
		"/app/admin/projects/p/roles":                 "/app/admin/config/projects/p/agents",
		"/app/admin/next/%2e%2e/%2e%2e/x":             "/app/admin/",
	} {
		rec := get(t, h, from)
		if rec.Code != http.StatusFound || rec.Header().Get("Location") != to {
			t.Errorf("GET %s = %d → %q, want 302 → %q", from, rec.Code, rec.Header().Get("Location"), to)
		}
	}
	// The app's own paths are its page (503 in a binary without the bundle).
	for _, p := range []string{"/app/admin/agents?status=running&q=x", "/app/admin/", "/app/admin/config/tracking", "/app/admin/config/users/u", "/app/admin/agents", "/app/admin/agents/a",
		"/app/admin/agents/a/session", "/app/admin/health", "/app/admin/health/intercom", "/app/admin/search?q=x"} {
		if rec := get(t, h, p); rec.Code == http.StatusFound || rec.Code >= 400 && !(rec.Code == http.StatusServiceUnavailable && !uiassets.AppBuilt()) {
			t.Errorf("GET %s = %d %s", p, rec.Code, rec.Header().Get("Location"))
		}
	}
	// A write to an old HTML route is no page.
	r := appui.WithPrincipal(httptest.NewRequest("POST", "/app/admin/users", strings.NewReader("name=x")), appui.Principal{Admin: true, Local: true})
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /app/admin/users = %d", rec.Code)
	}
}
