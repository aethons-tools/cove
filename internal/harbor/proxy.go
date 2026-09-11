package harbor

import (
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"
)

// Broker is the credential-injecting reverse proxy: authenticate the identity,
// run the three-question decision, resolve harbor's real credential, rewrite the
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
			w.Header().Set("WWW-Authenticate", `Basic realm="harbor"`)
		}
		http.Error(w, "missing identity", http.StatusUnauthorized)
		return
	}
	id, ok := b.store.Lookup(HashToken(tok))
	if !ok {
		http.Error(w, "unknown identity", http.StatusUnauthorized)
		return
	}
	var ownerRepo string
	if dest.RepoScoped {
		ownerRepo, _ = RepoFromPath(dest.Route, r.URL.Path)
	}
	dec, err := Decide(id, dest, ownerRepo, b.now())
	if err != nil {
		b.log.Warn("broker denied", "identity", id.ID, "destination", dest.Name, "reason", err.Error())
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var cred string
	if dec.NeedCred {
		if cred, err = b.creds.Resolve(dec.CredName); err != nil {
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
	}}
	b.log.Info("broker proxy", "identity", id.ID, "destination", dest.Name, "path", r.URL.Path)
	rp.ServeHTTP(w, r)
}

// presentedToken extracts the caller's identity token from the request per how.
func presentedToken(r *http.Request, how ApplyMethod) (string, bool) {
	switch how {
	case ApplyBearer:
		if s, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok && s != "" {
			return s, true
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

// applyCred sets harbor's real credential on the upstream request.
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
