package usersecret

import "github.com/aethons-tools/cove/internal/secret"

// Plan resolves each demanded secret to a runnable or literal Spec, applying the
// precedence: a kit-provided command wins; otherwise the store supplies a value
// (literal) or a command; otherwise the secret is unresolved. It returns the
// resolvable specs in demand order and the names of demanded secrets with no
// supply. Store entries whose names are not demanded are ignored.
func (s Store) Plan(demanded []secret.Spec) (resolvable []secret.Spec, unresolved []string) {
	for _, d := range demanded {
		if len(d.Command) > 0 {
			resolvable = append(resolvable, d)
			continue
		}
		e, ok := s[d.Name]
		if !ok {
			unresolved = append(unresolved, d.Name)
			continue
		}
		if len(e.Command) > 0 {
			resolvable = append(resolvable, secret.Spec{Name: d.Name, Command: e.Command})
		} else {
			resolvable = append(resolvable, secret.Spec{Name: d.Name, Value: e.Value, Literal: true})
		}
	}
	return resolvable, unresolved
}
