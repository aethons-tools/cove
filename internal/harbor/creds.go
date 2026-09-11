package harbor

import (
	"fmt"

	"github.com/aethons-tools/cove/internal/runner"
	"github.com/aethons-tools/cove/internal/secret"
)

// CredResolver returns harbor's real downstream credential value by name,
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
