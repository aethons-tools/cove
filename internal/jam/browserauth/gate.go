package browserauth

import (
	"log/slog"
	"net"
	"net/http"

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
//     session, injecting a principal (the operator god-view: local operator).
//     nil ⇒ a loopback request gets no special trust and must present a session
//     like any other, which is what the participant plane wants (we must know
//     WHICH human, and loopback cannot say).
//   - Session, if non-nil, authenticates a request from its session cookie,
//     returning r with the principal injected and a SessionOutcome. nil ⇒ no
//     session auth (a loopback-only operator UI with no browser login configured).
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
	Log           *slog.Logger
}

func (g Gate) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if jam.IsLoopbackRequest(r) {
			if !g.hostAllowed(r) {
				http.Error(w, "unexpected Host header", http.StatusForbidden)
				return
			}
			if g.LoopbackTrust != nil {
				next.ServeHTTP(w, g.LoopbackTrust(r))
				return
			}
			// No loopback trust (participant plane): fall through to the session
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
				http.Error(w, "signed in, but not authorized here — ask an operator to add your identity to a roster", http.StatusForbidden)
				return
			case SessionNone:
				if g.LoginPath != "" {
					http.Redirect(w, r, g.LoginPath, http.StatusFound)
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

// OperatorLoopbackTrust is the LoopbackTrust hook for the operator UI: a loopback
// request is the trusted local operator.
func OperatorLoopbackTrust() func(*http.Request) *http.Request {
	return func(r *http.Request) *http.Request {
		return jam.WithOperator(r, jam.Operator{ID: "local"})
	}
}

// OperatorSession is the Session hook for the operator UI: it verifies the
// session cookie with Jam's token verifier — the same verification (iss/aud/exp
// + require-scope) as the bearer API — and injects the resolved Operator. A
// missing or invalid session is SessionNone (redirect to login); a verified
// token is SessionOK (any valid operator token is authorized for /ui).
func OperatorSession(auth *jam.OIDCAuthenticator, cookie string) func(*http.Request) (*http.Request, SessionOutcome) {
	return func(r *http.Request) (*http.Request, SessionOutcome) {
		c, err := r.Cookie(cookie)
		if err != nil || c.Value == "" {
			return r, SessionNone
		}
		op, err := auth.VerifyToken(r.Context(), c.Value)
		if err != nil {
			return r, SessionNone
		}
		return jam.WithOperator(r, op), SessionOK
	}
}
