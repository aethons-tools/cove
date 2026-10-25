package collabhttp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	// started holds, per preview host, the login cookie of the browser that
	// last started a login there (startLogin); trade presents it.
	started map[string]*http.Cookie
}

// startLogin runs LoginRedirect on host as a browser would and returns the
// login cookie it set and the bind value it sent to the login host.
func (f *previewFixture) startLogin(t *testing.T, host string) (*http.Cookie, string) {
	t.Helper()
	r := httptest.NewRequest("GET", "https://"+host+"/", nil)
	r.Header.Set("Accept", "text/html")
	w := httptest.NewRecorder()
	f.LoginRedirect(w, r)
	var ck *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == PreviewLoginCookie {
			ck = c
		}
	}
	loc, err := url.Parse(w.Header().Get("Location"))
	if ck == nil || err != nil {
		t.Fatalf("LoginRedirect: cookie %v, Location %q", ck, w.Header().Get("Location"))
	}
	return ck, loc.Query().Get("bind")
}

// bound starts a login on returnTo's host in a fresh browser (remembered for
// trade) and returns the login return value carrying its bind.
func (f *previewFixture) bound(t *testing.T, returnTo string) string {
	t.Helper()
	u, err := url.Parse(returnTo)
	if err != nil || u.Host == "" {
		return returnTo // not a URL: passed through for refusal tests
	}
	ck, bind := f.startLogin(t, u.Host)
	if f.started == nil {
		f.started = map[string]*http.Cookie{}
	}
	f.started[u.Host] = ck
	return bind + "|" + returnTo
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
		Port:   "8443",
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
	f.Complete(rec, httptest.NewRequest("GET", "https://"+loginHost+"/__jam/auth/callback", nil), idTok, accessTok, f.bound(t, returnTo))
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
	if ck := f.started[host]; ck != nil {
		r.AddCookie(ck)
	}
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
		"https://vite--" + label26 + ".preview.jam.example:443/", "https://vite--" + label26 + ".preview.jam.example:9999/",
		"https://vite--" + label26 + ".preview.jam.example:/",
	} {
		if a.ValidReturn(rt) {
			t.Errorf("accepted return_to %q", rt)
		}
	}
	for _, ok := range []string{"https://vite--" + label26 + ".preview.jam.example/app?x=1", "https://vite--" + label26 + ".preview.jam.example:8443/app"} {
		if !a.ValidReturn(ok) {
			t.Errorf("valid return %q refused", ok)
		}
	}
}

func TestCodeSingleUseAndHostBound(t *testing.T) {
	a := newTestPreviewAuth(t)
	rec := httptest.NewRecorder()
	a.Complete(rec, httptest.NewRequest("GET", "https://jam.preview.jam.example/__jam/auth/callback", nil), "id-alice", "", a.bound(t, "https://"+hostA+"/app"))
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
		r.AddCookie(a.started[hostA]) // the browser that started the login
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
	if ck.Name != "__Host-jam_preview" || ck.Domain != "" || !ck.HttpOnly || !ck.Secure || ck.Path != "/" || ck.SameSite != http.SameSiteLaxMode {
		t.Fatalf("cookie %+v", ck)
	}
	if cc, rp := w.Header().Get("Cache-Control"), w.Header().Get("Referrer-Policy"); cc != "no-store" || rp != "no-referrer" {
		t.Errorf("trade headers Cache-Control=%q Referrer-Policy=%q", cc, rp)
	}
	w = trade(hostA)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("replayed code: %d", w.Code)
	}
	if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Errorf("refused trade headers %v", w.Header())
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
	if _, _, err := principalOn(a.PreviewAuth, hostA, &http.Cookie{Name: "__Host-jam_preview", Value: "forged"}); !errors.Is(err, ErrNoCredentials) {
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
		{ck, {Name: "__Host-jam_preview", Value: "tossed"}},
		{{Name: "__Host-jam_preview", Value: "tossed"}, ck},
	} {
		if _, _, err := principalOn(a.PreviewAuth, hostA, order...); !errors.Is(err, ErrNoCredentials) {
			t.Errorf("two jam_preview cookies: %v", err)
		}
	}
}

