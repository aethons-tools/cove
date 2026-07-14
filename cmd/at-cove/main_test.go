package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/backend"
	"github.com/aethons-tools/cove/internal/kit"
	"github.com/aethons-tools/cove/internal/mint"
	"github.com/aethons-tools/cove/internal/runner"
	"github.com/aethons-tools/cove/internal/state"
	"github.com/aethons-tools/cove/internal/usersecret"
)

// ptr returns a pointer to s — usersecret.Source.Value is *string so an
// explicit empty literal is distinct from "unset".
func ptr(s string) *string { return &s }

func TestKitDirFlagResolves(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.yml"), []byte("name: k\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var errb bytes.Buffer
	got, code := resolveKitDir(dir, nil, "build", &errb)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errb.String())
	}
	if got == "" {
		t.Fatalf("resolveKitDir returned empty for %q", dir)
	}
}

func TestKitDirFlagRejectsPositional(t *testing.T) {
	var errb bytes.Buffer
	if _, code := resolveKitDir(".", []string{"stray"}, "build", &errb); code != 2 {
		t.Fatalf("code=%d, want 2 for a stray positional", code)
	}
	if !strings.Contains(errb.String(), "--kit-dir") {
		t.Fatalf("error should mention --kit-dir; got %q", errb.String())
	}
}

func TestPlanRequired(t *testing.T) {
	// resolved from the store, keyed by kit name.
	store := usersecret.Store{Kits: map[string]map[string]usersecret.Source{
		"k": {"T": {Value: ptr("v")}},
	}}
	sp, err := planRequired(store, nil, "k", "/kitpath", "T", "/p/secrets.yml")
	if err != nil || !sp.Literal || sp.Value != "v" {
		t.Fatalf("store value should supply a literal: %+v err=%v", sp, err)
	}
	// unresolved → error naming the secret + path.
	if _, err := planRequired(usersecret.Store{}, nil, "k", "/kitpath", "T", "/p/secrets.yml"); err == nil ||
		!strings.Contains(err.Error(), "T") || !strings.Contains(err.Error(), "/p/secrets.yml") {
		t.Fatalf("unresolved must error naming the secret and path; got %v", err)
	}
}

func TestPlanRequiredExpandsMint(t *testing.T) {
	appKey := "/etc/cove/gh.pem"
	store := usersecret.Store{
		Minters: map[string]usersecret.Minter{
			"gh": {GitHub: &usersecret.GitHubMinter{AppID: "1", InstallID: "2", AppKey: usersecret.Source{Value: &appKey}}},
		},
		Kits: map[string]map[string]usersecret.Source{
			"k": {"AT_TASK_GIT_TOKEN": {Mint: "gh"}},
		},
	}
	expand := mint.Expander(&runner.Fake{}, store.Global, "o/r")
	spec, err := planRequired(store, expand, "k", "/p", "AT_TASK_GIT_TOKEN", "/cfg/secrets.yml")
	if err != nil {
		t.Fatal(err)
	}
	if len(spec.Command) == 0 || spec.Command[0] != "at-mint" || spec.Command[1] != "github" {
		t.Fatalf("expected an at-mint github spec, got %v", spec.Command)
	}
}

func TestCanonicalKitPath(t *testing.T) {
	dir := t.TempDir()
	got := canonicalKitPath(dir)
	if !filepath.IsAbs(got) {
		t.Fatalf("canonicalKitPath(%q) = %q, want absolute", dir, got)
	}
}

