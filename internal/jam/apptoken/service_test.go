package apptoken

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/appui"
)

const (
	testIssuer   = "https://idp.example"
	testVerifier = "a-test-verifier-that-is-long-enough-0123456789"
	testRedirect = "vscode://aethons.jam/auth"
	idCookie     = "jam_app_id"
	atCookie     = "jam_app_at"
)

func challengeOf(v string) string {
	sum := sha256.Sum256([]byte(v))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

type fixture struct {
	t     *testing.T
	store jam.Store
	svc   *Service
	h     http.Handler
	now   time.Time
	logs  *bytes.Buffer
	alice jam.User
}

// newFixture serves a Service over a MemStore holding project alpha, alice (a
// member, ID cookie "id:sub-alice") and dave (in no project, "id:sub-dave").
// An access cookie "at:<op>" verifies as operator <op>.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	st := jam.NewMemStore()
	if err := st.CreateProject("alpha"); err != nil {
		t.Fatal(err)
	}
	alice, err := st.CreateUser(jam.User{Name: "alice", OIDC: []jam.OIDCIdentity{{Issuer: testIssuer, Subject: "sub-alice"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateUser(jam.User{Name: "dave", OIDC: []jam.OIDCIdentity{{Issuer: testIssuer, Subject: "sub-dave"}}}); err != nil {
		t.Fatal(err)
	}
	p, _ := st.GetProject("alpha")
	if err := st.AddMember(p.ID, alice.ID); err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, store: st, now: time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC), logs: &bytes.Buffer{}, alice: alice}
	sess := &appui.Sessions{
		Issuer: testIssuer, IDCookie: idCookie, AccessCookie: atCookie, Store: st,
		VerifyID: func(_ context.Context, raw string) (string, error) {
			if sub, ok := strings.CutPrefix(raw, "id:"); ok {
				return sub, nil
			}
			return "", errors.New("bad id token")
		},
		VerifyAccess: func(_ context.Context, raw string) (jam.Operator, error) {
			if op, ok := strings.CutPrefix(raw, "at:"); ok {
				return jam.Operator{ID: op}, nil
			}
			return jam.Operator{}, errors.New("bad access token")
		},
	}
	f.svc = New(Config{Store: st, Sessions: sess, Brand: "Acme Jam", Now: func() time.Time { return f.now },
		Log: slog.New(slog.NewTextHandler(f.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))})
	f.h = f.svc.Routes()
	return f
}

func consentQuery(mod func(url.Values)) url.Values {
	q := url.Values{"redirect": {testRedirect}, "state": {"st-1"}, "code_challenge": {challengeOf(testVerifier)},
		"code_challenge_method": {"S256"}, "client": {"VS Code on <mac>"}}
	if mod != nil {
		mod(q)
	}
	return q
}

func (f *fixture) do(r *http.Request, cookies ...string) *httptest.ResponseRecorder {
	for _, c := range cookies {
		name, val, _ := strings.Cut(c, "=")
		r.AddCookie(&http.Cookie{Name: name, Value: val})
	}
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	return w
}

func (f *fixture) get(q url.Values, cookies ...string) *httptest.ResponseRecorder {
	return f.do(httptest.NewRequest("GET", "/app/auth/app-token?"+q.Encode(), nil), cookies...)
}

func (f *fixture) post(form url.Values, cookies ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/app/auth/app-token", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", "http://example.com")
	return f.do(r, cookies...)
}

// allow approves consent and returns the code from the redirect.
func (f *fixture) allow(q url.Values, cookies ...string) string {
	f.t.Helper()
	q.Set("action", "allow")
	w := f.post(q, cookies...)
	if w.Code != http.StatusSeeOther {
		f.t.Fatalf("allow: %d %s", w.Code, w.Body)
	}
	loc, err := url.Parse(w.Header().Get("Location"))
	if err != nil {
		f.t.Fatal(err)
	}
	code := loc.Query().Get("code")
	if code == "" {
		f.t.Fatalf("no code in %s", loc)
	}
	return code
}

func (f *fixture) exchange(body any) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	return f.do(httptest.NewRequest("POST", "/app/auth/app-token/exchange", bytes.NewReader(b)))
}

