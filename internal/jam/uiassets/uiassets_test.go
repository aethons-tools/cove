package uiassets_test

import (
	"net/http"
	"net/http/httptest"
	"regexp"
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

func TestServesBrandMark(t *testing.T) {
	rec := get(t, uiassets.Handler("/app/static/"), "/app/static/brand/j-logo-plain.svg")
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/svg+xml" {
		t.Fatalf("logo = %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
}

// A long brand truncates (and hides on phones) rather than squeezing the tabs
// and the principal menu off-screen.
func TestStylesheetTitlebarSurvivesNarrowScreens(t *testing.T) {
	css := get(t, uiassets.Handler("/s/"), "/s/jam.css").Body.String()
	for _, want := range []string{
		"white-space:nowrap;min-width:0;max-width:35%;flex-shrink:1}",
		"flex:1;min-width:120px;overflow-x:auto}",
		".app-logo span{overflow:hidden;text-overflow:ellipsis}",
		".app-principal{position:relative;margin-left:auto;flex-shrink:0}",
		".app-principal .nm,.app-logo span{display:none}",
	} {
		if !strings.Contains(css, want) {
			t.Errorf("jam.css missing %q", want)
		}
	}
}

// Title-bar badges wear the rail's colours: amber, red when broken, bold.
func TestStylesheetTitlebarBadgesMatchRail(t *testing.T) {
	css := get(t, uiassets.Handler("/s/"), "/s/jam.css").Body.String()
	for _, want := range []string{
		"background:var(--wait-soft);color:var(--wait);font-size:11px;font-weight:700",
		".app-tabs .badge.red{background:var(--bad-soft);color:var(--bad)}",
	} {
		if !strings.Contains(css, want) {
			t.Errorf("jam.css missing %q", want)
		}
	}
	if strings.Contains(css, ".app-tabs .badge{display:inline-block;min-width:18px;padding:0 5px;border-radius:9px;background:var(--idle-soft)") {
		t.Error("title-bar badge is still grey")
	}
}

// The chrome's height has one source, which the rail's sticky offset reads.
func TestStylesheetChromeHeightIsDerived(t *testing.T) {
	css := get(t, uiassets.Handler("/s/"), "/s/jam.css").Body.String()
	for _, want := range []string{"--chrome-h:calc(var(--titlebar-h) + var(--subbar-h))", "height:var(--titlebar-h)"} {
		if !strings.Contains(css, want) {
			t.Errorf("jam.css missing %q", want)
		}
	}
}

func TestServesIcons(t *testing.T) {
	rec := get(t, uiassets.Handler("/app/static/"), "/app/static/icons/icon-192.png")
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("icon = %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
}

func TestIconHrefIsVersioned(t *testing.T) {
	href := uiassets.IconHref("/app/static/", "icon-192.png")
	if !regexp.MustCompile(`^/app/static/icons/icon-192\.png\?v=[0-9a-f]{12}$`).MatchString(href) {
		t.Fatalf("href = %q", href)
	}
}

func TestIconHrefPanicsOnUnknownIcon(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("want panic for an unknown icon")
		}
	}()
	uiassets.IconHref("/app/static/", "nope.png")
}
