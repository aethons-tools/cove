package collabhttp

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam/collab"
)

const label26 = "01hx0000000000000000000000"

const (
	testDomain = "preview.jam.example"
	loginHost  = "jam." + testDomain
	hostA      = "vite--" + label26 + "." + testDomain
	hostB      = "api--" + label26 + "." + testDomain
)

// testClock is an adjustable clock for PreviewAuthOptions.Now.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *testClock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type previewFixture struct {
	*PreviewAuth
	clock *testClock
	logs  *bytes.Buffer
	login http.Handler
}

const aliceID = ident.ID("usr_alice")

// newTestPreviewAuth builds a PreviewAuth over testDomain whose Operator
// accepts access token "op-token" as op1 and whose Participant maps ID token
// "id-alice" to alice; every other token is refused.
func newTestPreviewAuth(t *testing.T) *previewFixture {
	t.Helper()
	clock := &testClock{t: time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)}
	logs := &bytes.Buffer{}
	login := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Login", r.URL.RequestURI())
		w.WriteHeader(http.StatusTeapot)
	})
	a := NewPreviewAuth(PreviewAuthOptions{
		Domain: testDomain,
		Login:  login,
		Operator: func(_ context.Context, tok string) (string, bool) {
			return "op1", tok == "op-token"
		},
		Participant: func(_ context.Context, tok string) (ident.ID, string, bool) {
			if tok == "id-alice" {
				return aliceID, "alice", true
			}
			return "", "", false
		},
		Bearer: func(r *http.Request) (collab.Principal, string, error) {
			if r.Header.Get("Authorization") == "Bearer good" {
				return collab.Principal{Operator: "bearer-op"}, "bearer-op", nil
			}
			return collab.Principal{}, "", ErrNoCredentials
		},
		Now: clock.Now,
		Log: slog.New(slog.NewTextHandler(logs, nil)),
	})
	return &previewFixture{PreviewAuth: a, clock: clock, logs: logs, login: login}
}

// bounce runs Complete for the given tokens and return_to and returns the
// code it minted (failing unless it bounced to /__jam/session on wantHost).
func (f *previewFixture) bounce(t *testing.T, idTok, accessTok, returnTo, wantHost string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	f.Complete(rec, httptest.NewRequest("GET", "https://"+loginHost+"/__jam/auth/callback", nil), idTok, accessTok, returnTo)
	if rec.Code != http.StatusFound {
		t.Fatalf("Complete = %d %s", rec.Code, rec.Body)
	}
	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil || loc.Scheme != "https" || loc.Host != wantHost || loc.Path != "/__jam/session" {
		t.Fatalf("bounce to %v", loc)
	}
	return loc.Query().Get("code")
}

func (f *previewFixture) trade(host, code, returnTo string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", "https://"+host+"/__jam/session?code="+url.QueryEscape(code)+"&return_to="+url.QueryEscape(returnTo), nil)
	w := httptest.NewRecorder()
	f.PreviewHostRoutes(http.NotFoundHandler()).ServeHTTP(w, r)
	return w
}

// session logs alice in on hostA and returns her jam_preview cookie.
func (f *previewFixture) session(t *testing.T) *http.Cookie {
	t.Helper()
	w := f.trade(hostA, f.bounce(t, "id-alice", "", "https://"+hostA+"/", hostA), "/")
	if w.Code != http.StatusFound {
		t.Fatalf("trade = %d", w.Code)
	}
	return w.Result().Cookies()[0]
}

func principalOn(a *PreviewAuth, host string, cookies ...*http.Cookie) (collab.Principal, string, error) {
	r := httptest.NewRequest("GET", "https://"+host+"/app", nil)
	for _, c := range cookies {
		r.AddCookie(c)
	}
	return a.Principal(r)
}

