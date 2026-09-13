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
)

// RawConfig is the browser-login configuration resolved from operator-auth.oidc.
type RawConfig struct {
	Issuer   string
	ClientID string
	Scope    string
	Audience string
}

// Service serves the /ui/auth/* login routes for the code+PKCE flow.
type Service struct {
	cfg          RawConfig
	authorizeURL string
	tokenURL     string
	idVerifier   *oidc.IDTokenVerifier
	doer         HTTPDoer
	log          *slog.Logger
}

// New discovers the IdP endpoints and builds the ID-token verifier (aud = ClientID).
func New(ctx context.Context, cfg RawConfig, doer HTTPDoer, log *slog.Logger) (*Service, error) {
	provider, err := oidc.NewProvider(ctx, cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery for browser login: %w", err)
	}
	if doer == nil {
		doer = http.DefaultClient
	}
	return &Service{
		cfg:          cfg,
		authorizeURL: provider.Endpoint().AuthURL,
		tokenURL:     provider.Endpoint().TokenURL,
		idVerifier:   provider.Verifier(&oidc.Config{ClientID: cfg.ClientID}),
		doer:         doer,
		log:          log,
	}, nil
}

func (s *Service) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ui/auth/login", s.login)
	mux.HandleFunc("GET /ui/auth/callback", s.callback)
	mux.HandleFunc("GET /ui/auth/logout", s.logout)
	return mux
}

func randToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func isSecure(r *http.Request) bool { return r.TLS != nil }

func redirectURI(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host + "/ui/auth/callback"
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
	rt := safeReturnTo(r.URL.Query().Get("return_to"))
	tempCookie(w, stateCookie, state+"|"+rt, secure)
	tempCookie(w, nonceCookie, nonce, secure)
	tempCookie(w, pkceCookie, verifier, secure)
	http.Redirect(w, r, AuthCodeURL(s.authorizeURL, s.cfg.ClientID, redirectURI(r), s.cfg.Scope, s.cfg.Audience, state, nonce, challenge), http.StatusFound)
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
	idTok, accessTok, err := ExchangeCode(r.Context(), s.doer, s.tokenURL, s.cfg.ClientID, r.URL.Query().Get("code"), pkce.Value, redirectURI(r))
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
		clearTemp(w, n)
	}
	setSession(w, accessTok, isSecure(r))
	http.Redirect(w, r, safeReturnTo(rt), http.StatusFound)
}

func (s *Service) logout(w http.ResponseWriter, r *http.Request) {
	clearSession(w)
	http.Redirect(w, r, "/ui/auth/login", http.StatusFound)
}

// safeReturnTo permits only same-site absolute paths under /ui, defeating open
// redirects. Anything else collapses to /ui/.
func safeReturnTo(p string) string {
	if p == "" || !strings.HasPrefix(p, "/ui") {
		return "/ui/"
	}
	if strings.HasPrefix(p, "//") || strings.Contains(p, "\\") {
		return "/ui/"
	}
	return p
}
