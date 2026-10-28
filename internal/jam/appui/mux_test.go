package appui

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// echo stubs an area handler: it answers with the path it was asked for.
func echo(tag string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s %s", tag, r.URL.Path)
	})
}

func testMux(t *testing.T) *http.ServeMux {
	t.Helper()
	return Handler(Deps{
		Gate:           NewGate(GateConfig{Sessions: testSessions(t), LoginPath: "/app/auth/login", Log: discardLog()}),
		AuthRoutes:     echo("auth"),
		User:           echo("user"),
		Admin:          echo("admin"),
		TrustedOrigins: []string{"https://jam.example"},
	})
}

// muxReq builds an off-loopback request with the given session cookies.
func muxReq(method, target, idTok, atTok string) *http.Request {
	r := httptest.NewRequest(method, target, nil)
	r.RemoteAddr = "203.0.113.5:1234"
	if idTok != "" {
		r.AddCookie(&http.Cookie{Name: "jam_app_id", Value: idTok})
	}
	if atTok != "" {
		r.AddCookie(&http.Cookie{Name: "jam_app_at", Value: atTok})
	}
	return r
}

func TestLegacyRedirects(t *testing.T) {
	cases := []struct {
		method, path string
		wantCode     int
		wantLoc      string
	}{
		{"GET", "/me/?c=chn_x", 308, "/app/user/?c=chn_x"},
		{"GET", "/me/rail", 308, "/app/user/rail"},
		{"GET", "/ui/agents?project=p", 308, "/app/admin/agents?project=p"},
		{"GET", "/ui/", 308, "/app/admin/"},
		{"GET", "/ui/auth/login", 308, "/app/auth/login"},
		{"GET", "/me/auth/callback?code=x", 308, "/app/auth/callback?code=x"},
		{"GET", "/ui/static/jam.css", 308, "/app/static/jam.css"},
		{"GET", "/me/static/htmx.min.js", 308, "/app/static/htmx.min.js"},
		{"HEAD", "/me/", 308, "/app/user/"},
		{"POST", "/me/send", 404, ""},
		{"POST", "/ui/agents", 404, ""},
	}
	m := testMux(t)
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			m.ServeHTTP(rec, muxReq(tc.method, tc.path, "", ""))
			if rec.Code != tc.wantCode {
				t.Fatalf("code = %d, want %d", rec.Code, tc.wantCode)
			}
			if got := rec.Header().Get("Location"); got != tc.wantLoc {
				t.Errorf("Location = %q, want %q", got, tc.wantLoc)
			}
			if tc.wantCode == 308 {
				if cc := rec.Header().Get("Cache-Control"); cc != "max-age=3600" {
					t.Errorf("Cache-Control = %q, want max-age=3600", cc)
				}
			}
		})
	}
}

// An explicit /me/studios/ route (cmd registers a 404 when the participant
// studios are not mounted) beats the /me/ legacy redirect.
func TestExplicitStudios404BeatsLegacyRedirect(t *testing.T) {
	m := Handler(Deps{Gate: NewGate(GateConfig{Log: discardLog()})})
	m.Handle("/me/studios/", http.NotFoundHandler())
	rec := httptest.NewRecorder()
	m.ServeHTTP(rec, muxReq("GET", "/me/studios/x", "", ""))
	if rec.Code != 404 {
		t.Fatalf("code = %d, want 404", rec.Code)
	}
}

func TestLanding(t *testing.T) {
	cases := []struct {
		name, path   string
		idTok, atTok string
		wantCode     int
		wantLoc      string
	}{
		{"root ungated", "/", "", "", 302, "/app/"},
		{"member", "/app/", "alice", "", 302, "/app/user/"},
		{"admin only", "/app/", "stranger", "ok", 302, "/app/admin/"},
		{"both", "/app/", "alice", "ok", 302, "/app/user/"},
		{"neither", "/app/", "stranger", "", 403, ""},
		{"no session", "/app/", "", "", 302, "/app/auth/login?return_to=%2Fapp%2F"},
	}
	m := testMux(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			m.ServeHTTP(rec, muxReq("GET", tc.path, tc.idTok, tc.atTok))
			if rec.Code != tc.wantCode {
				t.Fatalf("code = %d, want %d (body %q)", rec.Code, tc.wantCode, rec.Body)
			}
			if got := rec.Header().Get("Location"); got != tc.wantLoc {
				t.Errorf("Location = %q, want %q", got, tc.wantLoc)
			}
		})
	}
}

