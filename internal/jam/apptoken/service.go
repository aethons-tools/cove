// Package apptoken serves app tokens (docs/usage/jam/app-tokens.md): the
// consent page and PKCE code exchange that mint them, bearer resolution on
// /app/user, and the registry that ends a revoked token's open requests.
package apptoken

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/appui"
	"github.com/aethons-tools/cove/internal/jam/browserauth"
	"github.com/aethons-tools/cove/internal/jam/uiassets"
)

// Config configures New.
type Config struct {
	Store          jam.Store
	Sessions       *appui.Sessions // the browser login's session resolver (required)
	Live           *Live           // the open bearer requests; nil = a new one
	TrustedOrigins []string        // extra origins the consent form may post from
	Brand          string          // the title-bar brand; "" = "Jam"
	Now            func() time.Time
	Log            *slog.Logger
}

// Service is the app-token consent and exchange.
type Service struct {
	c     Config
	codes *codes
	live  *Live
}

// New returns the service.
func New(c Config) *Service {
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Log == nil {
		c.Log = slog.New(slog.DiscardHandler)
	}
	if c.Brand == "" {
		c.Brand = "Jam"
	}
	if c.Live == nil {
		c.Live = NewLive()
	}
	return &Service{c: c, codes: newCodes(c.Now), live: c.Live}
}

const (
	consentPath  = "/app/auth/app-token"
	exchangePath = "/app/auth/app-token/exchange"
	maxExchange  = 4 << 10
	// maxConsentForm fits the longest legal redirect (2048 bytes) and state
	// (512) with every byte percent-encoded, plus the other fields.
	maxConsentForm = 16 << 10
	sweepEvery     = 5 * time.Second
	expireEvery    = time.Hour
	consentCSP     = "default-src 'none'; style-src 'self'; form-action 'self' vscode: vscode-insiders: vscodium:; frame-ancestors 'none'"
)

// redirectPrefixes are the only redirect targets: the Jam extension's URI
// handler in VS Code, VS Code Insiders and VSCodium.
var redirectPrefixes = []string{"vscode://aethons.jam/", "vscode-insiders://aethons.jam/", "vscodium://aethons.jam/"}

// ValidRedirect reports whether s may be a consent's redirect: an exact
// prefix match on redirectPrefixes, nothing else.
func ValidRedirect(s string) bool {
	if len(s) > 2048 {
		return false
	}
	for _, p := range redirectPrefixes {
		if strings.HasPrefix(s, p) {
			_, err := url.Parse(s)
			return err == nil
		}
	}
	return false
}

// Routes serves GET and POST /app/auth/app-token and POST
// /app/auth/app-token/exchange.
func (s *Service) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+consentPath, s.consentPage)
	mux.Handle("POST "+consentPath, browserauth.RequireSameOrigin(s.c.TrustedOrigins, http.HandlerFunc(s.consentDecide)))
	mux.HandleFunc("POST "+exchangePath, s.exchange)
	return mux
}

// Run, until ctx ends: every few seconds it prunes expired codes and ends the
// open requests of tokens no longer valid (revoked, expired, user removed);
// hourly it deletes expired tokens.
func (s *Service) Run(ctx context.Context) {
	tick := time.NewTicker(sweepEvery)
	defer tick.Stop()
	var lastExpire time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		s.codes.prune()
		s.sweep()
		if now := s.c.Now(); now.Sub(lastExpire) >= expireEvery {
			lastExpire = now
			gone, err := s.c.Store.DeleteExpiredAppTokens(now)
			if err != nil {
				s.c.Log.Warn("app tokens: expiry sweep failed", "err", err)
			}
			for _, id := range gone {
				s.live.Revoke(id)
			}
		}
	}
}

// consentParams are a consent request's parameters, validated.
type consentParams struct {
	Redirect, State, Challenge, Client string
}

func parseConsent(v url.Values) (consentParams, bool) {
	p := consentParams{Redirect: v.Get("redirect"), State: v.Get("state"), Challenge: v.Get("code_challenge"),
		Client: jam.CleanAppTokenLabel(v.Get("client"))}
	ok := ValidRedirect(p.Redirect) && len(p.State) >= 1 && len(p.State) <= 512 &&
		validChallenge(p.Challenge) && v.Get("code_challenge_method") == "S256"
	return p, ok
}

// validChallenge: a base64url SHA-256, 43 characters.
func validChallenge(c string) bool {
	if len(c) != 43 {
		return false
	}
	_, err := base64.RawURLEncoding.DecodeString(c)
	return err == nil
}

// validVerifier: RFC 7636's 43–128 unreserved characters.
func validVerifier(v string) bool {
	if len(v) < 43 || len(v) > 128 {
		return false
	}
	for _, r := range v {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '.', r == '_', r == '~':
		default:
			return false
		}
	}
	return true
}

func securityHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Content-Security-Policy", consentCSP)
}

func badRequest(w http.ResponseWriter) {
	http.Error(w, "This sign-in link is not valid: start the sign-in again from the app.", http.StatusBadRequest)
}

