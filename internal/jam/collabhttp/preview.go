package collabhttp

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/attach"
	"github.com/aethons-tools/cove/internal/jam/collab"
	"github.com/aethons-tools/cove/internal/jam/collabwire"
	"github.com/aethons-tools/cove/internal/jam/preview"
)

const (
	// notExposedMsg answers every host and name that serves nothing.
	notExposedMsg = "nothing is exposed at this address"
	// previewAuditEvery is how often one viewer's use of one preview is
	// logged as "preview opened".
	previewAuditEvery = 10 * time.Minute
	// previewMaxConns caps one viewer's pooled connections to one preview port.
	previewMaxConns = 6
	// poolSuffix ends every pool host, so one is never mistaken for a name.
	poolSuffix = ".tunnel"
	// labelRebuildEvery throttles rebuilding the label map on a miss, and
	// labelMissTTL is how long an unknown label is answered 404 from memory.
	labelRebuildEvery = time.Second
	labelMissTTL      = 2 * time.Second
	labelMissMax      = 4096
)

// errPreviewAccessEnded: the caller lost preview between the request's check
// and its tunnel's registration (an unshare raced it).
var errPreviewAccessEnded = errors.New("preview access ended")

// Preview serves the previews domain (docs/usage/jam/previews.md):
// the login host jam.<domain> gets auth.LoginHostHandler; a preview host
// <name>--<label>.<domain> gets auth.PreviewHostRoutes over the proxy, which
// forwards each request to the studio port the session exposes as name —
// through a studio tunnel (Tunnels.Open), for a caller collab.Decide allows
// preview on a live (or woken) studio. Every other host — and every host when
// d.PreviewURL or d.Exposures is nil (previews off) — is 404.
//
// The app never sees Jam's credentials: the proxy drops the preview session
// cookie, a Jam bearer and any client-sent X-Jam-User from the request, and
// any Set-Cookie for a Jam cookie name from the response. Its pooled
// connections are per (viewer, session, port) and registered in d.Live, so an
// unshare, the revocation sweep or Live.CloseUnexposed closes them.
func Preview(d Deps, auth *PreviewAuth, domain string) *PreviewHandler {
	if d.PreviewURL == nil || d.Exposures == nil || auth == nil {
		return &PreviewHandler{Handler: http.HandlerFunc(notExposed)}
	}
	p := &previewProxy{
		h:       &handler{d: withDefaults(d)},
		auth:    auth,
		domain:  strings.ToLower(domain),
		labels:  map[string]string{},
		misses:  map[string]time.Time{},
		targets: map[string]previewTarget{},
		audited: map[previewAuditKey]time.Time{},
	}
	p.rp = &httputil.ReverseProxy{
		Rewrite:        p.rewrite,
		Transport:      p.transport(),
		ModifyResponse: func(resp *http.Response) error { sanitizeResponse(resp.Header); return nil },
		ErrorHandler:   p.proxyError,
		// The proxy's own log lines carry no request URI (a query may carry
		// a secret the app put there).
		ErrorLog: slog.NewLogLogger(p.h.d.Log.Handler(), slog.LevelWarn),
	}
	login := auth.LoginHostHandler()
	routes := auth.PreviewHostRoutes(http.HandlerFunc(p.serve))
	return &PreviewHandler{p: p, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case auth.isLoginHost(r.Host):
			login.ServeHTTP(w, r)
		case isPreviewHost(r.Host, p.domain) && r.Method == http.MethodConnect:
			// Never forwarded: not a plain CONNECT, nor an RFC 8441
			// extended-CONNECT WebSocket (the server does not advertise it,
			// so browsers use HTTP/1.1 Upgrade, which the proxy carries).
			w.Header().Set("Allow", "GET, HEAD, POST, PUT, PATCH, DELETE, OPTIONS")
			http.Error(w, "CONNECT is not proxied to previews", http.StatusMethodNotAllowed)
		case isPreviewHost(r.Host, p.domain):
			routes.ServeHTTP(w, r)
		default:
			notExposed(w, r)
		}
	})}
}

// PreviewHandler is the handler Preview returns.
type PreviewHandler struct {
	http.Handler
	p *previewProxy // nil when previews are off
}

