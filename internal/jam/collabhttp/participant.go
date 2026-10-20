package collabhttp

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/collab"
)

// ParticipantBearer is the /me plane's Deps.Principal: the caller presents an
// OIDC ID token (from at-jam login --participant) as a Bearer credential;
// verify checks it and returns its issuer and subject, which name the user
// bound to that identity. A missing or non-Bearer Authorization header is
// ErrNoCredentials, a token verify rejects is ErrInvalidToken, and a verified
// identity bound to no user (or one in no project) is ErrUnbound.
func ParticipantBearer(verify func(ctx context.Context, raw string) (issuer, subject string, err error), store jam.ParticipantStore) func(*http.Request) (collab.Principal, string, error) {
	return func(r *http.Request) (collab.Principal, string, error) {
		raw, ok := bearerToken(r)
		if !ok {
			return collab.Principal{}, "", ErrNoCredentials
		}
		iss, sub, err := verify(r.Context(), raw)
		if err != nil {
			return collab.Principal{}, "", ErrInvalidToken
		}
		p, ok := jam.ParticipantByIdentity(store, iss, sub)
		if !ok {
			return collab.Principal{}, "", ErrUnbound
		}
		return collab.Principal{UserID: p.UserID}, p.Name, nil
	}
}

// bearerToken returns the token of an "Authorization: Bearer <token>" header
// (the scheme is case-insensitive) and whether there is a non-empty one.
func bearerToken(r *http.Request) (string, bool) {
	scheme, tok, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	tok = strings.TrimSpace(tok)
	return tok, tok != ""
}

// IDTokenVerifier discovers issuer and returns a ParticipantBearer verify
// func for its ID tokens: signature, issuer, expiry, and audience clientID —
// the device-flow client at-jam login --participant signs in with.
func IDTokenVerifier(ctx context.Context, issuer, clientID string) (func(ctx context.Context, raw string) (string, string, error), error) {
	provider, err := oidc.NewProvider(ctx, issuer)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery for %q: %w", issuer, err)
	}
	v := provider.Verifier(&oidc.Config{ClientID: clientID})
	return func(ctx context.Context, raw string) (string, string, error) {
		t, err := v.Verify(ctx, raw)
		if err != nil {
			return "", "", err
		}
		return t.Issuer, t.Subject, nil
	}, nil
}
