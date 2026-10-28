package adminui_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminui"
	"github.com/aethons-tools/cove/internal/jam/appui"
	"github.com/aethons-tools/cove/internal/jam/uiassets"
)

// apiGet serves GET path to h as principal p.
func apiGet(t *testing.T, h http.Handler, path string, p appui.Principal) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, appui.WithPrincipal(httptest.NewRequest(http.MethodGet, path, nil), p))
	return rec
}

func adminHandler(t *testing.T) (http.Handler, jam.Store) {
	t.Helper()
	store := newStore(t)
	return adminui.Handler(store, testLogger(), nil, nil, anyCred, nil), store
}

func decodeBootstrap(t *testing.T, rec *httptest.ResponseRecorder) adminui.Bootstrap {
	t.Helper()
	if rec.Code != 200 {
		t.Fatalf("bootstrap = %d %s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q", ct)
	}
	if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("headers = %v, want no-store and nosniff", rec.Header())
	}
	var b adminui.Bootstrap
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatal(err)
	}
	return b
}

func tabKeys(b adminui.Bootstrap) string {
	var ks []string
	for _, t := range b.Tabs {
		ks = append(ks, t.Key)
	}
	return strings.Join(ks, ",")
}

func TestAdminBootstrap(t *testing.T) {
	h, _ := adminHandler(t)
	p := appui.Principal{Admin: true, User: &jam.Participant{Name: "alice", UserID: "usr_1"}}
	b := decodeBootstrap(t, apiGet(t, h, "/app/admin/api/bootstrap", p))
	if got := tabKeys(b); got != "inbox,dashboard,config,agents,health" {
		t.Errorf("tabs = %s", got)
	}
	if !b.Member || b.Roles != "member · admin" || b.Name != "alice" || b.Initial != "A" {
		t.Errorf("bootstrap = %+v", b)
	}
	want := map[string]string{"inbox": "/app/user/", "dashboard": "/app/admin/", "config": "/app/admin/config/tracking",
		"agents": "/app/admin/agents", "health": "/app/admin/health"}
	for _, tab := range b.Tabs {
		if tab.Href != want[tab.Key] {
			t.Errorf("tab %s href = %q, want %q", tab.Key, tab.Href, want[tab.Key])
		}
	}
}

func TestAdminBootstrapLocal(t *testing.T) {
	h, _ := adminHandler(t)
	p := appui.Principal{Admin: true, Local: true}
	b := decodeBootstrap(t, apiGet(t, h, "/app/admin/api/bootstrap", p))
	if got := tabKeys(b); got != "dashboard,config,agents,health" {
		t.Errorf("tabs = %s, want no Inbox for a non-member", got)
	}
	if b.Member || b.Roles != "admin" || b.Name != p.DisplayName() || b.SignOut {
		t.Errorf("bootstrap = %+v", b)
	}
}

func TestAdminAPINotFound(t *testing.T) {
	h, _ := adminHandler(t)
	rec := apiGet(t, h, "/app/admin/api/nope", appui.Principal{Admin: true, Local: true})
	var e adminui.APIError
	if rec.Code != 404 || json.Unmarshal(rec.Body.Bytes(), &e) != nil || e.Code != "not_found" {
		t.Errorf("GET api/nope = %d %s, want a JSON 404 not_found", rec.Code, rec.Body)
	}
}

// A cross-origin write never reaches an API handler: here, the catch-all's
// JSON 404 would show it had.
func TestAdminAPICrossOrigin(t *testing.T) {
	h, _ := adminHandler(t)
	r := httptest.NewRequest(http.MethodPost, "/app/admin/api/nope", strings.NewReader("{}"))
	r.Header.Set("Origin", "https://evil.example")
	r.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, appui.WithPrincipal(r, appui.Principal{Admin: true, Local: true}))
	var e adminui.APIError
	if rec.Code != 403 || json.Unmarshal(rec.Body.Bytes(), &e) != nil || e.Code != "forbidden" {
		t.Errorf("cross-origin POST = %d %s, want a JSON 403 forbidden before any handler", rec.Code, rec.Body)
	}
}

// WithTrustedOrigins lets a write from an exact trusted origin (a dev proxy
// on another port) through the API's CSRF check; any other origin — another
// port, another scheme — is still refused before a handler runs.
func TestAdminAPITrustedOrigins(t *testing.T) {
	write := func(h http.Handler, header, value, project string) int {
		r := httptest.NewRequest(http.MethodPost, "/app/admin/api/projects", strings.NewReader(`{"name":"`+project+`"}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set(header, value)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, appui.WithPrincipal(r, appui.Principal{Admin: true, Local: true}))
		return rec.Code
	}
	plain, _ := adminHandler(t)
	if got := write(plain, "Origin", "http://localhost:8090", "p0"); got != http.StatusForbidden {
		t.Errorf("proxied write without trusted origins = %d, want 403", got)
	}
	store := newStore(t)
	h := adminui.Handler(store, testLogger(), nil, nil, anyCred, nil, adminui.WithTrustedOrigins("http://localhost:8090"))
	for i, tc := range []struct {
		header, value string
		want          int
	}{
		{"Origin", "http://localhost:8090", http.StatusCreated},
		{"Referer", "http://localhost:8090/app/admin/config/projects", http.StatusCreated},
		{"Origin", "http://localhost:8091", http.StatusForbidden},  // another port is another origin
		{"Origin", "https://localhost:8090", http.StatusForbidden}, // so is another scheme
		{"Origin", "http://evil.example", http.StatusForbidden},
	} {
		project := fmt.Sprintf("p%d", i+1)
		if got := write(h, tc.header, tc.value, project); got != tc.want {
			t.Errorf("%s: %s → %d, want %d", tc.header, tc.value, got, tc.want)
		}
		if _, ok := store.GetProject(project); ok != (tc.want == http.StatusCreated) {
			t.Errorf("%s: %s: project stored = %v, want %v", tc.header, tc.value, ok, tc.want == http.StatusCreated)
		}
	}
}

func TestAdminAppServed(t *testing.T) {
	h, _ := adminHandler(t)
	rec := apiGet(t, h, "/app/admin/config/projects/x", appui.Principal{Admin: true, Local: true})
	if !uiassets.AppBuilt() {
		if rec.Code != 503 {
			t.Errorf("without a bundle = %d, want 503", rec.Code)
		}
		return
	}
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `id="jam-bootstrap"`) {
		t.Fatalf("app page = %d", rec.Code)
	}
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "frame-src 'none'") {
		t.Errorf("CSP = %q", csp)
	}
}

func TestTreeAPI(t *testing.T) {
	h, _ := adminHandler(t)
	rec := apiGet(t, h, "/app/admin/api/tree", appui.Principal{Admin: true, Local: true})
	var tr adminui.TreeResponse
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &tr) != nil || len(tr.Nodes) == 0 || tr.Nodes[0].Path != "config/tracking" {
		t.Errorf("GET api/tree = %d %s", rec.Code, rec.Body)
	}
}