func TestLoginBounceRejectsForeignReturn(t *testing.T) {
	a := newTestPreviewAuth(t) // Domain "preview.jam.example", fake Operator/Participant
	for _, rt := range []string{
		"https://evil.example/", "//evil.example/x", "http://vite--" + label26 + ".preview.jam.example/",
		"https://jam.preview.jam.example/", "https://vite--" + label26 + ".preview.jam.example.evil/",
		"https://user@vite--" + label26 + ".preview.jam.example/", "javascript:alert(1)",
		"https://vite--" + label26 + ".preview.jam.example/#frag", "https://VITE--" + label26 + ".preview.jam.example/",
		"https://vite--" + label26 + ".preview.jam.example./", "https:vite--" + label26 + ".preview.jam.example/",
		"https://vite--" + label26 + ".preview.jam.example\\@evil.example/", "/app", "",
	} {
		if a.ValidReturn(rt) {
			t.Errorf("accepted return_to %q", rt)
		}
	}
	if !a.ValidReturn("https://vite--" + label26 + ".preview.jam.example/app?x=1") {
		t.Fatal("valid return refused")
	}
}

func TestCodeSingleUseAndHostBound(t *testing.T) {
	a := newTestPreviewAuth(t)
	rec := httptest.NewRecorder()
	a.Complete(rec, httptest.NewRequest("GET", "https://jam.preview.jam.example/__jam/auth/callback", nil), "id-alice", "", "https://"+hostA+"/app")
	loc, _ := url.Parse(rec.Header().Get("Location"))
	if loc.Host != hostA || loc.Path != "/__jam/session" {
		t.Fatalf("bounce to %v", loc)
	}
	if got := loc.Query().Get("return_to"); got != "/app" {
		t.Fatalf("bounce return_to = %q, want /app", got)
	}
	code := loc.Query().Get("code")
	h := a.PreviewHostRoutes(http.NotFoundHandler())
	trade := func(host string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "https://"+host+"/__jam/session?code="+url.QueryEscape(code)+"&return_to=/app", nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := trade(hostB); w.Code != http.StatusBadRequest {
		t.Fatalf("code used on another host: %d", w.Code)
	}
	w := trade(hostA)
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/app" {
		t.Fatalf("trade: %d %v", w.Code, w.Header())
	}
	ck := w.Result().Cookies()[0]
	if ck.Name != "jam_preview" || ck.Domain != "" || !ck.HttpOnly || !ck.Secure || ck.Path != "/" || ck.SameSite != http.SameSiteLaxMode {
		t.Fatalf("cookie %+v", ck)
	}
	if w := trade(hostA); w.Code != http.StatusBadRequest {
		t.Fatalf("replayed code: %d", w.Code)
	}
	// Neither the code nor the session id is ever logged.
	if logs := a.logs.String(); strings.Contains(logs, code) || strings.Contains(logs, ck.Value) {
		t.Errorf("secret in logs: %s", logs)
	}
}

func TestCodeExpiresAfter60s(t *testing.T) {
	a := newTestPreviewAuth(t)
	code := a.bounce(t, "id-alice", "", "https://"+hostA+"/", hostA)
	a.clock.Add(61 * time.Second)
	if w := a.trade(hostA, code, "/"); w.Code != http.StatusBadRequest {
		t.Fatalf("stale code traded: %d", w.Code)
	}
	fresh := a.bounce(t, "id-alice", "", "https://"+hostA+"/", hostA)
	a.clock.Add(59 * time.Second)
	if w := a.trade(hostA, fresh, "/"); w.Code != http.StatusFound {
		t.Fatalf("code within 60s refused: %d", w.Code)
	}
}

func TestSessionReturnToMustBeAPath(t *testing.T) {
	a := newTestPreviewAuth(t)
	code := a.bounce(t, "id-alice", "", "https://"+hostA+"/", hostA)
	for _, rt := range []string{"//evil.example/", "https://evil.example/", "/\\evil.example", "app", ""} {
		if w := a.trade(hostA, code, rt); w.Code != http.StatusBadRequest {
			t.Errorf("return_to %q: %d, want 400", rt, w.Code)
		}
	}
	// A refused return_to does not burn the code.
	if w := a.trade(hostA, code, "/ok?x=1"); w.Code != http.StatusFound || w.Header().Get("Location") != "/ok?x=1" {
		t.Fatalf("trade after refusals: %d %v", w.Code, w.Header())
	}
}

