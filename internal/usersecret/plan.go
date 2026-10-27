package usersecret

import (
	"fmt"

	"github.com/aethons-tools/cove/internal/secret"
)

// MintExpander turns a resolved minter profile into a runnable secret.Spec (an
// jam mint invocation). Injected by the caller; nil until the minting wiring lands.
type MintExpander func(profileName string, m Minter, demandName string) (secret.Spec, error)

func (st Store) resolve(name string, src Source, expand MintExpander) (secret.Spec, error) {
	kind, err := src.Kind()
	if err != nil {
		return secret.Spec{}, err
	}
	switch kind {
	case "value":
		return secret.Spec{Name: name, Value: *src.Value, Literal: true}, nil
	case "command":
		return secret.Spec{Name: name, Command: src.Command}, nil
	case "global":
		g, ok := st.Global[src.Global]
		if !ok {
			return secret.Spec{}, fmt.Errorf("global %q is not defined", src.Global)
		}
		gk, err := g.Kind()
		if err != nil {
			return secret.Spec{}, fmt.Errorf("global %q: %w", src.Global, err)
		}
		switch gk {
		case "value":
			return secret.Spec{Name: name, Value: *g.Value, Literal: true}, nil
		case "command":
			return secret.Spec{Name: name, Command: g.Command}, nil
		default:
			return secret.Spec{}, fmt.Errorf("global %q must be a value or command, not %s", src.Global, gk)
		}
	case "mint":
		m, ok := st.Minters[src.Mint]
		if !ok {
			return secret.Spec{}, fmt.Errorf("mint %q is not a defined minter", src.Mint)
		}
		if expand == nil {
			return secret.Spec{}, fmt.Errorf("mint %q requires jam mint (not wired in this build)", src.Mint)
		}
		return expand(src.Mint, m, name)
	default:
		return secret.Spec{}, fmt.Errorf("unhandled source kind %q", kind)
	}
}

// PlanFlat resolves each demanded credential name to a secret.Spec against the
// flat Credentials map (LoadFlat). A name with no entry is returned in unresolved
// (the caller decides if it is required). A structural fault (an undefined
// global:/mint:, or a mint: with no expander) returns err. minters:/global: are
// never matched by demand name; they are reached only via a credentials source.
func (st Store) PlanFlat(demanded []string, expand MintExpander) (resolvable []secret.Spec, unresolved []string, err error) {
	for _, name := range demanded {
		src, ok := st.Credentials[name]
		if !ok {
			unresolved = append(unresolved, name)
			continue
		}
		spec, e := st.resolve(name, src, expand)
		if e != nil {
			return nil, nil, fmt.Errorf("credential %q: %w", name, e)
		}
		resolvable = append(resolvable, spec)
	}
	return resolvable, unresolved, nil
}
