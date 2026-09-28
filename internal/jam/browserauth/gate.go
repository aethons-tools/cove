package browserauth

import (
	"log/slog"
	"net"
	"net/http"

	"github.com/aethons-tools/cove/internal/jam"
)

// Gate guards a browser subtree. Its behavior is composed from two hooks:
//
//   - LoopbackTrust, if non-nil, authenticates a loopback request WITHOUT a
//     session, injecting a principal (the operator god-view: local operator).
//     nil ⇒ a loopback request gets no special trust and must present a session
//     like any other, which is what the participant plane wants (we must know
//     WHICH human, and loopback cannot say).
//   - Session, if non-nil, authenticates a request from its session cookie,
//     returning r with the principal injected or ok=false. nil ⇒ no session auth
//     (a loopback-only operator UI with no browser login configured).
//
// A loopback request is always subject to the Host check first: its Host header
// (port stripped) must be a loopback literal or one of ExpectedHosts, defeating
// DNS rebinding (an attacker name rebound to 127.0.0.1 yields a loopback
// connection with an attacker-controlled Host). A request that no hook
// authenticates is redirected to LoginPath when set, else refused.
type Gate struct {
	LoopbackTrust func(*http.Request) *http.Request
	Session       func(*http.Request) (*http.Request, bool)
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
			if rr, ok := g.Session(r); ok {
				next.ServeHTTP(w, rr)
				return
			}
			if g.LoginPath != "" {
				http.Redirect(w, r, g.LoginPath, http.StatusFound)
				return
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
// + require-scope) as the bearer API — and injects the resolved Operator.
func OperatorSession(auth *jam.OIDCAuthenticator, cookie string) func(*http.Request) (*http.Request, bool) {
	return func(r *http.Request) (*http.Request, bool) {
		c, err := r.Cookie(cookie)
		if err != nil || c.Value == "" {
			return r, false
		}
		op, err := auth.VerifyToken(r.Context(), c.Value)
		if err != nil {
			return r, false
		}
		return jam.WithOperator(r, op), true
	}
}
