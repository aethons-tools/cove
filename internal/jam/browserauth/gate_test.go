package browserauth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
}

func (f *fakeIdP) mintAccess(t *testing.T, aud, sub string, exp time.Time) string {
	return f.mint(t, map[string]any{
		"iss": f.url, "aud": aud, "sub": sub, "scope": "jam:admin",
		"iat": time.Now().Unix(), "exp": exp.Unix(),
	})
}

// operatorGate builds the operator "/ui" gate (loopback-trusted) over an
// optional session verifier, matching the production wiring.
func operatorGate(sess func(*http.Request) (*http.Request, SessionOutcome)) Gate {
	return Gate{
		LoopbackTrust: OperatorLoopbackTrust(),
		Session:       sess,
		LoginPath:     "/ui/auth/login",
		Log:           discard(),
	}
}

func TestGateLoopbackAlwaysAllowed(t *testing.T) {
	g := operatorGate(nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/ui/coves", nil)
	req.RemoteAddr = "127.0.0.1:5000"
	req.Host = "127.0.0.1:5000"
	g.Wrap(okHandler()).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("loopback = %d, want 200", rec.Code)
	}
}

func TestGateOffLoopbackLoopbackOnlyRefused(t *testing.T) {
	g := operatorGate(nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/ui/coves", nil)
	req.RemoteAddr = "203.0.113.7:5555"
	g.Wrap(okHandler()).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("off-loopback loopback-only = %d, want 403", rec.Code)
	}
}

func TestGateOffLoopbackNoSessionRedirects(t *testing.T) {
	idp := newFakeIdP(t)
	auth, err := jam.NewOIDCAuthenticator(context.Background(), idp.url, "aud", "")
	if err != nil {
		t.Fatal(err)
	}
	g := operatorGate(OperatorSession(auth, OperatorUIMount().SessionCookie))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/ui/coves", nil)
	req.RemoteAddr = "203.0.113.7:5555"
	g.Wrap(okHandler()).ServeHTTP(rec, req)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/ui/auth/login" {
		t.Fatalf("no-session off-loopback = %d %q, want 302 /ui/auth/login", rec.Code, rec.Header().Get("Location"))
	}
}

func TestGateOffLoopbackWriteWithoutSessionRefused(t *testing.T) {
	idp := newFakeIdP(t)
	auth, err := jam.NewOIDCAuthenticator(context.Background(), idp.url, "aud", "")
	if err != nil {
		t.Fatal(err)
	}
	g := operatorGate(OperatorSession(auth, OperatorUIMount().SessionCookie))
	var called bool
	stub := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/ui/enrollments", nil)
	req.RemoteAddr = "203.0.113.7:5555"
	g.Wrap(stub).ServeHTTP(rec, req)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/ui/auth/login" {
		t.Fatalf("no-session off-loopback write = %d %q, want 302 /ui/auth/login", rec.Code, rec.Header().Get("Location"))
	}
	if called {
		t.Error("wrapped handler was called; write must not reach the handler without a session")
	}
}

func TestGateAttributesOperator(t *testing.T) {
	// Loopback → "local".
	var gotLoopback string
	g := operatorGate(nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/ui/actors", nil)
	req.RemoteAddr = "127.0.0.1:5000"
	req.Host = "localhost"
	g.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotLoopback = jam.OperatorID(r)
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec, req)
	if gotLoopback != "local" {
		t.Errorf("loopback operator = %q, want local", gotLoopback)
	}

	// Off-loopback valid session → the token's sub.
	idp := newFakeIdP(t)
	auth, err := jam.NewOIDCAuthenticator(context.Background(), idp.url, "aud", "")
	if err != nil {
		t.Fatal(err)
	}
	var gotSession string
	gs := operatorGate(OperatorSession(auth, OperatorUIMount().SessionCookie))
	rec = httptest.NewRecorder()
	sreq := httptest.NewRequest("GET", "/ui/actors", nil)
	sreq.RemoteAddr = "203.0.113.7:5555"
	sreq.AddCookie(&http.Cookie{Name: OperatorUIMount().SessionCookie, Value: idp.mintAccess(t, "aud", "auth0|alice", time.Now().Add(time.Hour))})
	gs.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSession = jam.OperatorID(r)
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec, sreq)
	if gotSession != "auth0|alice" {
		t.Errorf("session operator = %q, want auth0|alice", gotSession)
	}
}