func TestValidRedirect(t *testing.T) {
	for s, want := range map[string]bool{
		"vscode://aethons.jam/auth":              true,
		"vscode-insiders://aethons.jam/auth?x=1": true,
		"vscode://aethons.jam.evil/":             false,
		"vscode://aethons.jam":                   false,
		"vscode://other/":                        false,
		"https://aethons.jam/":                   false,
		"":                                       false,
		"VSCODE://aethons.jam/auth":              false,
	} {
		if got := ValidRedirect(s); got != want {
			t.Errorf("ValidRedirect(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestConsentBadParams400NoLoginRedirect(t *testing.T) {
	f := newFixture(t)
	for name, mod := range map[string]func(url.Values){
		"evil host":       func(q url.Values) { q.Set("redirect", "vscode://aethons.jam.evil/") },
		"no slash":        func(q url.Values) { q.Set("redirect", "vscode://aethons.jam") },
		"https":           func(q url.Values) { q.Set("redirect", "https://aethons.jam/auth") },
		"no redirect":     func(q url.Values) { q.Del("redirect") },
		"no challenge":    func(q url.Values) { q.Del("code_challenge") },
		"short challenge": func(q url.Values) { q.Set("code_challenge", "abc") },
		"bad challenge":   func(q url.Values) { q.Set("code_challenge", strings.Repeat("!", 43)) },
		"plain":           func(q url.Values) { q.Set("code_challenge_method", "plain") },
		"no method":       func(q url.Values) { q.Del("code_challenge_method") },
		"no state":        func(q url.Values) { q.Del("state") },
		"long state":      func(q url.Values) { q.Set("state", strings.Repeat("s", 513)) },
	} {
		t.Run(name, func(t *testing.T) {
			for _, cookies := range [][]string{nil, {idCookie + "=id:sub-alice"}} {
				w := f.get(consentQuery(mod), cookies...)
				if w.Code != http.StatusBadRequest || w.Header().Get("Location") != "" {
					t.Fatalf("cookies %v: %d Location=%q", cookies, w.Code, w.Header().Get("Location"))
				}
			}
			q := consentQuery(mod)
			q.Set("action", "allow")
			if w := f.post(q, idCookie+"=id:sub-alice"); w.Code != http.StatusBadRequest || w.Header().Get("Location") != "" {
				t.Fatalf("post: %d Location=%q", w.Code, w.Header().Get("Location"))
			}
		})
	}
}

func TestConsentNoSession302Login(t *testing.T) {
	f := newFixture(t)
	q := consentQuery(nil)
	for _, remote := range []string{"192.0.2.1:1234", "127.0.0.1:1234"} {
		r := httptest.NewRequest("GET", "/app/auth/app-token?"+q.Encode(), nil)
		r.RemoteAddr = remote
		w := f.do(r, idCookie+"=forged")
		want := "/app/auth/login?return_to=" + url.QueryEscape("/app/auth/app-token?"+q.Encode())
		if w.Code != http.StatusFound || w.Header().Get("Location") != want {
			t.Fatalf("%s: %d Location=%q want %q", remote, w.Code, w.Header().Get("Location"), want)
		}
		if !strings.HasPrefix(w.Header().Get("Location"), "/app/auth/login?return_to=%2Fapp%2Fauth%2Fapp-token%3F") {
			t.Fatal(w.Header().Get("Location"))
		}
	}
}

func checkSecurityHeaders(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	h := w.Header()
	if h.Get("Cache-Control") != "no-store" || h.Get("X-Frame-Options") != "DENY" ||
		h.Get("Content-Security-Policy") != "default-src 'none'; style-src 'self'; form-action 'self' vscode: vscode-insiders:; frame-ancestors 'none'" {
		t.Fatalf("headers %v", h)
	}
}

func TestConsentShowsClientAndUser(t *testing.T) {
	f := newFixture(t)
	w := f.get(consentQuery(nil), idCookie+"=id:sub-alice")
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	checkSecurityHeaders(t, w)
	body := w.Body.String()
	for _, want := range []string{"VS Code on &lt;mac&gt;", "alice", `name="state" value="st-1"`, `value="allow"`, `value="cancel"`, "Acme Jam"} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "<mac>") || strings.Contains(body, "operator rights") {
		t.Fatalf("body:\n%s", body)
	}
	// With operator standing, the page says so and until when.
	w = f.get(consentQuery(nil), idCookie+"=id:sub-alice", atCookie+"=at:op-1")
	if body := w.Body.String(); !strings.Contains(body, "with your operator rights, until 28 November 2026") {
		t.Fatalf("operator body:\n%s", body)
	}
}

func TestConsentAdminOnly403(t *testing.T) {
	f := newFixture(t)
	for _, cookies := range [][]string{{atCookie + "=at:op-1"}, {idCookie + "=id:sub-dave"}} {
		w := f.get(consentQuery(nil), cookies...)
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "App tokens need a Jam user in at least one project") {
			t.Fatalf("%v: %d %s", cookies, w.Code, w.Body)
		}
		q := consentQuery(nil)
		q.Set("action", "allow")
		if w := f.post(q, cookies...); w.Code != http.StatusForbidden {
			t.Fatalf("post %v: %d", cookies, w.Code)
		}
	}
}

func TestConsentPostCrossOrigin403(t *testing.T) {
	f := newFixture(t)
	q := consentQuery(nil)
	q.Set("action", "allow")
	for _, origin := range []string{"https://evil.example", ""} {
		r := httptest.NewRequest("POST", "/app/auth/app-token", strings.NewReader(q.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		w := f.do(r, idCookie+"=id:sub-alice")
		if w.Code != http.StatusForbidden || w.Header().Get("Location") != "" {
			t.Fatalf("origin %q: %d", origin, w.Code)
		}
	}
}

func TestConsentAllowRedirectsWithCodeAndState(t *testing.T) {
	f := newFixture(t)
	q := consentQuery(func(q url.Values) { q.Set("redirect", "vscode-insiders://aethons.jam/auth?keep=1") })
	q.Set("action", "allow")
	w := f.post(q, idCookie+"=id:sub-alice")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	loc, _ := url.Parse(w.Header().Get("Location"))
	if loc.Scheme != "vscode-insiders" || loc.Host != "aethons.jam" || loc.Path != "/auth" ||
		loc.Query().Get("keep") != "1" || loc.Query().Get("state") != "st-1" || len(loc.Query().Get("code")) < 32 {
		t.Fatalf("Location %s", loc)
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("no no-store")
	}
}

func TestConsentCancelAccessDenied(t *testing.T) {
	f := newFixture(t)
	q := consentQuery(nil)
	q.Set("action", "cancel")
	w := f.post(q, idCookie+"=id:sub-alice")
	loc, _ := url.Parse(w.Header().Get("Location"))
	if w.Code != http.StatusSeeOther || loc.Query().Get("error") != "access_denied" || loc.Query().Get("state") != "st-1" || loc.Query().Has("code") {
		t.Fatalf("%d %s", w.Code, loc)
	}
	if len(f.svc.codes.m) != 0 {
		t.Fatal("cancel issued a code")
	}
}

type exchangeResp struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	User      string    `json:"user"`
	Error     string    `json:"error"`
}

func decode(t *testing.T, w *httptest.ResponseRecorder) exchangeResp {
	t.Helper()
	var out exchangeResp
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %q: %v", w.Body, err)
	}
	return out
}

func TestExchangeIssuesToken(t *testing.T) {
	f := newFixture(t)
	code := f.allow(consentQuery(nil), idCookie+"=id:sub-alice")
	w := f.exchange(map[string]string{"code": code, "code_verifier": testVerifier, "redirect": testRedirect})
	if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("%d %v %s", w.Code, w.Header(), w.Body)
	}
	got := decode(t, w)
	if got.Token == "" || got.User != "alice" || !got.ExpiresAt.Equal(f.now.Add(jam.AppTokenTTL)) {
		t.Fatalf("resp %+v", got)
	}
	row, ok := f.store.AppTokenByHash(jam.HashToken(got.Token))
	if !ok || row.UserID != f.alice.ID || row.Label != "VS Code on <mac>" || row.OperatorID != "" {
		t.Fatalf("row %+v ok=%v", row, ok)
	}
	logs := f.logs.String()
	if !strings.Contains(logs, "app token issued") || !strings.Contains(logs, string(row.ID)) {
		t.Fatalf("logs:\n%s", logs)
	}
	for _, secret := range []string{got.Token, code, testVerifier, row.Hash} {
		if strings.Contains(logs, secret) {
			t.Fatalf("log contains a secret %q:\n%s", secret, logs)
		}
	}
}

