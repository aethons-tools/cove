package jam

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
)

// OIDCAuthenticator validates an operator's OIDC/Auth0 bearer token (signature +
// iss + aud + exp via go-oidc), and optionally requires a scope/permission.
type OIDCAuthenticator struct {
	provider     *oidc.Provider
	verifier     *oidc.IDTokenVerifier
	requireScope string
}

// NewOIDCAuthenticator does OIDC discovery against issuer (fetching the JWKS) and
// builds a verifier bound to audience. Fails closed if the issuer is unreachable.
func NewOIDCAuthenticator(ctx context.Context, issuer, audience, requireScope string) (*OIDCAuthenticator, error) {
	provider, err := oidc.NewProvider(ctx, issuer)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery for %q: %w", issuer, err)
	}
	return &OIDCAuthenticator{
		provider:     provider,
		verifier:     provider.Verifier(&oidc.Config{ClientID: audience}),
		requireScope: requireScope,
	}, nil
}

// IDTokenVerifier verifies ID tokens from the same issuer for a different
// audience, clientID (e.g. the device-flow client a participant signs in
// with), reusing the provider already discovered — no further discovery. It
// checks signature, issuer, audience and expiry (no operator scope) and
// returns the token's issuer and subject.
func (a *OIDCAuthenticator) IDTokenVerifier(clientID string) func(ctx context.Context, raw string) (issuer, subject string, err error) {
	v := a.provider.Verifier(&oidc.Config{ClientID: clientID})
	return func(ctx context.Context, raw string) (string, string, error) {
		t, err := v.Verify(ctx, raw)
		if err != nil {
			return "", "", err
		}
		return t.Issuer, t.Subject, nil
	}
}

func (a *OIDCAuthenticator) Authenticate(r *http.Request) (Operator, error) {
	raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || raw == "" {
		return Operator{}, fmt.Errorf("missing bearer token")
	}
	return a.VerifyToken(r.Context(), raw)
}

// VerifyToken verifies a raw JWT (signature, iss, aud, exp via go-oidc) and, when
// configured, the required scope. It is the single verification path shared by the
// bearer header (Authenticate) and the browser session cookie.
func (a *OIDCAuthenticator) VerifyToken(ctx context.Context, raw string) (Operator, error) {
	if raw == "" {
		return Operator{}, fmt.Errorf("missing token")
	}
	tok, err := a.verifier.Verify(ctx, raw)
	if err != nil {
		return Operator{}, fmt.Errorf("token verification failed: %w", err)
	}
	var claims struct {
		Scope       string   `json:"scope"`
		Permissions []string `json:"permissions"`
	}
	if err := tok.Claims(&claims); err != nil {
		return Operator{}, fmt.Errorf("claims: %w", err)
	}
	if a.requireScope != "" && !hasScope(a.requireScope, claims.Scope, claims.Permissions) {
		return Operator{}, fmt.Errorf("operator lacks required scope %q", a.requireScope)
	}
	return Operator{ID: tok.Subject}, nil
}

// hasScope reports whether want is in the space-delimited scope string or the
// permissions array (Auth0 puts scopes in either, depending on RBAC config).
func hasScope(want, scope string, perms []string) bool {
	for _, s := range strings.Fields(scope) {
		if s == want {
			return true
		}
	}
	for _, p := range perms {
		if p == want {
			return true
		}
	}
	return false
}
