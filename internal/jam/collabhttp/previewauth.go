package collabhttp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam/collab"
	"github.com/aethons-tools/cove/internal/jam/preview"
)

const (
	// PreviewCookie names the preview session cookie: host-only, so it reaches
	// exactly the preview host it was issued on. The __Host- prefix makes
	// browsers refuse it with a Domain attribute, so a sibling preview app
	// cannot toss one.
	PreviewCookie = "__Host-jam_preview"
	// PreviewSessionTTL is how long a preview login lasts.
	PreviewSessionTTL = 8 * time.Hour
	// PreviewCodeTTL is how long a login code may wait to be traded.
	PreviewCodeTTL = 60 * time.Second
	// previewPruneEvery is how often Run drops expired sessions and codes.
	previewPruneEvery = 5 * time.Minute
)

// PreviewAuthOptions configures a PreviewAuth.
type PreviewAuthOptions struct {
	// Domain is the previews domain; the login host is jam.<Domain>.
	Domain string
	// Port is the admin listener's port: a return URL may name no port or
	// exactly this one.
	Port string
	// Login serves the login host's /__jam/auth/* routes: the browserauth
	// Service main builds over browserauth.PreviewMount(pa.Complete, pa.ValidReturn).
	Login http.Handler
	// Operator verifies a login's access token as an operator token.
	Operator func(ctx context.Context, accessToken string) (operatorID string, ok bool)
	// Participant verifies a login's ID token and names the user it is bound to.
	Participant func(ctx context.Context, idToken string) (user ident.ID, name string, ok bool)
	// Bearer names a non-browser caller from its Authorization header (an
	// operator access token or a participant ID token). Nil: no bearer access.
	Bearer func(*http.Request) (collab.Principal, string, error)
	// BearerIssuer is the issuer of Jam's own tokens (the operator IdP). The
	// preview proxy keeps any bearer whose unverified iss names it from the
	// app, even one that fails verification. "" = only verified bearers.
	BearerIssuer string
	Now          func() time.Time
	Log          *slog.Logger
}

// previewEntry is one session or login code: who it is for, the canonical
// preview host it is bound to, and when it lapses.
type previewEntry struct {
	principal collab.Principal
	name      string
	host      string
	expires   time.Time
}

// PreviewAuth logs browsers in to studio previews (docs/usage/jam/collaboration.md#previews).
// A preview host has no login of its own: a browser without a session is sent
// to the login host (LoginRedirect), which runs the OIDC login and then
// bounces back with a single-use code bound to the preview host (Complete);
// the preview host trades the code for a host-only __Host-jam_preview cookie
// (PreviewHostRoutes). Sessions and codes live in memory, keyed by the SHA-256
// of their random value — the raw values are never stored or logged.
type PreviewAuth struct {
	o PreviewAuthOptions

	mu       sync.Mutex
	sessions map[[sha256.Size]byte]previewEntry
	codes    map[[sha256.Size]byte]previewEntry
}

// NewPreviewAuth builds a PreviewAuth; call Run to prune expired entries.
func NewPreviewAuth(o PreviewAuthOptions) *PreviewAuth {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	o.Domain = strings.ToLower(o.Domain)
	return &PreviewAuth{
		o:        o,
		sessions: map[[sha256.Size]byte]previewEntry{},
		codes:    map[[sha256.Size]byte]previewEntry{},
	}
}

// Run drops expired sessions and codes every five minutes until ctx ends.
func (a *PreviewAuth) Run(ctx context.Context) {
	t := time.NewTicker(previewPruneEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.prune()
		}
	}
}

func (a *PreviewAuth) prune() {
	now := a.o.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, m := range []map[[sha256.Size]byte]previewEntry{a.sessions, a.codes} {
		for k, e := range m {
			if !now.Before(e.expires) {
				delete(m, k)
			}
		}
	}
}

func newSecret() (raw string, key [sha256.Size]byte, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", key, err
	}
	raw = base64.RawURLEncoding.EncodeToString(b)
	return raw, sha256.Sum256([]byte(raw)), nil
}

// canonicalHost is host lowercased with any :port and one trailing '.'
// dropped, when that is a preview host under the domain; ok is false otherwise.
func (a *PreviewAuth) canonicalHost(host string) (string, bool) {
	h := host
	if hh, _, err := net.SplitHostPort(h); err == nil {
		h = hh
	}
	h = strings.TrimSuffix(strings.ToLower(h), ".")
	if _, _, ok := preview.Parse(h, a.o.Domain); !ok {
		return "", false
	}
	return h, true
}

func (a *PreviewAuth) isLoginHost(host string) bool {
	h := host
	if hh, _, err := net.SplitHostPort(h); err == nil {
		h = hh
	}
	return strings.TrimSuffix(strings.ToLower(h), ".") == preview.LoginHost(a.o.Domain)
}