func TestPlainPreviewCookieIgnored(t *testing.T) {
	a := newTestPreviewAuth(t)
	ck := a.session(t)
	// A tossable (non-__Host-) cookie is never read as a session, even one
	// carrying a live session's value, nor counted as a duplicate.
	plain := &http.Cookie{Name: "jam_preview", Value: ck.Value}
	if _, _, err := principalOn(a.PreviewAuth, hostA, plain); !errors.Is(err, ErrNoCredentials) {
		t.Errorf("plain jam_preview read as a session: %v", err)
	}
	if p, _, err := principalOn(a.PreviewAuth, hostA, plain, ck); err != nil || p.UserID != aliceID {
		t.Errorf("plain cookie beside the session: %+v %v", p, err)
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
	a.Complete(rec, httptest.NewRequest("GET", "https://"+loginHost+"/__jam/auth/callback", nil), "id-bob", "not-op", a.bound(t, "https://"+hostA+"/"))
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
	cks := w.Result().Cookies()
	if len(cks) != 1 || cks[0].Name != "__Host-jam_preview_login" || cks[0].Domain != "" || cks[0].Path != "/" ||
		!cks[0].Secure || !cks[0].HttpOnly || cks[0].SameSite != http.SameSiteLaxMode || cks[0].MaxAge != 600 || len(cks[0].Value) < 32 {
		t.Fatalf("login cookie %+v", cks)
	}
	sum := sha256.Sum256([]byte(cks[0].Value))
	bind := hex.EncodeToString(sum[:])
	want := "https://" + loginHost + "/__jam/start?return_to=" + url.QueryEscape("https://"+hostA+"/app?x=1") + "&bind=" + bind
	if w.Code != http.StatusFound || w.Header().Get("Location") != want {
		t.Fatalf("browser: %d %q, want %q", w.Code, w.Header().Get("Location"), want)
	}
	// The same browser (another tab) keeps its login cookie.
	r = httptest.NewRequest("GET", "https://"+hostA+"/other", nil)
	r.Header.Set("Accept", "text/html")
	r.AddCookie(cks[0])
	w = httptest.NewRecorder()
	a.LoginRedirect(w, r)
	if again := w.Result().Cookies(); len(again) != 1 || again[0].Value != cks[0].Value || !strings.HasSuffix(w.Header().Get("Location"), "&bind="+bind) {
		t.Fatalf("second tab: %+v %q", again, w.Header().Get("Location"))
	}
	// A "//"-led path still completes login (the trade refuses "//" paths).
	r = httptest.NewRequest("GET", "https://"+hostA+"/", nil)
	r.URL.Path, r.URL.RawQuery = "//x/y", "q=1"
	r.Header.Set("Accept", "text/html")
	w = httptest.NewRecorder()
	a.LoginRedirect(w, r)
	want = "https://" + loginHost + "/__jam/start?return_to=" + url.QueryEscape("https://"+hostA+"/x/y?q=1") + "&bind="
	if !strings.HasPrefix(w.Header().Get("Location"), want) {
		t.Errorf("// path: %q, want %q", w.Header().Get("Location"), want)
	}
	r = httptest.NewRequest("GET", "https://"+hostA+"/api", nil)
	r.Header.Set("Accept", "application/json")
	w = httptest.NewRecorder()
	a.LoginRedirect(w, r)
	if w.Code != http.StatusUnauthorized || !strings.HasPrefix(w.Header().Get("WWW-Authenticate"), "Bearer") || w.Body.Len() == 0 {
		t.Fatalf("non-browser: %d %v", w.Code, w.Header())
	}
	if len(w.Result().Cookies()) != 0 {
		t.Fatalf("non-browser got cookies %+v", w.Result().Cookies())
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
	bind := strings.Repeat("ab", 32)
	w := get(loginHost, "/__jam/start?return_to="+url.QueryEscape(rt)+"&bind="+bind)
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/__jam/auth/login?return_to="+url.QueryEscape(bind+"|"+rt) {
		t.Fatalf("start: %d %v", w.Code, w.Header())
	}
	for _, b := range []string{"", "ab", strings.Repeat("AB", 32), strings.Repeat("zz", 32), bind + "00"} {
		if w := get(loginHost, "/__jam/start?return_to="+url.QueryEscape(rt)+"&bind="+b); w.Code != http.StatusBadRequest {
			t.Errorf("start with bind %q: %d", b, w.Code)
		}
	}
	if cks := w.Result().Cookies(); len(cks) != 0 {
		t.Errorf("start set cookies %+v", cks)
	}
	if w := get(loginHost, "/__jam/start?return_to="+url.QueryEscape("https://evil.example/")+"&bind="+bind); w.Code != http.StatusBadRequest {
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
	if len(cleared) != 1 || cleared[0].Name != "__Host-jam_preview" || cleared[0].MaxAge >= 0 || cleared[0].Domain != "" {
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

// otherLabelHost is a preview host of another session: its page shares the
// site (*.<domain>) with hostA but must never drive hostA as the viewer.
var otherLabelHost = "app--" + strings.Repeat("z", 26) + "." + testDomain

// The session cookie is honoured only where another preview's page cannot
// have sent it: same-origin (or user-typed) requests, top-level GET/HEAD
// navigations, and requests whose Origin is this session's own preview hosts;
// a WebSocket always needs such an Origin, and without Fetch Metadata an
// unsafe method needs one (or such a Referer).
func TestPreviewCookieCrossSiteUse(t *testing.T) {
	a := newTestPreviewAuth(t)
	c := a.session(t)
	type hdrs map[string]string
	fetch := func(site, mode, dest string) hdrs {
		return hdrs{"Sec-Fetch-Site": site, "Sec-Fetch-Mode": mode, "Sec-Fetch-Dest": dest}
	}
	with := func(h hdrs, k, v string) hdrs {
		out := hdrs{k: v}
		for kk, vv := range h {
			out[kk] = vv
		}
		return out
	}
	ws := hdrs{"Connection": "Upgrade", "Upgrade": "websocket"}
	for _, tc := range []struct {
		name   string
		method string
		h      hdrs
		ok     bool
	}{
		{"no metadata GET", "GET", nil, true},
		{"same-origin fetch POST", "POST", fetch("same-origin", "cors", "empty"), true},
		{"user-typed navigation", "GET", fetch("none", "navigate", "document"), true},
		{"top-level navigation from a sibling", "GET", fetch("same-site", "navigate", "document"), true},
		{"top-level HEAD from another site", "HEAD", fetch("cross-site", "navigate", "document"), true},
		// A sibling's iframe navigation ignores the cookie instead
		// (TestFrameCookieBeatsPreviewCookieInIframe).
		{"iframe non-navigation from a sibling", "GET", fetch("same-site", "no-cors", "iframe"), false},
		{"form POST from another label", "POST", with(fetch("same-site", "navigate", "document"), "Origin", "https://"+otherLabelHost), false},
		{"cross-label fetch", "GET", with(fetch("same-site", "cors", "empty"), "Origin", "https://"+otherLabelHost), false},
		{"cross-label fetch with a port", "POST", with(fetch("same-site", "cors", "empty"), "Origin", "https://"+otherLabelHost+":8443"), false},
		{"same-label fetch", "POST", with(fetch("same-site", "cors", "empty"), "Origin", "https://"+hostB), true},
		{"same-label fetch, port", "PUT", with(fetch("same-site", "cors", "empty"), "Origin", "https://"+hostB+":8443"), true},
		{"same-label fetch over http", "POST", with(fetch("same-site", "cors", "empty"), "Origin", "http://"+hostB), false},
		{"login host fetch", "GET", with(fetch("same-site", "cors", "empty"), "Origin", "https://"+loginHost), false},
		{"no-cors image from a sibling", "GET", fetch("same-site", "no-cors", "image"), false},
		{"websocket, no Origin", "GET", with(ws, "Sec-Fetch-Site", "same-origin"), false},
		{"websocket, other label", "GET", with(ws, "Origin", "https://"+otherLabelHost), false},
		{"websocket, null Origin", "GET", with(ws, "Origin", "null"), false},
		{"websocket, same host", "GET", with(with(ws, "Origin", "https://"+hostA), "Sec-Fetch-Site", "same-origin"), true},
		{"websocket, same label", "GET", with(ws, "Origin", "https://"+hostB), true},
		{"no metadata POST, no Origin", "POST", nil, false},
		{"no metadata POST, other label", "POST", hdrs{"Origin": "https://" + otherLabelHost}, false},
		{"no metadata POST, same host", "POST", hdrs{"Origin": "https://" + hostA}, true},
		{"no metadata DELETE, same-label Referer", "DELETE", hdrs{"Referer": "https://" + hostB + "/page"}, true},
		{"no metadata POST, other-label Referer", "POST", hdrs{"Referer": "https://" + otherLabelHost + "/"}, false},
		{"no metadata POST, null Origin", "POST", hdrs{"Origin": "null", "Referer": "https://" + hostA + "/"}, false},
	} {
		r := httptest.NewRequest(tc.method, "https://"+hostA+"/app", nil)
		for k, v := range tc.h {
			r.Header.Set(k, v)
		}
		r.AddCookie(c)
		_, _, err := a.Principal(r)
		switch {
		case tc.ok && err != nil:
			t.Errorf("%s: refused: %v", tc.name, err)
		case !tc.ok && !errors.Is(err, ErrCrossSite):
			t.Errorf("%s: err %v, want ErrCrossSite", tc.name, err)
		}
	}
	// A bearer is no ambient credential: the same contexts are fine for it.
	r := httptest.NewRequest("POST", "https://"+hostA+"/app", nil)
	for k, v := range with(fetch("same-site", "cors", "empty"), "Origin", "https://"+otherLabelHost) {
		r.Header.Set(k, v)
	}
	r.Header.Set("Authorization", "Bearer good")
	if p, _, err := a.Principal(r); err != nil || p.Operator != "bearer-op" {
		t.Fatalf("cross-site bearer: %+v %v", p, err)
	}
}

// A login code is bound to the browser that started the login: traded in
// another browser — an attacker's code in a victim's, to sign the victim in
// as the attacker — it is refused (and not burned), and the trade clears the
// login cookie.
func TestLoginCodeBoundToStartingBrowser(t *testing.T) {
	a := newTestPreviewAuth(t)
	code := a.bounce(t, "id-alice", "", "https://"+hostA+"/", hostA) // the attacker's login
	attacker := a.started[hostA]
	victim, _ := a.startLogin(t, hostA)
	h := a.PreviewHostRoutes(http.NotFoundHandler())
	trade := func(cks ...*http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "https://"+hostA+"/__jam/session?code="+url.QueryEscape(code)+"&return_to=/", nil)
		for _, c := range cks {
			r.AddCookie(c)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	for name, cks := range map[string][]*http.Cookie{
		"no login cookie":       nil,
		"another browser's":     {victim},
		"empty":                 {{Name: PreviewLoginCookie, Value: ""}},
		"unprefixed attacker's": {{Name: "jam_preview_login", Value: attacker.Value}},
	} {
		if w := trade(cks...); w.Code != http.StatusBadRequest || len(w.Result().Cookies()) != 0 {
			t.Errorf("%s: %d %+v", name, w.Code, w.Result().Cookies())
		}
	}
	w := trade(attacker)
	if w.Code != http.StatusFound {
		t.Fatalf("starting browser: %d %s", w.Code, w.Body)
	}
	var session, cleared bool
	for _, c := range w.Result().Cookies() {
		switch {
		case c.Name == PreviewCookie && c.MaxAge > 0:
			session = true
		case c.Name == PreviewLoginCookie && c.MaxAge < 0 && c.Path == "/" && c.Secure && c.Domain == "":
			cleared = true
		}
	}
	if !session || !cleared {
		t.Fatalf("trade cookies %+v", w.Result().Cookies())
	}
	if logs := a.logs.String(); strings.Contains(logs, attacker.Value) || strings.Contains(logs, victim.Value) {
		t.Errorf("login cookie in logs: %s", logs)
	}
}

// The login return value carries the bind before the preview URL.
func TestValidLoginReturn(t *testing.T) {
	a := newTestPreviewAuth(t)
	bind := strings.Repeat("0f", 32)
	if !a.ValidLoginReturn(bind + "|https://" + hostA + "/x") {
		t.Fatal("bound preview URL refused")
	}
	for _, v := range []string{"https://" + hostA + "/x", bind + "|https://evil.example/", "|https://" + hostA + "/", "xyz|https://" + hostA + "/", bind + "|"} {
		if a.ValidLoginReturn(v) {
			t.Errorf("%q accepted", v)
		}
	}
}

// framed returns a fixture whose PreviewAuth honours frame tickets.
func framed(t *testing.T) (*previewFixture, *FrameTickets) {
	t.Helper()
	a := newTestPreviewAuth(t)
	ft := NewFrameTickets(a.clock.Now)
	a.o.Frames = ft
	return a, ft
}

// exchange presents ticket on host's /__jam/frame as the given Sec-Fetch-Dest.
func (f *previewFixture) exchange(host, ticket, dest string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", "https://"+host+"/__jam/frame?t="+url.QueryEscape(ticket), nil)
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	r.Header.Set("Sec-Fetch-Mode", "navigate")
	if dest != "" {
		r.Header.Set("Sec-Fetch-Dest", dest)
	}
	w := httptest.NewRecorder()
	f.PreviewHostRoutes(http.NotFoundHandler()).ServeHTTP(w, r)
	return w
}

// frameSession mints a ticket for alice on hostA and exchanges it, returning
// the frame cookie.
func (f *previewFixture) frameSession(t *testing.T, ft *FrameTickets) *http.Cookie {
	t.Helper()
	tk, err := ft.Mint(collab.Principal{UserID: aliceID}, "alice", hostA, collab.Preview)
	if err != nil {
		t.Fatal(err)
	}
	w := f.exchange(hostA, tk, "iframe")
	if w.Code != http.StatusOK {
		t.Fatalf("exchange = %d %s", w.Code, w.Body)
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == FrameCookie {
			return c
		}
	}
	t.Fatalf("no frame cookie in %v", w.Header())
	return nil
}

func TestFrameExchange(t *testing.T) {
	a, ft := framed(t)
	tk, err := ft.Mint(collab.Principal{UserID: aliceID}, "alice", hostA, collab.Preview)
	if err != nil {
		t.Fatal(err)
	}
	// Outside an iframe the link is refused (and not spent).
	w := a.exchange(hostA, tk, "document")
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "frame links open only inside the Jam pane") ||
		len(w.Header().Values("Set-Cookie")) != 0 {
		t.Fatalf("document exchange = %d %q %v", w.Code, w.Body, w.Header())
	}
	if w := a.exchange(hostA, tk, ""); w.Code != http.StatusBadRequest {
		t.Fatalf("no Sec-Fetch-Dest = %d", w.Code)
	}
	// A 200 landing page, not a redirect: a redirected navigation keeps the
	// UI as its initiator (cross-site), while one the preview document
	// starts itself is same-origin.
	w = a.exchange(hostA, tk, "iframe")
	if w.Code != http.StatusOK || w.Header().Get("Location") != "" {
		t.Fatalf("exchange = %d %q", w.Code, w.Header().Get("Location"))
	}
	if body := w.Body.String(); !strings.Contains(body, `<script>location.replace("/")</script>`) ||
		!strings.Contains(body, `<noscript>`) || !strings.Contains(body, `href="/"`) ||
		!strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("landing page %q %q", w.Header().Get("Content-Type"), body)
	}
	if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("exchange headers %v", w.Header())
	}
	sc := w.Header().Values("Set-Cookie")
	if len(sc) != 1 {
		t.Fatalf("Set-Cookie %q", sc)
	}
	name, rest, _ := strings.Cut(sc[0], "=")
	val, attrs, _ := strings.Cut(rest, ";")
	if name != "__Host-jam_frame" || len(val) < 43 {
		t.Fatalf("Set-Cookie %q", sc[0])
	}
	got := map[string]bool{}
	for a := range strings.SplitSeq(attrs, ";") {
		got[strings.TrimSpace(a)] = true
	}
	want := map[string]bool{"Path=/": true, "Max-Age=28800": true, "Secure": true, "HttpOnly": true, "SameSite=None": true, "Partitioned": true}
	if len(got) != len(want) {
		t.Fatalf("Set-Cookie attributes %q, want %v", sc[0], want)
	}
	for k := range want {
		if !got[k] {
			t.Fatalf("Set-Cookie %q lacks %s", sc[0], k)
		}
	}
	logs := a.logs.String()
	if !strings.Contains(logs, "preview frame opened") || !strings.Contains(logs, string(aliceID)) ||
		!strings.Contains(logs, hostA) || !strings.Contains(logs, "right=preview") {
		t.Fatalf("audit log %q", logs)
	}
	if strings.Contains(logs, tk) || strings.Contains(logs, val) {
		t.Fatalf("log leaks ticket or cookie: %q", logs)
	}
	a.mu.Lock()
	var e previewEntry
	for _, v := range a.frameSessions {
		e = v
	}
	n := len(a.frameSessions)
	a.mu.Unlock()
	if n != 1 || e.host != hostA || e.right != collab.Preview || e.principal.UserID != aliceID || !e.expires.Equal(a.clock.Now().Add(8*time.Hour)) {
		t.Fatalf("frame session %d %+v", n, e)
	}
	// Reused: refused.
	if w := a.exchange(hostA, tk, "iframe"); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "frame link expired — reopen the tab") {
		t.Fatalf("reused ticket = %d %q", w.Code, w.Body)
	}
	// Minted for another host: refused, and spent.
	tkB, _ := ft.Mint(collab.Principal{UserID: aliceID}, "alice", hostB, collab.Preview)
	if w := a.exchange(hostA, tkB, "iframe"); w.Code != http.StatusBadRequest {
		t.Fatalf("other host's ticket = %d", w.Code)
	}
	if w := a.exchange(hostB, tkB, "iframe"); w.Code != http.StatusBadRequest {
		t.Fatalf("probed ticket = %d", w.Code)
	}
	// Expired.
	tk2, _ := ft.Mint(collab.Principal{UserID: aliceID}, "alice", hostA, collab.Preview)
	a.clock.Add(61 * time.Second)
	if w := a.exchange(hostA, tk2, "iframe"); w.Code != http.StatusBadRequest {
		t.Fatalf("expired ticket = %d", w.Code)
	}
	if w := a.exchange(hostA, "garbage", "iframe"); w.Code != http.StatusBadRequest {
		t.Fatalf("garbage ticket = %d", w.Code)
	}
}

// Without a ticket store (framing off), /__jam/frame is like any other
// reserved path: 404.
func TestFrameExchangeOff(t *testing.T) {
	a := newTestPreviewAuth(t)
	if w := a.exchange(hostA, "x", "iframe"); w.Code != http.StatusNotFound {
		t.Fatalf("frame without Frames = %d", w.Code)
	}
}

// The frame cookie is honoured only for a same-origin request or a WebSocket
// whose Origin is exactly this host — never for a cross-site iframe
// navigation (each pane load brings a fresh ticket; anything else arriving
// that way, such as an app's redirect into another studio, is not signed in)
// and never with missing Fetch Metadata. Ignored on a top-level or the UI's
// iframe navigation (no credentials: log in), refused elsewhere.
func TestFrameCookieUse(t *testing.T) {
	a, ft := framed(t)
	c := a.frameSession(t, ft)
	type hdrs map[string]string
	fetch := func(site, mode, dest string) hdrs {
		return hdrs{"Sec-Fetch-Site": site, "Sec-Fetch-Mode": mode, "Sec-Fetch-Dest": dest}
	}
	ws := func(origin, site string) hdrs {
		h := hdrs{"Connection": "Upgrade", "Upgrade": "websocket"}
		if origin != "" {
			h["Origin"] = origin
		}
		if site != "" {
			h["Sec-Fetch-Site"] = site
		}
		return h
	}
	for _, tc := range []struct {
		name   string
		method string
		h      hdrs
		want   error // nil: honoured
	}{
		{"iframe navigation, same-origin", "GET", fetch("same-origin", "navigate", "iframe"), nil},
		{"same-origin fetch POST", "POST", fetch("same-origin", "cors", "empty"), nil},
		{"same-origin subresource", "GET", fetch("same-origin", "no-cors", "script"), nil},
		{"websocket, same host", "GET", ws("https://"+hostA, ""), nil},
		{"websocket, same host, same-origin", "GET", ws("https://"+hostA, "same-origin"), nil},
		{"iframe navigation from the UI (or a redirect through it)", "GET", fetch("cross-site", "navigate", "iframe"), ErrNoCredentials},
		{"iframe POST navigation", "POST", fetch("cross-site", "navigate", "iframe"), ErrNoCredentials},
		{"top-level navigation", "GET", fetch("cross-site", "navigate", "document"), ErrNoCredentials},
		{"same-site top-level navigation", "GET", fetch("same-site", "navigate", "document"), ErrNoCredentials},
		{"user-typed navigation", "GET", fetch("none", "navigate", "document"), ErrNoCredentials},
		{"cross-site fetch", "GET", fetch("cross-site", "cors", "empty"), ErrCrossSite},
		{"same-site fetch from a sibling", "POST", fetch("same-site", "cors", "empty"), ErrCrossSite},
		{"iframe no-cors", "GET", fetch("cross-site", "no-cors", "iframe"), ErrCrossSite},
		{"same-site iframe navigation (the UI on a shared registrable domain)", "GET", fetch("same-site", "navigate", "iframe"), ErrNoCredentials},
		{"user-typed iframe navigation", "GET", fetch("none", "navigate", "iframe"), ErrCrossSite},
		{"iframe navigation, no Sec-Fetch-Site", "GET", hdrs{"Sec-Fetch-Mode": "navigate", "Sec-Fetch-Dest": "iframe"}, ErrCrossSite},
		{"websocket, same session's other host", "GET", ws("https://"+hostB, "same-site"), ErrCrossSite},
		{"websocket, other host claiming same-origin", "GET", ws("https://"+hostB, "same-origin"), ErrCrossSite},
		{"websocket, other label", "GET", ws("https://"+otherLabelHost, ""), ErrCrossSite},
		{"websocket, http Origin", "GET", ws("http://"+hostA, ""), ErrCrossSite},
		{"websocket, Origin with path", "GET", ws("https://"+hostA+"/", ""), ErrCrossSite},
		{"websocket, no Origin", "GET", ws("", "same-origin"), ErrCrossSite},
		{"websocket, null Origin", "GET", ws("null", ""), ErrCrossSite},
		{"no metadata GET", "GET", nil, ErrCrossSite},
		{"no metadata POST, same Origin", "POST", hdrs{"Origin": "https://" + hostA}, ErrCrossSite},
	} {
		r := httptest.NewRequest(tc.method, "https://"+hostA+"/app", nil)
		for k, v := range tc.h {
			r.Header.Set(k, v)
		}
		r.AddCookie(c)
		p, name, isFramed, err := a.PrincipalFrame(r)
		switch {
		case tc.want == nil && (err != nil || p != (collab.Principal{UserID: aliceID}) || name != "alice" || !isFramed):
			t.Errorf("%s: %+v %q %v %v", tc.name, p, name, isFramed, err)
		case tc.want != nil && (!errors.Is(err, tc.want) || isFramed):
			t.Errorf("%s: err %v framed %v, want %v", tc.name, err, isFramed, tc.want)
		}
		if _, _, err2 := a.Principal(r); !errors.Is(err2, err) && err2 != err {
			t.Errorf("%s: Principal %v, PrincipalFrame %v", tc.name, err2, err)
		}
	}
	// An ignored frame cookie does not hide a bearer.
	r := httptest.NewRequest("GET", "https://"+hostA+"/app", nil)
	for k, v := range fetch("cross-site", "navigate", "document") {
		r.Header.Set(k, v)
	}
	r.Header.Set("Authorization", "Bearer good")
	r.AddCookie(c)
	if p, _, f, err := a.PrincipalFrame(r); err != nil || f || p.Operator != "bearer-op" {
		t.Errorf("bearer beside an ignored frame cookie: %+v %v %v", p, f, err)
	}
}

func frameReq(host string, cookies ...*http.Cookie) *http.Request {
	r := httptest.NewRequest("GET", "https://"+host+"/app", nil)
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	r.Header.Set("Sec-Fetch-Mode", "cors")
	r.Header.Set("Sec-Fetch-Dest", "empty")
	for _, c := range cookies {
		r.AddCookie(c)
	}
	return r
}

func TestFrameSessionBindingAndExpiry(t *testing.T) {
	a, ft := framed(t)
	c := a.frameSession(t, ft)
	if _, _, f, err := a.PrincipalFrame(frameReq(strings.ToUpper(hostA)+":443", c)); err != nil || !f {
		t.Fatalf("same host (case, port): %v %v", f, err)
	}
	for _, h := range []string{hostB, otherLabelHost, loginHost, "evil.example"} {
		if _, _, _, err := a.PrincipalFrame(frameReq(h, c)); !errors.Is(err, ErrNoCredentials) {
			t.Errorf("frame session used on %s: %v", h, err)
		}
	}
	if _, _, _, err := a.PrincipalFrame(frameReq(hostA, &http.Cookie{Name: FrameCookie, Value: "forged"})); !errors.Is(err, ErrNoCredentials) {
		t.Errorf("forged: %v", err)
	}
	if _, _, _, err := a.PrincipalFrame(frameReq(hostA, &http.Cookie{Name: "jam_frame", Value: c.Value})); !errors.Is(err, ErrNoCredentials) {
		t.Errorf("unprefixed jam_frame honoured: %v", err)
	}
	if _, _, _, err := a.PrincipalFrame(frameReq(hostA, c, &http.Cookie{Name: FrameCookie, Value: "other"})); !errors.Is(err, ErrNoCredentials) {
		t.Errorf("two frame cookies: %v", err)
	}
	a.clock.Add(8*time.Hour - time.Second)
	if _, _, _, err := a.PrincipalFrame(frameReq(hostA, c)); err != nil {
		t.Fatalf("refused before 8h: %v", err)
	}
	a.clock.Add(2 * time.Second)
	if _, _, _, err := a.PrincipalFrame(frameReq(hostA, c)); !errors.Is(err, ErrNoCredentials) {
		t.Fatalf("after 8h: %v", err)
	}
}

// Outside an iframe the top-level preview cookie keeps priority: with both
// present its rules (and only its rules) decide, and the request is not
// frame-authenticated. Inside one see TestFrameCookieBeatsPreviewCookieInIframe.
func TestPreviewCookieBeatsFrameCookie(t *testing.T) {
	a, ft := framed(t)
	fc := a.frameSession(t, ft)
	pc := a.session(t)
	r := frameReq(hostA, pc, fc)
	if p, _, f, err := a.PrincipalFrame(r); err != nil || f || p.UserID != aliceID {
		t.Fatalf("both, same-origin: %+v %v %v", p, f, err)
	}
	// A cross-site fetch: the preview cookie's rules refuse it, and the
	// frame cookie is never consulted.
	r = httptest.NewRequest("GET", "https://"+hostA+"/app", nil)
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	r.Header.Set("Sec-Fetch-Mode", "cors")
	r.Header.Set("Sec-Fetch-Dest", "empty")
	r.AddCookie(pc)
	r.AddCookie(fc)
	if _, _, _, err := a.PrincipalFrame(r); !errors.Is(err, ErrCrossSite) {
		t.Fatalf("both, cross-site fetch: %v", err)
	}
	// A bearer and the plain preview cookie are not framed.
	br := httptest.NewRequest("GET", "https://"+hostA+"/app", nil)
	br.Header.Set("Authorization", "Bearer good")
	if _, _, f, err := a.PrincipalFrame(br); err != nil || f {
		t.Fatalf("bearer: %v %v", f, err)
	}
}

func TestLoginRedirectInFrame(t *testing.T) {
	a := newTestPreviewAuth(t)
	r := httptest.NewRequest("GET", "https://"+hostA+"/app?x=1&y=<b>", nil)
	r.Header.Set("Accept", "text/html")
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	r.Header.Set("Sec-Fetch-Dest", "iframe")
	r.Header.Set("Sec-Fetch-Mode", "navigate")
	a.o.FrameAncestors = []string{"https://jam.example", "https://other.example"}
	w := httptest.NewRecorder()
	a.LoginRedirect(w, r)
	body := w.Body.String()
	if w.Code != http.StatusUnauthorized || w.Header().Get("Location") != "" || len(w.Result().Cookies()) != 0 {
		t.Fatalf("framed login = %d %v", w.Code, w.Header())
	}
	if !strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("Content-Type %q", w.Header().Get("Content-Type"))
	}
	for _, s := range []string{
		"Reopen this preview from the Jam pane.",
		`<a href="https://` + hostA + `/app?x=1&amp;y=&lt;b&gt;" target="_top">Open in new tab</a>`,
		`parent.postMessage({jam:"frame-error",status:"signin"}, "https://jam.example")`,
	} {
		if !strings.Contains(body, s) {
			t.Errorf("body lacks %q:\n%s", s, body)
		}
	}
	if strings.Contains(body, "<b>") {
		t.Errorf("unescaped request URI in body:\n%s", body)
	}
	// Same-origin in the frame with no frame cookie: the browser refused the
	// partitioned cookie after the exchange (Safari).
	so := httptest.NewRequest("GET", "https://"+hostA+"/", nil)
	so.Header.Set("Accept", "text/html")
	so.Header.Set("Sec-Fetch-Site", "same-origin")
	so.Header.Set("Sec-Fetch-Dest", "iframe")
	so.Header.Set("Sec-Fetch-Mode", "navigate")
	w = httptest.NewRecorder()
	a.LoginRedirect(w, so)
	if body := w.Body.String(); w.Code != http.StatusUnauthorized ||
		!strings.Contains(body, `status:"cookies"`) || !strings.Contains(body, "keep the pane") {
		t.Errorf("same-origin, no cookie: %d %s", w.Code, body)
	}
	// No UI origins: no frame may show it, so no message is posted.
	a.o.FrameAncestors = nil
	w = httptest.NewRecorder()
	a.LoginRedirect(w, r)
	if w.Code != http.StatusUnauthorized || strings.Contains(w.Body.String(), "postMessage") {
		t.Errorf("no origins: %d %s", w.Code, w.Body)
	}
}

