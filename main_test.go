package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/backend"
	"github.com/aethons-tools/cove/internal/kit"
	"github.com/aethons-tools/cove/internal/runner"
	"github.com/aethons-tools/cove/internal/state"
)

func writeKit(t *testing.T, dir string) string {
	t.Helper()
	cove := filepath.Join(dir, ".at-cove")
	if err := os.MkdirAll(cove, 0o755); err != nil {
		t.Fatal(err)
	}
	yml := "name: box\nbackend: colima\n"
	if err := os.WriteFile(filepath.Join(cove, "config.yml"), []byte(yml), 0o644); err != nil {
		t.Fatal(err)
	}
	return cove
}

// writeState records a created instance so the state-driven commands have
// something to operate on.
func writeState(t *testing.T, kitDir, backendName, container string, secrets ...state.Secret) {
	t.Helper()
	if err := state.Save(kitDir, state.State{
		Name: container, Backend: backendName, Container: container,
		Image: "at-cove-for-" + container, WorkspaceMode: "isolated", Secrets: secrets,
	}); err != nil {
		t.Fatal(err)
	}
}

func dummyLookPath(string) (string, error) { return "/usr/bin/x", nil }

func dockerArg0Index(calls []runner.Call, arg0 string) int {
	for i, c := range calls {
		if c.Name == "docker" && len(c.Args) > 0 && c.Args[0] == arg0 {
			return i
		}
	}
	return -1
}

// seedConfigDir points configDir() at a temp dir pre-loaded with a keypair, so
// keys.Ensure does not shell out to ssh-keygen during non-dry-run tests.
func seedConfigDir(t *testing.T) {
	t.Helper()
	cfgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	coveCfg := filepath.Join(cfgHome, "at-cove")
	if err := os.MkdirAll(coveCfg, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(coveCfg, "id_ed25519"), []byte("PRIV"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(coveCfg, "id_ed25519.pub"), []byte("ssh-ed25519 AAAA test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestStatusDispatchesToBackend(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeKit(t, dir)
	writeState(t, kitDir, "colima", "box")
	f := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: "true\n"}}}
	var out, errOut bytes.Buffer
	code := run([]string{"status", kitDir}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "running") {
		t.Fatalf("status output = %q", out.String())
	}
}

