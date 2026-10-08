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
// run the three-question decision, check the path against the destination's
// allow_paths, resolve Jam's real credential, rewrite the
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

// operatorPathHint answers a request for an operator surface that reached the
// broker: the usual cause is an admin URL pointed at `listen` instead of
// `admin-listen`.
const operatorPathHint = "not found: this is Jam's agent listener (listen). The admin API and UI are served on admin-listen (e.g. 127.0.0.1:8081) — point --admin-url, or settings.yml admin-url, there."

// operatorPath reports whether path belongs to an operator surface — the admin
// API, the admin UI or the participant intercom — which this listener never
// serves (a destination's own route is matched before this is asked).
func operatorPath(path string) bool {
	for _, p := range []string{"/admin/", "/ui/", "/me/"} {
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}

func (b *Broker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	dest, ok := b.store.Match(r.URL.Path)
	if !ok {
		if operatorPath(r.URL.Path) {
			http.Error(w, operatorPathHint, http.StatusNotFound)
			return
		}
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
	trimmed := strings.TrimSuffix(dest.Route, "/") // "/anthropic/" -> "/anthropic"
	upPath := strings.TrimPrefix(r.URL.Path, trimmed)
	if !dest.PathAllowed(upPath) {
		b.log.Warn("broker denied", "actor", actor.ID, "destination", dest.Name, "reason", "path not in allow_paths", "path", r.URL.Path)
		http.Error(w, "path not allowed on this destination", http.StatusForbidden)
		return
	}
	var cred string
	fromPool := false // the credential is a subscription-OAuth token from the pool
	if dec.NeedCred {
		// Prefer an identity-aware resolver (the subscription pool: the token
		// depends on which account this identity is bound to). tokenHash is the
		// same hash used for Lookup above.
		if ir, ok := b.creds.(IdentityCredResolver); ok {
			cred, err = ir.ResolveFor(dec.CredName, HashToken(tok))
			if pr, ok := b.creds.(PoolCredResolver); ok && pr.PoolCredential(dec.CredName) {
				fromPool = true
			}
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
	rp := &httputil.ReverseProxy{Director: func(out *http.Request) {
		out.URL.Scheme = up.Scheme
		out.URL.Host = up.Host
		out.Host = up.Host
		out.URL.Path = upPath           // the route prefix stripped
		out.URL.RawPath = ""            // forward exactly the path allow_paths checked
		out.Header.Del("Authorization") // never forward a caller's Authorization upstream
		out.Header.Del(in.Header)       // the identity is Jam's, never the upstream's, whatever header it arrived in
		if dec.NeedCred {
			// The decision is the one source for how the credential is applied.
			if spec, ok := outboundSpec(dec.Apply, dec.Dest.ApplySpec); ok {
				spec.apply(out.Header, cred) // never logged
			}
		}
		// Principal rules after the credential; then, for a pool token, the
		// subscription-OAuth beta — last, so no rule can drop it.
		applyHeaderRules(out.Header, rules)
		if fromPool {
			ensureListItem(out.Header, "anthropic-beta", subscriptionOAuthBeta)
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

// subscriptionOAuthBeta is the anthropic-beta item Anthropic requires to accept
// a subscription-OAuth bearer. It belongs to the credential TYPE: the broker
// ensures it on every request whose credential the subscription pool supplied
// (a cove on ANTHROPIC_AUTH_TOKEN does not send it) — whatever the route, the
// actor's model-spec, or its header rules. It replaced the destination
// oauth_beta flag (COV-241).
const subscriptionOAuthBeta = "oauth-2025-04-20"