func TestPruneDropsExpiredFrameSessions(t *testing.T) {
	a, ft := framed(t)
	a.frameSession(t, ft)
	_, _ = ft.Mint(collab.Principal{UserID: aliceID}, "alice", hostA, collab.Preview)
	a.clock.Add(9 * time.Hour)
	a.prune()
	a.mu.Lock()
	n := len(a.frameSessions)
	a.mu.Unlock()
	ft.mu.Lock()
	nt := len(ft.m)
	ft.mu.Unlock()
	if n != 0 || nt != 0 {
		t.Fatalf("after prune: %d frame sessions, %d tickets", n, nt)
	}
}

// An app can neither set nor read the frame cookie through the proxy.
func TestFrameCookieIsJams(t *testing.T) {
	h := http.Header{}
	for _, c := range []string{
		"__Host-jam_frame=fixed; Path=/; Secure; SameSite=None; Partitioned",
		"__HOST-JAM_FRAME=fixed; Path=/; Secure",
		"jam_frame=fixed",
		"app=keep",
	} {
		h.Add("Set-Cookie", c)
	}
	sanitizeResponse(h)
	if got := h.Values("Set-Cookie"); len(got) != 1 || got[0] != "app=keep" {
		t.Fatalf("Set-Cookie after sanitize %q", got)
	}
	rh := http.Header{"Cookie": {"__Host-jam_frame=s; app=1", "jam_frame=x"}}
	stripJamCookies(rh)
	if got := rh.Get("Cookie"); got != "app=1" {
		t.Fatalf("Cookie after strip %q", got)
	}
}