func TestStatusAbsentWhenNoState(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeKit(t, dir)
	var out, errOut bytes.Buffer
	code := run([]string{"status", kitDir}, &runner.Fake{}, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 0 || !strings.Contains(out.String(), "absent") {
		t.Fatalf("status with no state: code=%d out=%q", code, out.String())
	}
}

func TestUnknownBackendErrors(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeKit(t, dir)
	writeState(t, kitDir, "bogus", "box") // state names an unknown backend
	var out, errOut bytes.Buffer
	code := run([]string{"status", kitDir}, &runner.Fake{}, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code == 0 || !strings.Contains(errOut.String(), "bogus") {
		t.Fatalf("expected unknown-backend error, code=%d stderr=%q", code, errOut.String())
	}
}

func TestDryRunCreatePrintsNoExec(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeKit(t, dir)
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	code := run([]string{"--dry-run", "create", kitDir}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, errOut.String())
	}
	if len(f.Calls) != 0 {
		t.Fatalf("dry-run executed commands: %+v", f.Calls)
	}
	if !strings.Contains(out.String(), "would") {
		t.Fatalf("dry-run should describe planned actions: %q", out.String())
	}
}

func TestCreateWritesStateAndRejectsSecond(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeKit(t, dir)
	seedConfigDir(t)
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	if code := run([]string{"create", kitDir}, f, os.LookupEnv, dummyLookPath, &out, &errOut); code != 0 {
		t.Fatalf("create exit=%d stderr=%s", code, errOut.String())
	}
	if dockerArg0Index(f.Calls, "build") == -1 || dockerArg0Index(f.Calls, "run") == -1 {
		t.Fatalf("create must build + run; calls=%+v", f.Calls)
	}
	st, err := state.Load(kitDir)
	if err != nil {
		t.Fatalf("state not written: %v", err)
	}
	if st.Container != "box" || st.Image != "at-cove-for-box" || st.Backend != "colima" {
		t.Fatalf("state = %+v", st)
	}
	var o2, e2 bytes.Buffer
	code := run([]string{"create", kitDir}, &runner.Fake{}, os.LookupEnv, dummyLookPath, &o2, &e2)
	if code == 0 || !strings.Contains(e2.String(), "already created") {
		t.Fatalf("second create should refuse; code=%d stderr=%q", code, e2.String())
	}
}

func TestDestroyRemovesContainerImageAndState(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeKit(t, dir)
	writeState(t, kitDir, "colima", "box")
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	if code := run([]string{"destroy", kitDir}, f, os.LookupEnv, dummyLookPath, &out, &errOut); code != 0 {
		t.Fatalf("destroy exit=%d stderr=%s", code, errOut.String())
	}
	if dockerArg0Index(f.Calls, "rm") == -1 || dockerArg0Index(f.Calls, "rmi") == -1 {
		t.Fatalf("destroy must rm + rmi; calls=%+v", f.Calls)
	}
	if state.Exists(kitDir) {
		t.Fatal("destroy must delete the state file")
	}
}

func TestDestroyBlockedByActiveConnection(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeKit(t, dir)
	writeState(t, kitDir, "colima", "box")
	lock, err := state.AcquireShared(kitDir) // simulate an open connection
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	var out, errOut bytes.Buffer
	code := run([]string{"destroy", kitDir}, &runner.Fake{}, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code == 0 || !strings.Contains(errOut.String(), "active connection") {
		t.Fatalf("destroy should refuse with an active connection; code=%d stderr=%q", code, errOut.String())
	}
	if !state.Exists(kitDir) {
		t.Fatal("a blocked destroy must not delete the state file")
	}
}

func TestDryRunConnectRawNoAuth(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeKit(t, dir)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // hermetic: no real ~/.config/at-cove/secrets.yml
	writeState(t, kitDir, "colima", "box", state.Secret{Name: "GITHUB_TOKEN", Command: []string{"op", "x"}})
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	code := run([]string{"--dry-run", "--raw", "--no-auth", "connect", kitDir},
		f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errOut.String())
	}
	if len(f.Calls) != 0 {
		t.Fatalf("dry-run executed commands: %+v", f.Calls)
	}
	s := out.String()
	if !strings.Contains(s, "bash") || !strings.Contains(s, "no auth") {
		t.Fatalf("dry-run connect --raw --no-auth message = %q", s)
	}
}

func TestVersionSubcommand(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run([]string{"version"}, &runner.Fake{}, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "at-cove "+version) {
		t.Fatalf("version output=%q want to contain %q", out.String(), "at-cove "+version)
	}
}

func TestVersionFlag(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run([]string{"--version"}, &runner.Fake{}, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 0 || !strings.Contains(out.String(), "at-cove "+version) {
		t.Fatalf("--version: code=%d out=%q", code, out.String())
	}
}

func TestDryRunRecreatePrintsNoExec(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeKit(t, dir)
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	code := run([]string{"--dry-run", "recreate", kitDir}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, errOut.String())
	}
	if len(f.Calls) != 0 {
		t.Fatalf("dry-run executed commands: %+v", f.Calls)
	}
	if !strings.Contains(out.String(), "would") || !strings.Contains(out.String(), "keeping volumes") {
		t.Fatalf("dry-run should describe a volume-keeping recreate: %q", out.String())
	}
}

func TestRecreateDestroysThenCreatesKeepingVolumes(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeKit(t, dir)
	writeState(t, kitDir, "colima", "box") // already created -> recreate must destroy first
	seedConfigDir(t)
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	code := run([]string{"recreate", kitDir}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, errOut.String())
	}
	rmIdx := dockerArg0Index(f.Calls, "rm")
	buildIdx := dockerArg0Index(f.Calls, "build")
	runIdx := dockerArg0Index(f.Calls, "run")
	if rmIdx == -1 {
		t.Fatalf("recreate must destroy the existing container; calls=%+v", f.Calls)
	}
	if buildIdx == -1 || runIdx == -1 {
		t.Fatalf("recreate must create the container; calls=%+v", f.Calls)
	}
	if rmIdx > buildIdx {
		t.Fatalf("destroy must precede create; calls=%+v", f.Calls)
	}
	for _, a := range f.Calls[rmIdx].Args {
		if a == "-v" || a == "--volumes" {
			t.Fatalf("recreate must keep volumes: %v", f.Calls[rmIdx].Args)
		}
	}
}

