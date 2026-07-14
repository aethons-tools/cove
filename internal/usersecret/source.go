package usersecret

import "fmt"

// Source is how one demanded secret (or a global/profile field) is produced:
// exactly one of the four forms. Value is a *string so an explicit empty literal
// (value: "") is distinct from "unset".
//
//	value:   a literal string
//	command: a host argv whose trimmed stdout is the value
//	global:  delegate to a named entry in the store's global: library
//	mint:    mint via a named entry in the store's minters: library
type Source struct {
	Value   *string
	Command []string
	Global  string
	Mint    string
}

// Kind returns the single set form, or an error if zero or more than one is set.
func (s Source) Kind() (string, error) {
	kinds := make([]string, 0, 1)
	if s.Value != nil {
		kinds = append(kinds, "value")
	}
	if len(s.Command) > 0 {
		kinds = append(kinds, "command")
	}
	if s.Global != "" {
		kinds = append(kinds, "global")
	}
	if s.Mint != "" {
		kinds = append(kinds, "mint")
	}
	switch len(kinds) {
	case 1:
		return kinds[0], nil
	case 0:
		return "", fmt.Errorf("a supply must set exactly one of value/command/global/mint (set none)")
	default:
		return "", fmt.Errorf("a supply must set exactly one of value/command/global/mint (set %v)", kinds)
	}
}