func TestDispatchTrackerTokenFromSecretsYML(t *testing.T) {
	cfgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	if err := os.MkdirAll(filepath.Join(cfgHome, "at-cove"), 0o755); err != nil {
		t.Fatal(err)
	}
	// secrets.yml supplies the tracker token as a literal, keyed by kit name.
	if err := os.WriteFile(filepath.Join(cfgHome, "at-cove", "secrets.yml"),
		[]byte("kits:\n  dispatch-kit:\n    AT_DISPATCH_TRACKER_TOKEN: { value: \"supplied-tok\" }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A valid dispatch kit (kits declare demand only — no resolver command).
	dir := writeDispatchKit(t, dispatchGoodConfig)
	var out, errOut bytes.Buffer
	code := run([]string{"dispatch", "--kit-dir", dir}, &runner.Fake{}, os.LookupEnv, dummyLookPath, &out, &errOut)
	// It must get PAST token resolution (past "kit OK"); it then fails connecting to
	// Linear (no network), which is fine — the point is the token resolved from secrets.yml.
	if !strings.Contains(out.String(), "kit OK") {
		t.Fatalf("expected to reach token resolution + connect; stdout=%q stderr=%q", out.String(), errOut.String())
	}
	if strings.Contains(errOut.String(), "AT_DISPATCH_TRACKER_TOKEN has no supply entry") {
		t.Fatalf("token should have resolved from secrets.yml; stderr=%q", errOut.String())
	}
	_ = code
}

func writeKit(t *testing.T, dir string) string {
	t.Helper()
	cove := filepath.Join(dir, ".at-cove")
	if err := os.MkdirAll(cove, 0o755); err != nil {
		t.Fatal(err)
	}
	yml := "name: box\n"
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
		if c.Name != "docker" {
			continue
		}
		a := c.Args
		if len(a) >= 2 && a[0] == "--context" { // skip the pinned colima context
			a = a[2:]
		}
		if len(a) > 0 && a[0] == arg0 {
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
	// preflight `docker info` is a Probe (no Output consumed); `inspect` is the Output.
	f := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: "true\n"}}}
	var out, errOut bytes.Buffer
	code := run([]string{"status", "--kit-dir", kitDir}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "running") {
		t.Fatalf("status output = %q", out.String())
	}
}