// writeSharedState records a previously created instance whose workspace was a
// shared bind-mount (i.e. `create --ws <hostPath>`).
func writeSharedState(t *testing.T, kitDir, container, hostPath string) {
	t.Helper()
	if err := state.Save(kitDir, state.State{
		Name: container, Backend: "colima", Container: container,
		Image: "at-cove-for-" + container, WorkspaceMode: "shared", WorkspaceHostPath: hostPath,
	}); err != nil {
		t.Fatal(err)
	}
}

// dockerRunHasArg reports whether the `docker run` call carries the given arg.
func dockerRunHasArg(t *testing.T, calls []runner.Call, want string) bool {
	t.Helper()
	i := dockerArg0Index(calls, "run")
	if i == -1 {
		t.Fatalf("no docker run call; calls=%+v", calls)
	}
	for _, a := range calls[i].Args {
		if a == want {
			return true
		}
	}
	return false
}

// Recreate keeps volumes, but a shared bind-mount is not a volume — it must be
// re-specified at `docker run`. Without --ws, recreate must recover the shared
// workspace from state instead of silently falling back to an isolated volume.
func TestRecreatePreservesSharedWorkspaceFromState(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeKit(t, dir)
	hostPath := filepath.Join(dir, "myrepo")
	if err := os.MkdirAll(hostPath, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSharedState(t, kitDir, "box", hostPath)
	seedConfigDir(t)
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	code := run([]string{"recreate", kitDir}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, errOut.String())
	}
	wantMount := hostPath + ":/home/agent/workspace"
	if !dockerRunHasArg(t, f.Calls, wantMount) {
		t.Fatalf("recreate dropped the shared workspace; want mount %q in run args", wantMount)
	}
	st, err := state.Load(kitDir)
	if err != nil {
		t.Fatal(err)
	}
	if st.WorkspaceMode != "shared" || st.WorkspaceHostPath != hostPath {
		t.Fatalf("recreate must persist the shared workspace; state=%+v", st)
	}
}

// An explicit --ws on recreate overrides whatever the prior state recorded.
func TestRecreateWorkspaceFlagOverridesState(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeKit(t, dir)
	oldPath := filepath.Join(dir, "old")
	newPath := filepath.Join(dir, "new")
	for _, p := range []string{oldPath, newPath} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeSharedState(t, kitDir, "box", oldPath)
	seedConfigDir(t)
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	code := run([]string{"recreate", kitDir, "--ws", newPath}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, errOut.String())
	}
	if !dockerRunHasArg(t, f.Calls, newPath+":/home/agent/workspace") {
		t.Fatalf("explicit --ws must win over state; calls=%+v", f.Calls)
	}
	if dockerRunHasArg(t, f.Calls, oldPath+":/home/agent/workspace") {
		t.Fatalf("recreate used the stale workspace from state; calls=%+v", f.Calls)
	}
}

func TestRecreateSkipsDestroyWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeKit(t, dir)
	seedConfigDir(t)
	f := &runner.Fake{} // no state -> nothing to destroy
	var out, errOut bytes.Buffer
	code := run([]string{"recreate", kitDir}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, errOut.String())
	}
	if dockerArg0Index(f.Calls, "rm") != -1 {
		t.Fatalf("must not destroy when nothing is created; calls=%+v", f.Calls)
	}
	if dockerArg0Index(f.Calls, "build") == -1 || dockerArg0Index(f.Calls, "run") == -1 {
		t.Fatalf("recreate must still create the container; calls=%+v", f.Calls)
	}
}

func TestDryRunConnectWarnsUnresolvedSecret(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeKit(t, dir)
	writeState(t, kitDir, "colima", "box", state.Secret{Name: "GITHUB_TOKEN"}) // demanded, no command
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())                                   // empty config dir -> no secrets.yml
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	code := run([]string{"--dry-run", "connect", kitDir}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "GITHUB_TOKEN") || !strings.Contains(errOut.String(), "will not be set") {
		t.Fatalf("expected unresolved warning on stderr; got %q", errOut.String())
	}
	if !strings.Contains(out.String(), "would resolve 0 secrets") {
		t.Fatalf("resolvable count should be 0; got %q", out.String())
	}
}

func TestDryRunConnectResumesByDefault(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeKit(t, dir)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	writeState(t, kitDir, "colima", "box", state.Secret{Name: "GITHUB_TOKEN", Command: []string{"op", "x"}})
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	code := run([]string{"--dry-run", "connect", kitDir}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "resuming") {
		t.Fatalf("default connect should resume; msg=%q", out.String())
	}
}

