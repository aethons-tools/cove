package usersecret

import (
	"fmt"

	"github.com/aethons-tools/cove/internal/secret"
)

// MintExpander turns a resolved minter profile into a runnable secret.Spec (an
// at-mint invocation). Injected by the caller; nil until the minting wiring lands.
type MintExpander func(profileName string, m Minter, demandName string) (secret.Spec, error)

// Plan resolves each demanded secret name to a secret.Spec, with precedence
// secrets.local.yml (by kit path) -> secrets.yml kits: (by kit name) -> unresolved.
// A demand with no matching entry is returned in unresolved (the caller decides if
// it is required). A structural fault (a global pointing at a non-terminal source,
// or a mint with no expander) returns err. minters:/global: are never matched by
// demand name; they are reached only via an explicit source under the kit.
func (st Store) Plan(kitName, kitPath string, demanded []string, expand MintExpander) (resolvable []secret.Spec, unresolved []string, err error) {
	for _, name := range demanded {
		src, ok := st.lookup(kitName, kitPath, name)
		if !ok {
			unresolved = append(unresolved, name)
			continue
		}
		spec, e := st.resolve(name, src, expand)
		if e != nil {
			return nil, nil, fmt.Errorf("secret %q for kit %q: %w", name, kitName, e)
		}
		resolvable = append(resolvable, spec)
	}
	return resolvable, unresolved, nil
}

func (st Store) lookup(kitName, kitPath, name string) (Source, bool) {
	if m, ok := st.Local[kitPath]; ok {
		if s, ok := m[name]; ok {
			return s, true
		}
	}
	if m, ok := st.Kits[kitName]; ok {
		if s, ok := m[name]; ok {
			return s, true
		}
	}
	return Source{}, false
}

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
			return secret.Spec{}, fmt.Errorf("mint %q requires at-mint (not wired in this build)", src.Mint)
		}
		return expand(src.Mint, m, name)
	default:
		return secret.Spec{}, fmt.Errorf("unhandled source kind %q", kind)
	}
}