func TestGateLoopbackPrefersValidSession(t *testing.T) {
	// A loopback operator who has signed in is attributed to their login (so
	// /ui can resolve "me" in a roster); a bad cookie still falls back to the
	// loopback "local" operator rather than redirecting to login.
	idp := newFakeIdP(t)
	auth, err := jam.NewOIDCAuthenticator(context.Background(), idp.url, "aud", "")
	if err != nil {
		t.Fatal(err)
	}
	g := operatorGate(OperatorSession(auth, OperatorUIMount().SessionCookie))
	for _, tc := range []struct {
		name, cookie, want string
	}{
		{"valid session", idp.mintAccess(t, "aud", "auth0|alice", time.Now().Add(time.Hour)), "auth0|alice"},
		{"expired session", idp.mintAccess(t, "aud", "auth0|alice", time.Now().Add(-time.Hour)), "local"},
		{"no session", "", "local"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got string
			rec := httptest.NewRecorder()
			req := httptest.NewRequest("GET", "/ui/roles", nil)
			req.RemoteAddr = "127.0.0.1:5000"
			req.Host = "localhost"
			if tc.cookie != "" {
				req.AddCookie(&http.Cookie{Name: OperatorUIMount().SessionCookie, Value: tc.cookie})
			}
			g.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got = jam.OperatorID(r)
				w.WriteHeader(http.StatusOK)
			})).ServeHTTP(rec, req)
			if rec.Code != http.StatusOK || got != tc.want {
				t.Errorf("code=%d operator=%q, want 200 %q", rec.Code, got, tc.want)
			}
		})
	}
}

func TestGateOffLoopbackValidSessionAllowed(t *testing.T) {
	idp := newFakeIdP(t)
	auth, err := jam.NewOIDCAuthenticator(context.Background(), idp.url, "aud", "")
	if err != nil {
		t.Fatal(err)
	}
	g := operatorGate(OperatorSession(auth, OperatorUIMount().SessionCookie))
	tok := idp.mintAccess(t, "aud", "auth0|bob", time.Now().Add(time.Hour))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/ui/coves", nil)
	req.RemoteAddr = "203.0.113.7:5555"
	req.AddCookie(&http.Cookie{Name: OperatorUIMount().SessionCookie, Value: tok})
	g.Wrap(okHandler()).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid session = %d, want 200", rec.Code)
	}
}

func TestGateLoopbackRejectsForeignHost(t *testing.T) {
	// A DNS-rebinding request: loopback connection, but Host is an attacker name
	// not in the expected set. Must be refused even though it is loopback.
	g := operatorGate(nil)
	var called bool
	stub := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true; w.WriteHeader(http.StatusOK) })
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/ui/enrollments", nil)
	req.RemoteAddr = "127.0.0.1:5000"
	req.Host = "evil.example"
	g.Wrap(stub).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("loopback + foreign Host = %d, want 403", rec.Code)
	}
	if called {
		t.Error("wrapped handler was called; a rebound request must not reach it")
	}
}

func TestGateLoopbackAllowsConfiguredHost(t *testing.T) {
	g := Gate{LoopbackTrust: OperatorLoopbackTrust(), LoginPath: "/ui/auth/login", ExpectedHosts: []string{"jam.local.aethons.tools"}, Log: discard()}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/ui/coves", nil)
	req.RemoteAddr = "127.0.0.1:5000"
	req.Host = "jam.local.aethons.tools"
	g.Wrap(okHandler()).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("loopback + configured Host = %d, want 200", rec.Code)
	}
	// A different Host, not configured, is still refused.
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("GET", "/ui/coves", nil)
	req2.RemoteAddr = "127.0.0.1:5000"
	req2.Host = "jam.evil.example"
	g.Wrap(okHandler()).ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusForbidden {
		t.Fatalf("loopback + non-configured Host = %d, want 403", rec2.Code)
	}
}

func TestGateLoopbackAllowsLoopbackLiterals(t *testing.T) {
	g := operatorGate(nil) // no ExpectedHosts
	for _, host := range []string{"127.0.0.1:8081", "localhost:8081", "[::1]:8081"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/ui/coves", nil)
		req.RemoteAddr = "127.0.0.1:5000"
		req.Host = host
		g.Wrap(okHandler()).ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("loopback + Host %q = %d, want 200", host, rec.Code)
		}
	}
}

// --- Participant plane (/me): no loopback trust; session maps to a roster human ---

