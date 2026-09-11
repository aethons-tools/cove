package harbor

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// fakeOIDC stands up a minimal OIDC provider: discovery + JWKS, and mints RS256 tokens.
type fakeOIDC struct {
	url string
	key *rsa.PrivateKey
	kid string
}

func newFakeOIDC(t *testing.T) *fakeOIDC {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeOIDC{key: key, kid: "test-1"}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"issuer": f.url, "jwks_uri": f.url + "/jwks",
			"authorization_endpoint": f.url + "/authorize", "token_endpoint": f.url + "/token",
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]any{{
			"kty": "RSA", "use": "sig", "alg": "RS256", "kid": f.kid,
			"n": b64(key.N.Bytes()), "e": b64(big.NewInt(int64(key.E)).Bytes()),
		}}})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	f.url = srv.URL
	return f
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// mint signs a JWT with the given header kid + claims (RS256). alg="none" or a bad
// kid can be forced by the caller for the reject cases.
func (f *fakeOIDC) mint(t *testing.T, alg, kid string, claims map[string]any, sign bool) string {
	t.Helper()
	hdr := map[string]any{"alg": alg, "typ": "JWT", "kid": kid}
	enc := func(v any) string { b, _ := json.Marshal(v); return b64(b) }
	signing := enc(hdr) + "." + enc(claims)
	if !sign {
		return signing + "." // no signature (alg=none case)
	}
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return signing + "." + b64(sig)
}

func (f *fakeOIDC) claims(aud, sub, scope string, exp time.Time) map[string]any {
	return map[string]any{"iss": f.url, "aud": aud, "sub": sub, "scope": scope,
		"iat": time.Now().Unix(), "exp": exp.Unix()}
}

const testAud = "https://harbor.test/api"

func req(token string) *http.Request {
	r := httptest.NewRequest("GET", "/admin/healthz", nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	return r
}

func TestOIDCAuthenticatorAcceptsValidToken(t *testing.T) {
	f := newFakeOIDC(t)
	a, err := NewOIDCAuthenticator(context.Background(), f.url, testAud, "")
	if err != nil {
		t.Fatalf("NewOIDCAuthenticator: %v", err)
	}
	tok := f.mint(t, "RS256", f.kid, f.claims(testAud, "auth0|abc", "read write", time.Now().Add(time.Hour)), true)
	op, err := a.Authenticate(req(tok))
	if err != nil || op.ID != "auth0|abc" {
		t.Fatalf("accept: op=%+v err=%v", op, err)
	}
}

func TestOIDCAuthenticatorRejects(t *testing.T) {
	f := newFakeOIDC(t)
	a, err := NewOIDCAuthenticator(context.Background(), f.url, testAud, "")
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"missing":     "",
		"expired":     f.mint(t, "RS256", f.kid, f.claims(testAud, "s", "", time.Now().Add(-time.Hour)), true),
		"wrong-aud":   f.mint(t, "RS256", f.kid, f.claims("someone-else", "s", "", time.Now().Add(time.Hour)), true),
		"bad-sig":     f.mint(t, "RS256", f.kid, f.claims(testAud, "s", "", time.Now().Add(time.Hour)), true) + "tampered",
		"alg-none":    f.mint(t, "none", f.kid, f.claims(testAud, "s", "", time.Now().Add(time.Hour)), false),
		"unknown-kid": f.mint(t, "RS256", "nope", f.claims(testAud, "s", "", time.Now().Add(time.Hour)), true),
	}
	for name, tok := range cases {
		if _, err := a.Authenticate(req(tok)); err == nil {
			t.Errorf("%s: expected rejection, got nil error", name)
		}
	}
}

func TestOIDCAuthenticatorRequireScope(t *testing.T) {
	f := newFakeOIDC(t)
	a, _ := NewOIDCAuthenticator(context.Background(), f.url, testAud, "harbor:admin")
	// scope claim carries it
	ok := f.mint(t, "RS256", f.kid, f.claims(testAud, "s", "openid harbor:admin", time.Now().Add(time.Hour)), true)
	if _, err := a.Authenticate(req(ok)); err != nil {
		t.Fatalf("in-scope token rejected: %v", err)
	}
	no := f.mint(t, "RS256", f.kid, f.claims(testAud, "s", "openid", time.Now().Add(time.Hour)), true)
	if _, err := a.Authenticate(req(no)); err == nil {
		t.Fatal("token lacking required scope was accepted")
	}
}
