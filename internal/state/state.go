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
	"sort"
	"strings"

	"github.com/aethons-tools/cove/internal/kit"
)

// schemaVersion 2 added the Volumes sub-object (COV-76); version 3 added
// ImageDigest, the built image's own sha256 the run was pinned to (COV-78);
// version 4 dropped Secret.Command (COV-90), a never-written resolver argv. Older
// files simply omit (or, for v4, still carry) these keys and load without error —
// a missing "volumes" falls back to reconstructing names from the container, a
// missing "imageDigest" leaves the run recorded under its mutable tag alone, and
// a stale "command" on a secret is ignored by the lenient JSON load.
const schemaVersion = 4

// Secret is a snapshot of a kit secret demand (its name) taken at create time.
// Only the name is recorded: a kit never carries a resolver command (the
// demand/supply trust boundary — supply lives host-side and is resolved fresh at
// each connect), and neither values nor resolver argv are ever persisted.
type Secret struct {
	Name string `json:"name"`
}

// Volumes records the named volumes the backend created for an instance, so
// teardown removes exactly those rather than re-deriving them from the container
// name (COV-76). The create path names the volumes, state records them here, and
// destroy reads them back — one source of truth. State (/agent-data) is always
// present; Workspace is empty for a shared (bind-mount) workspace. Absent in
// schemaVersion 1 files, which fall back to <Container>-state/-workspace at
// teardown.
type Volumes struct {
	State     string `json:"state"`
	Workspace string `json:"workspace,omitempty"`
}

// State describes one created cove instance.
type State struct {
	SchemaVersion     int      `json:"schemaVersion"`
	Name              string   `json:"name"`
	Backend           string   `json:"backend"`
	Container         string   `json:"container"`
	Image             string   `json:"image"`                       // the built image tag (display/diagnostics)
	ImageDigest       string   `json:"imageDigest,omitempty"`       // built image's own sha256 the run pinned (COV-78); empty in legacy files
	WorkspaceMode     string   `json:"workspaceMode"`               // "isolated" | "shared"
	WorkspaceHostPath string   `json:"workspaceHostPath,omitempty"` // set iff shared
	Volumes           *Volumes `json:"volumes,omitempty"`           // named volumes created (COV-76); nil in legacy files
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
	// Writing .state/ keeps the kit's .gitignore current, so a created sandbox's
	// runtime state never leaks into git (even on a path that never assembled).
	if err := kit.EnsureGitignore(kitDir); err != nil {
		return err
	}
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

// List returns every instance that has a state file in the kit's .state dir, so
// multi-VM verbs (status list-all, destroy --all) can enumerate a kit's
// instances without consulting config. The interactive state.json maps to
// Interactive; every other "<class>.json" maps to Instance("<class>").
// install.json (the compiled manifest, which shares the .state dir), non-.json
// files, and subdirectories (e.g. logs/) are ignored. The result is sorted for
// determinism, so a plain kit's single Interactive instance sorts first.
func List(kitDir string) ([]Instance, error) {
	entries, err := os.ReadDir(Dir(kitDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var insts []Instance
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".json") || name == "install.json" {
			continue
		}
		if name == "state.json" {
			insts = append(insts, Interactive)
			continue
		}
		insts = append(insts, Instance(strings.TrimSuffix(name, ".json")))
	}
	sort.Slice(insts, func(i, j int) bool { return insts[i] < insts[j] })
	return insts, nil
}
