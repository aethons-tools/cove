package state

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Save must write the kit's managed .gitignore, so a created sandbox never leaks
// its .state into git even if no build ran first.
func TestSaveEnsuresGitignore(t *testing.T) {
	dir := t.TempDir()
	if err := Save(dir, State{Name: "x", Backend: "colima", Container: "c"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatalf("expected .gitignore written by Save: %v", err)
	}
	if !strings.Contains(string(b), ".state/") {
		t.Fatalf(".gitignore missing .state/:\n%s", string(b))
	}
}

// The shared-workspace shadow-dirs and their volume names round-trip through the
// state file, so recreate re-emits the mounts and destroy removes them (COV-132).
func TestStateRoundTripsShadowDirs(t *testing.T) {
	in := State{
		SchemaVersion: 2, Name: "k", Container: "atcove-k-human",
		WorkspaceMode: "shared", WorkspaceHostPath: "/host/repo",
		ShadowDirs: []string{".venv", "node_modules"},
		Volumes: &Volumes{State: "atcove-k-human-agent-data",
			Shadow: []string{"atcove-k-human-shadow-venv", "atcove-k-human-shadow-node_modules"}},
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out State
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.ShadowDirs) != 2 || out.ShadowDirs[1] != "node_modules" {
		t.Fatalf("ShadowDirs lost: %+v", out.ShadowDirs)
	}
	if out.Volumes == nil || len(out.Volumes.Shadow) != 2 || out.Volumes.Shadow[0] != "atcove-k-human-shadow-venv" {
		t.Fatalf("Volumes.Shadow lost: %+v", out.Volumes)
	}
}

func TestSaveLoadDelete(t *testing.T) {
	dir := t.TempDir()
	if Exists(dir) {
		t.Fatal("should not exist yet")
	}
	if _, err := Load(dir); !errors.Is(err, ErrNotCreated) {
		t.Fatalf("Load missing: want ErrNotCreated, got %v", err)
	}

	want := State{
		Name: "box", Backend: "colima", Container: "box",
		Image: "at-cove-for-box", ImageDigest: "sha256:cafef00d", WorkspaceMode: "isolated",
		Secrets:   []Secret{{Name: "GITHUB_TOKEN"}},
		CreatedAt: "2026-06-27T00:00:00Z",
	}
	if err := Save(dir, want); err != nil {
		t.Fatal(err)
	}
	if !Exists(dir) {
		t.Fatal("should exist after save")
	}
	got, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.SchemaVersion != schemaVersion {
		t.Errorf("schemaVersion = %d, want %d", got.SchemaVersion, schemaVersion)
	}
	if got.Container != "box" || got.Image != "at-cove-for-box" || got.ImageDigest != "sha256:cafef00d" || got.WorkspaceMode != "isolated" {
		t.Errorf("roundtrip mismatch: %+v", got)
	}
	if len(got.Secrets) != 1 || got.Secrets[0].Name != "GITHUB_TOKEN" {
		t.Errorf("secrets not round-tripped: %+v", got.Secrets)
	}

	if err := Delete(dir); err != nil {
		t.Fatal(err)
	}
	if Exists(dir) {
		t.Fatal("should be gone after delete")
	}
	if err := Delete(dir); err != nil {
		t.Fatalf("delete should be idempotent: %v", err)
	}
}

// TestLegacyStateWithSecretCommandStillLoads (COV-90): a state file written
// before schemaVersion 4 still carries a "command" argv on each secret. After the
// field was dropped, the lenient JSON load must ignore that stale key and load the
// rest of the file — never erroring — preserving the secret's name.
func TestLegacyStateWithSecretCommandStillLoads(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(Dir(dir), 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := `{
  "schemaVersion": 3,
  "name": "box",
  "backend": "colima",
  "container": "box",
  "secrets": [{"name": "GITHUB_TOKEN", "command": ["op", "read", "x"]}],
  "createdAt": "2026-06-27T00:00:00Z"
}`
	if err := os.WriteFile(PathFor(dir, Interactive), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Load(dir)
	if err != nil {
		t.Fatalf("legacy state with a secret command must still load: %v", err)
	}
	if len(got.Secrets) != 1 || got.Secrets[0].Name != "GITHUB_TOKEN" {
		t.Fatalf("secret name not loaded from legacy file: %+v", got.Secrets)
	}
}

// Volume names recorded at create time round-trip through Save/Load (COV-76),
// and a legacy state file (older schemaVersion, no "volumes" key) still loads
// without error — leaving Volumes absent so teardown falls back to reconstructing
// the names from the container.
func TestVolumesRoundTripAndLegacyFallback(t *testing.T) {
	dir := t.TempDir()
	want := State{
		Name: "box", Backend: "colima", Container: "box",
		Image: "at-cove-for-box", WorkspaceMode: "isolated",
		Volumes: &Volumes{State: "box-state", Workspace: "box-workspace"},
	}
	if err := Save(dir, want); err != nil {
		t.Fatal(err)
	}
	got, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.SchemaVersion != schemaVersion {
		t.Errorf("schemaVersion = %d, want %d", got.SchemaVersion, schemaVersion)
	}
	if got.Volumes == nil || got.Volumes.State != "box-state" || got.Volumes.Workspace != "box-workspace" {
		t.Fatalf("volumes not round-tripped: %+v", got.Volumes)
	}

	// A legacy state file (schemaVersion 1, no "volumes" key) must load without
	// error, with Volumes absent (nil) so teardown falls back to the historical
	// <container>-state/-workspace reconstruction.
	legacy := `{"schemaVersion":1,"name":"box","backend":"colima","container":"box","image":"at-cove-for-box","workspaceMode":"isolated","createdAt":"2026-01-01T00:00:00Z"}`
	if err := os.WriteFile(PathFor(dir, Interactive), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	old, err := Load(dir)
	if err != nil {
		t.Fatalf("legacy state must load without error: %v", err)
	}
	if old.Volumes != nil {
		t.Fatalf("legacy state must have no recorded volumes; got %+v", old.Volumes)
	}
}

func TestLockSharedMultipleExclusiveBlocks(t *testing.T) {
	dir := t.TempDir()
	if err := Save(dir, State{Name: "x", Backend: "colima", Container: "x"}); err != nil {
		t.Fatal(err)
	}

	s1, err := AcquireShared(dir)
	if err != nil {
		t.Fatalf("first shared: %v", err)
	}
	s2, err := AcquireShared(dir)
	if err != nil {
		t.Fatalf("second shared should also succeed: %v", err)
	}
	if _, err := AcquireExclusive(dir); !errors.Is(err, ErrLocked) {
		t.Fatalf("exclusive must be blocked while shared held, got %v", err)
	}
	s1.Release()
	s2.Release()

	ex, err := AcquireExclusive(dir)
	if err != nil {
		t.Fatalf("exclusive after release: %v", err)
	}
	if _, err := AcquireShared(dir); !errors.Is(err, ErrLocked) {
		t.Fatalf("shared must be blocked while exclusive held, got %v", err)
	}
	ex.Release()
}

func TestAcquireMissingState(t *testing.T) {
	if _, err := AcquireShared(t.TempDir()); !errors.Is(err, ErrNotCreated) {
		t.Fatalf("want ErrNotCreated, got %v", err)
	}
}

func TestInstanceFilenames(t *testing.T) {
	dir := t.TempDir()
	if got := PathFor(dir, Interactive); got != Path(dir) {
		t.Fatalf("Interactive PathFor = %q, want Path() %q", got, Path(dir))
	}
	if got := PathFor(dir, Interactive); !strings.HasSuffix(got, "/.state/state.json") {
		t.Fatalf("interactive path = %q, want .../.state/state.json", got)
	}
	if got := PathFor(dir, Instance("foo")); !strings.HasSuffix(got, "/.state/foo.json") {
		t.Fatalf("named instance path = %q, want .../.state/foo.json", got)
	}
}

func TestNamedInstancesAreIsolated(t *testing.T) {
	dir := t.TempDir()
	foo := Instance("foo")
	if err := SaveFor(dir, Interactive, State{Name: "box", Backend: "colima", Container: "box"}); err != nil {
		t.Fatal(err)
	}
	if err := SaveFor(dir, foo, State{Name: "box", Backend: "colima", Container: "box-foo"}); err != nil {
		t.Fatal(err)
	}
	if !ExistsFor(dir, Interactive) || !ExistsFor(dir, foo) {
		t.Fatal("both instances should exist")
	}
	got, err := LoadFor(dir, foo)
	if err != nil {
		t.Fatal(err)
	}
	if got.Container != "box-foo" {
		t.Fatalf("named instance load returned wrong state: %+v", got)
	}
	// Deleting the named instance must not touch the interactive instance.
	if err := DeleteFor(dir, foo); err != nil {
		t.Fatal(err)
	}
	if ExistsFor(dir, foo) {
		t.Fatal("named instance should be gone")
	}
	if !ExistsFor(dir, Interactive) {
		t.Fatal("interactive instance must survive named-instance delete")
	}
}

func TestListEnumeratesInstancesIgnoringInstallJSON(t *testing.T) {
	dir := t.TempDir()
	// No .state dir yet -> empty list, no error.
	if got, err := List(dir); err != nil || len(got) != 0 {
		t.Fatalf("List on absent .state = (%v, %v), want ([], nil)", got, err)
	}
	if err := SaveFor(dir, Interactive, State{Name: "box", Backend: "colima", Container: "box"}); err != nil {
		t.Fatal(err)
	}
	if err := SaveFor(dir, Instance("steward"), State{Name: "box", Backend: "colima", Container: "box-steward"}); err != nil {
		t.Fatal(err)
	}
	if err := SaveFor(dir, Instance("planner"), State{Name: "box", Backend: "colima", Container: "box-planner"}); err != nil {
		t.Fatal(err)
	}
	// install.json shares the .state dir; it must never be mistaken for an instance.
	if err := os.WriteFile(filepath.Join(Dir(dir), "install.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A subdirectory (e.g. logs/) must be skipped too.
	if err := os.MkdirAll(filepath.Join(Dir(dir), "logs"), 0o700); err != nil {
		t.Fatal(err)
	}
	got, err := List(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []Instance{Interactive, Instance("planner"), Instance("steward")} // sorted: "" < "planner" < "steward"
	if len(got) != len(want) {
		t.Fatalf("List = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("List[%d] = %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}
}

func TestLoadForMissingInstance(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadFor(dir, Instance("nope")); !errors.Is(err, ErrNotCreated) {
		t.Fatalf("missing instance load: want ErrNotCreated, got %v", err)
	}
}
