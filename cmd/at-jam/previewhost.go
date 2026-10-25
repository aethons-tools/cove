package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/browserauth"
	"github.com/aethons-tools/cove/internal/jam/collab"
	"github.com/aethons-tools/cove/internal/jam/collabhttp"
	"github.com/aethons-tools/cove/internal/jam/preview"
	"golang.org/x/net/publicsuffix"
)

// Studio previews (docs/usage/jam/previews.md) ride the admin
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

// previewURLFunc builds the URL a collaborator opens for a preview. Previews
// are served on the admin listener, so the URL names its port unless that is
// 443 — a bare https:// URL would reach the cove-facing listener instead.
func previewURLFunc(domain, adminListen string) func(actorID, name string) (string, error) {
	return func(actorID, name string) (string, error) {
		h, err := preview.Host(domain, name, actorID)
		if err != nil {
			return "", err
		}
		return previewOrigin(h, adminListen) + "/", nil
	}
}

// previewOrigin is https://host, with the admin listener's port unless 443.
func previewOrigin(host, adminListen string) string {
	if _, port, _ := net.SplitHostPort(adminListen); port != "" && port != "443" {
		host = net.JoinHostPort(host, port)
	}
	return "https://" + host
}

// frameURLs mints the Jam pane's frame URLs (userui.Deps.Frames): the
// preview host's ticket exchange, /__jam/frame?t=, on the same origin as
// previewURLFunc's URL, with a ticket minted for that host.
type frameURLs struct {
	tickets             *collabhttp.FrameTickets
	domain, adminListen string
}

