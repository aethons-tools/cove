package appui

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/browserauth"
)

// Sessions resolves the /app session cookies into a Principal.
type Sessions struct {
	Issuer       string
	IDCookie     string
	AccessCookie string
	VerifyID     func(ctx context.Context, raw string) (subject string, err error)
	VerifyAccess func(ctx context.Context, raw string) (jam.Operator, error) // nil = no operator OIDC
	Store        jam.ParticipantStore
}

// Resolve reads the two session cookies. Either verified token authenticates;
// the ID token may name a roster user, the access token may carry admin. A
// verified identity that is neither is SessionForbidden (re-login would
// resolve the same), no verified token is SessionNone.
func (s Sessions) Resolve(r *http.Request) (*http.Request, browserauth.SessionOutcome) {
	var p Principal
	authed := false
	if c, err := r.Cookie(s.IDCookie); err == nil && c.Value != "" {
		if sub, err := s.VerifyID(r.Context(), c.Value); err == nil {
			authed = true
			if u, ok := jam.ParticipantByIdentity(s.Store, s.Issuer, sub); ok {
				p.User = &u
			}
		}
	}
	if s.VerifyAccess != nil {
		if c, err := r.Cookie(s.AccessCookie); err == nil && c.Value != "" {
			if op, err := s.VerifyAccess(r.Context(), c.Value); err == nil {
				authed, p.Admin, p.Operator = true, true, op
			}
		}
	}
	switch {
	case !authed:
		return r, browserauth.SessionNone
	case p.User == nil && !p.Admin:
		return r, browserauth.SessionForbidden
	}
	return WithPrincipal(r, p), browserauth.SessionOK
}

// GateConfig configures NewGate.
type GateConfig struct {
	Sessions      *Sessions                // nil = no browser login (loopback-only)
	Dev           *browserauth.DevIdentity // nil = none
	LoginPath     string                   // "/app/auth/login" when Sessions != nil
	ExpectedHosts []string
	Log           *slog.Logger
}

// NewGate builds the /app gate: loopback is always an admin (the local
// operator, as /ui was), a signed-in loopback viewer keeps their session's
// identity and gains admin, and off-loopback needs a session.
func NewGate(c GateConfig) browserauth.Gate {
	g := browserauth.Gate{LoginPath: c.LoginPath, ExpectedHosts: c.ExpectedHosts, Log: c.Log}
	login := c.LoginPath != ""
	g.Forbidden = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forbidNeither(w, withLogin(r, login))
	})
	g.LoopbackTrust = func(r *http.Request) *http.Request {
		r = withLogin(r, login)
		p := Principal{Admin: true, Local: true, Operator: jam.Operator{ID: "local"}}
		if c.Dev != nil {
			p.Operator.ID = c.Dev.OperatorID()
			if u, ok := c.Dev.Participant(); ok {
				p.User = &u
			}
		}
		return WithPrincipal(r, p)
	}
	if c.Sessions != nil {
		g.Session = func(r *http.Request) (*http.Request, browserauth.SessionOutcome) {
			rr, out := c.Sessions.Resolve(withLogin(r, login))
			if out == browserauth.SessionOK && jam.IsLoopbackRequest(r) {
				p, _ := PrincipalFrom(rr)
				if !p.Admin {
					p.Admin, p.Operator = true, jam.Operator{ID: "local"}
					rr = WithPrincipal(rr, p)
				}
			}
			return rr, out
		}
	}
	return g
}

// RequireUser admits a principal that is a roster member. A local admin with
// no login is asked to sign in (a link when browser login is configured).
func RequireUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := PrincipalFrom(r)
		switch {
		case ok && p.User == nil && p.Local:
			if loginConfigured(r) {
				forbid(w, "Sign in to use the Inbox.", loginPath, "Sign in")
			} else {
				forbid(w, "Sign in to use the Inbox.", "", "")
			}
			return
		case !ok || p.User == nil:
			forbidSignedIn(w, r, "You're signed in, but this area needs a project membership — ask an admin to add you to a roster.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequireAdmin admits an admin principal.
func RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p, ok := PrincipalFrom(r); !ok || !p.Admin {
			forbidSignedIn(w, r, "You're signed in, but this area needs admin rights.")
			return
		}
		next.ServeHTTP(w, r)
	})
}
