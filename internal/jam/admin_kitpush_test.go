package jam

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/kittree/kittreetest"
)

// GET /admin/kits/{name} lists a directory kit's files with their modes.
func TestKitShowRouteListsFiles(t *testing.T) {
	st := NewMemStore()
	h := NewAdminHandler(st, nil, nil, LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	if _, _, err := PushKitFiles(st, "app", kittreetest.Files("kit.yml", "kind: studio\n", "Dockerfile", dockerfile)); err != nil {
		t.Fatal(err)
	}
	var got KitConfigResult
	getJSON(t, h, "/admin/kits/app", &got)
	if got.Tree == "" || len(got.Files) != 2 || got.Files[0].Path != "Dockerfile" || got.Files[0].Mode != "0644" {
		t.Fatalf("show = %+v", got)
	}
}

// GET /admin/kits/{name}: a broken version answers with its own fixed error;
// a directory kit's own view carries its note files' contents.
func TestKitShowRouteBrokenAndOwn(t *testing.T) {
	st := NewMemStore()
	h := NewAdminHandler(st, nil, nil, LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	show := func(path string) KitConfigResult {
		t.Helper()
		r := httptest.NewRequest("GET", path, nil)
		r.RemoteAddr = "127.0.0.1:5000"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		var got KitConfigResult
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &got) != nil {
			t.Fatalf("%s = %d %s", path, w.Code, w.Body)
		}
		return got
	}
	if err := st.PutKitDocs([]Kit{{Name: "old", Current: 1, Broken: map[int]string{1: "its base.context is not a readable tar.gz"}}}, KitObjects{}); err != nil {
		t.Fatal(err)
	}
	if got := show("/admin/kits/old"); got.ChainError != "kit old@v1 could not be migrated to a tree: its base.context is not a readable tar.gz" || got.Own != "" {
		t.Fatalf("broken: %+v", got)
	}
	if _, _, err := PushKitFiles(st, "doc", kittreetest.Files("kit.yml", "kind: studio\nnotes:\n  - name: r.md\n    read-when: releasing\n    file: docs/r.md\n", "docs/r.md", "RELEASE STEPS")); err != nil {
		t.Fatal(err)
	}
	if got := show("/admin/kits/doc"); !strings.Contains(got.Own, "RELEASE STEPS") || strings.Contains(got.Own, "file:") || !strings.Contains(got.Config, "file: docs/r.md") {
		t.Fatalf("own: %+v", got)
	}
}
