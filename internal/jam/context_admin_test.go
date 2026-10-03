package jam

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam/sessionctx"
)

func TestContextEndpoints(t *testing.T) {
	h, store := newTestAdmin(t)
	if err := store.PutRole("default", Role{Name: "dev", Scope: Scope{TTL: time.Hour}}); err != nil {
		t.Fatal(err)
	}
	good := ContextBody{Core: "C", Leaves: []sessionctx.Leaf{{Name: "a.md", ReadWhen: "w", Body: "b"}}}
	for _, path := range []string{"/admin/roles/default/dev/context", "/admin/projects/default/context", "/admin/jam/context"} {
		if rec := doJSON(t, h, "PUT", path, good); rec.Code != http.StatusNoContent {
			t.Fatalf("PUT %s = %d %s", path, rec.Code, rec.Body)
		}
		var got ContextBody
		getJSON(t, h, path, &got)
		if got.Core != "C" || len(got.Leaves) != 1 {
			t.Fatalf("GET %s = %+v", path, got)
		}
		if rec := doJSON(t, h, "PUT", path, ContextBody{Core: strings.Repeat("x", 1300)}); rec.Code != http.StatusBadRequest {
			t.Errorf("over-budget PUT %s = %d, want 400", path, rec.Code)
		}
		if rec := doJSON(t, h, "PUT", path, ContextBody{Leaves: []sessionctx.Leaf{{Name: "../x.md", ReadWhen: "w"}}}); rec.Code != http.StatusBadRequest {
			t.Errorf("bad leaf PUT %s = %d, want 400", path, rec.Code)
		}
		if rec := doReq(t, h, "DELETE", path, nil); rec.Code != http.StatusNoContent {
			t.Fatalf("DELETE %s = %d", path, rec.Code)
		}
		got = ContextBody{}
		getJSON(t, h, path, &got)
		if got.Core != "" || len(got.Leaves) != 0 {
			t.Errorf("after DELETE %s: %+v", path, got)
		}
	}
	if rec := doJSON(t, h, "PUT", "/admin/projects/default/context", ContextBody{Resources: []sessionctx.Resource{{Name: "r", Kind: "wiki", Ref: "x"}}}); rec.Code != http.StatusBadRequest {
		t.Errorf("bad resource kind = %d, want 400", rec.Code)
	}
	for _, path := range []string{"/admin/roles/default/dev/context", "/admin/jam/context"} {
		if rec := doJSON(t, h, "PUT", path, ContextBody{Resources: []sessionctx.Resource{{Name: "r", Kind: "url", Ref: "x"}}}); rec.Code != http.StatusBadRequest {
			t.Errorf("resources on %s = %d, want 400", path, rec.Code)
		}
	}
	if rec := doJSON(t, h, "PUT", "/admin/projects/ghost/context", ContextBody{Core: "C"}); rec.Code != http.StatusNotFound {
		t.Errorf("unknown project = %d, want 404", rec.Code)
	}
	if rec := doJSON(t, h, "PUT", "/admin/roles/default/ghost/context", ContextBody{Core: "C"}); rec.Code != http.StatusNotFound {
		t.Errorf("unknown role = %d, want 404", rec.Code)
	}
	var pv ContextBody
	if rec := doJSON(t, h, "PUT", "/admin/projects/default/context", ContextBody{Core: "P", Resources: []sessionctx.Resource{{Name: "cove", Kind: "repo", Ref: "aethons-tools/cove"}}}); rec.Code != http.StatusNoContent {
		t.Fatalf("project with resources = %d %s", rec.Code, rec.Body)
	}
	getJSON(t, h, "/admin/projects/default/context", &pv)
	if len(pv.Resources) != 1 || pv.Resources[0].Ref != "aethons-tools/cove" {
		t.Fatalf("resources = %+v", pv.Resources)
	}
}