func TestSessionPrincipalExpiryAndHostBinding(t *testing.T) {
	a := newTestPreviewAuth(t)
	ck := a.session(t)
	p, name, err := principalOn(a.PreviewAuth, hostA, ck)
	if err != nil || p != (collab.Principal{UserID: aliceID}) || name != "alice" {
		t.Fatalf("Principal = %+v %q %v", p, name, err)
	}
	if _, _, err := principalOn(a.PreviewAuth, strings.ToUpper(hostA)+":443", ck); err != nil {
		t.Errorf("same host (case, port) refused: %v", err)
	}
	for _, h := range []string{hostB, loginHost, "evil.example"} {
		if _, _, err := principalOn(a.PreviewAuth, h, ck); !errors.Is(err, ErrNoCredentials) {
			t.Errorf("session used on %s: %v", h, err)
		}
	}
	if _, _, err := principalOn(a.PreviewAuth, hostA, &http.Cookie{Name: "jam_preview", Value: "forged"}); !errors.Is(err, ErrNoCredentials) {
		t.Errorf("unknown session: %v", err)
	}
	a.clock.Add(8*time.Hour - time.Second)
	if _, _, err := principalOn(a.PreviewAuth, hostA, ck); err != nil {
		t.Fatalf("session refused before 8h: %v", err)
	}
	a.clock.Add(2 * time.Second)
	if _, _, err := principalOn(a.PreviewAuth, hostA, ck); !errors.Is(err, ErrNoCredentials) {
		t.Fatalf("session after 8h: %v", err)
	}
}

func TestDuplicatePreviewCookiesAreNoCredentials(t *testing.T) {
	a := newTestPreviewAuth(t)
	ck := a.session(t)
	// A sibling app tossing a Domain=<domain> jam_preview cookie: never pick one.
	for _, order := range [][]*http.Cookie{
		{ck, {Name: "jam_preview", Value: "tossed"}},
		{{Name: "jam_preview", Value: "tossed"}, ck},
	} {
		if _, _, err := principalOn(a.PreviewAuth, hostA, order...); !errors.Is(err, ErrNoCredentials) {
			t.Errorf("two jam_preview cookies: %v", err)
		}
	}
}

func TestPrincipalBearerFallback(t *testing.T) {
	a := newTestPreviewAuth(t)
	r := httptest.NewRequest("GET", "https://"+hostA+"/", nil)
	r.Header.Set("Authorization", "Bearer good")
	if p, _, err := a.Principal(r); err != nil || p.Operator != "bearer-op" {
		t.Fatalf("bearer: %+v %v", p, err)
	}
	if _, _, err := principalOn(a.PreviewAuth, hostA); !errors.Is(err, ErrNoCredentials) {
		t.Fatalf("nothing: %v", err)
	}
}

func TestCompleteOperatorAndUnbound(t *testing.T) {
	a := newTestPreviewAuth(t)
	// The access token wins: an operator login is an operator principal.
	w := a.trade(hostA, a.bounce(t, "id-alice", "op-token", "https://"+hostA+"/", hostA), "/")
	if w.Code != http.StatusFound {
		t.Fatalf("trade = %d", w.Code)
	}
	if p, _, err := principalOn(a.PreviewAuth, hostA, w.Result().Cookies()[0]); err != nil || p != (collab.Principal{Operator: "op1"}) {
		t.Fatalf("operator Principal = %+v %v", p, err)
	}
	rec := httptest.NewRecorder()
	a.Complete(rec, httptest.NewRequest("GET", "https://"+loginHost+"/__jam/auth/callback", nil), "id-bob", "not-op", "https://"+hostA+"/")
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "not linked to a Jam user") {
		t.Fatalf("unbound = %d %q", rec.Code, rec.Body)
	}
	// A return_to that is not a preview URL is refused even here.
	rec = httptest.NewRecorder()
	a.Complete(rec, httptest.NewRequest("GET", "https://"+loginHost+"/__jam/auth/callback", nil), "id-alice", "", "/__jam/")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("Complete with bad return_to = %d", rec.Code)
	}
}

