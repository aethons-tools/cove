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
