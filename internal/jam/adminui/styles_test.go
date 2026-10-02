package adminui_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam/adminui"
)

// /ui takes its tokens from the shared jam.css (one source with /me) and
// carries no token block of its own.
func TestAdminUsesSharedStylesheet(t *testing.T) {
	h := adminui.Handler(newStore(t), testLogger(), nil, nil, anyCred, nil)
	body := get(t, h, "/ui/").Body.String()
	if !strings.Contains(body, `<link rel="stylesheet" href="/ui/static/jam.css?v=`) {
		t.Error("/ui should link the shared jam.css")
	}
	if strings.Contains(body, ":root{") {
		t.Error("/ui should not define its own token block")
	}
	if rec := get(t, h, "/ui/static/jam.css"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "--bad:") {
		t.Fatalf("/ui/static/jam.css = %d", rec.Code)
	}
}
