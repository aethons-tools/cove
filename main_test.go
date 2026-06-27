package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
