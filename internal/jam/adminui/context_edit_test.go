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
	body := get(t, h, "/ui/projects/acme/roles/review").Body.String()
	for _, want := range []string{"Agent context", `hx-post="/ui/roles/acme/review/context"`, "0 / 1200 bytes"} {
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
	page := get(t, h, "/ui/projects/acme/roles/review").Body.String()
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

func TestProjectAndJamContextPanels(t *testing.T) {
	store := roleStore(t)
	h := adminui.Handler(store, testLogger(), nil, nil, credAny, nil)
	yml := "core: Ship 1.0.\nresources:\n  - {name: cove, kind: repo, ref: aethons-tools/cove, note: main repo}\n"
	if rec := post(t, h, "/ui/projects/acme/context", url.Values{"yaml": {yml}}); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `id="project"`) {
		t.Fatalf("project save = %d: %s", rec.Code, rec.Body.String())
	}
	if p, _ := store.GetProject("acme"); p.Context.Core != "Ship 1.0." || len(p.Resources) != 1 {
		t.Fatalf("stored = %+v", p)
	}
	page := get(t, h, "/ui/projects/acme").Body.String()
	for _, want := range []string{"Ship 1.0.", "aethons-tools/cove", "main repo", `hx-post="/ui/projects/acme/context"`} {
		if !strings.Contains(page, want) {
			t.Errorf("project panel missing %q", want)
		}
	}
	if rec := post(t, h, "/ui/projects/ghost/context", url.Values{"yaml": {"core: C\n"}}); rec.Code != http.StatusNotFound {
		t.Errorf("unknown project = %d, want 404", rec.Code)
	}

	dash := get(t, h, "/ui/").Body.String()
	for _, want := range []string{`id="jam-context"`, `hx-post="/ui/jam/context"`, "0 / 800 bytes"} {
		if !strings.Contains(dash, want) {
			t.Errorf("dashboard missing %q", want)
		}
	}
	rec := post(t, h, "/ui/jam/context", url.Values{"yaml": {"core: Never push to main.\n"}})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Never push to main.") || strings.Contains(rec.Body.String(), "<html") {
		t.Fatalf("jam save = %d: %s", rec.Code, rec.Body.String())
	}
	if store.GetJamContext().Core != "Never push to main." {
		t.Fatal("jam context not stored")
	}
	if rec := del(t, h, "/ui/jam/context"); rec.Code != http.StatusOK || !store.GetJamContext().Empty() {
		t.Fatalf("jam clear = %d", rec.Code)
	}
}

// The card carries its own styles, so it renders the same on the dashboard
// (which has none of the role/project page styles) and wraps long cores.
func TestContextCardStyledEverywhere(t *testing.T) {
	h := adminui.Handler(roleStore(t), testLogger(), nil, nil, credAny, nil)
	for _, path := range []string{"/ui/", "/ui/projects/acme/roles/review", "/ui/projects/acme"} {
		body := get(t, h, path).Body.String()
		for _, want := range []string{`class="card full ctx-card"`, ".ctx-card>header", ".ctx-card .ctx-core{white-space:pre-wrap"} {
			if !strings.Contains(body, want) {
				t.Errorf("%s missing %q", path, want)
			}
		}
	}
}

func TestContextClearAndDeletes(t *testing.T) {
	store := roleStore(t)
	h := adminui.Handler(store, testLogger(), nil, nil, credAny, nil)
	if err := jam.SetProjectContextChecked(store, "acme", jam.ContextBody{Core: "P"}); err != nil {
		t.Fatal(err)
	}
	page := get(t, h, "/ui/projects/acme").Body.String()
	if !strings.Contains(page, `hx-delete="/ui/projects/acme/context" hx-params="none"`) {
		t.Error("Clear must not send the form's YAML in the DELETE query")
	}
	if rec := del(t, h, "/ui/projects/acme/context"); rec.Code != http.StatusOK {
		t.Fatalf("project clear = %d", rec.Code)
	}
	if p, _ := store.GetProject("acme"); !p.Context.Empty() {
		t.Fatalf("project context not cleared: %+v", p.Context)
	}
	for _, path := range []string{"/ui/roles/acme/review/context", "/ui/projects/acme/context", "/ui/jam/context"} {
		req := httptest.NewRequest(http.MethodDelete, path, nil)
		req.Header.Set("Origin", "http://evil.example")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s cross-origin DELETE = %d, want 403", path, rec.Code)
		}
	}
}
