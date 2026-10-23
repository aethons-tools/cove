package userui

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
)

// /app/user takes its tokens from the shared jam.css (one source with /app/admin),
// served by appui at /app/static, and carries no token block of its own.
func TestUserUsesSharedStylesheet(t *testing.T) {
	e, p := fixture()
	h := Handler(e.Deps, nil)
	req := jam.WithParticipant(httptest.NewRequest("GET", "/app/user/?c="+url.QueryEscape(engID), nil), p)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	body := rec.Body.String()
	if !strings.Contains(body, `<link rel="stylesheet" href="/app/static/jam.css?v=`) {
		t.Error("/app/user should link the shared jam.css")
	}
	if strings.Contains(body, ":root{") {
		t.Error("/app/user should not define its own token block")
	}
}

// On a narrow screen the channel list hides but the rail header (view toggle,
// New message) stays reachable.
func TestNarrowViewportKeepsRailHead(t *testing.T) {
	e, p := fixture()
	rec := httptest.NewRecorder()
	Handler(e.Deps, nil).ServeHTTP(rec, jam.WithParticipant(httptest.NewRequest("GET", "/app/user/", nil), p))
	body := rec.Body.String()
	i := strings.Index(body, "@media (max-width:720px)")
	if i < 0 {
		t.Fatal("no narrow-viewport rule")
	}
	rule := body[i : i+strings.Index(body[i:], "}}")]
	if !strings.Contains(rule, ".rail-list{display:none}") {
		t.Errorf("narrow rule should hide .rail-list: %s", rule)
	}
	if strings.Contains(rule, ".rail{display:none}") || strings.Contains(rule, ".rail-head{display:none}") {
		t.Errorf("narrow rule must not hide the rail header: %s", rule)
	}
}

// The Inbox's <head> carries the installable-app head once: the manifest link
// and the guarded service-worker registration.
func TestUserHeadIsInstallable(t *testing.T) {
	e, p := fixture()
	rec := httptest.NewRecorder()
	Handler(e.Deps, nil).ServeHTTP(rec, jam.WithParticipant(httptest.NewRequest("GET", "/app/user/", nil), p))
	body := rec.Body.String()
	head, _, ok := strings.Cut(body, "</head>")
	if !ok {
		t.Fatal("no </head>")
	}
	for _, want := range []string{`rel="manifest"`, `navigator.serviceWorker.register('/app/sw.js'`} {
		if n := strings.Count(head, want); n != 1 {
			t.Errorf("<head> has %q %d times, want 1", want, n)
		}
	}
	if strings.Count(body, `rel="manifest"`) != 1 {
		t.Error("manifest link outside <head>")
	}
}
