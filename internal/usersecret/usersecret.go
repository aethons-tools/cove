// Package usersecret loads the host-side secret-supply files
// (~/.config/at-cove/secrets.yml and secrets.local.yml). These are the "supply"
// side: kits declare *demands* (secret names), the machine supplies them here,
// per-kit, out of source control. secrets.yml keys kits by name; secrets.local.yml
// keys them by canonical kit path (an escape hatch for collisions and testing).
package usersecret

import (
	"bytes"
	"errors"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Store is the parsed supply. Minters and Global are inert libraries: reachable
// only through an explicit mint:/global: reference under a specific kit (or path).
type Store struct {
	Minters map[string]Minter
	Global  map[string]Source
	Kits    map[string]map[string]Source // secrets.yml: kit name -> secret -> source
	Local   map[string]map[string]Source // secrets.local.yml: kit path -> secret -> source
}

// file is the on-disk shape of each secrets file.
type file struct {
	Minters map[string]Minter            `yaml:"minters"`
	Global  map[string]Source            `yaml:"global"`
	Kits    map[string]map[string]Source `yaml:"kits"`
}

// UnmarshalYAML decodes a supply mapping into exactly one Source form.
func (s *Source) UnmarshalYAML(node *yaml.Node) error {
	var raw struct {
		Value   *string  `yaml:"value"`
		Command []string `yaml:"command"`
		Global  string   `yaml:"global"`
		Mint    string   `yaml:"mint"`
	}
	if err := node.Decode(&raw); err != nil {
		return err
	}
	s.Value, s.Command, s.Global, s.Mint = raw.Value, raw.Command, raw.Global, raw.Mint
	if _, err := s.Kind(); err != nil {
		return err
	}
	return nil
}

// Load parses both supply files into one Store. Missing files contribute empty
// sections. Local minters/global override yml by key; local kits populate Local.
// It validates every minter and every source, and that every global:/mint:
// reference resolves to a defined shared supply / minter profile.
func Load(ymlPath, localPath string) (Store, error) {
	yml, err := readFile(ymlPath)
	if err != nil {
		return Store{}, err
	}
	local, err := readFile(localPath)
	if err != nil {
		return Store{}, err
	}
	st := Store{
		Minters: map[string]Minter{},
		Global:  map[string]Source{},
		Kits:    map[string]map[string]Source{},
		Local:   map[string]map[string]Source{},
	}
	for k, v := range yml.Minters {
		st.Minters[k] = v
	}
	for k, v := range local.Minters { // local overrides yml
		st.Minters[k] = v
	}
	for k, v := range yml.Global {
		st.Global[k] = v
	}
	for k, v := range local.Global {
		st.Global[k] = v
	}
	st.Kits = yml.Kits
	if st.Kits == nil {
		st.Kits = map[string]map[string]Source{}
	}
	st.Local = local.Kits
	if st.Local == nil {
		st.Local = map[string]map[string]Source{}
	}
	if err := st.validate(); err != nil {
		return Store{}, err
	}
	return st, nil
}

func readFile(path string) (file, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return file{}, nil
	}
	if err != nil {
		return file{}, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var f file
	if err := dec.Decode(&f); err != nil {
		return file{}, fmt.Errorf("%s: %w", path, err)
	}
	return f, nil
}

func (st Store) validate() error {
	for name, m := range st.Minters {
		if err := m.Validate(); err != nil {
			return fmt.Errorf("minters.%s: %w", name, err)
		}
	}
	check := func(where string, entries map[string]map[string]Source) error {
		for kit, secrets := range entries {
			for name, src := range secrets {
				kind, err := src.Kind()
				if err != nil {
					return fmt.Errorf("%s.%s.%s: %w", where, kit, name, err)
				}
				switch kind {
				case "global":
					if _, ok := st.Global[src.Global]; !ok {
						return fmt.Errorf("%s.%s.%s: global %q is not defined", where, kit, name, src.Global)
					}
				case "mint":
					if _, ok := st.Minters[src.Mint]; !ok {
						return fmt.Errorf("%s.%s.%s: mint %q is not a defined minter", where, kit, name, src.Mint)
					}
				}
			}
		}
		return nil
	}
	if err := check("kits", st.Kits); err != nil {
		return err
	}
	return check("local", st.Local)
}