// Principal names the caller of a request on a preview host: its
// __Host-jam_preview session when it carries one (unknown, expired or issued
// for another host ⇒ ErrNoCredentials; more than one such cookie is also
// ErrNoCredentials, never a pick; an unprefixed jam_preview is ignored), else
// its Authorization bearer via Bearer, else ErrNoCredentials.
func (a *PreviewAuth) Principal(r *http.Request) (collab.Principal, string, error) {
	var vals []string
	for _, c := range r.Cookies() {
		if c.Name == PreviewCookie {
			vals = append(vals, c.Value)
		}
	}
	switch {
	case len(vals) > 1:
		return collab.Principal{}, "", ErrNoCredentials
	case len(vals) == 1:
		host, ok := a.canonicalHost(r.Host)
		if !ok || vals[0] == "" {
			return collab.Principal{}, "", ErrNoCredentials
		}
		key := sha256.Sum256([]byte(vals[0]))
		now := a.o.Now()
		a.mu.Lock()
		e, found := a.sessions[key]
		if found && !now.Before(e.expires) {
			delete(a.sessions, key)
			found = false
		}
		a.mu.Unlock()
		if !found || e.host != host {
			return collab.Principal{}, "", ErrNoCredentials
		}
		return e.principal, e.name, nil
	}
	if a.o.Bearer == nil {
		return collab.Principal{}, "", ErrNoCredentials
	}
	return a.o.Bearer(r)
}

// ValidReturn reports whether u may be a login's return_to: an absolute
// https URL on a preview host (in canonical lowercase form, with no port or
// the admin listener's; never the login host) with no userinfo and no fragment.
func (a *PreviewAuth) ValidReturn(u string) bool {
	if strings.ContainsAny(u, "\\#") {
		return false
	}
	p, err := url.Parse(u)
	if err != nil || p.Scheme != "https" || p.Opaque != "" || p.User != nil || p.Host == "" {
		return false
	}
	if port := p.Port(); strings.HasSuffix(p.Host, ":") || (port != "" && port != a.o.Port) {
		return false
	}
	host, ok := a.canonicalHost(p.Host)
	return ok && host == p.Hostname()
}

