package jam

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/kittree/kittreetest"
)

func TestKitVersionDiff(t *testing.T) {
	st := NewMemStore()
	for _, fs := range [][]string{{"kit.yml", "kind: studio\nprompt: a\n", "run.sh", "echo\n"}, {"kit.yml", "kind: studio\nprompt: b\n", "run.sh*", "echo\n"}} {
		if _, _, err := PushKitFiles(st, "app", kittreetest.Files(fs...)); err != nil {
			t.Fatal(err)
		}
	}
	d, err := KitVersionDiff(st, "app", 0, 0)
	if err != nil || d.From != 1 || d.To != 2 {
		t.Fatalf("diff = %+v %v", d, err)
	}
	if !strings.Contains(d.Text, "  -prompt: a\n  +prompt: b\n") || !strings.Contains(d.Text, "  mode      run.sh  0644 → 0755\n") {
		t.Fatalf("text:\n%s", d.Text)
	}
	if _, err := KitVersionDiff(st, "app", 1, 9); err == nil || WriteStatus(err, 500) != 404 {
		t.Fatalf("unknown version: %v", err)
	}
	if _, err := KitVersionDiff(st, "app", 1, 1); err == nil || WriteStatus(err, 500) != 400 {
		t.Fatalf("same version: %v", err)
	}
	if _, err := KitVersionDiff(st, "nope", 0, 0); err == nil || WriteStatus(err, 500) != 404 {
		t.Fatalf("unknown kit: %v", err)
	}
}

// A broken version (the migration could not make a tree of it) is refused
// with its own fixed error; nothing of it is rendered.
func TestKitVersionDiffBrokenVersion(t *testing.T) {
	st := NewMemStore()
	if _, _, err := PushKitFiles(st, "app", kittreetest.Files("kit.yml", "kind: studio\n")); err != nil {
		t.Fatal(err)
	}
	k, _ := st.GetKit("app")
	k.Current = 2
	k.Broken = map[int]string{2: "its base.context is not a readable tar.gz"}
	if err := st.PutKitDocs([]Kit{k}, KitObjects{}); err != nil {
		t.Fatal(err)
	}
	_, err := KitVersionDiff(st, "app", 0, 0)
	if err == nil || WriteStatus(err, 500) != 400 || err.Error() != "kit app@v2 could not be migrated to a tree: its base.context is not a readable tar.gz" {
		t.Fatalf("broken: %v", err)
	}
}

// Binary contents never reach the diff: a binary file is a one-line marker.
func TestKitVersionDiffBinaryIsAMarker(t *testing.T) {
	st := NewMemStore()
	for _, bin := range []string{"\x00\x01\x02secret-a", "\x00\x01\x02secret-b"} {
		if _, _, err := PushKitFiles(st, "app", kittreetest.Files("kit.yml", "kind: studio\n", "blob.bin", bin)); err != nil {
			t.Fatal(err)
		}
	}
	d, err := KitVersionDiff(st, "app", 0, 0)
	if err != nil || !strings.Contains(d.Text, "blob.bin") || !strings.Contains(d.Text, "(binary)") || strings.Contains(d.Text, "secret") {
		t.Fatalf("diff = %+v %v", d, err)
	}
}

func TestKitDiffRoute(t *testing.T) {
	st := NewMemStore()
	for _, p := range []string{"prompt: a\n", "prompt: b\n", "prompt: c\n"} {
		if _, err := st.PushKit("app", "kind: studio\n"+p); err != nil {
			t.Fatal(err)
		}
	}
	h := NewAdminHandler(st, nil, nil, LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	for _, c := range []struct {
		query string
		code  int
		want  string
	}{
		{"?from=1&to=3", 200, `"from":1,"to":3`},
		{"", 200, `"from":2,"to":3`},
		{"?from=x", 400, "from must be a version number"},
		{"?to=0", 400, "to must be a version number"},
		{"?from=2&to=2", 400, "pick two different versions"},
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, loopback(httptest.NewRequest(http.MethodGet, "/admin/kits/app/diff"+c.query, nil)))
		if w.Code != c.code || !strings.Contains(w.Body.String(), c.want) {
			t.Errorf("%q: %d %s", c.query, w.Code, w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, loopback(httptest.NewRequest(http.MethodPost, "/admin/kits/app/diff", nil)))
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST: %d", w.Code)
	}
	w = httptest.NewRecorder() // admin-only: an unauthenticated caller gets nothing
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/admin/kits/app/diff", nil))
	if w.Code != http.StatusForbidden || strings.Contains(w.Body.String(), "prompt") {
		t.Errorf("unauthenticated: %d %s", w.Code, w.Body.String())
	}
}

func loopback(r *http.Request) *http.Request {
	r.RemoteAddr = "127.0.0.1:1234"
	return r
}
