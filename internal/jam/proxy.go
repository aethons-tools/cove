package jam

import (
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"
)

// Broker is the credential-injecting reverse proxy: authenticate the identity,
// run the three-question decision, resolve Jam's real credential, rewrite the
// request with it, and proxy to the upstream. Implements http.Handler.
type Broker struct {
	store Store
	creds CredResolver
	now   func() time.Time
	log   *slog.Logger
	warns *warnDedupe // rate-limits the principal header-rule warnings
}

// NewBroker constructs a Broker that matches destinations from the live store.
func NewBroker(store Store, creds CredResolver, log *slog.Logger) *Broker {
	return &Broker{store: store, creds: creds, now: time.Now, log: log, warns: newWarnDedupe(time.Now)}
}

func (b *Broker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	dest, ok := b.store.Match(r.URL.Path)
	if !ok {
		http.Error(w, "no such destination", http.StatusNotFound)
		return
	}
	in, hasIn := dest.InboundSpec()
	tok, ok := "", false
	if hasIn {
		tok, ok = presentedToken(r, in)
	}
	if !ok {
		// Basic-auth clients (git) send credentials only after a challenge; without
		// this header git reports "Authentication failed" and never presents the token.
		if in.Encoding == EncodingBasic {
			w.Header().Set("WWW-Authenticate", `Basic realm="jam"`)
		}
		http.Error(w, "missing identity", http.StatusUnauthorized)
		return
	}
	actor, ok := b.store.Lookup(HashToken(tok))
	if !ok {
		http.Error(w, "unknown identity", http.StatusUnauthorized)
		return
	}
	dec, err := Decide(actor, b.resolveScopes(actor), dest, b.now())
	if err != nil {
		b.log.Warn("broker denied", "actor", actor.ID, "destination", dest.Name, "reason", err.Error())
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var cred string
	if dec.NeedCred {
		// Prefer an identity-aware resolver (the subscription pool: the token
		// depends on which account this identity is bound to). tokenHash is the
		// same hash used for Lookup above.
		if ir, ok := b.creds.(IdentityCredResolver); ok {
			cred, err = ir.ResolveFor(dec.CredName, HashToken(tok))
		} else {
			cred, err = b.creds.Resolve(dec.CredName)
		}
		if err != nil {
			b.log.Error("credential resolve failed", "destination", dest.Name)
			http.Error(w, "credential unavailable", http.StatusBadGateway)
			return
		}
	}
	up, err := url.Parse(dest.Upstream)
	if err != nil {
		http.Error(w, "bad upstream", http.StatusInternalServerError)
		return
	}
	rules := b.principalHeaderRules(actor, dest, in) // resolved and filtered once, outside the Director
	trimmed := strings.TrimSuffix(dest.Route, "/")   // "/anthropic/" -> "/anthropic"
	rp := &httputil.ReverseProxy{Director: func(out *http.Request) {
		out.URL.Scheme = up.Scheme
		out.URL.Host = up.Host
		out.Host = up.Host
		out.URL.Path = strings.TrimPrefix(r.URL.Path, trimmed) // strip the route prefix
		out.Header.Del("Authorization")                        // never forward a caller's Authorization upstream
		out.Header.Del(in.Header)                              // the identity is Jam's, never the upstream's, whatever header it arrived in
		if dec.NeedCred {
			// The decision is the one source for how the credential is applied.
			if spec, ok := outboundSpec(dec.Apply, dec.Dest.ApplySpec); ok {
				spec.apply(out.Header, cred) // never logged
			}
		}
		// Principal rules after the credential but before the destination's
		// oauth_beta ensure, so a set rule can never drop the flag's beta.
		applyHeaderRules(out.Header, rules)
		if dest.OAuthBeta {
			ensureAnthropicOAuthBeta(out.Header)
		}
	}}
	b.log.Info("broker proxy", "actor", actor.ID, "destination", dest.Name, "path", r.URL.Path)
	rp.ServeHTTP(w, r)
}

// resolveScopes is ScopesFor over the broker's live store.
func (b *Broker) resolveScopes(actor Actor) []Scope { return ScopesFor(b.store, actor) }

// presentedToken extracts the caller's identity token from the request per
// the identity-in spec.
func presentedToken(r *http.Request, in InboundSpec) (string, bool) { return in.extract(r.Header) }

// anthropicOAuthBeta is the beta flag Anthropic requires for a subscription-OAuth
// bearer. A cove on ANTHROPIC_AUTH_TOKEN doesn't send it, so the broker adds it.
const anthropicOAuthBeta = "oauth-2025-04-20"

// ensureAnthropicOAuthBeta adds anthropicOAuthBeta to the request's anthropic-beta
// header (a comma-separated list), preserving any betas already present and never
// duplicating — the destination OAuthBeta flag, via the generalized ensureListItem.
func ensureAnthropicOAuthBeta(h http.Header) { ensureListItem(h, "anthropic-beta", anthropicOAuthBeta) }