// TestColimaDownPrintsActionableError guards that a stopped colima surfaces the
// "colima start" guidance to the user — not swallowed by main's ExitError path.
func TestColimaDownPrintsActionableError(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeKit(t, dir)
	writeState(t, kitDir, "colima", "box")
	// The preflight `docker info` Probe fails (colima unreachable).
	f := &runner.Fake{Err: &runner.ExitError{Code: 1}}
	var out, errOut bytes.Buffer
	code := run([]string{"status", "--kit-dir", kitDir}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code == 0 {
		t.Fatal("status must fail when colima is unreachable")
	}
	if !strings.Contains(errOut.String(), "colima start") {
		t.Fatalf("must print actionable colima guidance; stderr=%q", errOut.String())
	}
}

func TestStatusAbsentWhenNoState(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeKit(t, dir)
	var out, errOut bytes.Buffer
	code := run([]string{"status", "--kit-dir", kitDir}, &runner.Fake{}, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 0 || !strings.Contains(out.String(), "absent") {
		t.Fatalf("status with no state: code=%d out=%q", code, out.String())
	}
}

func TestUnknownBackendErrors(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeKit(t, dir)
	writeState(t, kitDir, "bogus", "box") // state names an unknown backend
	var out, errOut bytes.Buffer
	code := run([]string{"status", "--kit-dir", kitDir}, &runner.Fake{}, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code == 0 || !strings.Contains(errOut.String(), "bogus") {
		t.Fatalf("expected unknown-backend error, code=%d stderr=%q", code, errOut.String())
	}
}

func TestDryRunCreatePrintsNoExec(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeKit(t, dir)
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	code := run([]string{"--dry-run", "create", "--kit-dir", kitDir}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
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
	if code := run([]string{"create", "--kit-dir", kitDir}, f, os.LookupEnv, dummyLookPath, &out, &errOut); code != 0 {
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
	code := run([]string{"create", "--kit-dir", kitDir}, &runner.Fake{}, os.LookupEnv, dummyLookPath, &o2, &e2)
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
	if code := run([]string{"destroy", "--kit-dir", kitDir}, f, os.LookupEnv, dummyLookPath, &out, &errOut); code != 0 {
		t.Fatalf("destroy exit=%d stderr=%s", code, errOut.String())
	}
	if dockerArg0Index(f.Calls, "rm") == -1 || dockerArg0Index(f.Calls, "rmi") == -1 {
		t.Fatalf("destroy must rm + rmi; calls=%+v", f.Calls)
	}
	// A real destroy purges the instance's named volumes (no orphaned -state/-workspace).
	vol := dockerArg0Index(f.Calls, "volume")
	if vol == -1 {
		t.Fatalf("destroy must remove the instance volumes; calls=%+v", f.Calls)
	}
	gotState := false
	for _, a := range f.Calls[vol].Args {
		if a == "box-state" {
			gotState = true
		}
	}
	if !gotState {
		t.Fatalf("destroy must remove the box-state (/agent-data) volume; calls=%+v", f.Calls[vol].Args)
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
	code := run([]string{"destroy", "--kit-dir", kitDir}, &runner.Fake{}, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code == 0 || !strings.Contains(errOut.String(), "active connection") {
		t.Fatalf("destroy should refuse with an active connection; code=%d stderr=%q", code, errOut.String())
	}
	if !state.Exists(kitDir) {
		t.Fatal("a blocked destroy must not delete the state file")
	}
}

func TestDryRunChatRawNoAuth(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeKit(t, dir)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // hermetic: no real ~/.config/at-cove/secrets.yml
	writeState(t, kitDir, "colima", "box", state.Secret{Name: "GITHUB_TOKEN", Command: []string{"op", "x"}})
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	code := run([]string{"--dry-run", "chat", "--raw", "--no-auth", "--kit-dir", kitDir},
		f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errOut.String())
	}
	if len(f.Calls) != 0 {
		t.Fatalf("dry-run executed commands: %+v", f.Calls)
	}
	s := out.String()
	if !strings.Contains(s, "bash") || !strings.Contains(s, "no collaborator") {
		t.Fatalf("dry-run chat --raw --no-auth message = %q", s)
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
	code := run([]string{"--dry-run", "recreate", "--kit-dir", kitDir}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
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
	code := run([]string{"recreate", "--kit-dir", kitDir}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
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
	// recreate must NOT purge volumes (the saved login on /agent-data survives).
	if dockerArg0Index(f.Calls, "volume") != -1 {
		t.Fatalf("recreate must keep volumes (no `docker volume rm`): %+v", f.Calls)
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
	code := run([]string{"recreate", "--kit-dir", kitDir}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
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
	code := run([]string{"recreate", "--kit-dir", kitDir, "--ws", newPath}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
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
	code := run([]string{"recreate", "--kit-dir", kitDir}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
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

func TestDryRunChatWarnsUnresolvedSecret(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeKit(t, dir)
	writeState(t, kitDir, "colima", "box", state.Secret{Name: "GITHUB_TOKEN"}) // demanded, no command
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())                                   // empty config dir -> no secrets.yml
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	code := run([]string{"--dry-run", "chat", "--kit-dir", kitDir}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
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

// A kit with no collaborators declared resolves to "no collaborator" in the
// dry-run message, and --fresh is accepted without error (fresh/resume is a
// launch-time detail no longer surfaced in the dry-run summary).
func TestDryRunChatNoCollaboratorFresh(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeKit(t, dir)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	writeState(t, kitDir, "colima", "box", state.Secret{Name: "GITHUB_TOKEN", Command: []string{"op", "x"}})
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	code := run([]string{"--dry-run", "chat", "--fresh", "--kit-dir", kitDir}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errOut.String())
	}
	s := out.String()
	if !strings.Contains(s, "as no collaborator") {
		t.Fatalf("kit with no collaborators should report 'no collaborator'; msg=%q", s)
	}
}

// A kit declaring a single collaborator class auto-selects it (SelectCollaborator's
// "sole class" default), and the dry-run message names it.
func TestDryRunChatResolvesDefaultCollaborator(t *testing.T) {
	dir := t.TempDir()
	kitDir := filepath.Join(dir, ".at-cove")
	if err := os.MkdirAll(kitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	yml := "name: box\ncollaborators:\n  steward:\n    prompt: \"you are the steward\"\n"
	if err := os.WriteFile(filepath.Join(kitDir, "config.yml"), []byte(yml), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	writeState(t, kitDir, "colima", "box", state.Secret{Name: "GITHUB_TOKEN", Command: []string{"op", "x"}})
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	code := run([]string{"--dry-run", "chat", "--kit-dir", kitDir}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "as collaborator steward") {
		t.Fatalf("expected the sole collaborator to be auto-selected; msg=%q", out.String())
	}
}

func TestChatMalformedSecretsFileAborts(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeKit(t, dir)
	writeState(t, kitDir, "colima", "box", state.Secret{Name: "GITHUB_TOKEN"})
	cfgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	coveCfg := filepath.Join(cfgHome, "at-cove")
	if err := os.MkdirAll(coveCfg, 0o700); err != nil {
		t.Fatal(err)
	}
	// Malformed: a supply must set exactly one of value/command/global/mint —
	// this one sets both value and command.
	badYAML := "kits:\n  box:\n    GITHUB_TOKEN: { value: \"x\", command: [\"true\"] }\n"
	if err := os.WriteFile(filepath.Join(coveCfg, "secrets.yml"), []byte(badYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	code := run([]string{"--dry-run", "chat", "--kit-dir", kitDir}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code == 0 {
		t.Fatalf("malformed secrets.yml should abort; out=%q", out.String())
	}
	if !strings.Contains(errOut.String(), "secrets.yml") {
		t.Fatalf("error should name the offending file; stderr=%q", errOut.String())
	}
	if !strings.Contains(errOut.String(), "exactly one of value/command") {
		t.Fatalf("error should explain the malformed source; stderr=%q", errOut.String())
	}
}

func TestSaveStateSnapshot(t *testing.T) {
	dir := t.TempDir()
	cfg := kit.Config{Name: "box", Secrets: map[string]kit.SecretConfig{
		"GITHUB_TOKEN": {Description: "code host token"},
	}}
	inst := backend.Instance{Backend: "colima", Container: "box", Image: "img",
		Workspace: backend.WorkspaceMount{Mode: backend.Isolated}}
	if err := saveState(dir, cfg, inst); err != nil {
		t.Fatal(err)
	}
	st, err := state.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if st.Name != "box" || st.Backend != "colima" || st.Container != "box" || st.Image != "img" {
		t.Fatalf("state = %+v", st)
	}
	if len(st.Secrets) != 1 || st.Secrets[0].Name != "GITHUB_TOKEN" {
		t.Fatalf("secrets not snapshotted: %+v", st.Secrets)
	}
}

func TestWorkRequiresInAndOut(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run([]string{"work", "--kit-dir", "somekit"}, &runner.Fake{}, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 2 {
		t.Fatalf("exit = %d; want 2 (missing --in/--out)", code)
	}
	// Note: a bare "--in" substring check would also match "--interval" in the
	// generic unknown-command usage fallback, so this pins the exact message.
	if !strings.Contains(errOut.String(), "--in and --out are required") {
		t.Fatalf("stderr = %q; want mention of --in/--out being required", errOut.String())
	}
}

func TestWorkRejectsPositionalKitDir(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run([]string{"work", "somekit"}, &runner.Fake{}, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 2 {
		t.Fatalf("exit = %d; want 2 (stray positional)", code)
	}
	if !strings.Contains(errOut.String(), "--kit-dir") {
		t.Fatalf("stderr = %q; want mention of --kit-dir", errOut.String())
	}
}

// --reap must not require --in/--out: it only scavenges labeled orphans.
func TestWorkReapDoesNotRequireInOut(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeKit(t, dir)
	// preflight `docker info` is a Probe (no Output consumed); `docker ps` is the
	// one Output call ScavengeLabeled makes (empty => no orphans to remove).
	f := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: ""}}}
	var out, errOut bytes.Buffer
	code := run([]string{"work", "--kit-dir", kitDir, "--reap"}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d; want 0, stderr=%s", code, errOut.String())
	}
}

func TestWorkRequiresWorkers(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeKit(t, dir) // no workers declared
	inFile := filepath.Join(dir, "in.json")
	outFile := filepath.Join(dir, "out.json")
	if err := os.WriteFile(inFile, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	code := run([]string{"work", "--kit-dir", kitDir, "--in", inFile, "--out", outFile}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 1 {
		t.Fatalf("exit = %d; want 1 (no workers)", code)
	}
	if !strings.Contains(errOut.String(), "declares no workers") {
		t.Fatalf("stderr = %q; want mention of declares no workers", errOut.String())
	}
}

// TestDryRunWorkPrintsNoExec guards Fix A: --dry-run work must print
// the plan and exit 0 without touching the backend, assembling, or resolving
// secrets (no calls recorded on the fake runner at all).
func TestDryRunWorkPrintsNoExec(t *testing.T) {
	dir := t.TempDir()
	cove := filepath.Join(dir, ".at-cove")
	if err := os.MkdirAll(cove, 0o755); err != nil {
		t.Fatal(err)
	}
	yml := "name: box\nworkers:\n  implement:\n    prompt: do the thing\n"
	if err := os.WriteFile(filepath.Join(cove, "config.yml"), []byte(yml), 0o644); err != nil {
		t.Fatal(err)
	}
	inFile := filepath.Join(dir, "in.json")
	if err := os.WriteFile(inFile, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	outFile := filepath.Join(dir, "out.json")
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	code := run([]string{"--dry-run", "work", "--kit-dir", cove, "--in", inFile, "--out", outFile},
		f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, errOut.String())
	}
	if len(f.Calls) != 0 {
		t.Fatalf("dry-run work executed commands: %+v", f.Calls)
	}
	if _, err := os.Stat(outFile); err == nil {
		t.Fatal("dry-run work must not write the output file")
	}
	s := out.String()
	if !strings.Contains(s, "would") || !strings.Contains(s, inFile) || !strings.Contains(s, outFile) {
		t.Fatalf("dry-run work should describe the planned actions incl. --in/--out: %q", s)
	}
}

// TestDryRunWorkReapPrintsNoExec guards --dry-run work --reap: it must
// not call Reap (no ScavengeLabeled == no Output call on the fake).
func TestDryRunWorkReapPrintsNoExec(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeKit(t, dir)
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	code := run([]string{"--dry-run", "work", "--kit-dir", kitDir, "--reap"}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, errOut.String())
	}
	if len(f.Calls) != 0 {
		t.Fatalf("dry-run work --reap executed commands: %+v", f.Calls)
	}
	if !strings.Contains(out.String(), "would") {
		t.Fatalf("dry-run work --reap should describe the planned scavenge: %q", out.String())
	}
}

// dispatchGoodConfig is a complete dispatch-capable kit config.yml (name +
// source-control + tracker.linear + dispatch + workers).
const dispatchGoodConfig = `name: dispatch-kit
source-control:
  github:
    project: your-org/your-repo
    secrets:
      AT_TASK_GIT_TOKEN: {}
tracker:
  linear:
    team: AET
    poll-interval: 60s
    states: { ready: Todo, in-progress: In Progress, in-review: In Review, done: Done, needs-input: Needs Input, blocked: Backlog }
    secrets:
      AT_DISPATCH_TRACKER_TOKEN:  {}
      AT_DISPATCH_WEBHOOK_SECRET: {}
dispatch:
  concurrency: 1
  reaper-timeout: 45m
workers:
  implement: { prompt: "impl", timeout: 30m }
`

// writeDispatchKit writes body as a kit config.yml into a temp dir and returns
// the dir (suitable as the `dispatch` positional kit-dir).
func writeDispatchKit(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.yml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestDispatchTokenResolveFailure: valid kit, but the tracker token resolver
// command fails → dispatch exits 1 before constructing the tracker client.
func TestDispatchTokenResolveFailure(t *testing.T) {
	cfgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	if err := os.MkdirAll(filepath.Join(cfgHome, "at-cove"), 0o755); err != nil {
		t.Fatal(err)
	}
	// secrets.yml supplies the tracker token via a resolver command that fails.
	if err := os.WriteFile(filepath.Join(cfgHome, "at-cove", "secrets.yml"),
		[]byte("kits:\n  dispatch-kit:\n    AT_DISPATCH_TRACKER_TOKEN: { command: [\"false\"] }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := writeDispatchKit(t, dispatchGoodConfig)
	var out, errOut bytes.Buffer
	code := run([]string{"dispatch", "--kit-dir", dir}, &runner.Fake{}, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 1 {
		t.Fatalf("exit = %d; want 1 (stderr: %q)", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "token") {
		t.Fatalf("stderr = %q; want a token-resolution error", errOut.String())
	}
}

// TestDispatchRejectsBadConfig: kit with no tracker section → the missing-surface
// check rejects it, exit 1.
func TestDispatchRejectsBadConfig(t *testing.T) {
	dir := writeDispatchKit(t, "name: dispatch-kit\nworkers:\n  implement: { prompt: \"impl\", timeout: 30m }\n")
	var out, errOut bytes.Buffer
	code := run([]string{"dispatch", "--kit-dir", dir}, &runner.Fake{}, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 1 {
		t.Fatalf("exit = %d; want 1 (stderr: %q)", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "must declare") {
		t.Fatalf("stderr = %q; want the missing-surface error", errOut.String())
	}
}

// TestDispatchRejectsIncompleteKit: a kit missing tracker/dispatch/workers exits 1
// with the missing-surface message.
func TestDispatchRejectsIncompleteKit(t *testing.T) {
	dir := writeDispatchKit(t, "name: dispatch-kit\n")
	var out, errOut bytes.Buffer
	code := run([]string{"dispatch", "--kit-dir", dir}, &runner.Fake{}, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 1 {
		t.Fatalf("exit = %d; want 1 (stderr: %q)", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "must declare") {
		t.Fatalf("stderr = %q; want the missing-surface error", errOut.String())
	}
}
