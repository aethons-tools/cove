package browserauth

import (
	"log/slog"
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

// Gate guards the /ui subtree. Loopback requests are always allowed (the trusted
// local operator). Off-loopback requests require a valid session when Sess is
// set (missing/invalid → 302 to LoginPath); when Sess is nil the UI is
// loopback-only and off-loopback is refused.
type Gate struct {
	Sess      *SessionVerifier
	LoginPath string
	Log       *slog.Logger
}

func (g Gate) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if harbor.IsLoopbackRequest(r) {
			next.ServeHTTP(w, r)
			return
		}
		if g.Sess == nil {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if _, ok := g.Sess.verify(r); ok {
			next.ServeHTTP(w, r)
			return
		}
		http.Redirect(w, r, g.LoginPath, http.StatusFound)
	})
}