// The exchange runs only for an iframe navigation another site's page (the
// Jam UI) started: cross-site or same-site, never same-origin, user-typed or
// without Fetch Metadata, and in navigate mode.
func TestFrameExchangeNeedsFrameNavigation(t *testing.T) {
	a, ft := framed(t)
	for _, tc := range []struct{ name, site, mode string }{
		{"same-origin", "same-origin", "navigate"},
		{"user-typed", "none", "navigate"},
		{"no Sec-Fetch-Site", "", "navigate"},
		{"no-cors", "cross-site", "no-cors"},
		{"no Sec-Fetch-Mode", "cross-site", ""},
	} {
		tk, _ := ft.Mint(collab.Principal{UserID: aliceID}, "alice", hostA, collab.Preview)
		r := httptest.NewRequest("GET", "https://"+hostA+"/__jam/frame?t="+url.QueryEscape(tk), nil)
		r.Header.Set("Sec-Fetch-Dest", "iframe")
		if tc.site != "" {
			r.Header.Set("Sec-Fetch-Site", tc.site)
		}
		if tc.mode != "" {
			r.Header.Set("Sec-Fetch-Mode", tc.mode)
		}
		w := httptest.NewRecorder()
		a.PreviewHostRoutes(http.NotFoundHandler()).ServeHTTP(w, r)
		if w.Code != http.StatusBadRequest || len(w.Header().Values("Set-Cookie")) != 0 {
			t.Errorf("%s: %d %v", tc.name, w.Code, w.Header())
		}
	}
}

