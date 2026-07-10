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

// Instance identifies one named cove instance within a kit. The zero value,
// Interactive, is the human-facing instance recorded in state.json.
type Instance string

// Interactive is the default instance: the one create/connect/destroy/status
// operate on today.
const Interactive Instance = ""

// file is the state filename for this instance, inside the kit's .state dir.
func (i Instance) file() string {
	if i == Interactive {
		return "state.json"
	}
	return string(i) + ".json"
}

// ErrNotCreated is returned by Load/lock acquisition when no state file exists.
var ErrNotCreated = errors.New("no cove state for this kit (run `at-cove create` first)")

// Dir returns the .state directory inside the kit.
func Dir(kitDir string) string { return filepath.Join(kitDir, ".state") }

// PathFor returns the state-file path for the given instance inside the kit.
func PathFor(kitDir string, inst Instance) string {
	return filepath.Join(Dir(kitDir), inst.file())
}

// Path returns the interactive instance's state.json path.
func Path(kitDir string) string { return PathFor(kitDir, Interactive) }

// ExistsFor reports whether the given instance's state file is present.
func ExistsFor(kitDir string, inst Instance) bool {
	_, err := os.Stat(PathFor(kitDir, inst))
	return err == nil
}

// Exists reports whether the interactive state file is present.
func Exists(kitDir string) bool { return ExistsFor(kitDir, Interactive) }

// SaveFor writes the given instance's state file (creating .state/), stamping
// the schema version.
func SaveFor(kitDir string, inst Instance, s State) error {
	s.SchemaVersion = schemaVersion
	if err := os.MkdirAll(Dir(kitDir), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(PathFor(kitDir, inst), append(b, '\n'), 0o600)
}

// Save writes the interactive instance's state file.
func Save(kitDir string, s State) error { return SaveFor(kitDir, Interactive, s) }

// LoadFor reads the given instance's state file. Returns ErrNotCreated if absent.
func LoadFor(kitDir string, inst Instance) (State, error) {
	b, err := os.ReadFile(PathFor(kitDir, inst))
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

// Load reads the interactive instance's state file.
func Load(kitDir string) (State, error) { return LoadFor(kitDir, Interactive) }

// DeleteFor removes the given instance's state file. Idempotent.
func DeleteFor(kitDir string, inst Instance) error {
	err := os.Remove(PathFor(kitDir, inst))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// Delete removes the interactive instance's state file. Idempotent.
func Delete(kitDir string) error { return DeleteFor(kitDir, Interactive) }