func TestLoginRedirect(t *testing.T) {
	a := newTestPreviewAuth(t)
	r := httptest.NewRequest("GET", "https://"+hostA+"/app?x=1", nil)
	r.Header.Set("Accept", "text/html,application/xhtml+xml")
	w := httptest.NewRecorder()
	a.LoginRedirect(w, r)
	want := "https://" + loginHost + "/__jam/start?return_to=" + url.QueryEscape("https://"+hostA+"/app?x=1")
	if w.Code != http.StatusFound || w.Header().Get("Location") != want {
		t.Fatalf("browser: %d %q, want %q", w.Code, w.Header().Get("Location"), want)
	}
	r = httptest.NewRequest("GET", "https://"+hostA+"/api", nil)
	r.Header.Set("Accept", "application/json")
	w = httptest.NewRecorder()
	a.LoginRedirect(w, r)
	if w.Code != http.StatusUnauthorized || !strings.HasPrefix(w.Header().Get("WWW-Authenticate"), "Bearer") || w.Body.Len() == 0 {
		t.Fatalf("non-browser: %d %v", w.Code, w.Header())
	}
}

func TestLoginHostHandler(t *testing.T) {
	a := newTestPreviewAuth(t)
	h := a.LoginHostHandler()
	get := func(host, target string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "https://"+host+target, nil))
		return w
	}
	rt := "https://" + hostA + "/app?x=1"
	w := get(loginHost, "/__jam/start?return_to="+url.QueryEscape(rt))
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/__jam/auth/login?return_to="+url.QueryEscape(rt) {
		t.Fatalf("start: %d %v", w.Code, w.Header())
	}
	if w := get(loginHost, "/__jam/start?return_to="+url.QueryEscape("https://evil.example/")); w.Code != http.StatusBadRequest {
		t.Errorf("start with foreign return_to: %d", w.Code)
	}
	if w := get(loginHost, "/__jam/auth/login?return_to=x"); w.Code != http.StatusTeapot || w.Header().Get("X-Login") != "/__jam/auth/login?return_to=x" {
		t.Errorf("auth routes not delegated: %d", w.Code)
	}
	for _, target := range []string{"/", "/app", "/__jam/session?code=x", "/__jam/other"} {
		if w := get(loginHost, target); w.Code != http.StatusNotFound {
			t.Errorf("%s on login host: %d", target, w.Code)
		}
	}
	// Login never starts on any other host.
	for _, host := range []string{hostA, "evil.example"} {
		for _, target := range []string{"/__jam/start?return_to=" + url.QueryEscape(rt), "/__jam/auth/login"} {
			if w := get(host, target); w.Code != http.StatusNotFound {
				t.Errorf("%s on %s: %d", target, host, w.Code)
			}
		}
	}
	rec := httptest.NewRecorder()
	a.Complete(rec, httptest.NewRequest("GET", "https://"+hostA+"/__jam/auth/callback", nil), "id-alice", "", rt)
	if rec.Code != http.StatusNotFound {
		t.Errorf("Complete on a preview host: %d", rec.Code)
	}
}

func TestPreviewHostRoutesLogoutAndPassThrough(t *testing.T) {
	a := newTestPreviewAuth(t)
	ck := a.session(t)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) })
	h := a.PreviewHostRoutes(next)
	serve := func(target string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "https://"+hostA+target, nil)
		for _, c := range cookies {
			r.AddCookie(c)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := serve("/app"); w.Code != http.StatusTeapot {
		t.Errorf("app path not passed through: %d", w.Code)
	}
	if w := serve("/__jam/other"); w.Code != http.StatusNotFound {
		t.Errorf("/__jam/other: %d", w.Code)
	}
	for _, target := range []string{"/__jam", "/app/../__jam/x"} {
		if w := serve(target); w.Code == http.StatusTeapot {
			t.Errorf("%s reached next", target)
		}
	}
	w := serve("/__jam/logout", ck)
	cleared := w.Result().Cookies()
	if len(cleared) != 1 || cleared[0].Name != "jam_preview" || cleared[0].MaxAge >= 0 || cleared[0].Domain != "" {
		t.Fatalf("logout cookies %+v", cleared)
	}
	if _, _, err := principalOn(a.PreviewAuth, hostA, ck); !errors.Is(err, ErrNoCredentials) {
		t.Fatalf("session survives logout: %v", err)
	}
}

func TestPruneDropsExpired(t *testing.T) {
	a := newTestPreviewAuth(t)
	a.session(t)
	a.bounce(t, "id-alice", "", "https://"+hostA+"/", hostA)
	a.clock.Add(9 * time.Hour)
	a.prune()
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.sessions) != 0 || len(a.codes) != 0 {
		t.Fatalf("after prune: %d sessions, %d codes", len(a.sessions), len(a.codes))
	}
}