// A Jam UI sharing the previews' registrable domain frames them same-site:
// the exchange accepts that iframe navigation only with a Referer whose
// origin is a UI origin — else any page on the site could plant a ticket's
// cookie in the UI's partition. Cross-site needs no Referer.
func TestFrameExchangeSameSite(t *testing.T) {
	a, ft := framed(t)
	a.o.FrameAncestors = []string{"https://other.example", "https://jam.example"}
	req := func(tk, site, ref string) *http.Request {
		r := httptest.NewRequest("GET", "https://"+hostA+"/__jam/frame?t="+url.QueryEscape(tk), nil)
		r.Header.Set("Sec-Fetch-Site", site)
		r.Header.Set("Sec-Fetch-Mode", "navigate")
		r.Header.Set("Sec-Fetch-Dest", "iframe")
		if ref != "" {
			r.Header.Set("Referer", ref)
		}
		return r
	}
	for _, tc := range []struct{ name, site, ref string }{
		{"same-site, no Referer", "same-site", ""},
		{"same-site, sibling preview Referer", "same-site", "https://" + hostB + "/"},
		{"same-site, same host Referer", "same-site", "https://" + hostA + "/"},
		{"same-site, login host Referer", "same-site", "https://" + loginHost + "/"},
		{"same-site, other subdomain Referer", "same-site", "https://blog.jam.example/"},
		{"same-site, UI host over http", "same-site", "http://jam.example/"},
		{"same-site, UI host other port", "same-site", "https://jam.example:8443/"},
		{"same-site, UI host as userinfo", "same-site", "https://jam.example@evil.jam.example/"},
		{"same-site, UI host as suffix", "same-site", "https://jam.example.evil.example/"},
		{"same-site, garbage Referer", "same-site", "::"},
	} {
		tk, _ := ft.Mint(collab.Principal{UserID: aliceID}, "alice", hostA, collab.Preview)
		w := httptest.NewRecorder()
		a.PreviewHostRoutes(http.NotFoundHandler()).ServeHTTP(w, req(tk, tc.site, tc.ref))
		if w.Code != http.StatusBadRequest || len(w.Header().Values("Set-Cookie")) != 0 {
			t.Errorf("%s: %d %v", tc.name, w.Code, w.Header())
		}
	}
	// Cross-site needs no Referer (the pane's own iframe, another site).
	tk, _ := ft.Mint(collab.Principal{UserID: aliceID}, "alice", hostA, collab.Preview)
	w := httptest.NewRecorder()
	a.PreviewHostRoutes(http.NotFoundHandler()).ServeHTTP(w, req(tk, "cross-site", ""))
	if w.Code != http.StatusOK {
		t.Fatalf("cross-site, no Referer = %d %q", w.Code, w.Body)
	}
	for _, ref := range []string{"https://jam.example/", "https://jam.example", "https://JAM.example/x?y"} {
		tk, _ := ft.Mint(collab.Principal{UserID: aliceID}, "alice", hostA, collab.Preview)
		w := httptest.NewRecorder()
		a.PreviewHostRoutes(http.NotFoundHandler()).ServeHTTP(w, req(tk, "same-site", ref))
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `location.replace("/")`) {
			t.Fatalf("same-site, Referer %q = %d %q", ref, w.Code, w.Body)
		}
		var fc *http.Cookie
		for _, c := range w.Result().Cookies() {
			if c.Name == FrameCookie {
				fc = c
			}
		}
		if fc == nil || fc.Value == "" {
			t.Fatalf("no frame cookie in %v", w.Header())
		}
		// The landing page's same-origin navigation is signed in.
		r := httptest.NewRequest("GET", "https://"+hostA+"/", nil)
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		r.Header.Set("Sec-Fetch-Mode", "navigate")
		r.Header.Set("Sec-Fetch-Dest", "iframe")
		r.AddCookie(fc)
		if p, _, isFramed, err := a.PrincipalFrame(r); err != nil || p.UserID != aliceID || !isFramed {
			t.Fatalf("after same-site exchange: %+v %v %v", p, isFramed, err)
		}
		// Single use: the spent ticket is refused.
		w = httptest.NewRecorder()
		a.PreviewHostRoutes(http.NotFoundHandler()).ServeHTTP(w, req(tk, "same-site", ref))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("replayed ticket = %d", w.Code)
		}
	}
}