func TestAreasGated(t *testing.T) {
	cases := []struct {
		name, method, path string
		idTok, atTok       string
		origin             string
		wantCode           int
		wantBody           string
	}{
		{"user area, member", "GET", "/app/user/x", "alice", "", "", 200, "user /app/user/x"},
		{"user area, admin only", "GET", "/app/user/x", "stranger", "ok", "", 403, ""},
		{"admin area, admin", "GET", "/app/admin/x", "stranger", "ok", "", 200, "admin /app/admin/x"},
		{"admin area, member only", "GET", "/app/admin/x", "alice", "", "", 403, ""},
		{"user send, cross-origin", "POST", "/app/user/send", "alice", "", "https://evil.example", 403, ""},
		{"user send, trusted origin", "POST", "/app/user/send", "alice", "", "https://jam.example", 200, "user /app/user/send"},
		{"auth routes ungated", "GET", "/app/auth/login", "", "", "", 200, "auth /app/auth/login"},
		{"user area, signed out", "GET", "/app/user/", "", "", "", 302, "return_to=%2Fapp%2Fuser%2F"},
	}
	m := testMux(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := muxReq(tc.method, tc.path, tc.idTok, tc.atTok)
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			rec := httptest.NewRecorder()
			m.ServeHTTP(rec, r)
			if rec.Code != tc.wantCode {
				t.Fatalf("code = %d, want %d (body %q)", rec.Code, tc.wantCode, rec.Body)
			}
			if !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Errorf("body = %q, want substring %q", rec.Body, tc.wantBody)
			}
		})
	}
}

func TestStaticUngated(t *testing.T) {
	rec := httptest.NewRecorder()
	testMux(t).ServeHTTP(rec, muxReq("GET", "/app/static/jam.css", "", ""))
	if rec.Code != 200 {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/css") {
		t.Errorf("Content-Type = %q, want text/css", ct)
	}
}

// The app bundle's assets are code, not data: served to anyone signed in
// or not, for both areas.
func TestAssetsUngated(t *testing.T) {
	mux := Handler(Deps{
		Gate:  NewGate(GateConfig{Sessions: testSessions(t), LoginPath: "/app/auth/login", Log: discardLog()}),
		User:  echo("user"),
		Admin: echo("admin"),
		App:   fstest.MapFS{"index.html": {Data: []byte("x")}, "assets/x.js": {Data: []byte("js")}},
	})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, muxReq("GET", "/app/assets/x.js", "", ""))
	if rec.Code != 200 || rec.Body.String() != "js" {
		t.Fatalf("GET /app/assets/x.js = %d %q (Location %q), want 200 js", rec.Code, rec.Body, rec.Header().Get("Location"))
	}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, muxReq("POST", "/app/assets/x.js", "", ""))
	if rec.Code == 200 {
		t.Errorf("POST /app/assets/x.js = 200, want refused")
	}
}

func TestStudiosRouteCanBeAdded(t *testing.T) {
	m := testMux(t)
	m.Handle("/me/studios/", echo("studios"))
	rec := httptest.NewRecorder()
	m.ServeHTTP(rec, muxReq("GET", "/me/studios/x", "", ""))
	if rec.Code != 200 || rec.Body.String() != "studios /me/studios/x" {
		t.Fatalf("got %d %q, want the studios stub", rec.Code, rec.Body)
	}
}

// The Jam app's pre-cutover home redirects to the same path under the user
// area, its query kept; a write there is not redirected.
func TestNextRedirectsToTheUserArea(t *testing.T) {
	m := Handler(Deps{Gate: NewGate(GateConfig{Log: discardLog()})})
	rec := httptest.NewRecorder()
	m.ServeHTTP(rec, httptest.NewRequest("GET", "/app/next/c/chn_1?tab=status", nil))
	if rec.Code != http.StatusPermanentRedirect || rec.Header().Get("Location") != "/app/user/c/chn_1?tab=status" {
		t.Errorf("GET /app/next/… = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	rec = httptest.NewRecorder()
	m.ServeHTTP(rec, httptest.NewRequest("POST", "/app/next/c/chn_1", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("POST /app/next/… = %d", rec.Code)
	}
}
