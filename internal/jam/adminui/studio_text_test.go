package adminui_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam/adminui"
)

// The admin UI calls a cove a Studio (routes and ids stay "cove"), and the
// product is Jam.
func TestUISaysStudioAndJam(t *testing.T) {
	store := newStore(t)
	h := adminui.Handler(store, testLogger(), nil, nil, anyCred, nil)

	page := get(t, h, "/ui/agents")
	if page.Code != http.StatusOK {
		t.Fatalf("GET /ui/coves = %d", page.Code)
	}
	body := page.Body.String()
	for _, want := range []string{"<title>Jam — Agents</title>", "<h1>Agents</h1>", `<a href="/ui/agents" aria-current="page">Agents</a>`, "No agents."} {
		if !strings.Contains(body, want) {
			t.Errorf("studios page missing %q; got:\n%s", want, body)
		}
	}
	for _, gone := range []string{">Coves<", "No coves."} {
		if strings.Contains(body, gone) {
			t.Errorf("studios page still shows %q", gone)
		}
	}

	dash := get(t, h, "/ui/").Body.String()
	if !strings.Contains(dash, "Live studios") || strings.Contains(dash, "Live coves") {
		t.Errorf("dashboard should say Live studios; got:\n%s", dash)
	}
}