// With both cookies (a deployment whose UI shares the previews' registrable
// domain sends the Lax preview cookie on the pane's navigations too): inside
// an iframe the frame cookie decides, and on a frame navigation the preview
// cookie is ignored, so a ticketless reload signs in again rather than 403.
func TestFrameCookieBeatsPreviewCookieInIframe(t *testing.T) {
	a, ft := framed(t)
	pc := a.session(t)
	fc := a.frameSession(t, ft)
	get := func(site, mode, dest string, cs ...*http.Cookie) (collab.Principal, bool, error) {
		r := httptest.NewRequest("GET", "https://"+hostA+"/app", nil)
		r.Header.Set("Sec-Fetch-Site", site)
		r.Header.Set("Sec-Fetch-Mode", mode)
		r.Header.Set("Sec-Fetch-Dest", dest)
		for _, c := range cs {
			r.AddCookie(c)
		}
		p, _, isFramed, err := a.PrincipalFrame(r)
		return p, isFramed, err
	}
	if p, isFramed, err := get("same-origin", "navigate", "iframe", pc, fc); err != nil || p.UserID != aliceID || !isFramed {
		t.Errorf("same-origin iframe, both: %+v %v %v", p, isFramed, err)
	}
	if p, isFramed, err := get("same-origin", "no-cors", "iframe", pc, fc); err != nil || p.UserID != aliceID || !isFramed {
		t.Errorf("same-origin iframe subresource, both: %+v %v %v", p, isFramed, err)
	}
	// Top level and subresources keep the preview cookie's precedence.
	if p, isFramed, err := get("same-origin", "navigate", "document", pc, fc); err != nil || p.UserID != aliceID || isFramed {
		t.Errorf("top-level, both: %+v %v %v", p, isFramed, err)
	}
	if p, isFramed, err := get("same-origin", "cors", "empty", pc, fc); err != nil || p.UserID != aliceID || isFramed {
		t.Errorf("fetch, both: %+v %v %v", p, isFramed, err)
	}
	for _, site := range []string{"same-site", "cross-site"} {
		for _, cs := range [][]*http.Cookie{{pc}, {pc, fc}} {
			if _, isFramed, err := get(site, "navigate", "iframe", cs...); !errors.Is(err, ErrNoCredentials) || isFramed {
				t.Errorf("%s iframe navigation, %d cookies: %v %v", site, len(cs), isFramed, err)
			}
		}
	}
	// A preview cookie in another site's iframe that is not a navigation
	// is still refused.
	if _, _, err := get("same-site", "no-cors", "iframe", pc); !errors.Is(err, ErrCrossSite) {
		t.Errorf("same-site iframe no-cors, preview cookie: %v", err)
	}
}

