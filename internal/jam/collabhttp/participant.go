package collabhttp

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/collab"
)

// ParticipantBearer is the /me plane's Deps.Principal: the caller presents an
// OIDC ID token (from jam login --participant) as a Bearer credential;
// verify checks it and returns its issuer and subject, which name the user
// bound to that identity. A missing or non-Bearer Authorization header is
// ErrNoCredentials, a token verify rejects is ErrInvalidToken, and a verified
// identity bound to no live user is ErrUnbound. Project membership is not
// required — collab.Decide authorizes each request.
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
		// Bound and live is enough: collab.Decide authorizes, and a share
		// grant may reach a user who is in no project.
		u, ok := jam.UserByIdentity(store, iss, sub)
		if !ok {
			return collab.Principal{}, "", ErrUnbound
		}
		return collab.Principal{UserID: u.ID}, u.Name, nil
	}
}

// AppTokenBearer is the /me plane's Deps.Principal for an app token
// (docs/usage/jam/app-tokens.md): the caller is the token's user, with the
// operator standing recorded at consent. A missing or non-Bearer
// Authorization header is ErrNoCredentials; an unknown or expired token, or
// one whose user was removed, is ErrInvalidToken. These requests are not
// tracked for revocation: an open tunnel runs on, new requests fail.
func AppTokenBearer(store jam.Store, now func() time.Time, log *slog.Logger) func(*http.Request) (collab.Principal, string, error) {
	return func(r *http.Request) (collab.Principal, string, error) {
		raw, ok := bearerToken(r)
		if !ok {
			return collab.Principal{}, "", ErrNoCredentials
		}
		t, u, err := jam.ResolveAppToken(store, raw, now(), log)
		if err != nil {
			return collab.Principal{}, "", ErrInvalidToken
		}
		return collab.Principal{UserID: u.ID, Operator: t.OperatorID}, u.Name, nil
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
