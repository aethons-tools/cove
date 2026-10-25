package collabhttp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"html"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"path"
	"slices"
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
	// PreviewLoginCookie binds a login to the browser that started it: set on
	// the preview host by LoginRedirect, its SHA-256 rides the login into the
	// code, and the trade (/__jam/session) requires it, then clears it.
	PreviewLoginCookie = "__Host-jam_preview_login"
	// FrameCookie names the frame session cookie set by /__jam/frame: host-only
	// and Partitioned (CHIPS), so it lives only in the Jam UI's partition and
	// is honoured only where frameCookieUseAllowed says.
	FrameCookie = "__Host-jam_frame"
	// PreviewLoginTTL is how long a started login may take.
	PreviewLoginTTL = 10 * time.Minute
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
	// Service main builds over browserauth.PreviewMount(pa.Complete, pa.ValidLoginReturn).
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
	// Frames holds the one-time tickets the Jam UI mints to frame a preview;
	// /__jam/frame trades one for a frame session. Nil: framing off (404).
	Frames *FrameTickets
	// FrameAncestors are the Jam UI's origins (ui-origins), the only pages
	// that may frame a preview (Content-Security-Policy frame-ancestors); the
	// first is where framed pages post their status (postMessage). Empty:
	// nothing may frame a preview.
	FrameAncestors []string
	Now            func() time.Time
	Log            *slog.Logger
}

// ErrCrossSite: a request carried the preview session cookie in a context
// another page may have caused (Principal) — answered 403, never a login.
var ErrCrossSite = errors.New("cross-site request refused")

// previewEntry is one session or login code: who it is for, the canonical
// preview host it is bound to, and when it lapses.
type previewEntry struct {
	principal collab.Principal
	name      string
	host      string
	expires   time.Time
	bind      [sha256.Size]byte // a code's: SHA-256 of the starting browser's login cookie
	right     collab.Right      // a frame session's ticket right, for audit only (rights are re-checked live)
}

