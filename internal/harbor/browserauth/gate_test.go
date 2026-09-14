package browserauth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/harbor"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
}

func (f *fakeIdP) mintAccess(t *testing.T, aud, sub string, exp time.Time) string {
	return f.mint(t, map[string]any{
		"iss": f.url, "aud": aud, "sub": sub, "scope": "harbor:admin",
		"iat": time.Now().Unix(), "exp": exp.Unix(),
	})
}

func TestGateLoopbackAlwaysAllowed(t *testing.T) {
	g := Gate{Sess: nil, LoginPath: "/ui/auth/login", Log: discard()}
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
	g := Gate{Sess: nil, LoginPath: "/ui/auth/login", Log: discard()}
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
	auth, err := harbor.NewOIDCAuthenticator(context.Background(), idp.url, "aud", "")
	if err != nil {
		t.Fatal(err)
	}
	g := Gate{Sess: &SessionVerifier{Auth: auth}, LoginPath: "/ui/auth/login", Log: discard()}
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
	auth, err := harbor.NewOIDCAuthenticator(context.Background(), idp.url, "aud", "")
	if err != nil {
		t.Fatal(err)
	}
	g := Gate{Sess: &SessionVerifier{Auth: auth}, LoginPath: "/ui/auth/login", Log: discard()}
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
	g := Gate{Sess: nil, LoginPath: "/ui/auth/login", Log: discard()}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/ui/roster", nil)
	req.RemoteAddr = "127.0.0.1:5000"
	req.Host = "localhost"
	g.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotLoopback = harbor.OperatorID(r)
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec, req)
	if gotLoopback != "local" {
		t.Errorf("loopback operator = %q, want local", gotLoopback)
	}

	// Off-loopback valid session → the token's sub.
	idp := newFakeIdP(t)
	auth, err := harbor.NewOIDCAuthenticator(context.Background(), idp.url, "aud", "")
	if err != nil {
		t.Fatal(err)
	}
	var gotSession string
	gs := Gate{Sess: &SessionVerifier{Auth: auth}, LoginPath: "/ui/auth/login", Log: discard()}
	rec = httptest.NewRecorder()
	sreq := httptest.NewRequest("GET", "/ui/roster", nil)
	sreq.RemoteAddr = "203.0.113.7:5555"
	sreq.AddCookie(&http.Cookie{Name: SessionCookie, Value: idp.mintAccess(t, "aud", "auth0|alice", time.Now().Add(time.Hour))})
	gs.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSession = harbor.OperatorID(r)
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec, sreq)
	if gotSession != "auth0|alice" {
		t.Errorf("session operator = %q, want auth0|alice", gotSession)
	}
}

func TestGateOffLoopbackValidSessionAllowed(t *testing.T) {
	idp := newFakeIdP(t)
	auth, err := harbor.NewOIDCAuthenticator(context.Background(), idp.url, "aud", "")
	if err != nil {
		t.Fatal(err)
	}
	g := Gate{Sess: &SessionVerifier{Auth: auth}, LoginPath: "/ui/auth/login", Log: discard()}
	tok := idp.mintAccess(t, "aud", "auth0|bob", time.Now().Add(time.Hour))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/ui/coves", nil)
	req.RemoteAddr = "203.0.113.7:5555"
	req.AddCookie(&http.Cookie{Name: SessionCookie, Value: tok})
	g.Wrap(okHandler()).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid session = %d, want 200", rec.Code)
	}
}

func TestGateLoopbackRejectsForeignHost(t *testing.T) {
	// A DNS-rebinding request: loopback connection, but Host is an attacker name
	// not in the expected set. Must be refused even though it is loopback.
	g := Gate{Sess: nil, LoginPath: "/ui/auth/login", Log: discard()}
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
	g := Gate{Sess: nil, LoginPath: "/ui/auth/login", ExpectedHosts: []string{"harbor.local.aethons.tools"}, Log: discard()}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/ui/coves", nil)
	req.RemoteAddr = "127.0.0.1:5000"
	req.Host = "harbor.local.aethons.tools"
	g.Wrap(okHandler()).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("loopback + configured Host = %d, want 200", rec.Code)
	}
	// A different Host, not configured, is still refused.
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("GET", "/ui/coves", nil)
	req2.RemoteAddr = "127.0.0.1:5000"
	req2.Host = "harbor.evil.example"
	g.Wrap(okHandler()).ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusForbidden {
		t.Fatalf("loopback + non-configured Host = %d, want 403", rec2.Code)
	}
}

func TestGateLoopbackAllowsLoopbackLiterals(t *testing.T) {
	g := Gate{Sess: nil, LoginPath: "/ui/auth/login", Log: discard()} // no ExpectedHosts
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
