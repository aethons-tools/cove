// Package state persists per-kit runtime info about a created cove (the running
// instance), separate from the human-authored kit config. connect/destroy/status
// operate on this — not on config.yml — so kit edits never affect a live sandbox.
//
// The state file doubles as a lockfile (see lock.go): connect holds a shared
// lock for the duration of its session; destroy needs an exclusive lock, so it
// refuses to tear down a sandbox while connections are open.
package state

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

const schemaVersion = 1

// Secret is a snapshot of a kit secret spec (name + resolver argv) taken at
// create time. Secret VALUES are never stored — only the command that produces
// them, resolved fresh at each connect.
type Secret struct {
	Name    string   `json:"name"`
	Command []string `json:"command"`
}

// State describes one created cove instance.
type State struct {
	SchemaVersion     int      `json:"schemaVersion"`
	Name              string   `json:"name"`
	Backend           string   `json:"backend"`
	Container         string   `json:"container"`
	Image             string   `json:"image"`
	WorkspaceMode     string   `json:"workspaceMode"`               // "isolated" | "shared"
	WorkspaceHostPath string   `json:"workspaceHostPath,omitempty"` // set iff shared
	Secrets           []Secret `json:"secrets,omitempty"`
	CreatedAt         string   `json:"createdAt"`
}

// ErrNotCreated is returned by Load/lock acquisition when no state file exists.
var ErrNotCreated = errors.New("no cove state for this kit (run `at-cove create` first)")

// Dir returns the .state directory inside the kit.
func Dir(kitDir string) string { return filepath.Join(kitDir, ".state") }

// Path returns the state.json path inside the kit.
func Path(kitDir string) string { return filepath.Join(Dir(kitDir), "state.json") }

// Exists reports whether a state file is present.
func Exists(kitDir string) bool {
	_, err := os.Stat(Path(kitDir))
	return err == nil
}

// Save writes the state file (creating .state/), stamping the schema version.
func Save(kitDir string, s State) error {
	s.SchemaVersion = schemaVersion
	if err := os.MkdirAll(Dir(kitDir), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(Path(kitDir), append(b, '\n'), 0o600)
}

// Load reads the state file. Returns ErrNotCreated if it is absent.
func Load(kitDir string) (State, error) {
	b, err := os.ReadFile(Path(kitDir))
	if errors.Is(err, os.ErrNotExist) {
		return State{}, ErrNotCreated
	}
	if err != nil {
		return State{}, err
	}
	var s State
	if err := json.Unmarshal(b, &s); err != nil {
		return State{}, err
	}
	return s, nil
}

// Delete removes the state file (leaving the .state/ dir). Idempotent.
func Delete(kitDir string) error {
	err := os.Remove(Path(kitDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
