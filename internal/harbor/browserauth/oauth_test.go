package browserauth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestPKCEChallengeIsS256OfVerifier(t *testing.T) {
	v, c, err := PKCE()
	if err != nil {
		t.Fatal(err)
	}
	if len(v) < 43 {
		t.Errorf("verifier too short: %d", len(v))
	}
	sum := sha256.Sum256([]byte(v))
	if want := base64.RawURLEncoding.EncodeToString(sum[:]); c != want {
		t.Errorf("challenge = %q, want S256(verifier) = %q", c, want)
	}
}

func TestAuthCodeURLParams(t *testing.T) {
	u := AuthCodeURL("https://idp/authorize", "cid", "https://h/ui/auth/callback",
		"openid profile", "https://h/api", "st8", "nonce9", "chal")
	parsed, err := url.Parse(u)
	if err != nil {
		t.Fatal(err)
	}
	q := parsed.Query()
	for k, want := range map[string]string{
		"response_type": "code", "client_id": "cid",
		"redirect_uri": "https://h/ui/auth/callback", "scope": "openid profile",
		"audience": "https://h/api", "state": "st8", "nonce": "nonce9",
		"code_challenge": "chal", "code_challenge_method": "S256",
	} {
		if got := q.Get(k); got != want {
			t.Errorf("param %s = %q, want %q", k, got, want)
		}
	}
	if !strings.HasPrefix(u, "https://idp/authorize?") {
		t.Errorf("URL should start at the authorize endpoint: %s", u)
	}
}

type doerFunc func(*http.Request) (*http.Response, error)

func (f doerFunc) Do(r *http.Request) (*http.Response, error) { return f(r) }

func TestExchangeCodePostsFormAndParsesTokens(t *testing.T) {
	var gotBody string
	doer := doerFunc(func(r *http.Request) (*http.Response, error) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"id_token":"idT","access_token":"acT"}`)),
		}, nil
	})
	id, ac, err := ExchangeCode(context.Background(), doer, "https://idp/token", "cid", "the-code", "the-verifier", "https://h/ui/auth/callback")
	if err != nil {
		t.Fatal(err)
	}
	if id != "idT" || ac != "acT" {
		t.Errorf("tokens = %q,%q want idT,acT", id, ac)
	}
	form, _ := url.ParseQuery(gotBody)
	for k, want := range map[string]string{
		"grant_type": "authorization_code", "code": "the-code",
		"code_verifier": "the-verifier", "client_id": "cid",
		"redirect_uri": "https://h/ui/auth/callback",
	} {
		if form.Get(k) != want {
			t.Errorf("form %s = %q, want %q", k, form.Get(k), want)
		}
	}
	if form.Has("client_secret") {
		t.Error("public client must not send client_secret")
	}
}