// InvalidateLabels forgets the proxy's rebuild throttle and remembered misses,
// so the next request for an unknown label rescans the store at once. The
// exposure fan-out calls it on any exposure change: a session's first
// preview link must not 404 because another label was rebuilt a moment ago.
// It only takes the cache lock briefly, so a caller under the supervisor's
// notify mutex never waits on a rebuild.
func (h *PreviewHandler) InvalidateLabels() {
	if h == nil || h.p == nil {
		return
	}
	h.p.mu.Lock()
	h.p.rebuilt = time.Time{}
	clear(h.p.misses)
	h.p.mu.Unlock()
}

func isPreviewHost(host, domain string) bool {
	_, _, ok := preview.Parse(host, domain)
	return ok
}

func notExposed(w http.ResponseWriter, _ *http.Request) {
	http.Error(w, notExposedMsg, http.StatusNotFound)
}

// previewProxy forwards preview-host requests to studio ports.
type previewProxy struct {
	h      *handler
	auth   *PreviewAuth
	domain string
	rp     *httputil.ReverseProxy

	mu      sync.Mutex
	labels  map[string]string    // preview label → actor id, of live and idled studios
	misses  map[string]time.Time // unknown label → when its 404 lapses
	rebuilt time.Time            // when labels was last rebuilt
	// rebuildMu serialises rebuilds: concurrent misses share one (each
	// re-checks the map and the throttle once it holds the lock).
	rebuildMu sync.Mutex
	targets   map[string]previewTarget // pool host → what a dial to it reaches
	audited   map[previewAuditKey]time.Time
}

// previewTarget is one viewer's way to one studio port: the transport pools
// connections by its pool host, so viewers never share a connection.
type previewTarget struct {
	actorID string
	port    int
	p       collab.Principal
	user    string // audit user (usr_… or op:<id>)
	name    string // display name
}

type previewAuditKey struct{ session, user, name string }

// proxyCtxKey carries a request's previewRequest from serve to rewrite and
// proxyError.
type proxyCtxKey struct{}

type previewRequest struct {
	host      string // pool host
	port      int
	session   string
	user      string
	name      string // display name, sent as X-Jam-User
	stripAuth bool   // Authorization is a Jam credential
}

// poolHost names t's connection pool. It is opaque (a hash) rather than the
// ids themselves, which need not be DNS-safe; dial maps it back.
func (t previewTarget) poolHost() string {
	sum := sha256.Sum256([]byte(t.user + "\x00" + strconv.Itoa(t.port) + "\x00" + t.actorID))
	return hex.EncodeToString(sum[:16]) + poolSuffix
}

func (p *previewProxy) serve(w http.ResponseWriter, r *http.Request) {
	name, label, ok := preview.Parse(r.Host, p.domain)
	if !ok {
		notExposed(w, r)
		return
	}
	actorID, ok := p.actor(label)
	if !ok {
		notExposed(w, r)
		return
	}
	inst, ok := p.h.d.Store.GetInstance(actorID)
	if !ok || (inst.Phase != jam.PhaseLive && inst.Phase != jam.PhaseIdled) {
		notExposed(w, r)
		return
	}
	port, ok := exposedPort(p.h.d.Exposures(actorID), name)
	if !ok {
		notExposed(w, r)
		return
	}
	pr, display, err := p.auth.Principal(r)
	switch {
	case err == nil:
	case errors.Is(err, ErrNoCredentials):
		p.auth.LoginRedirect(w, r)
		return
	case errors.Is(err, ErrInvalidToken):
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
		http.Error(w, "invalid or expired token; sign in again (at-jam login)", http.StatusUnauthorized)
		return
	case errors.Is(err, ErrUnbound):
		http.Error(w, ErrUnbound.Error(), http.StatusForbidden)
		return
	case errors.Is(err, ErrCrossSite):
		http.Error(w, ErrCrossSite.Error(), http.StatusForbidden)
		return
	default:
		p.h.d.Log.Error("preview: resolving caller failed", "session", actorID, "err", err.Error())
		http.Error(w, "could not resolve caller", http.StatusInternalServerError)
		return
	}
	a, ok := p.h.authorizeInst(w, r, pr, display, inst, collab.Preview)
	if !ok {
		return
	}
	t := previewTarget{actorID: actorID, port: port, p: a.p, user: a.auditUser(), name: a.name}
	host := t.poolHost()
	p.mu.Lock()
	p.targets[host] = t
	p.mu.Unlock()
	p.audit(a, name, port)
	req := &previewRequest{
		host: host, port: port, session: actorID, user: t.user, name: a.name,
		stripAuth: p.jamAuthorization(r),
	}
	p.rp.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), proxyCtxKey{}, req)))
}