// Logging out of a preview also ends the viewer's pane (frame) sessions on
// that host, and only theirs.
func TestLogoutEndsFrameSessions(t *testing.T) {
	a, ft := framed(t)
	fc := a.frameSession(t, ft) // alice on hostA
	// Bob's frame session on hostA and alice's on hostB survive.
	bobTk, _ := ft.Mint(collab.Principal{UserID: "usr_bob"}, "bob", hostA, collab.Preview)
	var bobCk *http.Cookie
	for _, c := range a.exchange(hostA, bobTk, "iframe").Result().Cookies() {
		bobCk = c
	}
	bTk, _ := ft.Mint(collab.Principal{UserID: aliceID}, "alice", hostB, collab.Preview)
	var bCk *http.Cookie
	for _, c := range a.exchange(hostB, bTk, "iframe").Result().Cookies() {
		bCk = c
	}
	pc := a.session(t)
	r := httptest.NewRequest("GET", "https://"+hostA+"/__jam/logout", nil)
	r.AddCookie(pc)
	a.PreviewHostRoutes(http.NotFoundHandler()).ServeHTTP(httptest.NewRecorder(), r)
	if _, _, _, err := a.PrincipalFrame(frameReq(hostA, fc)); !errors.Is(err, ErrNoCredentials) {
		t.Fatalf("frame session survives logout: %v", err)
	}
	if p, _, _, err := a.PrincipalFrame(frameReq(hostA, bobCk)); err != nil || p.UserID != "usr_bob" {
		t.Fatalf("bob's frame session: %+v %v", p, err)
	}
	if _, _, _, err := a.PrincipalFrame(frameReq(hostB, bCk)); err != nil {
		t.Fatalf("alice's hostB frame session: %v", err)
	}
	// A sibling studio (same-site) cannot log the pane out: the frame
	// cookie is honoured only where frameCookieUseAllowed says.
	r = httptest.NewRequest("GET", "https://"+hostA+"/__jam/logout", nil)
	r.AddCookie(bobCk)
	r.Header.Set("Sec-Fetch-Site", "same-site")
	r.Header.Set("Sec-Fetch-Mode", "navigate")
	r.Header.Set("Sec-Fetch-Dest", "iframe")
	a.PreviewHostRoutes(http.NotFoundHandler()).ServeHTTP(httptest.NewRecorder(), r)
	if _, _, _, err := a.PrincipalFrame(frameReq(hostA, bobCk)); err != nil {
		t.Fatalf("same-site logout ended the frame session: %v", err)
	}
	// Logging out from inside the pane (frame cookie only) ends that session.
	r = httptest.NewRequest("GET", "https://"+hostA+"/__jam/logout", nil)
	r.AddCookie(bobCk)
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	a.PreviewHostRoutes(http.NotFoundHandler()).ServeHTTP(httptest.NewRecorder(), r)
	if _, _, _, err := a.PrincipalFrame(frameReq(hostA, bobCk)); !errors.Is(err, ErrNoCredentials) {
		t.Fatalf("frame session survives in-pane logout: %v", err)
	}
}

