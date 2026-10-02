package meui

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
)

// /me takes its tokens from the shared jam.css (one source with /ui), served
// under its own static prefix, and carries no token block of its own.
func TestMeUsesSharedStylesheet(t *testing.T) {
	store, log, p := fixture()
	h := Handler(store, log, nil)
	req := jam.WithParticipant(httptest.NewRequest("GET", "/me/?c="+url.QueryEscape("named:eng"), nil), p)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	body := rec.Body.String()
	if !strings.Contains(body, `<link rel="stylesheet" href="/me/static/jam.css?v=`) {
		t.Error("/me should link the shared jam.css")
	}
	if strings.Contains(body, ":root{") {
		t.Error("/me should not define its own token block")
	}
	css := httptest.NewRecorder()
	h.ServeHTTP(css, jam.WithParticipant(httptest.NewRequest("GET", "/me/static/jam.css", nil), p))
	if css.Code != 200 || !strings.Contains(css.Body.String(), "--human-bubble:") {
		t.Fatalf("/me/static/jam.css = %d", css.Code)
	}
}
