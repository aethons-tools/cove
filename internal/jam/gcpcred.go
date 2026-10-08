package jam

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// gcpScope is the OAuth scope a brokered GCP access token is minted for. Vertex
// AI accepts only cloud-platform; what the token can actually do is bounded by
// the principal's IAM roles and, at the broker, the destination's allow-paths.
const gcpScope = "https://www.googleapis.com/auth/cloud-platform"

// gcpEarlyExpiry refreshes a cached access token this long before it expires,
// so a token handed to a request never lapses mid-flight.
const gcpEarlyExpiry = 5 * time.Minute

// gcpCredentialTypes are the Google credential JSON types a gcp-exchange
// credential may hold. The type is checked before loading (the google
// package's own guidance for operator-supplied JSON).
var gcpCredentialTypes = []google.CredentialsType{
	google.ServiceAccount,
	google.AuthorizedUser,
	google.ExternalAccount,
	google.ImpersonatedServiceAccount,
}

// gcpRetryAfter is how long a failed exchange is answered from memory before
// the supply is re-read and Google asked again, so a lapsed credential under
// steady cove traffic doesn't hammer the token endpoint.
const gcpRetryAfter = 10 * time.Second

// GCPTokenResolver is a CredResolver that exchanges named credentials — each
// supplied (by the base resolver) as a Google credentials JSON — for short-lived
// GCP access tokens, and delegates every other name to the base. The JSON is
// held in memory; tokens are cached and refreshed on demand by the google
// token source (gcpEarlyExpiry before expiry), so the broker always injects a
// current token and the JSON never leaves the host.
//
// When a refresh fails (a user ADC whose session lapsed, a revoked key), the
// cached source is dropped and, after gcpRetryAfter, the supply is re-read:
// re-authenticating on the host (e.g. `gcloud auth application-default
// login` under a `command: [cat, <adc file>]` supply) or rotating a key takes
// effect at the next request, without restarting serve. A lapse is logged once
// (a WARN naming the credential and Google's error code) and recovery once.
type GCPTokenResolver struct {
	base  CredResolver
	names []string
	log   *slog.Logger
	now   func() time.Time

	mu      sync.Mutex
	sources map[string]oauth2.TokenSource
	failed  map[string]gcpFailure // names whose last exchange failed
	// newSource builds a token source from a credentials JSON; a test seam.
	newSource func(data []byte) (oauth2.TokenSource, error)
}

// gcpFailure is a name's last failed exchange: when, and the error to answer
// with until gcpRetryAfter has passed.
type gcpFailure struct {
	at  time.Time
	err error
}

// NewGCPTokenResolver wraps base so names are served as GCP access tokens.
// log receives the lapse/recovery records (names and error codes only).
func NewGCPTokenResolver(base CredResolver, names []string, log *slog.Logger) *GCPTokenResolver {
	return &GCPTokenResolver{
		base: base, names: slices.Clone(names), log: log, now: time.Now,
		sources: map[string]oauth2.TokenSource{}, failed: map[string]gcpFailure{},
		newSource: gcpTokenSource,
	}
}

// Resolve returns a current access token for a gcp-exchange name, else the
// base's value. Fails closed; errors never carry the JSON or a token.
func (g *GCPTokenResolver) Resolve(name string) (string, error) {
	if !slices.Contains(g.names, name) {
		return g.base.Resolve(name)
	}
	g.mu.Lock()
	if f, ok := g.failed[name]; ok && g.now().Sub(f.at) < gcpRetryAfter {
		g.mu.Unlock()
		return "", f.err
	}
	g.mu.Unlock()
	ts, err := g.source(name)
	if err != nil {
		g.fail(name, err, "")
		return "", err
	}
	tok, err := ts.Token()
	if err != nil {
		code := "unknown"
		if re, ok := errors.AsType[*oauth2.RetrieveError](err); ok && re.ErrorCode != "" {
			code = re.ErrorCode
		}
		err = fmt.Errorf("credential %q: GCP token refresh failed (%s); re-authenticate or replace the supplied credentials — Jam re-reads them at the next request", name, code)
		g.fail(name, err, code)
		return "", err
	}
	g.mu.Lock()
	_, wasFailing := g.failed[name]
	delete(g.failed, name)
	g.mu.Unlock()
	if wasFailing {
		g.log.Info("GCP credential recovered", "cred", name)
	}
	return tok.AccessToken, nil
}

// fail records a failed exchange for name and drops its cached source, so the
// next attempt re-reads the supply. The first failure of a streak is logged.
func (g *GCPTokenResolver) fail(name string, err error, code string) {
	g.mu.Lock()
	_, already := g.failed[name]
	g.failed[name] = gcpFailure{at: g.now(), err: err}
	delete(g.sources, name)
	g.mu.Unlock()
	if !already {
		g.log.Warn("GCP credential unavailable; brokered requests fail until it is re-authenticated or replaced", "cred", name, "error_code", code)
	}
}

// Load builds the token source for every gcp-exchange name now, so a missing
// or malformed credentials JSON fails serve at startup rather than at the first
// request. It does not mint a token (no network), so a JSON that parses but
// can't sign or refresh (e.g. a malformed private key) fails at first use.
func (g *GCPTokenResolver) Load() error {
	for _, n := range g.names {
		if _, err := g.source(n); err != nil {
			return err
		}
	}
	return nil
}

// source returns name's cached token source, building it on first use. A
// failed build is not cached, so a fixed supply is picked up on retry.
func (g *GCPTokenResolver) source(name string) (oauth2.TokenSource, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if ts, ok := g.sources[name]; ok {
		return ts, nil
	}
	data, err := g.base.Resolve(name)
	if err != nil {
		return nil, err
	}
	ts, err := g.newSource([]byte(data))
	if err != nil {
		return nil, fmt.Errorf("credential %q: %w", name, err)
	}
	g.sources[name] = ts
	return ts, nil
}

// gcpTokenSource loads a Google credentials JSON of an accepted type into a
// refreshing token source for gcpScope.
func gcpTokenSource(data []byte) (oauth2.TokenSource, error) {
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return nil, errors.New("not a Google credentials JSON")
	}
	t := google.CredentialsType(head.Type)
	if !slices.Contains(gcpCredentialTypes, t) {
		return nil, fmt.Errorf("Google credentials type %q is not supported (want service_account, authorized_user, external_account or impersonated_service_account)", head.Type)
	}
	// Background: the token source outlives any one request.
	creds, err := google.CredentialsFromJSONWithType(context.Background(), data, t, gcpScope)
	if err != nil {
		return nil, errors.New("invalid Google credentials JSON") // never echo the parser's view of the secret
	}
	return oauth2.ReuseTokenSourceWithExpiry(nil, creds.TokenSource, gcpEarlyExpiry), nil
}