// A sibling studio's subresource (<img src=https://A/__jam/logout>, same-site,
// the Lax preview cookie sent) must not log the viewer out: the preview
// cookie counts in logout only where cookieUseAllowed says, so neither the
// preview session nor the viewer's pane sessions end, and the cookie is not
// cleared.
func TestLogoutPreviewCookieNeedsAllowedUse(t *testing.T) {
	a, ft := framed(t)
	fc := a.frameSession(t, ft) // alice's pane session on hostA
	pc := a.session(t)          // alice's preview session on hostA
	r := httptest.NewRequest("GET", "https://"+hostA+"/__jam/logout", nil)
	r.AddCookie(pc)
	r.Header.Set("Sec-Fetch-Site", "same-site")
	r.Header.Set("Sec-Fetch-Mode", "no-cors")
	r.Header.Set("Sec-Fetch-Dest", "image")
	w := httptest.NewRecorder()
	a.PreviewHostRoutes(http.NotFoundHandler()).ServeHTTP(w, r)
	if _, _, _, err := a.PrincipalFrame(frameReq(hostA, pc)); err != nil {
		t.Fatalf("same-site logout ended the preview session: %v", err)
	}
	if _, _, _, err := a.PrincipalFrame(frameReq(hostA, fc)); err != nil {
		t.Fatalf("same-site logout ended the pane session: %v", err)
	}
	if sc := w.Header().Values("Set-Cookie"); len(sc) != 0 {
		t.Fatalf("same-site logout cleared cookies: %q", sc)
	}
	// A top-level visit still logs out.
	r = httptest.NewRequest("GET", "https://"+hostA+"/__jam/logout", nil)
	r.AddCookie(pc)
	r.Header.Set("Sec-Fetch-Site", "none")
	r.Header.Set("Sec-Fetch-Mode", "navigate")
	r.Header.Set("Sec-Fetch-Dest", "document")
	a.PreviewHostRoutes(http.NotFoundHandler()).ServeHTTP(httptest.NewRecorder(), r)
	if _, _, _, err := a.PrincipalFrame(frameReq(hostA, pc)); !errors.Is(err, ErrNoCredentials) {
		t.Fatalf("top-level logout kept the preview session: %v", err)
	}
	if _, _, _, err := a.PrincipalFrame(frameReq(hostA, fc)); !errors.Is(err, ErrNoCredentials) {
		t.Fatalf("top-level logout kept the pane session: %v", err)
	}
}
