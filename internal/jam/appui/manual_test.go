package appui

import (
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestManualNotBuilt(t *testing.T) {
	rec := httptest.NewRecorder()
	manualHandler(fstest.MapFS{}).ServeHTTP(rec, httptest.NewRequest("GET", "/app/manual/", nil))
	if rec.Code != 404 || !strings.Contains(rec.Body.String(), "isn't built") {
		t.Fatalf("got %d %q", rec.Code, rec.Body)
	}
}

func TestManualCSP(t *testing.T) {
	fsys := fstest.MapFS{"index.html": {Data: []byte("manual page")}}
	rec := httptest.NewRecorder()
	manualHandler(fsys).ServeHTTP(rec, httptest.NewRequest("GET", "/app/manual/", nil))
	if rec.Code != 200 {
		t.Fatalf("code = %d", rec.Code)
	}
	want := "default-src 'self'; script-src 'none'; object-src 'none'"
	if got := rec.Header().Get("Content-Security-Policy"); got != want {
		t.Errorf("CSP = %q, want %q", got, want)
	}
}

func TestManualPaths(t *testing.T) {
	h := manualHandler(fstest.MapFS{
		"index.html":              {Data: []byte("root")},
		"jam/released/index.html": {Data: []byte("page")},
	})
	for _, tc := range []struct {
		path string
		code int
		body string
	}{
		{"/app/manual/", 200, "root"},
		{"/app/manual/jam/released/", 200, "page"},
		{"/app/manual/jam/released", 301, ""},
		{"/app/manual/nope.html", 404, ""},
		{"/app/manual/../../etc/passwd", 404, ""},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", tc.path, nil))
		if rec.Code != tc.code || !strings.Contains(rec.Body.String(), tc.body) {
			t.Errorf("%s: %d %q, want %d %q", tc.path, rec.Code, rec.Body, tc.code, tc.body)
		}
	}
}