func TestDryRunConnectFresh(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeKit(t, dir)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	writeState(t, kitDir, "colima", "box", state.Secret{Name: "GITHUB_TOKEN", Command: []string{"op", "x"}})
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	code := run([]string{"--dry-run", "--fresh", "connect", kitDir}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errOut.String())
	}
	s := out.String()
	if !strings.Contains(s, "fresh") || strings.Contains(s, "resuming") {
		t.Fatalf("--fresh connect should be fresh; msg=%q", s)
	}
}

func TestConnectMalformedSecretsFileAborts(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeKit(t, dir)
	writeState(t, kitDir, "colima", "box", state.Secret{Name: "GITHUB_TOKEN"})
	cfgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	coveCfg := filepath.Join(cfgHome, "at-cove")
	if err := os.MkdirAll(coveCfg, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(coveCfg, "secrets.yml"), []byte("GITHUB_TOKEN:\n  nested: bad\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	code := run([]string{"--dry-run", "connect", kitDir}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code == 0 {
		t.Fatalf("malformed secrets.yml should abort; out=%q", out.String())
	}
	if !strings.Contains(errOut.String(), "GITHUB_TOKEN") {
		t.Fatalf("error should name the bad key; stderr=%q", errOut.String())
	}
}

func TestSaveStateSnapshotsSetup(t *testing.T) {
	dir := t.TempDir()
	cfg := kit.Config{Name: "box", Backend: "colima", Setup: "git clone https://x ."}
	inst := backend.Instance{Backend: "colima", Container: "box", Image: "img",
		Workspace: backend.WorkspaceMount{Mode: backend.Isolated}}
	if err := saveState(dir, cfg, inst); err != nil {
		t.Fatal(err)
	}
	st, err := state.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if st.Setup != "git clone https://x ." {
		t.Fatalf("state Setup = %q", st.Setup)
	}
}

func TestDestroyLoopInstancePreservesImage(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeKit(t, dir)
	if err := state.SaveFor(kitDir, state.LoopInstance("foo"), state.State{
		Name: "box", Backend: "colima", Container: "box-loop-foo", Image: "at-cove-for-box",
	}); err != nil {
		t.Fatal(err)
	}
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	code := run([]string{"destroy", "--loop", "foo", kitDir}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errOut.String())
	}
	var rm, rmi bool
	for _, c := range f.Calls {
		if c.Name == "docker" && len(c.Args) > 0 && c.Args[0] == "rm" {
			rm = true
		}
		if c.Name == "docker" && len(c.Args) > 0 && c.Args[0] == "rmi" {
			rmi = true
		}
	}
	if !rm {
		t.Fatal("loop container should be removed")
	}
	if rmi {
		t.Fatal("shared image must NOT be removed on loop teardown")
	}
	if state.ExistsFor(kitDir, state.LoopInstance("foo")) {
		t.Fatal("loop state should be deleted")
	}
}

func TestStatusLoopInstance(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeKit(t, dir)
	if err := state.SaveFor(kitDir, state.LoopInstance("foo"), state.State{
		Name: "box", Backend: "colima", Container: "box-loop-foo", Image: "at-cove-for-box",
	}); err != nil {
		t.Fatal(err)
	}
	f := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: "true\n"}}}
	var out, errOut bytes.Buffer
	code := run([]string{"status", "--loop", "foo", kitDir}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "running") {
		t.Fatalf("status = %q", out.String())
	}
}

func TestLoopFlagRejectsBadName(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeKit(t, dir)
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	code := run([]string{"destroy", "--loop", "../etc", kitDir}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code == 0 {
		t.Fatal("invalid loop name must error")
	}
}

func TestLoopFlagRejectedForOtherCommands(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeKit(t, dir)
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	code := run([]string{"--loop", "foo", "build", kitDir}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code == 0 {
		t.Fatal("--loop on a non-destroy/status command must error")
	}
}

func TestDestroyInteractivePreservesImageWhenLoopsExist(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeKit(t, dir)
	writeState(t, kitDir, "colima", "box") // interactive instance
	if err := state.SaveFor(kitDir, state.LoopInstance("foo"), state.State{
		Name: "box", Backend: "colima", Container: "box-loop-foo", Image: "at-cove-for-box",
	}); err != nil {
		t.Fatal(err)
	}
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	code := run([]string{"destroy", kitDir}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errOut.String())
	}
	var rmi bool
	for _, c := range f.Calls {
		if c.Name == "docker" && len(c.Args) > 0 && c.Args[0] == "rmi" {
			rmi = true
		}
	}
	if rmi {
		t.Fatal("interactive destroy must NOT remove the shared image while a loop instance exists")
	}
}

func TestDestroyInteractiveDryRunHonestAboutSharedImage(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeKit(t, dir)
	writeState(t, kitDir, "colima", "box") // interactive
	if err := state.SaveFor(kitDir, state.LoopInstance("foo"), state.State{
		Name: "box", Backend: "colima", Container: "box-loop-foo", Image: "at-cove-for-box",
	}); err != nil {
		t.Fatal(err)
	}
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	code := run([]string{"--dry-run", "destroy", kitDir}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errOut.String())
	}
	s := out.String()
	if strings.Contains(s, "remove image") {
		t.Fatalf("dry-run must not claim image removal while loops exist: %q", s)
	}
	if !strings.Contains(s, "shared image") {
		t.Fatalf("dry-run should say the shared image is kept: %q", s)
	}
}

