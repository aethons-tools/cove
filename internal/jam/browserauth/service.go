package browserauth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"

	"github.com/aethons-tools/cove/internal/jam"
)

// RawConfig is the browser-login configuration resolved from operator-auth.oidc.
type RawConfig struct {
	Issuer   string
	ClientID string
	Scope    string
	Audience string
}

// Service serves the <prefix>/auth/* login routes for the code+PKCE flow of one
// Mount. Operator ("/ui") and participant ("/me") each get a Service over the
// same RawConfig; they differ only in their Mount.
type Service struct {
	cfg          RawConfig
	mount        Mount
	authorizeURL string
	tokenURL     string
	idVerifier   *oidc.IDTokenVerifier
	// verifySubject verifies a session token and returns its subject claim. It
	// wraps idVerifier by default; tests inject a fake to exercise the participant
	// resolver without a live IdP.
	verifySubject func(ctx context.Context, raw string) (string, error)
	doer          HTTPDoer
	log           *slog.Logger
}

// New discovers the IdP endpoints and builds the ID-token verifier (aud = ClientID)
// for the given mount.
func New(ctx context.Context, cfg RawConfig, mount Mount, doer HTTPDoer, log *slog.Logger) (*Service, error) {
	provider, err := oidc.NewProvider(ctx, cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery for browser login: %w", err)
	}
	if doer == nil {
		doer = http.DefaultClient
	}
	s := &Service{
		cfg:          cfg,
		mount:        mount,
		authorizeURL: provider.Endpoint().AuthURL,
		tokenURL:     provider.Endpoint().TokenURL,
		idVerifier:   provider.Verifier(&oidc.Config{ClientID: cfg.ClientID}),
		doer:         doer,
		log:          log,
	}
	s.verifySubject = func(ctx context.Context, raw string) (string, error) {
		t, err := s.idVerifier.Verify(ctx, raw)
		if err != nil {
			return "", err
		}
		return t.Subject, nil
	}
	return s, nil
}

func (s *Service) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+s.mount.loginPath(), s.login)
	mux.HandleFunc("GET "+s.mount.callbackPath(), s.callback)
	mux.HandleFunc("GET "+s.mount.authPrefix()+"/logout", s.logout)
	return mux
}

func randToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func isSecure(r *http.Request) bool { return r.TLS != nil }

func (s *Service) redirectURI(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host + s.mount.callbackPath()
}

func (s *Service) login(w http.ResponseWriter, r *http.Request) {
	state, nonce := randToken(), randToken()
	verifier, challenge, err := PKCE()
	if err != nil {
		http.Error(w, "login setup failed", http.StatusInternalServerError)
		return
	}
	secure := isSecure(r)
	// Bind the post-login destination into the state cookie as "<state>|<return_to>".
	rt := safeReturnTo(s.mount, r.URL.Query().Get("return_to"))
	s.mount.tempCookie(w, stateCookie, state+"|"+rt, secure)
	s.mount.tempCookie(w, nonceCookie, nonce, secure)
	s.mount.tempCookie(w, pkceCookie, verifier, secure)
	http.Redirect(w, r, AuthCodeURL(s.authorizeURL, s.cfg.ClientID, s.redirectURI(r), s.cfg.Scope, s.cfg.Audience, state, nonce, challenge), http.StatusFound)
}

func (s *Service) callback(w http.ResponseWriter, r *http.Request) {
	stateCk, err := r.Cookie(stateCookie)
	if err != nil {
		http.Error(w, "missing login state", http.StatusBadRequest)
		return
	}
	wantState, rt, _ := strings.Cut(stateCk.Value, "|")
	if subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("state")), []byte(wantState)) != 1 {
		http.Error(w, "state mismatch", http.StatusBadRequest)
		return
	}
	pkce, err := r.Cookie(pkceCookie)
	if err != nil {
		http.Error(w, "missing login state", http.StatusBadRequest)
		return
	}
	idTok, accessTok, err := ExchangeCode(r.Context(), s.doer, s.tokenURL, s.cfg.ClientID, r.URL.Query().Get("code"), pkce.Value, s.redirectURI(r))
	if err != nil {
		s.log.Warn("browser login: code exchange failed", "reason", err.Error())
		http.Error(w, "login failed", http.StatusUnauthorized)
		return
	}
	verified, err := s.idVerifier.Verify(r.Context(), idTok)
	if err != nil {
		s.log.Warn("browser login: id token verify failed", "reason", err.Error())
		http.Error(w, "login failed", http.StatusUnauthorized)
		return
	}
	nonceCk, err := r.Cookie(nonceCookie)
	if err != nil || subtle.ConstantTimeCompare([]byte(verified.Nonce), []byte(nonceCk.Value)) != 1 {
		http.Error(w, "nonce mismatch", http.StatusUnauthorized)
		return
	}
	for _, n := range []string{stateCookie, nonceCookie, pkceCookie} {
		s.mount.clearTemp(w, n)
	}
	// The session cookie carries the token the gate re-verifies: the access token
	// for the operator plane (API aud + scope), the ID token for the participant
	// plane (aud = ClientID, mapped to a roster human).
	tok := accessTok
	if s.mount.Session == IDTokenSession {
		tok = idTok
	}
	s.mount.setSession(w, tok, isSecure(r))
	http.Redirect(w, r, safeReturnTo(s.mount, rt), http.StatusFound)
}

func (s *Service) logout(w http.ResponseWriter, r *http.Request) {
	s.mount.clearSession(w)
	http.Redirect(w, r, s.mount.loginPath(), http.StatusFound)
}

// ParticipantSession returns a Gate session verifier for the participant plane:
// it verifies the session's ID token (aud = ClientID), maps its subject to a
// roster human via jam.ParticipantByIdentity, and injects the resolved
// Participant. An unmapped subject fails closed (ok=false → the gate refuses).
func (s *Service) ParticipantSession(store jam.ParticipantStore) func(*http.Request) (*http.Request, bool) {
	return func(r *http.Request) (*http.Request, bool) {
		c, err := r.Cookie(s.mount.SessionCookie)
		if err != nil || c.Value == "" {
			return r, false
		}
		sub, err := s.verifySubject(r.Context(), c.Value)
		if err != nil {
			return r, false
		}
		p, ok := jam.ParticipantByIdentity(store, s.cfg.Issuer, sub)
		if !ok {
			return r, false
		}
		return jam.WithParticipant(r, p), true
	}
}

// safeReturnTo permits only same-site absolute paths under the mount's prefix,
// defeating open redirects. Anything else collapses to "<prefix>/".
func safeReturnTo(m Mount, p string) string {
	home := m.Prefix + "/"
	if p == "" || !strings.HasPrefix(p, m.Prefix) {
		return home
	}
	if strings.HasPrefix(p, "//") || strings.Contains(p, "\\") {
		return home
	}
	return p
}