const needUser = "App tokens need a Jam user in at least one project."

// resolve resolves the browser session. ok=false means it answered.
func (s *Service) resolve(w http.ResponseWriter, r *http.Request, login bool) (appui.Principal, bool) {
	rr, out := s.c.Sessions.Resolve(r)
	switch out {
	case browserauth.SessionNone:
		if login {
			http.Redirect(w, r, "/app/auth/login?return_to="+url.QueryEscape(r.URL.RequestURI()), http.StatusFound)
		} else {
			http.Error(w, "Your sign-in has expired: start the sign-in again from the app.", http.StatusUnauthorized)
		}
		return appui.Principal{}, false
	case browserauth.SessionOK:
		if p, ok := appui.PrincipalFrom(rr); ok && p.User != nil {
			return p, true
		}
	}
	http.Error(w, needUser, http.StatusForbidden)
	return appui.Principal{}, false
}

//go:embed consent.html
var consentHTML string

var consentPage = template.Must(template.New("consent").Parse(consentHTML))

func (s *Service) consentPage(w http.ResponseWriter, r *http.Request) {
	securityHeaders(w)
	cp, ok := parseConsent(r.URL.Query())
	if !ok {
		badRequest(w)
		return
	}
	p, ok := s.resolve(w, r, true)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = consentPage.Execute(w, struct {
		consentParams
		CSS, Brand, User, Until string
		Operator                bool
	}{cp, uiassets.StylesheetHref("/app/static/"), s.c.Brand, p.User.Name,
		s.c.Now().Add(jam.AppTokenTTL).Format("2 January 2006"), p.Admin})
}

func (s *Service) consentDecide(w http.ResponseWriter, r *http.Request) {
	securityHeaders(w)
	r.Body = http.MaxBytesReader(w, r.Body, maxConsentForm)
	if err := r.ParseForm(); err != nil {
		badRequest(w)
		return
	}
	cp, ok := parseConsent(r.PostForm)
	if !ok {
		badRequest(w)
		return
	}
	p, ok := s.resolve(w, r, false)
	if !ok {
		return
	}
	q := url.Values{"state": {cp.State}}
	if r.PostForm.Get("action") == "allow" {
		g := grant{Challenge: cp.Challenge, Redirect: cp.Redirect, UserID: p.User.UserID, Label: cp.Client}
		if p.Admin {
			g.OperatorID = p.Operator.ID
		}
		code, err := s.codes.issue(g)
		if err != nil {
			http.Error(w, "Too many sign-ins in progress: try again in a minute.", http.StatusServiceUnavailable)
			return
		}
		q.Set("code", code)
	} else {
		q.Set("error", "access_denied")
	}
	http.Redirect(w, r, withQuery(cp.Redirect, q), http.StatusSeeOther)
}

// withQuery merges q into the redirect's own query. redirect passed
// ValidRedirect.
func withQuery(redirect string, q url.Values) string {
	u, _ := url.Parse(redirect)
	merged := u.Query()
	for k, v := range q {
		merged[k] = v
	}
	u.RawQuery = merged.Encode()
	return u.String()
}

type exchangeRequest struct {
	Code     string `json:"code"`
	Verifier string `json:"code_verifier"`
	Redirect string `json:"redirect"`
}

type exchangeResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	User      string    `json:"user"`
}

func (s *Service) exchange(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	invalid := func() {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}` + "\n"))
	}
	var req exchangeRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxExchange)).Decode(&req); err != nil {
		invalid()
		return
	}
	g, ok := s.codes.redeem(req.Code) // burnt whatever follows
	if !ok || req.Redirect != g.Redirect || !validVerifier(req.Verifier) {
		invalid()
		return
	}
	sum := sha256.Sum256([]byte(req.Verifier))
	if subtle.ConstantTimeCompare([]byte(base64.RawURLEncoding.EncodeToString(sum[:])), []byte(g.Challenge)) != 1 {
		invalid()
		return
	}
	u, ok := s.c.Store.GetUser(g.UserID)
	if !ok || u.Status != jam.StatusLive {
		invalid()
		return
	}
	t, raw, err := jam.NewAppToken(u.ID, g.Label, g.OperatorID, s.c.Now())
	if err != nil {
		http.Error(w, `{"error":"server_error"}`, http.StatusInternalServerError)
		return
	}
	evicted, err := s.c.Store.PutAppToken(t)
	if err != nil {
		s.c.Log.Warn("app token: store failed", "user", u.ID, "token", t.ID, "err", err)
		invalid()
		return
	}
	for _, id := range evicted {
		s.live.Revoke(id)
		s.c.Log.Info("app token evicted", "user", u.ID, "token", id)
	}
	s.c.Log.Info("app token issued", "user", u.ID, "token", t.ID, "label", t.Label, "operator", t.Operator())
	_ = json.NewEncoder(w).Encode(exchangeResponse{Token: raw, ExpiresAt: t.ExpiresAt, User: u.Name})
}
