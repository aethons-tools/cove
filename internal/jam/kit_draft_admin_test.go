package jam

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/kittree"
)

// draftDo issues one loopback admin request with an optional Content-Type
// and If-Match.
func draftDo(t *testing.T, h http.Handler, method, path, ctype, ifMatch string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, bytes.NewReader(body))
	r.RemoteAddr = "127.0.0.1:5000"
	if ctype != "" {
		r.Header.Set("Content-Type", ctype)
	}
	if ifMatch != "" {
		r.Header.Set("If-Match", ifMatch)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func body(w *httptest.ResponseRecorder) string { return strings.TrimSpace(w.Body.String()) }

func TestDraftOpenStatusDiscardRoutes(t *testing.T) {
	var logs bytes.Buffer
	st := NewMemStore()
	h := NewAdminHandler(st, nil, nil, LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(&logs, nil)), nil)
	pushApp(t, st, "kit.yml", "kind: studio\n")
	// JSON only: a browser's no-cors text/plain POST (CSRF) opens nothing.
	for _, ctype := range []string{"text/plain", "", "application/x-www-form-urlencoded"} {
		if w := draftDo(t, h, "POST", "/admin/kits/app/draft", ctype, "", []byte(`{}`)); w.Code != http.StatusUnsupportedMediaType || body(w) != "kit draft: Content-Type must be application/json" {
			t.Fatalf("Content-Type %q = %d %s", ctype, w.Code, w.Body)
		}
	}
	if _, ok := st.GetKitDraft("app"); ok {
		t.Fatal("a refused open made a draft")
	}
	if w := draftDo(t, h, "POST", "/admin/kits/app/draft", "application/json", "", []byte(`{"from":`)); w.Code != http.StatusBadRequest {
		t.Fatalf("bad JSON = %d", w.Code)
	}
	w := draftDo(t, h, "POST", "/admin/kits/app/draft", "application/json", "", nil) // an empty body opens from current
	var s KitDraftStatus
	if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &s) != nil || s.Base != 1 || w.Header().Get("ETag") != s.ETag {
		t.Fatalf("open = %d %s (ETag %q)", w.Code, w.Body, w.Header().Get("ETag"))
	}
	if w := draftDo(t, h, "POST", "/admin/kits/app/draft", "application/json", "", []byte(`{"from":1}`)); w.Code != http.StatusConflict || body(w) != "kit app already has a draft" {
		t.Fatalf("second open = %d %s", w.Code, w.Body)
	}
	g := draftDo(t, h, "GET", "/admin/kits/app/draft", "", "", nil)
	if g.Code != http.StatusOK || g.Header().Get("ETag") != s.ETag {
		t.Fatalf("status = %d %s", g.Code, g.Body)
	}
	var kits []KitSummary
	getJSON(t, h, "/admin/kits", &kits)
	if len(kits) != 1 || !kits[0].Draft {
		t.Fatalf("kits = %+v (app has a draft)", kits)
	}
	if w := draftDo(t, h, "DELETE", "/admin/kits/app/draft", "", "", nil); w.Code != http.StatusPreconditionRequired {
		t.Fatalf("discard without If-Match = %d", w.Code)
	}
	if w := draftDo(t, h, "DELETE", "/admin/kits/app/draft", "", `"`+strings.Repeat("0", 64)+`"`, nil); w.Code != http.StatusPreconditionFailed || body(w) != "draft app changed since you loaded it (reload)" {
		t.Fatalf("stale discard = %d %s", w.Code, w.Body)
	}
	if w := draftDo(t, h, "DELETE", "/admin/kits/app/draft", "", s.ETag, nil); w.Code != http.StatusNoContent {
		t.Fatalf("discard = %d %s", w.Code, w.Body)
	}
	if w := draftDo(t, h, "GET", "/admin/kits/app/draft", "", "", nil); w.Code != http.StatusNotFound || body(w) != "kit app has no draft" {
		t.Fatalf("status after discard = %d %s", w.Code, w.Body)
	}
	if w := draftDo(t, h, "GET", "/admin/kits/nope/draft", "", "", nil); w.Code != http.StatusNotFound || body(w) != "no such kit" {
		t.Fatalf("unknown kit = %d %s", w.Code, w.Body)
	}
	if w := draftDo(t, h, "POST", "/admin/kits/app/draft", "application/json", "", nil); w.Code != http.StatusCreated {
		t.Fatalf("reopen = %d", w.Code)
	}
	out := logs.String()
	for _, want := range []string{"kit draft opened", "kit draft discarded", "kit=app", "tree=" + s.Tree[:12], "operator=local"} {
		if !strings.Contains(out, want) {
			t.Fatalf("audit log lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "kind: studio") {
		t.Fatal("the audit log carries file content")
	}
}

// TestDraftRouteRefusals: an unsafe name is refused without echoing it, and a
// failure that is not the operator's answers a fixed 500 (ruling F9), its
// text only in the log; the operator gate covers every route.
func TestDraftRouteRefusals(t *testing.T) {
	var logs bytes.Buffer
	st := NewMemStore()
	h := NewAdminHandler(st, nil, nil, LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(&logs, nil)), nil)
	for _, method := range []string{"GET", "POST", "DELETE"} {
		w := draftDo(t, h, method, "/admin/kits/a%1B%5B31mb/draft", "application/json", `"x"`, nil)
		if w.Code != http.StatusBadRequest || body(w) != "kit name is not tag-safe (allowed: [A-Za-z0-9_.-])" {
			t.Fatalf("%s unsafe name = %d %q", method, w.Code, w.Body)
		}
	}
	// The operator gate: a non-loopback caller reaches none of the routes.
	pushApp(t, st, "kit.yml", "kind: studio\n")
	for _, method := range []string{"GET", "POST", "DELETE"} {
		r := httptest.NewRequest(method, "/admin/kits/app/draft", strings.NewReader(`{}`))
		r.RemoteAddr = "10.0.0.9:1234"
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s from a remote caller = %d", method, w.Code)
		}
	}
	if _, ok := st.GetKitDraft("app"); ok {
		t.Fatal("an unauthenticated open made a draft")
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/admin/kits/app/draft", nil)
	draftFail(w, r, slog.New(slog.NewTextHandler(&logs, nil)), errors.New("pq: secret detail"))
	if w.Code != http.StatusInternalServerError || body(w) != "internal error" {
		t.Fatalf("store failure = %d %q", w.Code, w.Body)
	}
	if !strings.Contains(logs.String(), "pq: secret detail") {
		t.Fatalf("the store failure is not logged:\n%s", logs.String())
	}
}
func TestDraftFileRoutes(t *testing.T) {
	var logs bytes.Buffer
	st := NewMemStore()
	h := NewAdminHandler(st, nil, nil, LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(&logs, nil)), nil)
	pushApp(t, st, "kit.yml", "kind: studio\n")
	tag := draftDo(t, h, "POST", "/admin/kits/app/draft", "application/json", "", nil).Header().Get("ETag")
	put := func(path, ctype, etag string, b []byte) *httptest.ResponseRecorder {
		return draftDo(t, h, "PUT", "/admin/kits/app/draft/files/"+path, ctype, etag, b)
	}
	const octet = "application/octet-stream"
	for _, ctype := range []string{"text/plain", "", "application/x-www-form-urlencoded"} {
		// No If-Match either: the Content-Type is checked first.
		if w := put("run.sh", ctype, "", []byte("echo\n")); w.Code != http.StatusUnsupportedMediaType || body(w) != "kit draft: Content-Type must be application/octet-stream" {
			t.Fatalf("Content-Type %q = %d %s", ctype, w.Code, w.Body)
		}
	}
	if w := put("run.sh", octet, "", []byte("echo\n")); w.Code != http.StatusPreconditionRequired {
		t.Fatalf("no If-Match = %d", w.Code)
	}
	if w := put("run.sh", octet, `"`+strings.Repeat("0", 64)+`"`, []byte("echo\n")); w.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale = %d", w.Code)
	}
	if w := put("run.sh?mode=0700", octet, tag, []byte("echo\n")); w.Code != http.StatusBadRequest || body(w) != "draft app: mode must be 0644 or 0755" {
		t.Fatalf("bad mode = %d %s", w.Code, w.Body)
	}
	w := put("run.sh?mode=755", octet, tag, []byte("echo SECRET-CONTENT\n"))
	var s KitDraftSave
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &s) != nil || w.Header().Get("ETag") != s.ETag || len(s.Changed) != 1 || s.Changed[0] != (DraftChange{"run.sh", "added"}) {
		t.Fatalf("put = %d %s", w.Code, w.Body)
	}
	g := draftDo(t, h, "GET", "/admin/kits/app/draft/files/run.sh", "", "", nil)
	if g.Code != http.StatusOK || g.Body.String() != "echo SECRET-CONTENT\n" || g.Header().Get("X-Kit-File-Mode") != "0755" ||
		g.Header().Get("ETag") != s.ETag || g.Header().Get("Content-Type") != octet || g.Header().Get("X-Content-Type-Options") != "nosniff" ||
		g.Header().Get("Content-Disposition") != "attachment" || g.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("get = %d %q %v", g.Code, g.Body, g.Header())
	}
	// A percent-encoded unicode path round-trips.
	w = put("docs/%C3%BCn%C3%AF.md", octet, s.ETag, []byte("ok\n"))
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &s) != nil || s.Changed[0].Path != "docs/ünï.md" {
		t.Fatalf("unicode put = %d %s", w.Code, w.Body)
	}
	if g := draftDo(t, h, "GET", "/admin/kits/app/draft/files/docs/%C3%BCn%C3%AF.md", "", "", nil); g.Body.String() != "ok\n" {
		t.Fatalf("unicode get = %d %q", g.Code, g.Body)
	}
	// A control or format character is refused, the name not echoed (Review Focus 4).
	for _, bad := range []string{"a%0Ab", "a%1B%5B31mb", "%E2%80%AEtxt.exe", "a%FFb"} {
		w := put(bad, octet, s.ETag, []byte("x"))
		if w.Code != http.StatusBadRequest || body(w) != unsafeDraftPath {
			t.Fatalf("PUT %s = %d %q", bad, w.Code, w.Body)
		}
		if g := draftDo(t, h, "GET", "/admin/kits/app/draft/files/"+bad, "", "", nil); g.Code != http.StatusBadRequest || body(g) != unsafeDraftPath {
			t.Fatalf("GET %s = %d %q", bad, g.Code, g.Body)
		}
	}
	d := draftDo(t, h, "DELETE", "/admin/kits/app/draft/files/run.sh", "", s.ETag, nil)
	if d.Code != http.StatusOK || json.Unmarshal(d.Body.Bytes(), &s) != nil || s.Changed[0] != (DraftChange{"run.sh", "removed"}) {
		t.Fatalf("delete = %d %s", d.Code, d.Body)
	}
	if g := draftDo(t, h, "GET", "/admin/kits/app/draft/files/run.sh", "", "", nil); g.Code != http.StatusNotFound || body(g) != "draft app has no file run.sh" {
		t.Fatalf("get removed = %d %s", g.Code, g.Body)
	}
	// An identical write changes nothing and logs no save.
	saves := strings.Count(logs.String(), "kit draft saved")
	put("docs/%C3%BCn%C3%AF.md", octet, s.ETag, []byte("ok\n"))
	if n := strings.Count(logs.String(), "kit draft saved"); n != saves {
		t.Fatalf("a no-op write logged a save (%d → %d)", saves, n)
	}
	// Over MaxFile: 413 from the body cap; a stale tag is refused first (412).
	withDraftLimits(t, kittree.MaxEntries, kittree.MaxTotal, 8)
	if w := put("big.bin", octet, `"`+strings.Repeat("0", 64)+`"`, make([]byte, 9)); w.Code != http.StatusPreconditionFailed {
		t.Fatalf("oversize with a stale tag = %d %s", w.Code, w.Body)
	}
	if w := put("big.bin", octet, s.ETag, make([]byte, 9)); w.Code != http.StatusRequestEntityTooLarge || body(w) != "draft app: big.bin: more than 8 bytes (limit 8 bytes per file)" {
		t.Fatalf("oversize = %d %s", w.Code, w.Body)
	}
	out := logs.String()
	if !strings.Contains(out, "kit draft saved") || !strings.Contains(out, "tree="+s.Tree[:12]) || strings.Contains(out, "SECRET-CONTENT") {
		t.Fatalf("audit log:\n%s", out)
	}
}

