package jam

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam/sessionctx"
)

// The context checks live in the store-level setters the console calls; the
// admin API keeps only the GETs.
func TestContextEndpoints(t *testing.T) {
	h, store := newTestAdmin(t)
	if err := store.PutRole("default", Role{Name: "dev", Scope: Scope{TTL: time.Hour}}); err != nil {
		t.Fatal(err)
	}
	good := ContextBody{Core: "C", Leaves: []sessionctx.Leaf{{Name: "a.md", ReadWhen: "w", Body: "b"}}}
	scopes := []struct {
		path  string
		set   func(ContextBody) error
		clear func() error
	}{
		{"/admin/roles/default/dev/context",
			func(b ContextBody) error { return SetRoleContextChecked(store, "default", "dev", b) },
			func() error { return ClearRoleContext(store, "default", "dev") }},
		{"/admin/projects/default/context",
			func(b ContextBody) error { return SetProjectContextChecked(store, "default", b) },
			func() error { return SetProjectContextChecked(store, "default", ContextBody{}) }},
		{"/admin/jam/context",
			func(b ContextBody) error { return SetJamContextChecked(store, b) },
			func() error { return SetJamContextChecked(store, ContextBody{}) }},
	}
	for _, sc := range scopes {
		if err := sc.set(good); err != nil {
			t.Fatalf("set %s: %v", sc.path, err)
		}
		var got ContextBody
		getJSON(t, h, sc.path, &got)
		if got.Core != "C" || len(got.Leaves) != 1 {
			t.Fatalf("GET %s = %+v", sc.path, got)
		}
		if err := sc.set(ContextBody{Core: strings.Repeat("x", 1300)}); WriteStatus(err, http.StatusInternalServerError) != http.StatusBadRequest {
			t.Errorf("over-budget set %s = %v, want a 400", sc.path, err)
		}
		if err := sc.set(ContextBody{Leaves: []sessionctx.Leaf{{Name: "../x.md", ReadWhen: "w"}}}); WriteStatus(err, http.StatusInternalServerError) != http.StatusBadRequest {
			t.Errorf("bad leaf set %s = %v, want a 400", sc.path, err)
		}
		if err := sc.clear(); err != nil {
			t.Fatalf("clear %s: %v", sc.path, err)
		}
		got = ContextBody{}
		getJSON(t, h, sc.path, &got)
		if got.Core != "" || len(got.Leaves) != 0 {
			t.Errorf("after clear %s: %+v", sc.path, got)
		}
	}
	if err := SetProjectContextChecked(store, "default", ContextBody{Resources: []sessionctx.Resource{{Name: "r", Kind: "wiki", Ref: "x"}}}); WriteStatus(err, http.StatusInternalServerError) != http.StatusBadRequest {
		t.Errorf("bad resource kind = %v, want a 400", err)
	}
	if err := SetJamContextChecked(store, ContextBody{Core: strings.Repeat("x", 900)}); err == nil || !strings.Contains(err.Error(), "900 bytes") {
		t.Errorf("over-budget Jam context err = %v, want it to name 900 bytes", err)
	}
	res := ContextBody{Resources: []sessionctx.Resource{{Name: "r", Kind: "url", Ref: "x"}}}
	for _, err := range []error{SetRoleContextChecked(store, "default", "dev", res), SetJamContextChecked(store, res)} {
		if WriteStatus(err, http.StatusInternalServerError) != http.StatusBadRequest {
			t.Errorf("resources on a role or the Jam = %v, want a 400", err)
		}
	}
	if err := SetProjectContextChecked(store, "ghost", ContextBody{Core: "C"}); WriteStatus(err, http.StatusInternalServerError) != http.StatusNotFound {
		t.Errorf("unknown project = %v, want a 404", err)
	}
	if err := SetRoleContextChecked(store, "default", "ghost", ContextBody{Core: "C"}); WriteStatus(err, http.StatusInternalServerError) != http.StatusNotFound {
		t.Errorf("unknown role = %v, want a 404", err)
	}
	var pv ContextBody
	if err := SetProjectContextChecked(store, "default", ContextBody{Core: "P", Resources: []sessionctx.Resource{{Name: "cove", Kind: "repo", Ref: "aethons-tools/cove"}}}); err != nil {
		t.Fatalf("project with resources = %v", err)
	}
	getJSON(t, h, "/admin/projects/default/context", &pv)
	if len(pv.Resources) != 1 || pv.Resources[0].Ref != "aethons-tools/cove" {
		t.Fatalf("resources = %+v", pv.Resources)
	}
}

func TestProjectContextReviewFixes(t *testing.T) {
	h, store := newTestAdmin(t)
	if rec := doReq(t, h, "GET", "/admin/projects/ghost/context", nil); rec.Code != http.StatusNotFound {
		t.Errorf("GET unknown project = %d, want 404", rec.Code)
	}
	if rec := doReq(t, h, "GET", "/admin/projects/default/context", nil); rec.Code != http.StatusOK {
		t.Errorf("GET default project = %d, want 200", rec.Code)
	}
	near := ContextBody{Core: strings.Repeat("x", sessionctx.BudgetProject-10), Resources: []sessionctx.Resource{{Name: "r", Kind: "url", Ref: "https://x"}}}
	if err := SetProjectContextChecked(store, "default", near); WriteStatus(err, http.StatusInternalServerError) != http.StatusBadRequest {
		t.Errorf("core + resources pointer over budget = %v, want a 400", err)
	}
	clash := ContextBody{Core: "C", Leaves: []sessionctx.Leaf{{Name: "resources.md", ReadWhen: "w", Body: "b"}}}
	if err := SetProjectContextChecked(store, "default", clash); WriteStatus(err, http.StatusInternalServerError) != http.StatusBadRequest {
		t.Errorf("authored resources.md = %v, want a 400", err)
	}
}
