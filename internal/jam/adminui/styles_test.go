package adminui_test

import (
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam/adminui"
)

// /app/admin takes its tokens from the shared jam.css (one source with /app/user),
// served by appui at /app/static, and carries no token block of its own.
func TestAdminUsesSharedStylesheet(t *testing.T) {
	h := adminui.Handler(newStore(t), testLogger(), nil, nil, anyCred, nil)
	body := get(t, h, "/app/admin/").Body.String()
	if !strings.Contains(body, `<link rel="stylesheet" href="/app/static/jam.css?v=`) {
		t.Error("/app/admin should link the shared jam.css")
	}
	if strings.Contains(body, ":root{") {
		t.Error("/app/admin should not define its own token block")
	}
}

// The rail sticks under the chrome using the same derived height, not a
// hand-copied number.
func TestRailOffsetUsesChromeHeight(t *testing.T) {
	h := adminui.Handler(newStore(t), testLogger(), nil, nil, anyCred, nil)
	body := get(t, h, "/app/admin/").Body.String()
	if strings.Contains(body, "top:90px") || strings.Contains(body, "- 90px") {
		t.Error("rail offset still hard-codes 90px")
	}
	if !strings.Contains(body, "top:var(--chrome-h)") || !strings.Contains(body, "height:var(--subbar-h)") {
		t.Error("rail/subbar should use --chrome-h/--subbar-h")
	}
}
