package jam

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
