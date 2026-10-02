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
}

// NewBroker constructs a Broker that matches destinations from the live store.
func NewBroker(store Store, creds CredResolver, log *slog.Logger) *Broker {
	return &Broker{store: store, creds: creds, now: time.Now, log: log}
}

func (b *Broker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	dest, ok := b.store.Match(r.URL.Path)
	if !ok {
		http.Error(w, "no such destination", http.StatusNotFound)
		return
	}
	tok, ok := presentedToken(r, dest.IdentityIn)
	if !ok {
		// Basic-auth clients (git) send credentials only after a challenge; without
		// this header git reports "Authentication failed" and never presents the token.
		if dest.IdentityIn == ApplyBasicPassword {
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
	trimmed := strings.TrimSuffix(dest.Route, "/") // "/anthropic/" -> "/anthropic"
	rp := &httputil.ReverseProxy{Director: func(out *http.Request) {
		out.URL.Scheme = up.Scheme
		out.URL.Host = up.Host
		out.Host = up.Host
		out.URL.Path = strings.TrimPrefix(r.URL.Path, trimmed) // strip the route prefix
		out.Header.Del("Authorization")                        // drop the inbound identity credential
		if dec.NeedCred {
			applyCred(out, dec.Apply, cred)
		}
		if dest.OAuthBeta {
			ensureAnthropicOAuthBeta(out.Header)
		}
	}}
	b.log.Info("broker proxy", "actor", actor.ID, "destination", dest.Name, "path", r.URL.Path)
	rp.ServeHTTP(w, r)
}

// resolveScopes is ScopesFor over the broker's live store.
func (b *Broker) resolveScopes(actor Actor) []Scope { return ScopesFor(b.store, actor) }

// presentedToken extracts the caller's identity token from the request per how.
func presentedToken(r *http.Request, how ApplyMethod) (string, bool) {
	switch how {
	case ApplyBearer:
		// "token <x>" is what gh sends to a GitHub Enterprise host (GH_HOST=<jam>).
		auth := r.Header.Get("Authorization")
		for _, scheme := range []string{"Bearer ", "token "} {
			if s, ok := strings.CutPrefix(auth, scheme); ok && s != "" {
				return s, true
			}
		}
	case ApplyBasicPassword:
		if _, pass, ok := r.BasicAuth(); ok && pass != "" {
			return pass, true
		}
	case ApplyXAPIKey:
		if k := r.Header.Get("X-Api-Key"); k != "" {
			return k, true
		}
	}
	return "", false
}

// anthropicOAuthBeta is the beta flag Anthropic requires for a subscription-OAuth
// bearer. A cove on ANTHROPIC_AUTH_TOKEN doesn't send it, so the broker adds it.
const anthropicOAuthBeta = "oauth-2025-04-20"

// ensureAnthropicOAuthBeta adds anthropicOAuthBeta to the request's anthropic-beta
// header (a comma-separated list), preserving any betas already present and never
// duplicating.
func ensureAnthropicOAuthBeta(h http.Header) {
	existing := h.Get("anthropic-beta")
	if existing == "" {
		h.Set("anthropic-beta", anthropicOAuthBeta)
		return
	}
	for _, b := range strings.Split(existing, ",") {
		if strings.TrimSpace(b) == anthropicOAuthBeta {
			return
		}
	}
	h.Set("anthropic-beta", existing+","+anthropicOAuthBeta)
}

// applyCred sets Jam's real credential on the upstream request.
func applyCred(r *http.Request, how ApplyMethod, cred string) {
	switch how {
	case ApplyBearer:
		r.Header.Set("Authorization", "Bearer "+cred)
	case ApplyBasicPassword:
		r.SetBasicAuth("x-access-token", cred) // git smart-HTTP: any username, PAT as password
	case ApplyXAPIKey:
		r.Header.Set("X-Api-Key", cred) // Anthropic API-key auth; Director already stripped Authorization
	}
}
