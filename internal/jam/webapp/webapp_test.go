package webapp

import (
	"errors"
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

const indexHTML = `<!doctype html><html><head><!--jam:bootstrap--><script type="module" src="/app/next/assets/index-abc.js"></script></head><body><div id="root"></div></body></html>`

func bundle() fstest.MapFS {
	return fstest.MapFS{
		"index.html":          {Data: []byte(indexHTML)},
		"assets/index-abc.js": {Data: []byte("console.log(1)")},
		"assets/font.woff2":   {Data: []byte("woff")},
		".keep":               {Data: []byte("x")},
	}
}

func serve(t *testing.T, c Config, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	Handler(c).ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

func cfg() Config {
	return Config{Prefix: "/app/next/", FS: bundle(), Name: "the Jam app",
		Bootstrap: func(*http.Request) (any, error) { return map[string]string{"brand": "Acme Jam"}, nil }}
}

func TestIndexCarriesBootstrapAndCSP(t *testing.T) {
	for _, path := range []string{"/app/next/", "/app/next/c/chn_x", "/app/next/session/ses_1/thinking"} {
		rec := serve(t, cfg(), "GET", path)
		if rec.Code != 200 {
			t.Fatalf("GET %s = %d", path, rec.Code)
		}
		body := rec.Body.String()
		if !strings.Contains(body, `<script id="jam-bootstrap" type="application/json">{"brand":"Acme Jam"}</script>`) {
			t.Errorf("GET %s: no bootstrap block:\n%s", path, body)
		}
		if strings.Contains(body, "<!--jam:bootstrap-->") {
			t.Errorf("GET %s: marker left in page", path)
		}
		h := rec.Header()
		if got, want := h.Get("Content-Security-Policy"), "default-src 'self'; script-src 'self'; connect-src 'self'; img-src 'self' blob: data:; font-src 'self'; style-src 'self' 'unsafe-inline'; frame-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"; got != want {
			t.Errorf("CSP = %q\nwant %q", got, want)
		}
		if got := h.Get("Cache-Control"); got != "no-store" {
			t.Errorf("index Cache-Control = %q, want no-store", got)
		}
		if got := h.Get("Content-Type"); got != "text/html; charset=utf-8" {
			t.Errorf("index Content-Type = %q", got)
		}
		if h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Referrer-Policy") != "same-origin" {
			t.Errorf("missing nosniff/referrer headers: %v", h)
		}
	}
}

func TestFrameSrcListsOrigins(t *testing.T) {
	c := cfg()
	c.FrameSrc = []string{"https://a.preview.example", "https://*.preview.example:8081"}
	csp := serve(t, c, "GET", "/app/next/").Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "frame-src https://a.preview.example https://*.preview.example:8081;") {
		t.Errorf("CSP = %q", csp)
	}
}

func TestBootstrapCannotBreakOutOfScript(t *testing.T) {
	c := cfg()
	c.Bootstrap = func(*http.Request) (any, error) {
		return map[string]string{"brand": `</script><script>alert(1)</script><!--`}, nil
	}
	body := serve(t, c, "GET", "/app/next/").Body.String()
	if strings.Count(body, "</script>") != 2 { // the bootstrap's own and the module script's
		t.Errorf("bootstrap broke out of its script:\n%s", body)
	}
	if strings.Contains(body, "<!--<") || strings.Contains(body, "alert(1)</script>") {
		t.Errorf("unescaped markup in bootstrap:\n%s", body)
	}
}

func TestBootstrapErrorIs500(t *testing.T) {
	c := cfg()
	c.Bootstrap = func(*http.Request) (any, error) { return nil, errors.New("store down") }
	if rec := serve(t, c, "GET", "/app/next/"); rec.Code != 500 {
		t.Errorf("code = %d, want 500", rec.Code)
	}
}

func TestAssetsImmutableAndMissingAssets404(t *testing.T) {
	rec := serve(t, cfg(), "GET", "/app/next/assets/index-abc.js")
	if rec.Code != 200 || rec.Body.String() != "console.log(1)" {
		t.Fatalf("asset = %d %q", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Errorf("asset Cache-Control = %q", got)
	}
	for _, path := range []string{"/app/next/assets/old-999.js", "/app/next/assets/", "/app/next/assets/.keep", "/app/next/.keep", "/app/next/index.html"} {
		if rec := serve(t, cfg(), "GET", path); path == "/app/next/index.html" {
			if rec.Code != 301 && rec.Code != 308 && rec.Code != 200 {
				t.Errorf("GET %s = %d", path, rec.Code)
			}
		} else if rec.Code != 404 {
			t.Errorf("GET %s = %d, want 404", path, rec.Code)
		}
	}
}

func TestOnlyGetAndHead(t *testing.T) {
	if rec := serve(t, cfg(), "POST", "/app/next/"); rec.Code != 405 {
		t.Errorf("POST = %d, want 405", rec.Code)
	}
	if rec := serve(t, cfg(), "HEAD", "/app/next/"); rec.Code != 200 {
		t.Errorf("HEAD = %d, want 200", rec.Code)
	}
}

func TestNotBuiltPage(t *testing.T) {
	c := cfg()
	c.FS = fstest.MapFS{".keep": {Data: []byte("x")}}
	for _, path := range []string{"/app/next/", "/app/next/c/x"} {
		rec := serve(t, c, "GET", path)
		if rec.Code != 503 {
			t.Errorf("GET %s = %d, want 503", path, rec.Code)
		}
		if body := rec.Body.String(); !strings.Contains(body, "the Jam app") || !strings.Contains(body, "just app") {
			t.Errorf("not-built page = %q", body)
		}
	}
	if rec := serve(t, c, "GET", "/app/next/assets/x.js"); rec.Code != 404 {
		t.Errorf("asset without bundle = %d, want 404", rec.Code)
	}
}

// Head markup (the app's manifest link and icons) lands at <!--jam:head-->;
// without a Head the marker goes quietly.
func TestHandlerInjectsHead(t *testing.T) {
	fsys := fstest.MapFS{"index.html": {Data: []byte("<head><!--jam:head--></head><body><!--jam:bootstrap--></body>")}}
	c := Config{Prefix: "/app/next/", FS: fsys, Name: "x",
		Bootstrap: func(*http.Request) (any, error) { return map[string]string{}, nil },
		Head:      func(*http.Request) template.HTML { return `<link rel="manifest" href="/m">` }}
	if rec := serve(t, c, "GET", "/app/next/c/x"); !strings.Contains(rec.Body.String(), `<head><link rel="manifest" href="/m"></head>`) {
		t.Errorf("page = %s", rec.Body)
	}
	c.Head = nil
	if rec := serve(t, c, "GET", "/app/next/"); strings.Contains(rec.Body.String(), "jam:head") {
		t.Errorf("marker left: %s", rec.Body)
	}
}