// actor maps a preview label to its live or idled studio. A miss rebuilds the
// cached map from the store — at most once per labelRebuildEvery, one rebuild
// shared by concurrent misses — and an unknown label is remembered for
// labelMissTTL, so a burst of made-up hosts costs one store scan. (A label
// first asked for within a second of another rebuild may 404 once.)
func (p *previewProxy) actor(label string) (string, bool) {
	if id, ok, known := p.cached(label); known {
		return id, ok
	}
	p.rebuildMu.Lock()
	defer p.rebuildMu.Unlock()
	if id, ok, known := p.cached(label); known {
		return id, ok
	}
	now := p.h.d.Now()
	p.mu.Lock()
	throttled := !p.rebuilt.IsZero() && now.Sub(p.rebuilt) < labelRebuildEvery
	p.mu.Unlock()
	var ids []string
	if !throttled {
		for _, inst := range p.h.d.Store.ListInstances() {
			if inst.Phase == jam.PhaseLive || inst.Phase == jam.PhaseIdled {
				ids = append(ids, inst.ActorID)
			}
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !throttled {
		p.rebuilt = now
		p.labels = make(map[string]string, len(ids))
		for _, id := range ids {
			p.labels[preview.Label(id)] = id
		}
		clear(p.misses)
		// Forget pool hosts of studios that have gone (a request re-adds a
		// live one's before it is proxied).
		for h, t := range p.targets {
			if _, live := p.labels[preview.Label(t.actorID)]; !live {
				delete(p.targets, h)
			}
		}
		if id, ok := preview.ActorForLabel(label, ids); ok {
			return id, true
		}
	}
	if len(p.misses) >= labelMissMax {
		clear(p.misses)
	}
	p.misses[label] = now.Add(labelMissTTL)
	return "", false
}

// cached answers label from the map or the miss cache; known is false when
// neither knows it.
func (p *previewProxy) cached(label string) (id string, ok, known bool) {
	now := p.h.d.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	if id, ok := p.labels[label]; ok {
		return id, true, true
	}
	if until, ok := p.misses[label]; ok {
		if now.Before(until) {
			return "", false, true
		}
		delete(p.misses, label)
	}
	return "", false, false
}

func exposedPort(exps []jam.Exposure, name string) (int, bool) {
	for _, e := range exps {
		if e.Name == name {
			return e.Port, true
		}
	}
	return 0, false
}

// jamAuthorization reports whether r's Authorization is a Jam credential, to
// be kept from the app: it named the caller (r has no preview session
// cookie), or it is a bearer Jam's Bearer accepts, or one whose unverified
// iss is Jam's issuer (an expired or mis-audienced Jam token is still Jam's).
// An app's own Authorization beside a preview session is passed through.
func (p *previewProxy) jamAuthorization(r *http.Request) bool {
	authz := r.Header.Get("Authorization")
	if authz == "" {
		return false
	}
	viaCookie := false
	for _, c := range r.Cookies() {
		if c.Name == PreviewCookie {
			viaCookie = true
		}
	}
	if !viaCookie {
		return true
	}
	scheme, tok, _ := strings.Cut(authz, " ")
	if !strings.EqualFold(scheme, "Bearer") {
		return false
	}
	if iss := p.auth.o.BearerIssuer; iss != "" && sameIssuer(unverifiedIssuer(strings.TrimSpace(tok)), iss) {
		return true
	}
	if p.auth.o.Bearer == nil {
		return false
	}
	_, _, err := p.auth.o.Bearer(r)
	return err == nil
}

// unverifiedIssuer is a JWT's iss claim, read without verifying it ("" when
// raw is not a JWT). Only ever used to decide what to withhold from an app.
func unverifiedIssuer(raw string) string {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return ""
	}
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return ""
	}
	var claims struct {
		Iss string `json:"iss"`
	}
	if json.Unmarshal(b, &claims) != nil {
		return ""
	}
	return claims.Iss
}

func sameIssuer(a, b string) bool {
	return a != "" && strings.TrimSuffix(a, "/") == strings.TrimSuffix(b, "/")
}

