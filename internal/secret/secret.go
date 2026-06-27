// Package secret resolves declared secrets by running a host command per
// secret and capturing its stdout. Fails closed.
package secret

import (
	"fmt"
	"strings"

	"github.com/aethons-tools/cove/internal/runner"
)

// Spec is a secret name and how to produce its value: either a literal Value
// (when Literal is set) or a host Command to run.
type Spec struct {
	Name    string
	Command []string
	Value   string
	Literal bool
}

// Resolve produces name->value for each spec. A literal spec contributes its
// Value directly (no command run); otherwise the spec's command is executed and
// its trimmed stdout used. Any command failure aborts with an error naming the
// secret; no partial map is returned.
func Resolve(r runner.Runner, specs []Spec) (map[string]string, error) {
	env := make(map[string]string, len(specs))
	for _, s := range specs {
		if s.Literal {
			env[s.Name] = s.Value
			continue
		}
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
