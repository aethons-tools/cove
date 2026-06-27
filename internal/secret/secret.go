// Package secret resolves declared secrets by running a host command per
// secret and capturing its stdout. Fails closed.
package secret

import (
	"fmt"
	"strings"

	"github.com/aethons-tools/at-sbx/internal/runner"
)

// Spec is a secret name and the argv that produces its value.
type Spec struct {
	Name    string
	Command []string
}

// Resolve runs each spec's command and returns name->value. Any failure aborts
// with an error naming the secret; no partial map is returned.
func Resolve(r runner.Runner, specs []Spec) (map[string]string, error) {
	env := make(map[string]string, len(specs))
	for _, s := range specs {
		if len(s.Command) == 0 {
			return nil, fmt.Errorf("secret %q: empty command", s.Name)
		}
		out, err := r.Output(s.Command[0], s.Command[1:]...)
		if err != nil {
			return nil, fmt.Errorf("secret %q: resolver command failed: %w", s.Name, err)
		}
		env[s.Name] = strings.TrimSuffix(out, "\n")
	}
	return env, nil
}
