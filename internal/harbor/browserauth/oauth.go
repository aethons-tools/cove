// Package browserauth implements OAuth 2.0 Authorization Code + PKCE browser
// login for the harbor admin UI: a public client (no secret), a session cookie
// holding the API access token (re-verified per request by harbor's existing
// OIDCAuthenticator), and the UI gate that lets loopback through and redirects
// off-loopback browsers to log in. It imports harbor; harbor never imports it.
package browserauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// HTTPDoer is the subset of *http.Client the flow needs (injected for tests).
type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// PKCE returns a fresh (verifier, S256 challenge) pair using crypto/rand.
func PKCE() (verifier, challenge string, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return "", "", err
	}
	verifier = base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge, nil
}

// AuthCodeURL builds the IdP authorization request URL for the code+PKCE flow.
func AuthCodeURL(authorizeEndpoint, clientID, redirectURI, scope, audience, state, nonce, challenge string) string {
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {redirectURI},
		"scope":                 {scope},
		"state":                 {state},
		"nonce":                 {nonce},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}
	if audience != "" {
		q.Set("audience", audience)
	}
	return authorizeEndpoint + "?" + q.Encode()
}

// ExchangeCode redeems an authorization code for tokens at the token endpoint as
// a public client (PKCE verifier, no client secret).
func ExchangeCode(ctx context.Context, doer HTTPDoer, tokenEndpoint, clientID, code, verifier, redirectURI string) (idToken, accessToken string, err error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"code_verifier": {verifier},
		"client_id":     {clientID},
		"redirect_uri":  {redirectURI},
	}
	req, err := http.NewRequestWithContext(ctx, "POST", tokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := doer.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("token exchange: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("token exchange: status %d", resp.StatusCode)
	}
	var out struct {
		IDToken     string `json:"id_token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", "", fmt.Errorf("token exchange decode: %w", err)
	}
	if out.AccessToken == "" {
		return "", "", fmt.Errorf("token exchange: no access_token in response")
	}
	return out.IDToken, out.AccessToken, nil
}
