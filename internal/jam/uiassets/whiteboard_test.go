package uiassets_test

import (
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam/uiassets"
)

// The whiteboard bundle is served under <prefix>whiteboard/ when built (by
// `just whiteboard`); WhiteboardBuilt says whether it was.
func TestWhiteboardBundle(t *testing.T) {
	h := uiassets.Handler("/app/static/")
	_, err := os.Stat("whiteboard/whiteboard.js")
	built := err == nil
	if uiassets.WhiteboardBuilt() != built {
		t.Fatalf("WhiteboardBuilt = %v, bundle on disk %v", uiassets.WhiteboardBuilt(), built)
	}
	if !built {
		t.Skip("bundle not built (run just whiteboard)")
	}
	if rec := get(t, h, "/app/static/whiteboard/whiteboard.js"); rec.Code != http.StatusOK {
		t.Fatalf("whiteboard.js = %d", rec.Code)
	}
	if rec := get(t, h, "/app/static/whiteboard/.keep"); rec.Code == http.StatusOK {
		t.Fatal("the placeholder must not be served")
	}
	if rec := get(t, h, "/app/static/whiteboard/chunks/"); rec.Code == http.StatusOK {
		t.Fatal("no directory listings")
	}
	if rec := get(t, h, "/app/static/whiteboard/whiteboard.js"); !strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("Cache-Control = %q", rec.Header().Get("Cache-Control"))
	}
	if uiassets.WhiteboardScript("/app/static/") == "" {
		t.Fatal("no versioned script URL")
	}
}