func TestExchangeOperatorStandingFromAccessCookie(t *testing.T) {
	f := newFixture(t)
	code := f.allow(consentQuery(nil), idCookie+"=id:sub-alice", atCookie+"=at:op-1")
	got := decode(t, f.exchange(map[string]string{"code": code, "code_verifier": testVerifier, "redirect": testRedirect}))
	row, ok := f.store.AppTokenByHash(jam.HashToken(got.Token))
	if !ok || row.OperatorID != "op-1" {
		t.Fatalf("row %+v", row)
	}
}

func TestExchangeLoopbackSessionNoOperatorStanding(t *testing.T) {
	f := newFixture(t)
	q := consentQuery(nil)
	q.Set("action", "allow")
	r := httptest.NewRequest("POST", "/app/auth/app-token", strings.NewReader(q.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", "http://example.com")
	r.RemoteAddr = "127.0.0.1:5555"
	w := f.do(r, idCookie+"=id:sub-alice")
	loc, _ := url.Parse(w.Header().Get("Location"))
	got := decode(t, f.exchange(map[string]string{"code": loc.Query().Get("code"), "code_verifier": testVerifier, "redirect": testRedirect}))
	row, ok := f.store.AppTokenByHash(jam.HashToken(got.Token))
	if !ok || row.OperatorID != "" || row.Operator() {
		t.Fatalf("loopback token %+v ok=%v", row, ok)
	}
	// The consent page shown on loopback doesn't claim operator rights either.
	r = httptest.NewRequest("GET", "/app/auth/app-token?"+consentQuery(nil).Encode(), nil)
	r.RemoteAddr = "127.0.0.1:5555"
	if body := f.do(r, idCookie+"=id:sub-alice").Body.String(); strings.Contains(body, "operator rights") {
		t.Fatalf("loopback page claims operator rights:\n%s", body)
	}
}

func TestExchangeReplayWrongVerifierWrongRedirectExpired(t *testing.T) {
	other := "another-verifier-that-is-also-long-enough-987654"
	for name, c := range map[string]struct {
		before func(f *fixture, code string)
		body   func(code string) map[string]string
		retry  bool // after the failure, the right request fails too
	}{
		"replay": {
			before: func(f *fixture, code string) {
				if w := f.exchange(map[string]string{"code": code, "code_verifier": testVerifier, "redirect": testRedirect}); w.Code != 200 {
					f.t.Fatalf("first: %d", w.Code)
				}
			},
		},
		"wrong verifier": {
			body: func(code string) map[string]string {
				return map[string]string{"code": code, "code_verifier": other, "redirect": testRedirect}
			},
			retry: true,
		},
		"malformed verifier": {
			body: func(code string) map[string]string {
				return map[string]string{"code": code, "code_verifier": "short", "redirect": testRedirect}
			},
			retry: true,
		},
		"wrong redirect": {
			body: func(code string) map[string]string {
				return map[string]string{"code": code, "code_verifier": testVerifier, "redirect": "vscode://aethons.jam/other"}
			},
			retry: true,
		},
		"expired": {
			before: func(f *fixture, _ string) { f.now = f.now.Add(CodeTTL) },
		},
		"unknown code": {
			body: func(string) map[string]string {
				return map[string]string{"code": "nope", "code_verifier": testVerifier, "redirect": testRedirect}
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			code := f.allow(consentQuery(nil), idCookie+"=id:sub-alice")
			if c.before != nil {
				c.before(f, code)
			}
			right := map[string]string{"code": code, "code_verifier": testVerifier, "redirect": testRedirect}
			body := right
			if c.body != nil {
				body = c.body(code)
			}
			w := f.exchange(body)
			if w.Code != http.StatusBadRequest || decode(t, w).Error != "invalid_grant" {
				t.Fatalf("%d %s", w.Code, w.Body)
			}
			if c.retry {
				if w := f.exchange(right); w.Code != http.StatusBadRequest || decode(t, w).Error != "invalid_grant" {
					t.Fatalf("retry after failure: %d %s", w.Code, w.Body)
				}
			}
		})
	}
	t.Run("oversize body", func(t *testing.T) {
		f := newFixture(t)
		w := f.do(httptest.NewRequest("POST", "/app/auth/app-token/exchange", strings.NewReader(`{"code":"`+strings.Repeat("x", 5000)+`"}`)))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%d", w.Code)
		}
	})
}
