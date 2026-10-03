package adminui_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminui"
)

func roleStore(t *testing.T) jam.Store {
	t.Helper()
	store := newStore(t)
	mustCreateProject(t, store, "acme")
	if err := store.PutRole("acme", jam.Role{Name: "review", Scope: jam.Scope{TTL: time.Hour}}); err != nil {
		t.Fatal(err)
	}
	return store
}

func TestRoleContextPanelShowsAndEdits(t *testing.T) {
	store := roleStore(t)
	h := adminui.Handler(store, testLogger(), nil, nil, credAny, nil)
	body := get(t, h, "/ui/roles/acme/review").Body.String()
	for _, want := range []string{"Session context", `hx-post="/ui/roles/acme/review/context"`, "0 / 1200 bytes"} {
		if !strings.Contains(body, want) {
			t.Errorf("role page missing %q", want)
		}
	}
	yml := "core: Review every PR within a day.\nleaves:\n  - name: style.md\n    read-when: you are commenting on style\n    body: Prefer small diffs.\n"
	rec := post(t, h, "/ui/roles/acme/review/context", url.Values{"yaml": {yml}})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `id="role"`) {
		t.Fatalf("save = %d: %s", rec.Code, rec.Body.String())
	}
	r, _ := store.GetRole("acme", "review")
	if r.Context.Core != "Review every PR within a day." || len(r.Context.Leaves) != 1 {
		t.Fatalf("stored = %+v", r.Context)
	}
	page := get(t, h, "/ui/roles/acme/review").Body.String()
	for _, want := range []string{"Review every PR within a day.", "style.md", "you are commenting on style", "29 / 1200 bytes"} {
		if !strings.Contains(page, want) {
			t.Errorf("panel missing %q", want)
		}
	}
	// Saving the YAML the panel shows is a no-op.
	shown, _ := jam.MarshalContextYAML(jam.ContextBody{Core: r.Context.Core, Leaves: r.Context.Leaves})
	if rec := post(t, h, "/ui/roles/acme/review/context", url.Values{"yaml": {string(shown)}}); rec.Code != http.StatusOK {
		t.Fatalf("re-save = %d", rec.Code)
	}
	if again, _ := store.GetRole("acme", "review"); again.Context.Core != r.Context.Core || again.Context.Leaves[0] != r.Context.Leaves[0] {
		t.Fatalf("round trip changed the layer: %+v", again.Context)
	}
	for name, form := range map[string]url.Values{
		"file leaf":   {"yaml": {"core: C\nleaves:\n  - {name: a.md, read-when: w, file: a.md}\n"}},
		"over budget": {"yaml": {"core: " + strings.Repeat("x", 1300) + "\n"}},
		"empty":       {"yaml": {""}},
	} {
		if rec := post(t, h, "/ui/roles/acme/review/context", form); rec.Code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", name, rec.Code)
		}
	}
	if rec := del(t, h, "/ui/roles/acme/review/context"); rec.Code != http.StatusOK {
		t.Fatalf("clear = %d", rec.Code)
	}
	if r, _ := store.GetRole("acme", "review"); !r.Context.Empty() {
		t.Fatalf("cleared = %+v", r.Context)
	}
}

func TestContextEditsRefuseCrossOrigin(t *testing.T) {
	h := adminui.Handler(roleStore(t), testLogger(), nil, nil, credAny, nil)
	for _, path := range []string{"/ui/roles/acme/review/context", "/ui/projects/acme/context", "/ui/jam/context"} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader("yaml=core%3A+C"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", "http://evil.example")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s cross-origin = %d, want 403", path, rec.Code)
		}
	}
}
