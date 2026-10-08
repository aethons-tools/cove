package uiassets_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam/uiassets"
)

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestServesSharedAssetsUnderAnyPrefix(t *testing.T) {
	for _, prefix := range []string{"/ui/static/", "/me/static/"} {
		h := uiassets.Handler(prefix)
		css := get(t, h, prefix+"jam.css")
		if css.Code != http.StatusOK || !strings.HasPrefix(css.Header().Get("Content-Type"), "text/css") {
			t.Fatalf("%sjam.css = %d %q", prefix, css.Code, css.Header().Get("Content-Type"))
		}
		js := get(t, h, prefix+"htmx.min.js")
		if js.Code != http.StatusOK || !strings.HasPrefix(js.Header().Get("Content-Type"), "text/javascript") {
			t.Fatalf("%shtmx.min.js = %d %q", prefix, js.Code, js.Header().Get("Content-Type"))
		}
	}
}

// jam.css is the one token source: both UIs' tokens, both themes.
func TestStylesheetCarriesBothUIsTokens(t *testing.T) {
	css := get(t, uiassets.Handler("/s/"), "/s/jam.css").Body.String()
	for _, want := range []string{
		"--bg:", "--accent:", "--bad:", "--bad-soft:", // admin
		"--agent-bubble:", "--human-bubble:", "--wait-line:", // /me
		"@media (prefers-color-scheme:dark)", `"IBM Plex Sans"`,
	} {
		if !strings.Contains(css, want) {
			t.Errorf("jam.css missing %q", want)
		}
	}
}

func TestStylesheetHrefIsVersioned(t *testing.T) {
	href := uiassets.StylesheetHref("/me/static/")
	if !strings.HasPrefix(href, "/me/static/jam.css?v=") || len(href) <= len("/me/static/jam.css?v=") {
		t.Fatalf("href = %q", href)
	}
	if href != uiassets.StylesheetHref("/me/static/") {
		t.Fatal("href must be stable for one build")
	}
}

// Only the shared assets are served — nothing else in the package.
func TestServesNothingElse(t *testing.T) {
	h := uiassets.Handler("/s/")
	for _, p := range []string{"/s/uiassets.go", "/s/", "/s/../uiassets.go"} {
		if rec := get(t, h, p); rec.Code == http.StatusOK && strings.Contains(rec.Body.String(), "package uiassets") {
			t.Errorf("%s exposed package source", p)
		}
	}
}