// PreviewAuth logs browsers in to studio previews (docs/usage/jam/previews.md).
// A preview host has no login of its own: a browser without a session is sent
// to the login host (LoginRedirect), which runs the OIDC login and then
// bounces back with a single-use code bound to the preview host (Complete);
// the preview host trades the code for a host-only __Host-jam_preview cookie
// (PreviewHostRoutes). Sessions and codes live in memory, keyed by the SHA-256
// of their random value — the raw values are never stored or logged.
type PreviewAuth struct {
	o PreviewAuthOptions

	mu            sync.Mutex
	sessions      map[[sha256.Size]byte]previewEntry
	codes         map[[sha256.Size]byte]previewEntry
	frameSessions map[[sha256.Size]byte]previewEntry
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
	// An origin may be configured with a trailing "/"; CSP and postMessage
	// want the bare origin.
	anc := make([]string, 0, len(o.FrameAncestors))
	for _, a := range o.FrameAncestors {
		anc = append(anc, strings.TrimSuffix(a, "/"))
	}
	o.FrameAncestors = anc
	return &PreviewAuth{
		o:             o,
		sessions:      map[[sha256.Size]byte]previewEntry{},
		codes:         map[[sha256.Size]byte]previewEntry{},
		frameSessions: map[[sha256.Size]byte]previewEntry{},
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
	if a.o.Frames != nil {
		a.o.Frames.Prune()
	}
	now := a.o.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, m := range []map[[sha256.Size]byte]previewEntry{a.sessions, a.codes, a.frameSessions} {
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

// Principal is PrincipalFrame without the framed report.
func (a *PreviewAuth) Principal(r *http.Request) (collab.Principal, string, error) {
	p, name, _, err := a.PrincipalFrame(r)
	return p, name, err
}

// PrincipalFrame names the caller of a request on a preview host, and
// reports framed when the frame cookie named it:
//  1. its __Host-jam_preview session when it carries one (unknown, expired or
//     issued for another host ⇒ ErrNoCredentials; more than one such cookie
//     is also ErrNoCredentials, never a pick; an unprefixed jam_preview is
//     ignored); carrying it in a context another site's page may have caused
//     is ErrCrossSite (cookieUseAllowed);
//  2. else its __Host-jam_frame session, by the same rules but honoured only
//     where frameCookieUseAllowed says; elsewhere it is ignored on a
//     top-level navigation or a frame navigation (frameCookieIgnored:
//     no credentials, so the caller logs in) and is ErrCrossSite otherwise;
//  3. else its Authorization bearer via Bearer, else ErrNoCredentials.
func (a *PreviewAuth) PrincipalFrame(r *http.Request) (collab.Principal, string, bool, error) {
	var previewVals, frameVals []string
	for _, c := range r.Cookies() {
		switch c.Name {
		case PreviewCookie:
			previewVals = append(previewVals, c.Value)
		case FrameCookie:
			frameVals = append(frameVals, c.Value)
		}
	}
	if len(previewVals) > 0 {
		if !a.cookieUseAllowed(r) {
			return collab.Principal{}, "", false, ErrCrossSite
		}
		p, name, err := a.lookupSession(a.sessions, previewVals, r.Host)
		return p, name, false, err
	}
	if len(frameVals) > 0 && !frameCookieIgnored(r) {
		if !frameCookieUseAllowed(r) {
			return collab.Principal{}, "", false, ErrCrossSite
		}
		p, name, err := a.lookupSession(a.frameSessions, frameVals, r.Host)
		return p, name, err == nil, err
	}
	if a.o.Bearer == nil {
		return collab.Principal{}, "", false, ErrNoCredentials
	}
	p, name, err := a.o.Bearer(r)
	return p, name, false, err
}

// lookupSession resolves the session cookie values vals in m for host:
// exactly one value, known, unexpired and issued for host; else
// ErrNoCredentials. An expired entry it finds is dropped.
func (a *PreviewAuth) lookupSession(m map[[sha256.Size]byte]previewEntry, vals []string, host string) (collab.Principal, string, error) {
	if len(vals) != 1 {
		return collab.Principal{}, "", ErrNoCredentials
	}
	h, ok := a.canonicalHost(host)
	if !ok || vals[0] == "" {
		return collab.Principal{}, "", ErrNoCredentials
	}
	key := sha256.Sum256([]byte(vals[0]))
	now := a.o.Now()
	a.mu.Lock()
	e, found := m[key]
	if found && !now.Before(e.expires) {
		delete(m, key)
		found = false
	}
	a.mu.Unlock()
	if !found || e.host != h {
		return collab.Principal{}, "", ErrNoCredentials
	}
	return e.principal, e.name, nil
}

// frameCookieUseAllowed reports whether r may be answered with its frame
// session cookie. The cookie is SameSite=None (it must reach a cross-site
// iframe), so it is honoured only where no other page can have caused the
// request: a WebSocket upgrade only with an Origin of exactly https://<Host>;
// else Sec-Fetch-Site same-origin. Nothing else: not a cross-site or
// same-site iframe navigation — every pane load trades a fresh ticket
// (/__jam/frame), whose
// landing page then navigates the frame same-origin, while an app in the
// pane redirecting or
// linking it to another studio arrives cross-site too (Fetch Metadata
// follows the UI initiator across redirects) — no top-level navigation, no
// same-site iframe (a sibling preview framed in the same pane shares the
// cookie partition), and no fallback for a request without Fetch Metadata,
// which every frame-capable browser sends.
func frameCookieUseAllowed(r *http.Request) bool {
	if isWebSocketUpgrade(r) {
		o := r.Header.Get("Origin")
		return o != "" && o == "https://"+r.Host
	}
	return r.Header.Get("Sec-Fetch-Site") == "same-origin"
}

// frameCookieIgnored reports whether r's frame cookie, when not allowed
// (frameCookieUseAllowed), counts as absent rather than refused: on a
// top-level navigation (a browser that does not partition cookies sends it
// there) or a frame navigation (isFrameNavigation: the pane without a
// ticket, e.g. a UI reload), so the viewer is asked to log in (LoginRedirect)
// instead of seeing a 403.
func frameCookieIgnored(r *http.Request) bool {
	if frameCookieUseAllowed(r) {
		return false
	}
	h := r.Header
	return isFrameNavigation(r) || (h.Get("Sec-Fetch-Mode") == "navigate" && h.Get("Sec-Fetch-Dest") == "document")
}

// isFrameNavigation reports whether r is an iframe navigation another site's
// page started: Sec-Fetch-Site cross-site or same-site (the Jam UI may share
// the previews' registrable domain), never same-origin, none or absent. Only
// the frame exchange trusts it, and a valid single-use host-bound ticket and
// frame-ancestors (the UI origins) still bound it. Accepting same-site does
// not expose the frame cookie, which no navigation carries
// (frameCookieUseAllowed); it only lets a sibling studio replay a ticket its
// owner minted for themselves — the session-fixation edge a redirect already
// has (docs/usage/jam/previews.md).
func isFrameNavigation(r *http.Request) bool {
	h := r.Header
	site := h.Get("Sec-Fetch-Site")
	return (site == "cross-site" || site == "same-site") && h.Get("Sec-Fetch-Mode") == "navigate" &&
		h.Get("Sec-Fetch-Dest") == "iframe"
}

// cookieUseAllowed reports whether r may be answered with its preview session
// cookie. Every preview host is one site (*.<domain>), so SameSite=Lax still
// sends the cookie on a fetch, form, iframe or WebSocket that another studio's
// page starts; this refuses those. Allowed: a WebSocket only with an Origin of
// this session's preview hosts (sameSession); else with Fetch Metadata,
// Sec-Fetch-Site same-origin or none, a top-level GET/HEAD navigation, or a
// sameSession Origin; else (an older browser) a safe method, or an unsafe one
// whose Origin — or, with no Origin, Referer — is sameSession.
func (a *PreviewAuth) cookieUseAllowed(r *http.Request) bool {
	host, ok := a.canonicalHost(r.Host)
	if !ok {
		return true // not a preview host: Principal refuses the cookie anyway
	}
	origin, hasOrigin := r.Header.Get("Origin"), r.Header.Get("Origin") != ""
	if isWebSocketUpgrade(r) {
		return hasOrigin && a.sameSession(origin, host)
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" {
		switch {
		case site == "same-origin" || site == "none":
			return true
		case r.Header.Get("Sec-Fetch-Mode") == "navigate" && r.Header.Get("Sec-Fetch-Dest") == "document" &&
			(r.Method == http.MethodGet || r.Method == http.MethodHead):
			return true
		}
		return hasOrigin && a.sameSession(origin, host)
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	if hasOrigin {
		return a.sameSession(origin, host)
	}
	ref := r.Header.Get("Referer")
	return ref != "" && a.sameSession(ref, host)
}

// sameSession reports whether u (an Origin or Referer) is an https URL on
// host or on another preview host of the same session label.
func (a *PreviewAuth) sameSession(u, host string) bool {
	p, err := url.Parse(u)
	if err != nil || p.Scheme != "https" || p.Host == "" || p.User != nil {
		return false
	}
	h, ok := a.canonicalHost(p.Host)
	if !ok {
		return false
	}
	if h == host {
		return true
	}
	_, l1, _ := preview.Parse(h, a.o.Domain)
	_, l2, _ := preview.Parse(host, a.o.Domain)
	return l1 == l2
}

func isWebSocketUpgrade(r *http.Request) bool {
	return strings.EqualFold(strings.TrimSpace(r.Header.Get("Upgrade")), "websocket")
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

// ValidLoginReturn reports whether v is a login's bound return value,
// "<bind>|<return_to>": bind the hex SHA-256 of the starting browser's login
// cookie (validBind) and return_to as ValidReturn. It is the preview login
// mount's ReturnTo; Complete splits it.
func (a *PreviewAuth) ValidLoginReturn(v string) bool {
	bind, rt, ok := strings.Cut(v, "|")
	return ok && validBind(bind) && a.ValidReturn(rt)
}

// validBind reports whether b is a SHA-256 in lowercase hex.
func validBind(b string) bool {
	if len(b) != 2*sha256.Size {
		return false
	}
	_, err := hex.DecodeString(b)
	return err == nil && strings.ToLower(b) == b
}

// LoginHostHandler serves the login host jam.<domain>: the browserauth login
// routes under /__jam/auth/, and GET /__jam/start?return_to=<preview URL>
// &bind=<hex SHA-256 of the login cookie>, which vets both and starts a login
// carrying them ("<bind>|<return_to>", ValidLoginReturn). Any other path — and any
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
		bind := r.URL.Query().Get("bind")
		if !validBind(bind) {
			http.Error(w, "invalid login link: open the preview to log in", http.StatusBadRequest)
			return
		}
		http.Redirect(w, r, "/__jam/auth/login?return_to="+url.QueryEscape(bind+"|"+rt), http.StatusFound)
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
// single-use code bound to returnTo's host and to the starting browser
// (returnTo is "<bind>|<preview URL>", ValidLoginReturn), and bounces the
// browser there:
// https://<host>/__jam/session?code=…&return_to=<path?query>.
func (a *PreviewAuth) Complete(w http.ResponseWriter, r *http.Request, idToken, accessToken, returnTo string) {
	if !a.isLoginHost(r.Host) {
		http.NotFound(w, r)
		return
	}
	if !a.ValidLoginReturn(returnTo) {
		http.Error(w, "invalid return_to: want an https preview URL", http.StatusBadRequest)
		return
	}
	bindHex, returnTo, _ := strings.Cut(returnTo, "|")
	var bind [sha256.Size]byte
	_, _ = hex.Decode(bind[:], []byte(bindHex)) // ValidLoginReturn checked it
	rt, _ := url.Parse(returnTo)                // ValidReturn parsed it
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
	e.host, e.expires, e.bind = host, a.o.Now().Add(PreviewCodeTTL), bind
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
//     this host (single use, 60 s) and started in this browser (its
//     __Host-jam_preview_login cookie, then cleared) for a host-only
//     __Host-jam_preview cookie and redirects to return_to, which must be a
//     path (not "//…" or "/\…");
//   - GET /__jam/frame?t=… trades a frame ticket minted for this host (when
//     Frames is set; else 404) for a partitioned __Host-jam_frame cookie
//     (frame);
//   - GET /__jam/logout drops the session (preview cookie where
//     cookieUseAllowed, frame cookie where frameCookieUseAllowed) and the
//     viewer's frame sessions on this host, and clears the preview cookie
//     (logout);
//   - any other /__jam path is 404.
func (a *PreviewAuth) PreviewHostRoutes(next http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /__jam/session", a.session)
	mux.HandleFunc("GET /__jam/logout", a.logout)
	if a.o.Frames != nil {
		mux.HandleFunc("GET /__jam/frame", a.frame)
	}
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
	// A code is consumed only on its own host, in the browser that started
	// its login: presenting it elsewhere gains nothing and must not let a
	// third party burn it.
	started := found && startedHere(r, e.bind)
	if found && e.host == host && started {
		delete(a.codes, key)
	}
	a.mu.Unlock()
	if !found || e.host != host {
		http.Error(w, "login link expired or not for this host; reload the page to log in again", http.StatusBadRequest)
		return
	}
	if !started {
		// Login CSRF: someone else's code, traded to sign this browser in as them.
		http.Error(w, "login link was not started in this browser; reload the page to log in again", http.StatusBadRequest)
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
	http.SetCookie(w, &http.Cookie{
		Name: PreviewLoginCookie, Value: "", Path: "/",
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode, MaxAge: -1,
	})
	w.Header().Set("Location", rt)
	w.WriteHeader(http.StatusFound)
}

// frame trades a frame ticket (?t=) for a frame session: only in the Jam
// UI's own iframe navigation (isFrameNavigation), only for a ticket minted
// for this host — any lookup spends it — and then sets the partitioned
// __Host-jam_frame cookie and answers 200 with a landing page that sends the
// frame to / itself (frameLandingPage). Not a redirect: a redirected
// navigation keeps the UI as its initiator, so it would arrive cross-site (or
// same-site) and
// the frame cookie would not count; one the preview document starts is
// same-origin.
func (a *PreviewAuth) frame(w http.ResponseWriter, r *http.Request) {
	// The URL carries a ticket: never cache it or leak it onward as a Referer.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if !isFrameNavigation(r) {
		http.Error(w, "frame links open only inside the Jam pane", http.StatusBadRequest)
		return
	}
	host, ok := a.canonicalHost(r.Host)
	t := r.URL.Query().Get("t")
	tk, redeemed := ticketRedeem(a.o.Frames, t, host, ok)
	if !redeemed {
		http.Error(w, "frame link expired — reopen the tab", http.StatusBadRequest)
		return
	}
	sid, skey, err := newSecret()
	if err != nil {
		http.Error(w, "frame login failed", http.StatusInternalServerError)
		return
	}
	a.mu.Lock()
	a.frameSessions[skey] = previewEntry{
		principal: tk.principal, name: tk.name, host: host, right: tk.right,
		expires: a.o.Now().Add(FrameSessionTTL),
	}
	a.mu.Unlock()
	a.o.Log.Info("preview frame opened", "host", host, "operator", tk.principal.Operator,
		"user", string(tk.principal.UserID), "right", string(tk.right))
	http.SetCookie(w, &http.Cookie{
		Name: FrameCookie, Value: sid, Path: "/",
		HttpOnly: true, Secure: true, SameSite: http.SameSiteNoneMode, Partitioned: true,
		MaxAge: int(FrameSessionTTL / time.Second),
	})
	frameLandingPage(w)
}

// frameLandingPage answers the exchange: a page whose script replaces itself
// with / (a same-origin navigation), with a link for when scripts are off.
func frameLandingPage(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, `<!doctype html><meta charset="utf-8"><title>Jam preview</title>`+
		`<script>location.replace("/")</script>`+
		`<noscript><p><a href="/">Open the preview</a></p></noscript>`+"\n")
}

// ticketRedeem redeems t for host, refusing an empty ticket or a host that is
// not a preview host (hostOK false) without a lookup.
func ticketRedeem(f *FrameTickets, t, host string, hostOK bool) (frameTicket, bool) {
	if !hostOK || t == "" {
		return frameTicket{}, false
	}
	return f.Redeem(t, host)
}

// startedHere reports whether r carries a login cookie whose SHA-256 is bind.
func startedHere(r *http.Request, bind [sha256.Size]byte) bool {
	ok := false
	for _, c := range r.Cookies() {
		if c.Name == PreviewLoginCookie && c.Value != "" {
			sum := sha256.Sum256([]byte(c.Value))
			ok = ok || subtle.ConstantTimeCompare(sum[:], bind[:]) == 1
		}
	}
	return ok
}

// logout ends the sessions r's cookies name on this host, each only where
// its cookie counts (cookieUseAllowed, frameCookieUseAllowed): a sibling
// studio's same-site subresource or fetch carries the Lax preview cookie, and
// must neither end it nor, through it, the viewer's pane sessions. The
// preview cookie is cleared only where it counts, for the same reason.
func (a *PreviewAuth) logout(w http.ResponseWriter, r *http.Request) {
	previewOK := a.cookieUseAllowed(r)
	if host, ok := a.canonicalHost(r.Host); ok {
		a.mu.Lock()
		var gone []collab.Principal
		for _, c := range r.Cookies() {
			var m map[[sha256.Size]byte]previewEntry
			switch c.Name {
			case PreviewCookie:
				if !previewOK {
					continue
				}
				m = a.sessions
			case FrameCookie:
				// Only from the pane itself: a sibling studio cannot end it.
				if !frameCookieUseAllowed(r) {
					continue
				}
				m = a.frameSessions
			default:
				continue
			}
			key := sha256.Sum256([]byte(c.Value))
			if e, found := m[key]; found && e.host == host {
				delete(m, key)
				gone = append(gone, e.principal)
			}
		}
		// Logging out of a preview ends the viewer's pane sessions on it too.
		for k, e := range a.frameSessions {
			if e.host == host && slices.Contains(gone, e.principal) {
				delete(a.frameSessions, k)
			}
		}
		a.mu.Unlock()
	}
	if previewOK {
		http.SetCookie(w, &http.Cookie{
			Name: PreviewCookie, Value: "", Path: "/",
			HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode, MaxAge: -1,
		})
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("Logged out of this Jam preview.\n"))
}

// LoginRedirect answers a request on a preview host that has no credentials:
// inside an iframe (Sec-Fetch-Dest: iframe) — the Jam pane, whose frame
// cookie the browser did not keep or that arrived without a fresh ticket
// (a redirect or link from another studio) — it never redirects (an IdP inside a frame
// cannot work) but answers 401 with a small page offering the preview in a
// new tab and telling the parent (framedLoginPage); a browser (Accept includes text/html) gets a __Host-jam_preview_login
// cookie (kept when it already has one, so tabs share it) and is sent to log
// in on the login host, carrying the cookie's SHA-256 as bind, and return to
// this URL; anything else gets 401 with a Bearer challenge.
func (a *PreviewAuth) LoginRedirect(w http.ResponseWriter, r *http.Request) {
	host, ok := a.canonicalHost(r.Host)
	self, login := host, preview.LoginHost(a.o.Domain)
	if _, port, err := net.SplitHostPort(r.Host); err == nil && port != "" {
		self, login = net.JoinHostPort(host, port), net.JoinHostPort(login, port)
	}
	// A "//"-led path would be refused as a return path; collapse it.
	rt := (&url.URL{Scheme: "https", Host: self}).String() + "/" + strings.TrimLeft(r.URL.RequestURI(), "/")
	if ok && r.Header.Get("Sec-Fetch-Dest") == "iframe" {
		framedLoginPage(w, r, rt, a.uiOrigin())
		return
	}
	if ok && strings.Contains(r.Header.Get("Accept"), "text/html") {
		nonce := ""
		if c, err := r.Cookie(PreviewLoginCookie); err == nil && validNonce(c.Value) {
			nonce = c.Value
		} else if nonce, _, err = newSecret(); err != nil {
			http.Error(w, "login failed", http.StatusInternalServerError)
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name: PreviewLoginCookie, Value: nonce, Path: "/",
			HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
			MaxAge: int(PreviewLoginTTL / time.Second),
		})
		sum := sha256.Sum256([]byte(nonce))
		q := "return_to=" + url.QueryEscape(rt) + "&bind=" + hex.EncodeToString(sum[:])
		http.Redirect(w, r, "https://"+login+"/__jam/start?"+q, http.StatusFound)
		return
	}
	w.Header().Set("WWW-Authenticate", `Bearer realm="jam-preview"`)
	http.Error(w, "Jam preview: log in in a browser, or send Authorization: Bearer <token> (at-jam login)", http.StatusUnauthorized)
}

// framedLoginPage answers a framed request r that has no credentials: 401 with
// a page linking to u in a new top-level tab, whose script tells the Jam pane
// (uiOrigin, JSON-encoded; no script when there is none — nothing may frame
// the page then) why: status "cookies" for a same-origin navigation carrying
// no frame cookie (the browser refused the partitioned cookie after the
// exchange, as Safari does), else "signin" (no usable session — a reload, an
// expired session, or a redirect from another studio — so the pane re-mints).
func framedLoginPage(w http.ResponseWriter, r *http.Request, u, uiOrigin string) {
	status, msg := "signin", "Reopen this preview from the Jam pane."
	if _, err := r.Cookie(FrameCookie); err != nil && r.Header.Get("Sec-Fetch-Site") == "same-origin" {
		status, msg = "cookies", "This browser didn't keep the pane's sign-in cookie; open the preview in a new tab."
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusUnauthorized)
	script := ""
	if uiOrigin != "" {
		target, _ := json.Marshal(uiOrigin)
		script = `<script>parent.postMessage({jam:"frame-error",status:"` + status + `"}, ` + string(target) + `)</script>`
	}
	_, _ = io.WriteString(w, `<!doctype html><meta charset="utf-8"><title>Jam preview</title>`+
		`<p>`+html.EscapeString(msg)+
		` <a href="`+html.EscapeString(u)+`" target="_top">Open in new tab</a></p>`+script+"\n")
}

// uiOrigin is the first UI origin, where framed pages post ("" when none).
func (a *PreviewAuth) uiOrigin() string {
	if len(a.o.FrameAncestors) == 0 {
		return ""
	}
	return a.o.FrameAncestors[0]
}

// validNonce reports whether v is shaped as newSecret's value (32 random
// bytes, unpadded base64url), so a malformed login cookie is replaced.
func validNonce(v string) bool {
	b, err := base64.RawURLEncoding.DecodeString(v)
	return err == nil && len(b) == 32
}