// TestDraftFileRoutesRefuseDotSegments: ServeMux would 307 a literal ., ..
// or empty segment to another path, so a redirect-following client would
// write or remove a different file; the guard refuses them first, with or
// without the browser app mounted.
func TestDraftFileRoutesRefuseDotSegments(t *testing.T) {
	st := NewMemStore()
	pushApp(t, st, "kit.yml", "kind: studio\n", "b", "b\n", "a/b", "ab\n")
	tag := openApp(t, st)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	browser := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "browser", http.StatusTeapot) })
	for hname, h := range map[string]http.Handler{
		"admin only":   NewAdminHandler(st, nil, nil, LoopbackAuthenticator{}, func(string) bool { return true }, nil, log, nil),
		"with browser": NewAdminHandler(st, nil, nil, LoopbackAuthenticator{}, func(string) bool { return true }, nil, log, browser),
	} {
		for _, p := range []string{"a/./b", "a/../b", "a//b", "./b", "../b", "a/b/.", "a/%2E%2E/b", "a/%2e/b"} {
			for _, m := range []string{"PUT", "GET", "DELETE"} {
				w := draftDo(t, h, m, "/admin/kits/app/draft/files/"+p, "application/octet-stream", tag, []byte("x"))
				if w.Code != http.StatusBadRequest || body(w) != unsafeDraftPath {
					t.Fatalf("%s: %s %s = %d %q %v", hname, m, p, w.Code, w.Body, w.Header())
				}
			}
		}
	}
	if d, _ := st.GetKitDraft("app"); DraftETag(d.Tree) != tag {
		t.Fatal("a refused path changed the draft")
	}
}