// audit logs "preview opened" for the first request of (session, user, name)
// in each previewAuditEvery.
func (p *previewProxy) audit(a access, name string, port int) {
	now := p.h.d.Now()
	k := previewAuditKey{a.inst.ActorID, a.auditUser(), name}
	p.mu.Lock()
	last, seen := p.audited[k]
	fresh := !seen || now.Sub(last) >= previewAuditEvery
	if fresh {
		p.audited[k] = now
		if len(p.audited) > 4096 {
			for k, t := range p.audited {
				if now.Sub(t) >= previewAuditEvery {
					delete(p.audited, k)
				}
			}
		}
	}
	p.mu.Unlock()
	if fresh {
		p.h.d.Log.Info("preview opened", "user", a.auditUser(), "display", a.name, "session", a.inst.ActorID, "name", name, "port", port, "right", string(collab.Preview), "reason", string(a.reason))
	}
}

// rewrite points the outbound request at its pool host and keeps Jam's
// credentials from the app.
func (p *previewProxy) rewrite(pr *httputil.ProxyRequest) {
	req, _ := pr.In.Context().Value(proxyCtxKey{}).(*previewRequest)
	if req == nil { // serve always sets it
		pr.Out.URL.Host = "invalid" + poolSuffix
		return
	}
	pr.Out.URL.Scheme = "http"
	pr.Out.URL.Host = req.host
	pr.Out.Host = pr.In.Host
	stripJamCookies(pr.Out.Header)
	if req.stripAuth {
		pr.Out.Header.Del("Authorization")
	}
	pr.Out.Header.Del("X-Jam-User")
	// The app hears who forwarded only from Jam (Rewrite already drops the
	// client's X-Forwarded-*).
	pr.Out.Header.Del("Forwarded")
	pr.Out.Header.Del("X-Real-Ip")
	pr.Out.Header.Set("X-Forwarded-Proto", "https")
	pr.Out.Header.Set("X-Forwarded-Host", pr.In.Host)
	pr.Out.Header.Set("X-Jam-User", headerSafe(req.name))
}

// transport is the proxy's one connection pool: by pool host, so per
// (viewer, session, port), HTTP/1.1 only, each dial a studio tunnel.
func (p *previewProxy) transport() *http.Transport {
	return &http.Transport{
		Proxy:             nil, // never the environment's proxy
		DialContext:       p.dial,
		MaxConnsPerHost:   previewMaxConns,
		IdleConnTimeout:   90 * time.Second,
		ForceAttemptHTTP2: false,
	}
}

var errUnknownPoolHost = errors.New("preview: unknown pool host")

// dial opens a tunnel to the pool host's studio port and registers it in Live
// as the viewer's preview connection until it closes.
func (p *previewProxy) dial(ctx context.Context, _, addr string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	p.mu.Lock()
	t, ok := p.targets[host]
	p.mu.Unlock()
	if !ok {
		return nil, errUnknownPoolHost
	}
	ctx, cancel := context.WithTimeout(ctx, openTimeout)
	defer cancel()
	conn, err := p.h.d.Tunnels.Open(ctx, t.actorID, int32(t.port))
	if err != nil {
		return nil, err
	}
	pc := &previewConn{Conn: conn}
	pc.remove = p.h.d.Live.add(&liveEntry{
		LiveConn: collabwire.LiveConn{Session: t.actorID, User: t.user, Name: t.name, Since: p.h.d.Now()},
		userID:   t.p.UserID, operator: t.p.Operator != "",
		right: collab.Preview, port: t.port,
		close: func() { _ = pc.Close() },
	})
	// An unshare removes the grant before it closes registered connections,
	// so re-checking once registered closes one that raced it.
	inst, ok := p.h.d.Store.GetInstance(t.actorID)
	if _, allowed := collab.Decide(p.h.d.Store, p.h.d.Now(), t.p, inst, collab.Preview); !ok || !allowed {
		_ = pc.Close()
		return nil, errPreviewAccessEnded
	}
	return pc, nil
}

// previewConn is a tunnel connection that leaves Live when it closes.
type previewConn struct {
	net.Conn
	once   sync.Once
	remove func()
	err    error
}

func (c *previewConn) Close() error {
	c.once.Do(func() {
		c.err = c.Conn.Close()
		if c.remove != nil {
			c.remove()
		}
	})
	return c.err
}

