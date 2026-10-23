package appui

import (
	"encoding/json"
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam/uiassets"
)

func pwaMux(t *testing.T, brand string) *http.ServeMux {
	t.Helper()
	return Handler(Deps{
		Gate:           NewGate(GateConfig{Sessions: testSessions(t), LoginPath: "/app/auth/login", Log: discardLog()}),
		AuthRoutes:     echo("auth"),
		User:           echo("user"),
		Admin:          echo("admin"),
		TrustedOrigins: []string{"https://jam.example"},
		Shell:          Shell{Brand: brand},
	})
}

func serve(m http.Handler, method, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	m.ServeHTTP(rec, muxReq(method, path, "", "")) // off-loopback, no cookies
	return rec
}

type gotIcon struct{ Src, Sizes, Type, Purpose string }

type gotManifest struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	ShortName       string    `json:"short_name"`
	Scope           string    `json:"scope"`
	StartURL        string    `json:"start_url"`
	Display         string    `json:"display"`
	ThemeColor      string    `json:"theme_color"`
	BackgroundColor string    `json:"background_color"`
	Icons           []gotIcon `json:"icons"`
}

// The browser fetches the manifest with no cookies off-loopback: it must be
// served ungated, as valid JSON carrying the brand verbatim.
func TestManifest(t *testing.T) {
	const brand = "Acme \"R&D\" <Ops> Jäm Jam"
	rec := serve(pwaMux(t, brand), "GET", "/app/manifest.webmanifest")
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/manifest+json" {
		t.Fatalf("manifest = %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	var m gotManifest
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("manifest not JSON: %v\n%s", err, rec.Body)
	}
	if m.ID != "/app/" || m.Name != brand || m.ShortName != "Jam" || m.Scope != "/app/" || m.StartURL != "/app/" ||
		m.Display != "standalone" || m.ThemeColor != "#2F67B2" || m.BackgroundColor != "#F5F6F8" {
		t.Errorf("manifest = %+v", m)
	}
	want := []gotIcon{
		{uiassets.IconHref("/app/static/", "icon-192.png"), "192x192", "image/png", "any"},
		{uiassets.IconHref("/app/static/", "icon-512.png"), "512x512", "image/png", "any"},
		{uiassets.IconHref("/app/static/", "icon-maskable-512.png"), "512x512", "image/png", "maskable"},
	}
	if len(m.Icons) != len(want) {
		t.Fatalf("icons = %+v", m.Icons)
	}
	for i := range want {
		if m.Icons[i] != want[i] {
			t.Errorf("icon %d = %+v, want %+v", i, m.Icons[i], want[i])
		}
	}
}

func TestManifestDefaultName(t *testing.T) {
	var m gotManifest
	if err := json.Unmarshal(serve(pwaMux(t, ""), "GET", "/app/manifest.webmanifest").Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if m.Name != "Jam" {
		t.Errorf("name = %q, want Jam", m.Name)
	}
}

func TestServiceWorker(t *testing.T) {
	rec := serve(pwaMux(t, ""), "GET", "/app/sw.js")
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/javascript") ||
		rec.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("sw.js = %d %q %q", rec.Code, rec.Header().Get("Content-Type"), rec.Header().Get("Cache-Control"))
	}
	body := rec.Body.String()
	for _, want := range []string{"skipWaiting", "clients.claim",
		// Push display and click-through (spec §8).
		"addEventListener('push'", "addEventListener('notificationclick'", "setAppBadge", "tag:",
		"renotify: true", "/app/user/?c=", "/app/static/icons/icon-192.png", "/app/static/icons/favicon-32.png",
		"getNotifications", "new messages", " · latest: ", "clients.openWindow", "matchAll",
		// Robustness: any payload shape, any failure still shows something;
		// a click only reuses an /app/user/ window.
		"jamPayload(", "jamFallback(", "'New message'", "catch (_)", "jamPickClient(", "'/app/user/'"} {
		if !strings.Contains(body, want) {
			t.Errorf("sw.js missing %q", want)
		}
	}
	for _, bad := range []string{"caches.", "respondWith", "addEventListener('fetch'", `addEventListener("fetch"`} {
		if strings.Contains(body, bad) {
			t.Errorf("sw.js must not intercept requests (no fetch handler, no caches), has %q", bad)
		}
	}
}

func TestIconsUngated(t *testing.T) {
	rec := serve(pwaMux(t, ""), "GET", "/app/static/icons/icon-512.png")
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("icon = %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
}

// Only the exact manifest and sw.js paths are ungated.
func TestPWARoutesUngateNothingElse(t *testing.T) {
	m := pwaMux(t, "")
	for _, p := range []string{"/app/sw.js.map", "/app/manifest.webmanifest/x", "/app/anything", "/app/sw.js/"} {
		if rec := serve(m, "GET", p); rec.Code == 200 {
			t.Errorf("GET %s = 200 without a session", p)
		}
	}
	for _, p := range []string{"/app/manifest.webmanifest", "/app/sw.js"} {
		if rec := serve(m, "POST", p); rec.Code != 405 && rec.Code != 404 {
			t.Errorf("POST %s = %d, want 405 or 404", p, rec.Code)
		}
	}
}

func TestHeadTemplate(t *testing.T) {
	var head Head
	probe := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { head = HeadFor(r) })
	WithShell(probe, Shell{Brand: "<b>x</b>"}).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/app/user/", nil))
	if head.Title != "<b>x</b>" {
		t.Errorf("title = %q", head.Title)
	}
	tmpl := template.Must(template.New("").Parse(HeadTemplate))
	var b strings.Builder
	if err := tmpl.ExecuteTemplate(&b, "app-head", head); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{
		`rel="manifest" href="/app/manifest.webmanifest"`,
		`<meta name="theme-color" content="#2F67B2" media="(prefers-color-scheme: light)">`,
		`<meta name="theme-color" content="#0F1116" media="(prefers-color-scheme: dark)">`,
		`<link rel="apple-touch-icon" href="` + uiassets.IconHref("/app/static/", "apple-touch-icon.png") + `">`,
		`?v=`,
		`<meta name="apple-mobile-web-app-capable" content="yes">`,
		`<meta name="mobile-web-app-capable" content="yes">`,
		`<meta name="apple-mobile-web-app-title" content="Jam">`,
		`<link rel="icon" type="image/png" sizes="32x32" href="` + uiassets.IconHref("/app/static/", "favicon-32.png") + `">`,
		`if('serviceWorker' in navigator){`,
		`{scope:'/app/'}`,
		`&lt;b&gt;x&lt;/b&gt;`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("head missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "<b>") {
		t.Errorf("brand not escaped:\n%s", out)
	}

	// No shell: the brand defaults to Jam.
	if h := HeadFor(httptest.NewRequest("GET", "/app/", nil)); h.Title != "Jam" {
		t.Errorf("default title = %q", h.Title)
	}
}
