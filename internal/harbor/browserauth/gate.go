package browserauth

import (
	"log/slog"
	"net"
	"net/http"

	"github.com/aethons-tools/cove/internal/harbor"
)

// SessionVerifier verifies the browser session cookie using harbor's own token
// verifier — the same verification (iss/aud/exp + require-scope) as the bearer API.
type SessionVerifier struct {
	Auth *harbor.OIDCAuthenticator
}

func (v *SessionVerifier) verify(r *http.Request) (harbor.Operator, bool) {
	c, err := r.Cookie(SessionCookie)
	if err != nil || c.Value == "" {
		return harbor.Operator{}, false
	}
	op, err := v.Auth.VerifyToken(r.Context(), c.Value)
	if err != nil {
		return harbor.Operator{}, false
	}
	return op, true
}

// Gate guards the /ui subtree. A loopback request is trusted as the local
// operator, but only when its Host header is a loopback literal or one of
// ExpectedHosts — otherwise it is refused. This defeats DNS rebinding, where an
// attacker name rebound to 127.0.0.1 yields a loopback connection with an
// attacker-controlled Host. Off-loopback requests require a valid session when
// Sess is set (missing/invalid → 302 to LoginPath); when Sess is nil the UI is
// loopback-only and off-loopback is refused.
type Gate struct {
	Sess      *SessionVerifier
	LoginPath string
	// ExpectedHosts are additional Host values (beyond the loopback literals)
	// accepted on a loopback request — typically a custom hostname that DNS-binds
	// to 127.0.0.1 (e.g. harbor.local.example). Empty ⇒ only loopback literals.
	ExpectedHosts []string
	Log           *slog.Logger
}

func (g Gate) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if harbor.IsLoopbackRequest(r) {
			if !g.hostAllowed(r) {
				http.Error(w, "unexpected Host header", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, harbor.WithOperator(r, harbor.Operator{ID: "local"}))
			return
		}
		if g.Sess == nil {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if op, ok := g.Sess.verify(r); ok {
			next.ServeHTTP(w, harbor.WithOperator(r, op))
			return
		}
		http.Redirect(w, r, g.LoginPath, http.StatusFound)
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
