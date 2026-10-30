package vscodeext

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeVSIX is a zip holding extension/package.json with version (pkg "" =
// no package.json).
func fakeVSIX(t *testing.T, pkg string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("extension.vsixmanifest")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write([]byte("<PackageManifest/>"))
	if pkg != "" {
		w, err := zw.Create("extension/package.json")
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(pkg))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestParse(t *testing.T) {
	b, err := Parse(fakeVSIX(t, `{"name":"jam","version":"0.123.0"}`))
	if err != nil || !b.Available() || b.Version() != "0.123.0" {
		t.Fatalf("Parse = %+v, %v; want 0.123.0", b, err)
	}
	for name, data := range map[string][]byte{
		"not a zip":       []byte("nope"),
		"no package.json": fakeVSIX(t, ""),
		"bad json":        fakeVSIX(t, `{`),
		"no version":      fakeVSIX(t, `{"name":"jam"}`),
		"not x.y.z":       fakeVSIX(t, `{"version":"1.2"}`),
		"a prerelease":    fakeVSIX(t, `{"version":"1.2.3-beta"}`),
		"leading zero":    fakeVSIX(t, `{"version":"0.01.0"}`),
	} {
		if _, err := Parse(data); err == nil {
			t.Errorf("%s: Parse succeeded, want an error", name)
		}
	}
}

// The committed tree stages no .vsix: a plain build serves none.
func TestEmbeddedPlaceholder(t *testing.T) {
	if b := Embedded(); b.Available() {
		t.Skipf("a .vsix is staged (%s); the placeholder case can't be checked", b.Version())
	}
	if Embedded().Version() != "" {
		t.Error("an empty bundle has a version")
	}
}

func serve(h http.Handler, method, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

func TestRoutes(t *testing.T) {
	data := fakeVSIX(t, `{"version":"0.123.0"}`)
	b, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	h := Routes("/me", b, nil)

	rec := serve(h, "GET", "/me/vscode")
	var info Info
	if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil || rec.Code != 200 || info != (Info{Available: true, Version: "0.123.0"}) {
		t.Fatalf("info: %d %q (%v)", rec.Code, rec.Body, err)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("info Cache-Control = %q", rec.Header().Get("Cache-Control"))
	}

	rec = serve(h, "GET", "/me/vscode/jam.vsix")
	if rec.Code != 200 || !bytes.Equal(rec.Body.Bytes(), data) {
		t.Fatalf("vsix: %d, %d bytes", rec.Code, rec.Body.Len())
	}
	if cd := rec.Header().Get("Content-Disposition"); cd != `attachment; filename="jam-vscode_0.123.0.vsix"` {
		t.Errorf("Content-Disposition = %q", cd)
	}
	if rec.Header().Get("Content-Type") != "application/octet-stream" {
		t.Errorf("Content-Type = %q", rec.Header().Get("Content-Type"))
	}

	rec = serve(h, "HEAD", "/me/vscode/jam.vsix")
	if rec.Code != 200 || rec.Body.Len() != 0 || rec.Header().Get("Content-Length") == "" {
		t.Errorf("HEAD: %d, %d bytes, length %q", rec.Code, rec.Body.Len(), rec.Header().Get("Content-Length"))
	}
	if rec := serve(h, "POST", "/me/vscode"); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST: %d", rec.Code)
	}
	if rec := serve(h, "GET", "/me/vscode/other"); rec.Code != http.StatusNotFound {
		t.Errorf("other path: %d", rec.Code)
	}
	// The prefix is the plane's: /admin's handler doesn't answer /me paths.
	if rec := serve(Routes("/admin", b, nil), "GET", "/me/vscode"); rec.Code != http.StatusNotFound {
		t.Errorf("/me path on /admin routes: %d", rec.Code)
	}
}

func TestRoutesWithoutBundle(t *testing.T) {
	h := Routes("/me", Bundle{}, nil)
	rec := serve(h, "GET", "/me/vscode")
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"available":false}` {
		t.Errorf("info: %d %q", rec.Code, rec.Body)
	}
	if rec := serve(h, "GET", "/me/vscode/jam.vsix"); rec.Code != http.StatusNotFound {
		t.Errorf("vsix: %d", rec.Code)
	}
}

func TestRoutesAuthorize(t *testing.T) {
	b, _ := Parse(fakeVSIX(t, `{"version":"0.1.0"}`))
	deny := Routes("/me", b, func(*http.Request) error { return errors.New("no") })
	for _, p := range []string{"/me/vscode", "/me/vscode/jam.vsix"} {
		rec := serve(deny, "GET", p)
		if rec.Code != http.StatusUnauthorized || rec.Header().Get("WWW-Authenticate") == "" || strings.Contains(rec.Body.String(), "0.1.0") {
			t.Errorf("%s denied: %d %q", p, rec.Code, rec.Body)
		}
	}
	allow := Routes("/me", b, func(*http.Request) error { return nil })
	if rec := serve(allow, "GET", "/me/vscode"); rec.Code != 200 {
		t.Errorf("allowed: %d", rec.Code)
	}
}
