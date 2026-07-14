// Package secret resolves declared secrets by running a host command per
// secret and capturing its stdout. Fails closed.
package secret

import (
	"fmt"
	"sort"
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
	// Env is extra KEY=VALUE environment for THIS spec's command only, merged over
	// the caller's extraEnv (spec wins). It carries resolved secret material (e.g. a
	// minter's client secret / App-key content) to the command in memory — never on
	// argv. Nil for ordinary specs.
	Env map[string]string
}

// Resolve produces name->value for each spec. A literal spec contributes its
// Value directly (no command run); otherwise the spec's command is executed and
// its trimmed stdout used. Any command failure aborts with an error naming the
// secret; no partial map is returned.
func Resolve(r runner.Runner, extraEnv map[string]string, specs []Spec) (map[string]string, error) {
	env := make(map[string]string, len(specs))
	for _, s := range specs {
		if s.Literal {
			env[s.Name] = s.Value
			continue
		}
		if len(s.Command) == 0 {
			return nil, fmt.Errorf("secret %q: empty command", s.Name)
		}
		child := flattenEnv(mergeEnv(extraEnv, s.Env))
		out, err := r.OutputEnv(child, s.Command[0], s.Command[1:]...)
		if err != nil {
			return nil, fmt.Errorf("secret %q: resolver command failed: %w", s.Name, err)
		}
		env[s.Name] = strings.TrimSuffix(out, "\n")
	}
	return env, nil
}

// mergeEnv returns base with over applied on top (over wins). base/over may be nil.
func mergeEnv(base, over map[string]string) map[string]string {
	if len(over) == 0 {
		return base
	}
	m := make(map[string]string, len(base)+len(over))
	for k, v := range base {
		m[k] = v
	}
	for k, v := range over {
		m[k] = v
	}
	return m
}

// flattenEnv turns a map into sorted "KEY=VALUE" entries (deterministic).
func flattenEnv(m map[string]string) []string {
	if len(m) == 0 {
		return nil
	}
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	out := make([]string, len(ks))
	for i, k := range ks {
		out[i] = k + "=" + m[k]
	}
	return out
}
