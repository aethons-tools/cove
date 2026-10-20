package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/browserauth"
	"github.com/aethons-tools/cove/internal/jam/collab"
	"github.com/aethons-tools/cove/internal/jam/collabhttp"
	"github.com/aethons-tools/cove/internal/jam/preview"
)

// Studio previews (docs/usage/jam/collaboration.md#previews) ride the admin
// listener: its TLS config carries the previews' wildcard cert beside the
// admin cert (picked by SNI), and hostRouter sends preview and login hosts to
// the preview handler, everything else to the admin API.

// adminTLSConfig presents previewCert to a client whose SNI is the previews
// domain or under it, and adminCert to every other client (no SNI, or any
// other name), so the choice never depends on certificate matching order.
func adminTLSConfig(adminCert, previewCert tls.Certificate, domain string) *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		// Served without consulting GetCertificate when the client sends no SNI.
		Certificates: []tls.Certificate{adminCert},
		GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
			if hello.ServerName != "" && preview.UnderDomain(hello.ServerName, domain) {
				return &previewCert, nil
			}
			return &adminCert, nil
		},
	}
}

// hostRouter dispatches a request on its Host: domain itself or any name
// under it goes to previews (which 404s what it cannot parse), so no name
// the wildcard cert covers ever reaches the admin API; anything else to rest.
func hostRouter(domain string, previews, rest http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if preview.UnderDomain(r.Host, domain) {
			previews.ServeHTTP(w, r)
			return
		}
		rest.ServeHTTP(w, r)
	})
}

// loadPreviewCert loads the previews' cert/key pair and checks its leaf covers
// both a preview host and the login host under domain, so a wrong cert fails
// serve at startup rather than every viewer's handshake. Errors name the files
// and hosts, never key material.
func loadPreviewCert(certFile, keyFile, domain string) (tls.Certificate, error) {
	c, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("previews.tls (%s, %s): %w", certFile, keyFile, err)
	}
	leaf := c.Leaf
	if leaf == nil {
		if leaf, err = x509.ParseCertificate(c.Certificate[0]); err != nil {
			return tls.Certificate{}, fmt.Errorf("previews.tls.cert %s: %w", certFile, err)
		}
	}
	sample := "x--" + strings.Repeat("a", 26) + "." + domain
	for _, h := range []string{sample, preview.LoginHost(domain)} {
		if leaf.VerifyHostname(h) != nil {
			return tls.Certificate{}, fmt.Errorf("previews.tls.cert %s does not cover %s (want a certificate for *.%s)", certFile, h, domain)
		}
	}
	return c, nil
}

// newPreviewAuth builds the previews' login (collabhttp.PreviewAuth) over the
// /ui browser client (bc: same client id, scope and audience, so a login's
// access token is the operator token). Its login routes are a browserauth
// Service over browserauth.PreviewMount, served on the login host, whose
// redirect URI is https://jam.<domain>/__jam/auth/callback. lc (nil when there
// is no device client) is the CLI login whose participant ID tokens a
// non-browser caller presents as a bearer.
func newPreviewAuth(domain string, bc browserauth.RawConfig, lc *jam.OperatorLoginConfig, auth jam.OperatorAuthenticator, st jam.Store, log *slog.Logger) (*collabhttp.PreviewAuth, error) {
	oidcAuth, ok := auth.(*jam.OIDCAuthenticator)
	if !ok {
		return nil, fmt.Errorf("previews require operator-auth.oidc")
	}
	browserID := oidcAuth.IDTokenVerifier(bc.ClientID)
	var loginRoutes http.Handler // set below: the Service needs pa's hooks
	pa := collabhttp.NewPreviewAuth(collabhttp.PreviewAuthOptions{
		Domain: domain,
		Login:  http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { loginRoutes.ServeHTTP(w, r) }),
		Operator: func(ctx context.Context, tok string) (string, bool) {
			op, err := oidcAuth.VerifyToken(ctx, tok)
			return op.ID, err == nil
		},
		Participant: func(ctx context.Context, tok string) (ident.ID, string, bool) {
			iss, sub, err := browserID(ctx, tok)
			if err != nil {
				return "", "", false
			}
			u, ok := jam.UserByIdentity(st, iss, sub)
			return u.ID, u.Name, ok
		},
		Bearer: previewBearer(oidcAuth, lc, st),
		Now:    time.Now,
		Log:    log,
	})
	svc, err := browserauth.New(context.Background(), bc, browserauth.PreviewMount(pa.Complete, pa.ValidReturn), nil, log)
	if err != nil {
		return nil, fmt.Errorf("preview login: %w", err)
	}
	loginRoutes = svc.Routes()
	return pa, nil
}

// previewBearer names a non-browser preview caller from its Authorization
// bearer: an operator access token (as on the admin API), else — when the CLI
// login is configured — a participant ID token (as on /me/studios/).
func previewBearer(oidcAuth *jam.OIDCAuthenticator, lc *jam.OperatorLoginConfig, st jam.Store) func(*http.Request) (collab.Principal, string, error) {
	operator := operatorPrincipal(st)
	var participant func(*http.Request) (collab.Principal, string, error)
	if lc != nil {
		participant = collabhttp.ParticipantBearer(oidcAuth.IDTokenVerifier(lc.ClientID), st)
	}
	return func(r *http.Request) (collab.Principal, string, error) {
		if op, err := oidcAuth.Authenticate(r); err == nil {
			return operator(jam.WithOperator(r, op))
		}
		if participant == nil {
			if r.Header.Get("Authorization") == "" {
				return collab.Principal{}, "", collabhttp.ErrNoCredentials
			}
			return collab.Principal{}, "", collabhttp.ErrInvalidToken
		}
		return participant(r)
	}
}
