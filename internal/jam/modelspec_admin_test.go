package jam

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"testing"
)

func newModelSpecAdmin(t *testing.T) (http.Handler, Store) {
	t.Helper()
	store := NewMemStore()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := NewAdminHandler(store, nil, nil, LoopbackAuthenticator{}, func(n string) bool { return n == "anthropic" }, nil, log, nil,
		WithModelSpecs(store))
	return h, store
}

// Model-specs change only in git: the admin API lists and shows them.
func TestAdminModelSpecListShow(t *testing.T) {
	h, store := newModelSpecAdmin(t)
	m := validSpec()
	if err := store.PutModelSpec(m); err != nil {
		t.Fatal(err)
	}

	rec := doReq(t, h, "GET", "/admin/model-specs", nil)
	var list []ModelSpec
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &list) != nil || len(list) != 1 || list[0].Name != m.Name {
		t.Fatalf("list = %d %s", rec.Code, rec.Body)
	}

	rec = doReq(t, h, "GET", "/admin/model-specs/"+m.Name, nil)
	var got ModelSpec
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil || got.Claude == nil || got.Claude.Provider != "vertex" {
		t.Fatalf("show = %d %s", rec.Code, rec.Body)
	}
	if rec := doReq(t, h, "GET", "/admin/model-specs/ghost", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("show missing = %d, want 404", rec.Code)
	}
}