// participantStore is a store whose project "proj" has humans as members.
func participantStore(t *testing.T, humans ...jam.Human) jam.Store {
	t.Helper()
	s := jam.NewMemStore()
	if err := s.CreateProject("proj"); err != nil {
		t.Fatal(err)
	}
	for _, h := range humans {
		if err := s.AddHuman("proj", h); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

// TestParticipantGateNoLoopbackBypass is the key boundary property: with no
// LoopbackTrust hook, even a loopback request must present a valid session — the
// gate must not silently authenticate a local caller as some participant.
func TestParticipantGateNoLoopbackBypass(t *testing.T) {
	var called bool
	g := Gate{ // no LoopbackTrust — participant plane
		Session:   func(r *http.Request) (*http.Request, SessionOutcome) { return r, SessionNone },
		LoginPath: "/me/auth/login",
		Log:       discard(),
	}
	stub := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true; w.WriteHeader(http.StatusOK) })
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/me/", nil)
	req.RemoteAddr = "127.0.0.1:5000" // loopback
	req.Host = "127.0.0.1:8081"
	g.Wrap(stub).ServeHTTP(rec, req)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/me/auth/login" {
		t.Fatalf("loopback no-session = %d %q, want 302 /me/auth/login (no loopback bypass)", rec.Code, rec.Header().Get("Location"))
	}
	if called {
		t.Error("participant handler reached on loopback without a session")
	}
}

func TestParticipantSessionResolvesAndFailsClosed(t *testing.T) {
	idp := newFakeIdP(t)
	svc, err := New(context.Background(), RawConfig{Issuer: idp.url, ClientID: "jam-browser"}, ParticipantMount(), nil, discard())
	if err != nil {
		t.Fatal(err)
	}
	// Inject a fake token→subject step so we don't need a live-signed token.
	svc.verifySubject = func(_ context.Context, raw string) (string, error) {
		switch raw {
		case "alice-token":
			return "sub-alice", nil
		case "stranger-token":
			return "sub-stranger", nil
		default:
			return "", fmt.Errorf("bad token")
		}
	}
	store := participantStore(t, jam.Human{Name: "alice", Identity: []jam.OIDCIdentity{{Issuer: idp.url, Subject: "sub-alice"}}})
	sess := svc.ParticipantSession(store)

	cases := []struct {
		name     string
		cookie   string
		want     SessionOutcome
		wantName string
	}{
		{"mapped subject → OK", "alice-token", SessionOK, "alice"},
		{"unmapped subject → Forbidden (not a loop)", "stranger-token", SessionForbidden, ""},
		{"unverifiable token → None (re-login)", "garbage", SessionNone, ""},
		{"no cookie → None (re-login)", "", SessionNone, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/me/", nil)
			if tc.cookie != "" {
				req.AddCookie(&http.Cookie{Name: ParticipantMount().SessionCookie, Value: tc.cookie})
			}
			rr, out := sess(req)
			if out != tc.want {
				t.Fatalf("outcome = %v, want %v", out, tc.want)
			}
			if tc.want != SessionOK {
				return
			}
			p, has := jam.ParticipantFrom(rr)
			if !has {
				t.Fatal("no participant injected on success")
			}
			if p.Name != tc.wantName || p.Subject != "sub-alice" || p.Issuer != idp.url {
				t.Errorf("participant = %+v, want name %q subject sub-alice issuer %s", p, tc.wantName, idp.url)
			}
		})
	}
}

// TestParticipantGateForbiddenDoesNotLoop is the fix: a valid session whose
// subject resolves to no roster human gets a 403, NOT a redirect back to login
// (which would loop, since re-login yields the same subject).
func TestParticipantGateForbiddenDoesNotLoop(t *testing.T) {
	var called bool
	stub := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true; w.WriteHeader(http.StatusOK) })

	forbidden := Gate{
		Session:   func(r *http.Request) (*http.Request, SessionOutcome) { return r, SessionForbidden },
		LoginPath: "/me/auth/login",
		Log:       discard(),
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/me/", nil)
	req.RemoteAddr = "203.0.113.7:5555"
	forbidden.Wrap(stub).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("forbidden session = %d, want 403 (must not redirect/loop)", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "" {
		t.Errorf("forbidden session redirected to %q; must not redirect", loc)
	}
	if called {
		t.Error("wrapped handler reached with an unauthorized session")
	}

	// By contrast, SessionNone still redirects to login.
	none := Gate{
		Session:   func(r *http.Request) (*http.Request, SessionOutcome) { return r, SessionNone },
		LoginPath: "/me/auth/login",
		Log:       discard(),
	}
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("GET", "/me/", nil)
	req2.RemoteAddr = "203.0.113.7:5555"
	none.Wrap(stub).ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusFound || rec2.Header().Get("Location") != "/me/auth/login" {
		t.Fatalf("no session = %d %q, want 302 /me/auth/login", rec2.Code, rec2.Header().Get("Location"))
	}
}