func (f frameURLs) FrameURL(p collab.Principal, display, actorID, name string, right collab.Right) (string, error) {
	h, err := preview.Host(f.domain, name, actorID)
	if err != nil {
		return "", err
	}
	t, err := f.tickets.Mint(p, display, h, right)
	if err != nil {
		return "", err
	}
	return previewOrigin(h, f.adminListen) + "/__jam/frame?t=" + url.QueryEscape(t), nil
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
// /app browser client (bc: same client id, scope and audience, so a login's
// access token is the operator token). Its login routes are a browserauth
// Service over browserauth.PreviewMount, served on the login host, whose
// redirect URI is https://jam.<domain>/__jam/auth/callback. lc (nil when there
// is no device client) is the CLI login whose participant ID tokens a
// non-browser caller presents as a bearer. port is the admin listener's, the
// only explicit port a login may return to. uiOrigins (ui-origins) are the
// only pages that may frame a preview; frames (nil: framing off) are the Jam
// pane's frame tickets, pruned by the PreviewAuth's Run.
func newPreviewAuth(domain, port string, uiOrigins []string, frames *collabhttp.FrameTickets, bc browserauth.RawConfig, lc *jam.OperatorLoginConfig, auth jam.OperatorAuthenticator, st jam.Store, log *slog.Logger) (*collabhttp.PreviewAuth, error) {
	oidcAuth, ok := auth.(*jam.OIDCAuthenticator)
	if !ok {
		return nil, fmt.Errorf("previews require operator-auth.oidc")
	}
	browserID := oidcAuth.IDTokenVerifier(bc.ClientID)
	var loginRoutes http.Handler // set below: the Service needs pa's hooks
	pa := collabhttp.NewPreviewAuth(collabhttp.PreviewAuthOptions{
		Domain: domain,
		Port:   port,
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
		Bearer:         previewBearer(oidcAuth, lc, st),
		BearerIssuer:   bc.Issuer,
		Frames:         frames,
		FrameAncestors: uiOrigins,
		Now:            time.Now,
		Log:            log,
	})
	svc, err := browserauth.New(context.Background(), bc, browserauth.PreviewMount(pa.Complete, pa.ValidLoginReturn), nil, log)
	if err != nil {
		return nil, fmt.Errorf("preview login: %w", err)
	}
	loginRoutes = svc.Routes()
	return pa, nil
}

// previewBearer names a non-browser preview caller from its Authorization
// bearer: an operator access token (as on the admin API), else — when the CLI
// login is configured — a participant ID token (as on /me/studios/). A token
// that is not shaped as the CLI client's ID token (idTokenShaped) — e.g. an
// access token that failed operator verification — is not retried as one.
func previewBearer(oidcAuth *jam.OIDCAuthenticator, lc *jam.OperatorLoginConfig, st jam.Store) func(*http.Request) (collab.Principal, string, error) {
	operator := operatorPrincipal(st)
	var participant func(*http.Request) (collab.Principal, string, error)
	if lc != nil {
		verify := oidcAuth.IDTokenVerifier(lc.ClientID)
		participant = collabhttp.ParticipantBearer(func(ctx context.Context, raw string) (string, string, error) {
			if err := idTokenShaped(raw, lc.ClientID); err != nil {
				return "", "", err
			}
			return verify(ctx, raw)
		}, st)
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

// idTokenShaped refuses a bearer that cannot be clientID's ID token before it
// is verified as one: a JWT typed as an access token (typ at+jwt, RFC 9068)
// or whose aud is anything but exactly clientID. It reads the token unverified
// — the verifier still checks signature, issuer and expiry. (A nonce is not
// required: the device flow that mints CLI ID tokens has none.)
func idTokenShaped(raw, clientID string) error {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return fmt.Errorf("not a JWT")
	}
	var hdr struct {
		Typ string `json:"typ"`
	}
	var claims struct {
		Aud json.RawMessage `json:"aud"`
	}
	for i, v := range []any{&hdr, &claims} {
		b, err := base64.RawURLEncoding.DecodeString(parts[i])
		if err != nil || json.Unmarshal(b, v) != nil {
			return fmt.Errorf("malformed JWT")
		}
	}
	if t := strings.ToLower(hdr.Typ); t == "at+jwt" || t == "application/at+jwt" {
		return fmt.Errorf("an access token is not an ID token")
	}
	var one string
	var many []string
	if json.Unmarshal(claims.Aud, &one) == nil {
		many = []string{one}
	} else if json.Unmarshal(claims.Aud, &many) != nil {
		return fmt.Errorf("malformed aud")
	}
	if len(many) != 1 || many[0] != clientID {
		return fmt.Errorf("aud is not the login client")
	}
	return nil
}

// exposureFanout is the supervisor's exposure sink: it passes every change on
// to next (the attach server's tunnel allow-lists) and has the registry close
// the session's open preview connections to any port it no longer exposes.
// It keeps no previous set: the registry judges its open connections against
// the new set, so the first change after a Jam restart closes them too. Like
// every jam.ExposureSink it runs under the supervisor's notify mutex, so it
// only signals (onChange must be as quick): CloseUnexposed collects under its
// lock and closes on its own goroutine.
type exposureFanout struct {
	next jam.ExposureSink
	live interface {
		CloseUnexposed(session string, exposed []int32) int
	}

	onChange func() // called on every change, e.g. the proxy's label-cache invalidation; nil = none
}

func newExposureFanout(next jam.ExposureSink, live interface {
	CloseUnexposed(session string, exposed []int32) int
}, onChange func()) *exposureFanout {
	return &exposureFanout{next: next, live: live, onChange: onChange}
}

// ExposuresChanged implements jam.ExposureSink.
func (f *exposureFanout) ExposuresChanged(actorID string, ports []int32) {
	f.next.ExposuresChanged(actorID, ports)
	if f.onChange != nil {
		f.onChange() // a newly exposed session's first link must not 404 on a stale label cache
	}
	f.live.CloseUnexposed(actorID, ports)
}

// previewSharedSite is an admin host on the previews domain's registrable
// domain (site).
type previewSharedSite struct{ host, site string }

// previewSharedSiteHosts lists the hosts (names; IPs and localhost skipped)
// that share domain's registrable domain (public-suffix eTLD+1). Preview apps
// can set cookies on that registrable domain, which the admin site would
// receive, so previews belong on a separate one.
func previewSharedSiteHosts(domain string, hosts []string) []previewSharedSite {
	site, err := publicsuffix.EffectiveTLDPlusOne(strings.TrimSuffix(strings.ToLower(domain), "."))
	if err != nil {
		return nil
	}
	var out []previewSharedSite
	for _, h := range hosts {
		n := strings.TrimSuffix(strings.ToLower(h), ".")
		if n == "" || n == "localhost" || net.ParseIP(n) != nil {
			continue
		}
		if s, err := publicsuffix.EffectiveTLDPlusOne(n); err == nil && s == site {
			out = append(out, previewSharedSite{host: h, site: site})
		}
	}
	return out
}

// previewAdminHosts is the admin site's host names: admin-listen's host when
// it is set, and ui-hosts.
func previewAdminHosts(c *serveConfig) []string {
	var out []string
	if h, _, err := net.SplitHostPort(c.AdminListen); err == nil && h != "" {
		out = append(out, h)
	}
	return append(out, c.UIHosts...)
}

// paneFramingOn: the Jam pane may frame previews (Studio, View here) only
// when previews are on and ui-origins names the UI; otherwise previews send
// frame-ancestors 'none' and the browser would block the frame unexplained.
func paneFramingOn(c *serveConfig) bool {
	return c.Previews != nil && len(c.UIOrigins) > 0
}

// previewUIOriginsWarning is serve's startup warning when previews are on but
// ui-origins is empty ("" otherwise): no page may then frame a preview.
func previewUIOriginsWarning(c *serveConfig) string {
	if c.Previews == nil || len(c.UIOrigins) > 0 {
		return ""
	}
	return "previews: ui-origins is empty, so the Jam pane can't frame previews; add the UI's own origin"
}
