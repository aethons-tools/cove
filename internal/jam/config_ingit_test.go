package jam

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// With config in git, the admin API refuses every live config write, and
// keeps the live-state writes and reads.
func TestConfigInGitRefusesLiveWrites(t *testing.T) {
	st := NewMemStore()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := NewAdminHandler(st, nil, nil, LoopbackAuthenticator{}, allCreds.CredExists, nil, log, nil,
		WithConfig(st, nil, followEnv, log), WithConfigInGit(followEnv.Followed))
	do := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.RemoteAddr = "127.0.0.1:1234"
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	for _, c := range []struct{ method, path string }{
		{"POST", "/admin/destinations"}, {"DELETE", "/admin/destinations/gh"}, {"POST", "/admin/model-specs"},
		{"POST", "/admin/users"}, {"PUT", "/admin/users/u/logins"}, {"POST", "/admin/projects"}, {"PUT", "/admin/projects/p/context"},
		{"POST", "/admin/roles"}, {"PUT", "/admin/roles/p/r/egress"}, {"POST", "/admin/roles/p/r/standing"}, {"PUT", "/admin/jam/context"},
		{"POST", "/admin/kits"}, {"POST", "/admin/kits/web/pin"}, {"POST", "/admin/kits/web/draft/publish"}, {"POST", "/admin/connections"},
		{"POST", "/admin/config"}, {"DELETE", "/admin/config/managed/user/bob"},
	} {
		w := do(c.method, c.path, `{}`)
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "config is in git") {
			t.Errorf("%s %s = %d %s", c.method, c.path, w.Code, w.Body)
		}
	}
	for _, c := range []struct{ method, path string }{
		{"GET", "/admin/destinations"}, {"POST", "/admin/roles/p/r/standing/s/reset"}, {"PUT", "/admin/users/u/oidc"},
		{"POST", "/admin/kits/web/sync"}, {"POST", "/admin/kits/import"}, {"POST", "/admin/kits/web/draft"},
		{"POST", "/admin/config/diff"}, {"POST", "/admin/config/sync"}, {"GET", "/admin/config/follow"},
	} {
		if w := do(c.method, c.path, `{}`); strings.Contains(w.Body.String(), "config is in git") {
			t.Errorf("%s %s refused: %s", c.method, c.path, w.Body)
		}
	}
	// apply: only of the followed repo.
	if w := do("POST", "/admin/config/apply", `{"source":"https://github.com/acme/other@main"}`); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "config is in git") {
		t.Errorf("apply of another source = %d %s", w.Code, w.Body)
	}
	if w := do("POST", "/admin/config/apply", `{"source":"https://github.com/acme/other@main"}x`); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "config is in git") {
		t.Errorf("apply with trailing data = %d %s", w.Code, w.Body)
	}
	if w := do("DELETE", "/admin/kits/import", ``); w.Code != http.StatusConflict {
		t.Errorf("DELETE kits/import = %d", w.Code)
	}
	if w := do("PUT", "/admin/users/u/future", `{}`); w.Code != http.StatusConflict {
		t.Errorf("a users sub-route = %d", w.Code)
	}
	r := httptest.NewRequest("POST", "/admin/config/apply", strings.NewReader("x"))
	r.RemoteAddr = "127.0.0.1:1234"
	r.Header.Set("Content-Type", "application/gzip")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "config is in git") {
		t.Errorf("apply --dir = %d %s", w.Code, w.Body)
	}
}