func TestReplaceDraftRoute(t *testing.T) {
	h, st := newTestAdmin(t)
	pushApp(t, st, "kit.yml", "kind: studio\n")
	tag := draftDo(t, h, "POST", "/admin/kits/app/draft", "application/json", "", nil).Header().Get("ETag")
	const xtar = "application/x-tar"
	// A hand-made `tar -C dir -cf - .`: "./" prefixes and directory entries.
	var hand bytes.Buffer
	tw := tar.NewWriter(&hand)
	for _, e := range []struct {
		name string
		flag byte
		mode int64
		data string
	}{{"./", tar.TypeDir, 0o755, ""}, {"./kit.yml", tar.TypeReg, 0o644, "kind: studio\n"}, {"./bin/", tar.TypeDir, 0o755, ""}, {"./bin/run", tar.TypeReg, 0o700, "echo\n"}} {
		if err := tw.WriteHeader(&tar.Header{Name: e.name, Typeflag: e.flag, Mode: e.mode, Size: int64(len(e.data))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(e.data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if w := draftDo(t, h, "PUT", "/admin/kits/app/draft", "text/plain", tag, hand.Bytes()); w.Code != http.StatusUnsupportedMediaType || body(w) != "kit draft: Content-Type must be application/x-tar" {
		t.Fatalf("Content-Type = %d %s", w.Code, w.Body)
	}
	if w := draftDo(t, h, "PUT", "/admin/kits/app/draft", xtar, "", hand.Bytes()); w.Code != http.StatusPreconditionRequired {
		t.Fatalf("no If-Match = %d", w.Code)
	}
	if w := draftDo(t, h, "PUT", "/admin/kits/app/draft", xtar, DraftETag(strings.Repeat("0", 64)), hand.Bytes()); w.Code != http.StatusPreconditionFailed || body(w) != "draft app changed since you loaded it (reload)" {
		t.Fatalf("stale If-Match = %d %s", w.Code, w.Body)
	}
	w := draftDo(t, h, "PUT", "/admin/kits/app/draft", xtar, tag, hand.Bytes())
	var s KitDraftSave
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &s) != nil || w.Header().Get("ETag") != s.ETag || !slices.Equal(s.Changed, []DraftChange{{"bin/run", "added"}}) {
		t.Fatalf("replace = %d %s", w.Code, w.Body)
	}
	if g := draftDo(t, h, "GET", "/admin/kits/app/draft/files/bin/run", "", "", nil); g.Header().Get("X-Kit-File-Mode") != "0755" {
		t.Fatalf("mode = %q", g.Header().Get("X-Kit-File-Mode"))
	}
	// Hostile tars (Review Focus 4): a symlink, escape-character and dot
	// segment names, garbage. Each is refused and stores nothing.
	var link bytes.Buffer
	lw := tar.NewWriter(&link)
	if err := lw.WriteHeader(&tar.Header{Name: "leak", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"}); err != nil {
		t.Fatal(err)
	}
	lw.Close()
	if w := draftDo(t, h, "PUT", "/admin/kits/app/draft", xtar, s.ETag, link.Bytes()); w.Code != http.StatusBadRequest || body(w) != "draft app: leak: symlinks are not allowed" {
		t.Fatalf("symlink = %d %s", w.Code, w.Body)
	}
	var esc bytes.Buffer
	if err := kittree.WriteTar(&esc, []kittree.File{{Path: "a\x1b[31mb", Data: []byte("x")}}); err != nil {
		t.Fatal(err)
	}
	if w := draftDo(t, h, "PUT", "/admin/kits/app/draft", xtar, s.ETag, esc.Bytes()); w.Code != http.StatusBadRequest || body(w) != unsafeDraftPath {
		t.Fatalf("escape name = %d %q", w.Code, w.Body)
	}
	for _, name := range []string{"../x", "a/./b", "x/../../y"} {
		var dot bytes.Buffer
		dw := tar.NewWriter(&dot)
		if err := dw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: 1}); err != nil {
			t.Fatal(err)
		}
		dw.Write([]byte("x"))
		dw.Close()
		if w := draftDo(t, h, "PUT", "/admin/kits/app/draft", xtar, s.ETag, dot.Bytes()); w.Code != http.StatusBadRequest || body(w) != unsafeDraftPath {
			t.Fatalf("tar %q = %d %q", name, w.Code, w.Body)
		}
	}
	if w := draftDo(t, h, "PUT", "/admin/kits/app/draft", xtar, s.ETag, []byte("garbage, not a tar, garbage, not a tar")); w.Code != http.StatusBadRequest || body(w) != "draft app: not a readable tar" {
		t.Fatalf("garbage = %d %s", w.Code, w.Body)
	}
	// The body cap (MaxUpload) answers 413, as push does.
	withDraftLimits(t, 1, 1024, 8<<20)
	var big bytes.Buffer
	if err := kittree.WriteTar(&big, []kittree.File{{Path: "kit.yml", Data: make([]byte, 2<<20)}}); err != nil {
		t.Fatal(err)
	}
	if w := draftDo(t, h, "PUT", "/admin/kits/app/draft", xtar, s.ETag, big.Bytes()); w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize = %d %s", w.Code, w.Body)
	}
	if d, _ := st.GetKitDraft("app"); DraftETag(d.Tree) != s.ETag {
		t.Fatal("a refused replace changed the draft")
	}
}
