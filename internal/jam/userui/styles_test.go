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