// proxyError answers a request the proxy could not complete with a plain
// text page (never app content) naming what failed.
func (p *previewProxy) proxyError(w http.ResponseWriter, r *http.Request, err error) {
	req, _ := r.Context().Value(proxyCtxKey{}).(*previewRequest)
	if req == nil {
		req = &previewRequest{}
	}
	if r.Context().Err() != nil && errors.Is(err, context.Canceled) {
		return // the viewer went away
	}
	code, msg := http.StatusBadGateway, fmt.Sprintf("the app at port %d is not answering", req.port)
	switch {
	case errors.Is(err, errPreviewAccessEnded):
		code, msg = http.StatusForbidden, "you may not use "+string(collab.Preview)+" on this studio"
	case errors.Is(err, attach.ErrDialBackTimeout):
		code, msg = http.StatusGatewayTimeout, "studio could not open the preview (is its image current?)"
	case errors.Is(err, context.DeadlineExceeded):
		code, msg = http.StatusGatewayTimeout, "studio did not open the preview in time"
	case errors.Is(err, attach.ErrStudioNotConnected):
		code, msg = http.StatusServiceUnavailable, "studio not connected"
	case errors.Is(err, attach.ErrTooManyTunnels):
		code, msg = http.StatusTooManyRequests, err.Error()
	case errors.Is(err, attach.ErrTunnelsClosed):
		code, msg = http.StatusConflict, err.Error()
	}
	p.h.d.Log.Warn("preview proxy failed", "user", req.user, "session", req.session, "port", req.port, "right", string(collab.Preview), "status", code, "err", err.Error())
	http.Error(w, msg, code)
}

// isJamCookie reports whether a cookie name is one of Jam's (the preview
// session, or the login flow's), case-folded as browsers fold the __Host-
// prefix.
func isJamCookie(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	return n == strings.ToLower(PreviewCookie) || n == "jam_preview" ||
		strings.HasPrefix(n, "__host-jam_oauth") || strings.HasPrefix(n, "jam_oauth")
}

// stripJamCookies drops Jam's cookies from h's Cookie headers, keeping the
// app's own byte-for-byte, joined into one header (or none when none remain).
func stripJamCookies(h http.Header) {
	vals := h.Values("Cookie")
	if len(vals) == 0 {
		return
	}
	var kept []string
	for _, v := range vals {
		for part := range strings.SplitSeq(v, ";") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			name, _, _ := strings.Cut(part, "=")
			if !isJamCookie(name) {
				kept = append(kept, part)
			}
		}
	}
	h.Del("Cookie")
	if len(kept) > 0 {
		h.Set("Cookie", strings.Join(kept, "; "))
	}
}

// sanitizeResponse keeps an app's response from reaching past its own
// preview host: it drops Clear-Site-Data (which clears a whole site's
// cookies — the login host's and every sibling preview's) and sanitises
// Set-Cookie (stripJamSetCookies).
func sanitizeResponse(h http.Header) {
	h.Del("Clear-Site-Data")
	stripJamSetCookies(h)
}

// stripJamSetCookies drops any Set-Cookie for a Jam cookie name, so an app
// can neither fix nor toss a viewer's Jam session, and removes the Domain
// attribute from the rest, so an app's cookies stay host-only and never land
// on another session's preview host or the login host.
func stripJamSetCookies(h http.Header) {
	vals := h.Values("Set-Cookie")
	if len(vals) == 0 {
		return
	}
	var kept []string
	for _, v := range vals {
		pair, _, _ := strings.Cut(v, ";")
		name, _, _ := strings.Cut(pair, "=")
		if isJamCookie(name) {
			continue
		}
		kept = append(kept, withoutDomain(v))
	}
	h.Del("Set-Cookie")
	for _, v := range kept {
		h.Add("Set-Cookie", v)
	}
}

// withoutDomain drops every Domain attribute from a Set-Cookie value, keeping
// the rest byte-for-byte.
func withoutDomain(v string) string {
	parts := strings.Split(v, ";")
	out := parts[:1]
	for _, a := range parts[1:] {
		name, _, _ := strings.Cut(a, "=")
		if strings.EqualFold(strings.TrimSpace(name), "domain") {
			continue
		}
		out = append(out, a)
	}
	return strings.Join(out, ";")
}

// headerSafe drops control characters, so a display name is always a valid
// header value.
func headerSafe(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}
