// Package usersecret loads the user-level secrets file
// (~/.config/at-cove/secrets.yml): a map of secret name to either a literal
// value (a YAML string) or a resolver command (a YAML string array). It is the
// "supply" side, consulted for kit-demanded secrets that have no command.
package usersecret

import (
	"errors"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Entry is one secret's supply: exactly one of Value or Command is set.
type Entry struct {
	Value   string   // literal value (the YAML scalar form)
	Command []string // resolver argv (the YAML string sequence)
}

// Store maps a secret name to its supply.
type Store map[string]Entry

// Load reads the secrets.yml at path. A missing file yields an empty Store and
// no error. A present-but-malformed file (invalid YAML, or a value that is
// neither a scalar nor a string sequence) is an error naming the offending key.
func Load(path string) (Store, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Store{}, nil
	}
	if err != nil {
		return nil, err
	}
	var doc map[string]yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("secrets.yml: %w", err)
	}
	store := make(Store, len(doc))
	for name, node := range doc {
		switch node.Kind {
		case yaml.ScalarNode:
			store[name] = Entry{Value: node.Value}
		case yaml.SequenceNode:
			var cmd []string
			if err := node.Decode(&cmd); err != nil {
				return nil, fmt.Errorf("secrets.yml: secret %q: command must be a list of strings: %w", name, err)
			}
			store[name] = Entry{Command: cmd}
		default:
			return nil, fmt.Errorf("secrets.yml: secret %q: value must be a string or a list of strings", name)
		}
	}
	return store, nil
}