func writeLoopKit(t *testing.T, dir string) string {
	t.Helper()
	cove := filepath.Join(dir, ".at-cove")
	if err := os.MkdirAll(cove, 0o755); err != nil {
		t.Fatal(err)
	}
	yml := "name: box\nbackend: colima\n" +
		"secrets:\n  - name: ANTHROPIC_API_KEY\n  - name: GITHUB_TOKEN\n" +
		"loops:\n  default:\n    interval: 5m\n    check: \"test -e q\"\n    prompt: \"do it\"\n    setup: \"git clone https://x .\"\n"
	if err := os.WriteFile(filepath.Join(cove, "config.yml"), []byte(yml), 0o644); err != nil {
		t.Fatal(err)
	}
	return cove
}

func TestCreateLoopInstance(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeLoopKit(t, dir)
	seedConfigDir(t)
	f := &runner.Fake{}
	cfg, err := kit.Load(kitDir)
	if err != nil {
		t.Fatal(err)
	}
	st, err := createLoopInstance(kitDir, f, cfg, "default", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if st.Container != "box-loop-default" {
		t.Fatalf("container = %q, want box-loop-default", st.Container)
	}
	if st.Image != "at-cove-for-box" {
		t.Fatalf("image = %q, want the shared kit image at-cove-for-box", st.Image)
	}
	if st.Setup != "git clone https://x ." {
		t.Fatalf("setup = %q (per-loop setup should win)", st.Setup)
	}
	if !state.ExistsFor(kitDir, state.LoopInstance("default")) {
		t.Fatal("loop state file not written")
	}
	bi := dockerArg0Index(f.Calls, "build")
	ri := dockerArg0Index(f.Calls, "run")
	if bi == -1 || ri == -1 {
		t.Fatalf("must build + run; calls=%+v", f.Calls)
	}
	if !slices.Contains(f.Calls[bi].Args, "at-cove-for-box") {
		t.Fatalf("build must tag the shared image: %+v", f.Calls[bi])
	}
	if !slices.Contains(f.Calls[ri].Args, "box-loop-default") {
		t.Fatalf("run must name the loop container: %+v", f.Calls[ri])
	}
}

func TestCreateLoopInstanceRequiresAPIKey(t *testing.T) {
	dir := t.TempDir()
	cove := filepath.Join(dir, ".at-cove")
	if err := os.MkdirAll(cove, 0o755); err != nil {
		t.Fatal(err)
	}
	yml := "name: box\nbackend: colima\nloops:\n  default:\n    interval: 1m\n    check: c\n    prompt: p\n"
	if err := os.WriteFile(filepath.Join(cove, "config.yml"), []byte(yml), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &runner.Fake{}
	cfg, _ := kit.Load(cove)
	_, err := createLoopInstance(cove, f, cfg, "default", io.Discard)
	if err == nil || !strings.Contains(err.Error(), "ANTHROPIC_API_KEY") {
		t.Fatalf("must require ANTHROPIC_API_KEY; err=%v", err)
	}
	if len(f.Calls) != 0 {
		t.Fatalf("must fail before building/creating; calls=%+v", f.Calls)
	}
}

func TestCreateLoopInstanceUnknownLoop(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeLoopKit(t, dir)
	f := &runner.Fake{}
	cfg, _ := kit.Load(kitDir)
	if _, err := createLoopInstance(kitDir, f, cfg, "nope", io.Discard); err == nil {
		t.Fatal("unknown loop must error")
	}
}
