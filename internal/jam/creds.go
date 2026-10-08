package jam

import (
	"fmt"

	"github.com/aethons-tools/cove/internal/runner"
	"github.com/aethons-tools/cove/internal/secret"
)

// CredResolver returns Jam's real downstream credential value by name,
// in-memory. Implementations must never log or persist the value.
type CredResolver interface {
	Resolve(name string) (string, error)
}

// SecretResolver resolves credentials via internal/secret specs (a resolver
// command or a literal per credential), run through a runner.Runner.
type SecretResolver struct {
	r     runner.Runner
	specs map[string]secret.Spec
}

// NewSecretResolver builds a resolver from credential name → secret.Spec.
func NewSecretResolver(r runner.Runner, specs map[string]secret.Spec) *SecretResolver {
	return &SecretResolver{r: r, specs: specs}
}

// Resolve runs the spec for name and returns its value. Fails closed.
func (s *SecretResolver) Resolve(name string) (string, error) {
	spec, ok := s.specs[name]
	if !ok {
		return "", fmt.Errorf("no credential configured for %q", name)
	}
	spec.Name = name
	vals, err := secret.Resolve(s.r, nil, []secret.Spec{spec})
	if err != nil {
		return "", err
	}
	return vals[name], nil
}

// IdentityCredResolver is an optional CredResolver that resolves a credential
// scoped to the requesting identity. The broker prefers it when the resolver
// implements it (the subscription pool: the token depends on the identity's
// bound account). Implementations must never log or persist the value.
type IdentityCredResolver interface {
	ResolveFor(name, identityHash string) (string, error)
}

// PoolCredResolver is an optional CredResolver that says which credential
// name it resolves from the subscription pool. The broker uses it to ensure
// the subscription-OAuth beta on every request carrying a pool token
// (subscriptionOAuthBeta).
type PoolCredResolver interface {
	PoolCredential(name string) bool
}

// ChainResolver routes one configured pool credential name to a Pool (by
// identity) and delegates every other name to a base CredResolver. It satisfies
// both CredResolver and IdentityCredResolver.
type ChainResolver struct {
	base     CredResolver
	pool     *Pool
	poolCred string
}

// NewChainResolver builds a resolver that sends poolCred to pool (identity-scoped)
// and everything else to base.
func NewChainResolver(base CredResolver, pool *Pool, poolCred string) *ChainResolver {
	return &ChainResolver{base: base, pool: pool, poolCred: poolCred}
}

// Resolve serves non-pool credentials. The pool credential requires an identity,
// so a bare Resolve of it fails closed.
func (c *ChainResolver) Resolve(name string) (string, error) {
	if name == c.poolCred {
		return "", fmt.Errorf("credential %q is identity-scoped (pool); no identity presented", name)
	}
	return c.base.Resolve(name)
}

// PoolCredential reports whether name is the pool's credential.
func (c *ChainResolver) PoolCredential(name string) bool { return name == c.poolCred }

// ResolveFor serves the pool credential from the identity's bound account, and
// delegates every other name to the base (identity ignored).
func (c *ChainResolver) ResolveFor(name, identityHash string) (string, error) {
	if name == c.poolCred {
		return c.pool.TokenFor(identityHash)
	}
	return c.base.Resolve(name)
}