// LoginHostHandler serves the login host jam.<domain>: the browserauth login
// routes under /__jam/auth/, and GET /__jam/start?return_to=<preview URL>,
// which vets return_to and starts a login for it. Any other path — and any
// request on another host — is 404, so a login never starts elsewhere.
func (a *PreviewAuth) LoginHostHandler() http.Handler {
	mux := http.NewServeMux()
	if a.o.Login != nil {
		mux.Handle("/__jam/auth/", a.o.Login)
	}
	mux.HandleFunc("GET /__jam/start", func(w http.ResponseWriter, r *http.Request) {
		rt := r.URL.Query().Get("return_to")
		if !a.ValidReturn(rt) {
			http.Error(w, "invalid return_to: want an https preview URL", http.StatusBadRequest)
			return
		}
		http.Redirect(w, r, "/__jam/auth/login?return_to="+url.QueryEscape(rt), http.StatusFound)
	})
	mux.Handle("/", http.NotFoundHandler())
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.isLoginHost(r.Host) {
			http.NotFound(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// Complete is the preview login mount's hook (browserauth.Mount.Complete): it
// names the login — an operator when Operator accepts the access token, else
// the user Participant binds the ID token to; neither is 403 — mints a
// single-use code bound to returnTo's host, and bounces the browser there:
// https://<host>/__jam/session?code=…&return_to=<path?query>.
func (a *PreviewAuth) Complete(w http.ResponseWriter, r *http.Request, idToken, accessToken, returnTo string) {
	if !a.isLoginHost(r.Host) {
		http.NotFound(w, r)
		return
	}
	if !a.ValidReturn(returnTo) {
		http.Error(w, "invalid return_to: want an https preview URL", http.StatusBadRequest)
		return
	}
	rt, _ := url.Parse(returnTo) // ValidReturn parsed it
	host, _ := a.canonicalHost(rt.Host)
	var e previewEntry
	if op, ok := a.verifyOperator(r.Context(), accessToken); ok {
		e = previewEntry{principal: collab.Principal{Operator: op}, name: op}
	} else if user, name, ok := a.verifyParticipant(r.Context(), idToken); ok {
		e = previewEntry{principal: collab.Principal{UserID: user}, name: name}
	} else {
		http.Error(w, "your login is not linked to a Jam user", http.StatusForbidden)
		return
	}
	code, key, err := newSecret()
	if err != nil {
		http.Error(w, "login failed", http.StatusInternalServerError)
		return
	}
	e.host, e.expires = host, a.o.Now().Add(PreviewCodeTTL)
	a.mu.Lock()
	a.codes[key] = e
	a.mu.Unlock()
	q := url.Values{"code": {code}, "return_to": {rt.RequestURI()}}
	http.Redirect(w, r, (&url.URL{Scheme: "https", Host: rt.Host, Path: "/__jam/session", RawQuery: q.Encode()}).String(), http.StatusFound)
}

func (a *PreviewAuth) verifyOperator(ctx context.Context, tok string) (string, bool) {
	if tok == "" || a.o.Operator == nil {
		return "", false
	}
	op, ok := a.o.Operator(ctx, tok)
	return op, ok && op != ""
}

func (a *PreviewAuth) verifyParticipant(ctx context.Context, tok string) (ident.ID, string, bool) {
	if tok == "" || a.o.Participant == nil {
		return "", "", false
	}
	u, name, ok := a.o.Participant(ctx, tok)
	return u, name, ok && u != ""
}

// PreviewHostRoutes serves Jam's reserved /__jam/ paths on a preview host and
// passes everything else to next:
//   - GET /__jam/session?code=…&return_to=/path trades a login code minted for
//     this host (single use, 60 s) for a host-only __Host-jam_preview cookie
//     and redirects to return_to, which must be a path (not "//…" or "/\…");
//   - GET /__jam/logout drops the session and clears the cookie;
//   - any other /__jam path is 404.
func (a *PreviewAuth) PreviewHostRoutes(next http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /__jam/session", a.session)
	mux.HandleFunc("GET /__jam/logout", a.logout)
	mux.Handle("/__jam/", http.NotFoundHandler())
	mux.Handle("/__jam", http.NotFoundHandler())
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Judge the cleaned path, so "/app/../__jam/…" never reaches next.
		if p := path.Clean("/" + r.URL.Path); p == "/__jam" || strings.HasPrefix(p, "/__jam/") {
			mux.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// validReturnPath reports whether p is a same-host path (with optional query)
// safe to redirect to.
func validReturnPath(p string) bool {
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.Contains(p, "\\") {
		return false
	}
	u, err := url.Parse(p)
	return err == nil && u.Scheme == "" && u.Host == "" && u.User == nil
}

func (a *PreviewAuth) session(w http.ResponseWriter, r *http.Request) {
	// The URL carries a code: never cache it or leak it onward as a Referer.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	host, ok := a.canonicalHost(r.Host)
	rt := r.URL.Query().Get("return_to")
	code := r.URL.Query().Get("code")
	if !ok || code == "" || !validReturnPath(rt) {
		http.Error(w, "invalid login link", http.StatusBadRequest)
		return
	}
	key := sha256.Sum256([]byte(code))
	now := a.o.Now()
	a.mu.Lock()
	e, found := a.codes[key]
	if found && !now.Before(e.expires) {
		delete(a.codes, key)
		found = false
	}
	// A code is consumed only on its own host: presenting it elsewhere gains
	// nothing and must not let a third party burn it.
	if found && e.host == host {
		delete(a.codes, key)
	}
	a.mu.Unlock()
	if !found || e.host != host {
		http.Error(w, "login link expired or not for this host; reload the page to log in again", http.StatusBadRequest)
		return
	}
	sid, skey, err := newSecret()
	if err != nil {
		http.Error(w, "login failed", http.StatusInternalServerError)
		return
	}
	e.expires = now.Add(PreviewSessionTTL)
	a.mu.Lock()
	a.sessions[skey] = e
	a.mu.Unlock()
	a.o.Log.Info("preview login", "host", host, "operator", e.principal.Operator, "user", string(e.principal.UserID))
	http.SetCookie(w, &http.Cookie{
		Name: PreviewCookie, Value: sid, Path: "/",
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
		MaxAge: int(PreviewSessionTTL / time.Second),
	})
	w.Header().Set("Location", rt)
	w.WriteHeader(http.StatusFound)
}

func (a *PreviewAuth) logout(w http.ResponseWriter, r *http.Request) {
	if host, ok := a.canonicalHost(r.Host); ok {
		for _, c := range r.Cookies() {
			if c.Name != PreviewCookie {
				continue
			}
			key := sha256.Sum256([]byte(c.Value))
			a.mu.Lock()
			if e, found := a.sessions[key]; found && e.host == host {
				delete(a.sessions, key)
			}
			a.mu.Unlock()
		}
	}
	http.SetCookie(w, &http.Cookie{
		Name: PreviewCookie, Value: "", Path: "/",
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode, MaxAge: -1,
	})
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("Logged out of this Jam preview.\n"))
}

// LoginRedirect answers a request on a preview host that has no credentials:
// a browser (Accept includes text/html) is sent to log in on the login host
// and return to this URL; anything else gets 401 with a Bearer challenge.
func (a *PreviewAuth) LoginRedirect(w http.ResponseWriter, r *http.Request) {
	host, ok := a.canonicalHost(r.Host)
	if ok && strings.Contains(r.Header.Get("Accept"), "text/html") {
		self, login := host, preview.LoginHost(a.o.Domain)
		if _, port, err := net.SplitHostPort(r.Host); err == nil && port != "" {
			self, login = net.JoinHostPort(host, port), net.JoinHostPort(login, port)
		}
		// A "//"-led path would be refused as a return path; collapse it.
		rt := (&url.URL{Scheme: "https", Host: self}).String() + "/" + strings.TrimLeft(r.URL.RequestURI(), "/")
		http.Redirect(w, r, "https://"+login+"/__jam/start?return_to="+url.QueryEscape(rt), http.StatusFound)
		return
	}
	w.Header().Set("WWW-Authenticate", `Bearer realm="jam-preview"`)
	http.Error(w, "Jam preview: log in in a browser, or send Authorization: Bearer <token> (at-jam login)", http.StatusUnauthorized)
}
