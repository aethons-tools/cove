package jam

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/kittree"
	"github.com/aethons-tools/cove/internal/kittree/kittreetest"
)

func TestPushTarRoute(t *testing.T) {
	st := NewMemStore()
	h := NewAdminHandler(st, nil, nil, LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	postAs := func(addr, ctype, name string, body []byte) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/admin/kits/"+name+"/versions", bytes.NewReader(body))
		r.RemoteAddr = addr
		if ctype != "" {
			r.Header.Set("Content-Type", ctype)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	postFrom := func(addr, name string, body []byte) *httptest.ResponseRecorder {
		return postAs(addr, "application/x-tar", name, body)
	}
	post := func(name string, body []byte) *httptest.ResponseRecorder {
		return postFrom("127.0.0.1:5000", name, body)
	}
	var body bytes.Buffer
	if err := kittree.WriteTar(&body, kittreetest.Files("kit.yml", "kind: studio\n", "Dockerfile", dockerfile)); err != nil {
		t.Fatal(err)
	}
	if w := postFrom("203.0.113.9:1000", "app", body.Bytes()); w.Code != http.StatusForbidden {
		t.Fatalf("off-loopback push = %d, want 403 (admin only)", w.Code)
	}
	// Only a tar body: a browser's no-cors text/plain POST to the loopback
	// admin port (CSRF) is refused before the body is read.
	for _, ctype := range []string{"text/plain", "", "application/x-www-form-urlencoded", "multipart/form-data; boundary=x"} {
		if w := postAs("127.0.0.1:5000", ctype, "app", body.Bytes()); w.Code != http.StatusUnsupportedMediaType {
			t.Fatalf("Content-Type %q = %d, want 415", ctype, w.Code)
		}
	}
	if _, ok := st.GetKit("app"); ok {
		t.Fatal("a refused Content-Type stored a kit")
	}
	w := postAs("127.0.0.1:5000", "application/x-tar; charset=binary", "app", body.Bytes())
	var res KitResult
	if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &res) != nil || res.Version != 1 {
		t.Fatalf("push = %d %s", w.Code, w.Body)
	}
	g := httptest.NewRecorder()
	gr := httptest.NewRequest("GET", "/admin/kits/app", nil)
	gr.RemoteAddr = "127.0.0.1:5000"
	h.ServeHTTP(g, gr)
	var got KitConfigResult
	if json.Unmarshal(g.Body.Bytes(), &got) != nil || got.Tree == "" || len(got.Files) != 2 || got.Files[0].Path != "Dockerfile" || got.Files[0].Mode != "0644" {
		t.Fatalf("show = %s", g.Body)
	}
	if w := post("app", []byte("garbage, not a tar, garbage, not a tar")); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "not a readable tar") {
		t.Fatalf("garbage = %d %s", w.Code, w.Body)
	}
	// A hand-made tar: its .dockerignore is applied again here, and an unsafe
	// name is refused without being echoed.
	var ignored bytes.Buffer
	if err := kittree.WriteTar(&ignored, kittreetest.Files("kit.yml", "kind: studio\n", ".dockerignore", "*.log\n", "debug.log", "x")); err != nil {
		t.Fatal(err)
	}
	if w := post("ign", ignored.Bytes()); w.Code != http.StatusCreated {
		t.Fatalf("ignored push = %d %s", w.Code, w.Body)
	}
	if v, _ := LoadKitVersion(st, "ign", 0); len(v.Tree.Entries) != 2 {
		t.Fatalf("server must apply .dockerignore: %+v", v.Tree)
	}
	var unsafe bytes.Buffer
	if err := kittree.WriteTar(&unsafe, kittreetest.Files("kit.yml", "kind: studio\n", "evil\x1b[2Jname", "x")); err != nil {
		t.Fatal(err)
	}
	if w := post("app", unsafe.Bytes()); w.Code != http.StatusBadRequest || strings.Contains(w.Body.String(), "evil") || !strings.Contains(w.Body.String(), "unsafe path") {
		t.Fatalf("unsafe tar name = %d %q", w.Code, w.Body)
	}
	if w := post("bad%20name", body.Bytes()); w.Code != http.StatusBadRequest {
		t.Fatalf("unsafe name = %d", w.Code)
	}
	// Shrink the cap (MaxUpload = MaxTotal + MaxEntries×2 KiB + 1 MiB) under one
	// big file, and lift the per-file limit, so the body cap trips first.
	e, to, f := kittree.MaxEntries, kittree.MaxTotal, kittree.MaxFile
	kittree.MaxEntries, kittree.MaxTotal, kittree.MaxFile = 1, 1024, 8<<20
	t.Cleanup(func() { kittree.MaxEntries, kittree.MaxTotal, kittree.MaxFile = e, to, f })
	var big bytes.Buffer
	if err := kittree.WriteTar(&big, []kittree.File{{Path: "kit.yml", Data: make([]byte, 2<<20)}}); err != nil {
		t.Fatal(err)
	}
	if w := post("app", big.Bytes()); w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize = %d %s", w.Code, w.Body)
	}
}
