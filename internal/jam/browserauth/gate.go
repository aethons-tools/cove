package browserauth

import (
	"log/slog"
	"net"
	"net/http"
	"net/url"

	"github.com/aethons-tools/cove/internal/jam"
)

// SessionOutcome is what a Session hook decides about a request's session.
type SessionOutcome int

const (
	// SessionNone: no usable session — the viewer must (re)authenticate, so the
	// gate redirects to LoginPath (or refuses when none is set).
	SessionNone SessionOutcome = iota
	// SessionOK: authenticated AND authorized — the returned request carries the
	// injected principal and proceeds.
	SessionOK
	// SessionForbidden: a valid session, but the authenticated identity is not
	// authorized for this surface (e.g. a browser subject not bound to any roster
	// human). The gate returns 403 rather than redirecting — re-login would
	// resolve to the same identity, so a redirect would loop.
	SessionForbidden
)

// Gate guards a browser subtree. Its behavior is composed from two hooks:
//
//   - LoopbackTrust, if non-nil, authenticates a loopback request WITHOUT a
//     session, injecting a principal (e.g. the local admin).
//     A loopback request that does carry a valid session (SessionOK) is
//     attributed to that session instead, so a signed-in viewer is known.
//     nil ⇒ a loopback request gets no special trust and must present a session
//     like any other.
//   - Session, if non-nil, authenticates a request from its session cookie,
//     returning r with the principal injected and a SessionOutcome. nil ⇒ no
//     session auth (a loopback-only UI with no browser login configured).
//
// A loopback request is always subject to the Host check first: its Host header
// (port stripped) must be a loopback literal or one of ExpectedHosts, defeating
// DNS rebinding (an attacker name rebound to 127.0.0.1 yields a loopback
// connection with an attacker-controlled Host). A request with no usable session
// is redirected to LoginPath when set, else refused; an authenticated-but-
// unauthorized session is refused with 403 (never redirected — that would loop).
type Gate struct {
	LoopbackTrust func(*http.Request) *http.Request
	Session       func(*http.Request) (*http.Request, SessionOutcome)
	LoginPath     string
	// ExpectedHosts are additional Host values (beyond the loopback literals)
	// accepted on a loopback request — typically a custom hostname that DNS-binds
	// to 127.0.0.1 (e.g. jam.local.example). Empty ⇒ only loopback literals.
	ExpectedHosts []string
	// Forbidden, if non-nil, answers a SessionForbidden request (it must write
	// the 403 itself); nil ⇒ a plain-text 403.
	Forbidden http.Handler
	Log       *slog.Logger
}

func (g Gate) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if jam.IsLoopbackRequest(r) {
			if !g.hostAllowed(r) {
				http.Error(w, "unexpected Host header", http.StatusForbidden)
				return
			}
			if g.LoopbackTrust != nil {
				// A signed-in loopback viewer is attributed to their session (so
				// the UI can tell WHO the operator is); otherwise, or with a bad
				// cookie, loopback trust applies — never a login redirect.
				if g.Session != nil {
					if rr, out := g.Session(r); out == SessionOK {
						next.ServeHTTP(w, rr)
						return
					}
				}
				next.ServeHTTP(w, g.LoopbackTrust(r))
				return
			}
			// No loopback trust: fall through to the session
			// requirement — loopback must log in too.
		}
		if g.Session != nil {
			switch rr, out := g.Session(r); out {
			case SessionOK:
				next.ServeHTTP(w, rr)
				return
			case SessionForbidden:
				// Authenticated but not authorized for this surface (e.g. an OIDC
				// subject not bound to any roster human). Do NOT redirect — re-login
				// resolves to the same identity and would loop.
				if g.Forbidden != nil {
					g.Forbidden.ServeHTTP(w, rr)
					return
				}
				http.Error(w, "signed in, but not authorized here — ask an operator to add your identity to a roster", http.StatusForbidden)
				return
			case SessionNone:
				if g.LoginPath != "" {
					dest := g.LoginPath
					if r.Method == http.MethodGet || r.Method == http.MethodHead {
						// Carry the deep link across login; the login handler
						// only honours return_to paths under its mount.
						dest += "?return_to=" + url.QueryEscape(r.URL.RequestURI())
					}
					http.Redirect(w, r, dest, http.StatusFound)
					return
				}
			}
		}
		http.Error(w, "forbidden", http.StatusForbidden)
	})
}

// hostAllowed reports whether a loopback request's Host (port stripped) is a
// loopback literal or a configured expected host. A browser cannot forge the
// Host header (it derives from the URL authority), so allowing the loopback
// literals unconditionally is safe; a rebound attacker name is not in the set.
func (g Gate) hostAllowed(r *http.Request) bool {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	switch host {
	case "127.0.0.1", "::1", "localhost":
		return true
	}
	for _, h := range g.ExpectedHosts {
		if host == h {
			return true
		}
	}
	return false
}
