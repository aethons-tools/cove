package connect

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/backend"
	"github.com/aethons-tools/cove/internal/runner"
	"github.com/aethons-tools/cove/internal/secret"
	"github.com/aethons-tools/cove/internal/sshargs"
)

// calledWith reports whether any recorded call carried an argument containing s.
func calledWith(calls []runner.Call, s string) bool {
	for _, c := range calls {
		for _, a := range c.Args {
			if strings.Contains(a, s) {
				return true
			}
		}
	}
	return false
}

type fakeBackend struct {
	state        backend.State
	dialErr      error
	cleaned      bool
	statusCalled bool
	dialCalled   bool
}

func (b *fakeBackend) Install(backend.InstallContext) (backend.InstalledImage, error) {
	return backend.InstalledImage{}, nil
}
func (b *fakeBackend) Create(backend.CreateContext) (backend.Instance, error) {
	return backend.Instance{}, nil
}
func (b *fakeBackend) Destroy(backend.Instance, bool) error { return nil }
func (b *fakeBackend) RemoveImage(string) error             { return nil }
func (b *fakeBackend) GetStatus(string) (backend.State, error) {
	b.statusCalled = true
	return b.state, nil
}
func (b *fakeBackend) Dial(string) (backend.Endpoint, func(), error) {
	b.dialCalled = true
	return backend.Endpoint{Host: "h", Port: 22, User: "agent"}, func() { b.cleaned = true }, b.dialErr
}

// rec records the ordered lifecycle events of a Connect so tests can assert
// the sleep assertion brackets the launch.
type rec struct{ ev []string }

type fakeInhibitor struct {
	r   *rec
	err error
}

func (f *fakeInhibitor) Inhibit() (func(), error) {
	if f.err != nil {
		return nil, f.err
	}
	f.r.ev = append(f.r.ev, "inhibit")
	return func() { f.r.ev = append(f.r.ev, "release") }, nil
}

type fakeTransport struct {
	launched bool
	gotEnv   map[string]string
	r        *rec // optional; records "launch" when set
}

func (t *fakeTransport) Launch(_ sshargs.Target, env map[string]string) error {
	t.launched = true
	t.gotEnv = env
	if t.r != nil {
		t.r.ev = append(t.r.ev, "launch")
	}
	return nil
}

func opts(dir string) Options {
	return Options{
		Container:     "box",
		Secrets:       []secret.Spec{{Name: "GITHUB_TOKEN", Command: []string{"op", "x"}}},
		IdentityFile:  "/id",
		KnownHostsDir: filepath.Join(dir, "known_hosts.d"),
	}
}

// cloneOpts is opts plus a workspace-clone request with a literal token (so
// resolving it consumes no queued Output).
func cloneOpts(dir string) Options {
	o := opts(dir)
	o.WorkspaceClone = &WorkspaceClone{
		RepoURL: "https://github.com/acme/myrepo.git",
		Branch:  "main",
		Token:   secret.Spec{Name: "AT_TASK_GIT_TOKEN", Value: "ghp_secret_value", Literal: true},
	}
	return o
}

// On first session start into an empty isolated workspace, Connect clones the
// repo's default branch in — with the derived URL and branch — before launching,
// and the token flows via stdin (never on argv).
func TestConnectClonesEmptyWorkspace(t *testing.T) {
	b := &fakeBackend{state: backend.StateRunning}
	tr := &fakeTransport{}
	// [0] secret, [1] auth probe, [2] workspace probe (empty), [3] the clone.
	r := &runner.Fake{Outputs: []runner.FakeResult{
		{Stdout: "tok\n"}, {Stdout: "cove-authed\n"}, {Stdout: "cove-ws-empty\n"}, {Stdout: ""},
	}}
	if err := Connect(b, r, tr, &fakeInhibitor{r: &rec{}}, cloneOpts(t.TempDir())); err != nil {
		t.Fatal(err)
	}
	// The clone runs at-task's task-less verb, not a raw git clone: all
	// git-with-token lives in ShellGit, so connect owns no second askpass.
	cloneIdx := callIndex(r.Calls, "clone-workspace")
	if cloneIdx == -1 {
		t.Fatalf("expected an at-task clone-workspace into the empty workspace; calls=%+v", r.Calls)
	}
	joined := strings.Join(r.Calls[cloneIdx].Args, " ")
	if !strings.Contains(joined, "at-task clone-workspace") {
		t.Fatalf("must invoke the at-task clone verb; args=%q", joined)
	}
	if !strings.Contains(joined, "https://github.com/acme/myrepo.git") {
		t.Fatalf("clone must use the derived repo URL; args=%q", joined)
	}
	if !strings.Contains(joined, "main") {
		t.Fatalf("clone must check out the default branch; args=%q", joined)
	}
	if !strings.Contains(joined, workspaceDir) {
		t.Fatalf("clone must target the workspace dir; args=%q", joined)
	}
	if !tr.launched {
		t.Fatal("must still launch the session after cloning")
	}
	// The token value must never reach argv — only stdin (the tmpfs env file).
	if calledWith(r.Calls, "ghp_secret_value") {
		t.Fatalf("token value leaked onto argv; calls=%+v", r.Calls)
	}
	var stagedViaStdin bool
	for _, c := range r.Calls {
		if strings.Contains(c.Stdin, "ghp_secret_value") {
			stagedViaStdin = true
			// The token stages into at-task's own env var, not a connect-side askpass.
			if !strings.Contains(c.Stdin, "AT_TASK_GIT_TOKEN") {
				t.Fatalf("token must stage into at-task's AT_TASK_GIT_TOKEN env; stdin=%q", c.Stdin)
			}
		}
	}
	if !stagedViaStdin {
		t.Fatalf("token must be staged into the VM via stdin; calls=%+v", r.Calls)
	}
	// No second askpass: connect must not write its own askpass script anymore.
	if calledWith(r.Calls, "AT_TASK_ASKPASS_TOKEN") {
		t.Fatalf("connect must not implement its own askpass; calls=%+v", r.Calls)
	}
	for _, c := range r.Calls {
		if strings.Contains(c.Stdin, "AT_TASK_ASKPASS_TOKEN") {
			t.Fatalf("connect must not stage its own askpass; stdin=%q", c.Stdin)
		}
	}
}

// Reconnecting to a VM whose workspace already holds a checkout must not
// re-clone — the once-per-lifetime guard reuses the existing (possibly
// in-progress) workspace.
func TestConnectSkipsCloneWhenWorkspaceReady(t *testing.T) {
	b := &fakeBackend{state: backend.StateRunning}
	tr := &fakeTransport{}
	r := &runner.Fake{Outputs: []runner.FakeResult{
		{Stdout: "tok\n"}, {Stdout: "cove-authed\n"}, {Stdout: "cove-ws-cloned\n"},
	}}
	if err := Connect(b, r, tr, &fakeInhibitor{r: &rec{}}, cloneOpts(t.TempDir())); err != nil {
		t.Fatal(err)
	}
	if calledWith(r.Calls, "clone-workspace") {
		t.Fatalf("must not re-clone a workspace that already has a checkout; calls=%+v", r.Calls)
	}
	if !tr.launched {
		t.Fatal("must still launch the session")
	}
}

// With no WorkspaceClone (shared workspace, or source-control/token not
// configured), Connect never probes or clones the workspace.
func TestConnectNoCloneWhenUnset(t *testing.T) {
	b := &fakeBackend{state: backend.StateRunning}
	tr := &fakeTransport{}
	r := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: "tok\n"}, {Stdout: "cove-authed\n"}}}
	if err := Connect(b, r, tr, &fakeInhibitor{r: &rec{}}, opts(t.TempDir())); err != nil {
		t.Fatal(err)
	}
	if calledWith(r.Calls, "clone-workspace") || calledWith(r.Calls, "cove-ws-") {
		t.Fatalf("no clone/probe expected without a WorkspaceClone; calls=%+v", r.Calls)
	}
}

// A configured clone that fails is a hard error: the session must not launch.
func TestConnectCloneFailureAborts(t *testing.T) {
	b := &fakeBackend{state: backend.StateRunning}
	tr := &fakeTransport{}
	r := &runner.Fake{Outputs: []runner.FakeResult{
		{Stdout: "tok\n"}, {Stdout: "cove-authed\n"}, {Stdout: "cove-ws-empty\n"},
		{Err: &runner.ExitError{Code: 128}},
	}}
	err := Connect(b, r, tr, &fakeInhibitor{r: &rec{}}, cloneOpts(t.TempDir()))
	if err == nil {
		t.Fatal("expected a hard error when the clone fails")
	}
	if tr.launched {
		t.Fatal("must not launch the session after a failed clone")
	}
}

func TestConnectHappyPath(t *testing.T) {
	b := &fakeBackend{state: backend.StateRunning}
	tr := &fakeTransport{}
	// Outputs are consumed in order: [0] the secret command, [1] the auth probe.
	r := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: "tok\n"}, {Stdout: "cove-authed\n"}}}
	if err := Connect(b, r, tr, &fakeInhibitor{r: &rec{}}, opts(t.TempDir())); err != nil {
		t.Fatal(err)
	}
	if !tr.launched || tr.gotEnv["GITHUB_TOKEN"] != "tok" {
		t.Fatalf("transport not launched with env: %+v", tr)
	}
	if !b.cleaned {
		t.Fatal("Dial cleanup not invoked")
	}
	if calledWith(r.Calls, loginCmd) {
		t.Fatal("must not run login when the sandbox is already authenticated")
	}
}

func TestConnectFirstSessionRunsLogin(t *testing.T) {
	b := &fakeBackend{state: backend.StateRunning}
	tr := &fakeTransport{}
	r := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: "tok\n"}, {Stdout: "cove-noauth\n"}}}
	if err := Connect(b, r, tr, &fakeInhibitor{r: &rec{}}, opts(t.TempDir())); err != nil {
		t.Fatal(err)
	}
	if !calledWith(r.Calls, loginCmd) {
		t.Fatalf("first session must run %q; calls=%+v", loginCmd, r.Calls)
	}
	if !tr.launched {
		t.Fatal("agent must still launch after a successful login")
	}
}

func TestConnectSkipsLoginWhenAuthed(t *testing.T) {
	b := &fakeBackend{state: backend.StateRunning}
	tr := &fakeTransport{}
	r := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: "tok\n"}, {Stdout: "cove-authed\n"}}}
	if err := Connect(b, r, tr, &fakeInhibitor{r: &rec{}}, opts(t.TempDir())); err != nil {
		t.Fatal(err)
	}
	if calledWith(r.Calls, loginCmd) {
		t.Fatal("must skip login when credentials already exist")
	}
}

func TestConnectSkipAuth(t *testing.T) {
	b := &fakeBackend{state: backend.StateRunning}
	tr := &fakeTransport{}
	// Only the secret output; no auth probe should be consumed.
	r := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: "tok\n"}}}
	o := opts(t.TempDir())
	o.SkipAuth = true
	if err := Connect(b, r, tr, &fakeInhibitor{r: &rec{}}, o); err != nil {
		t.Fatal(err)
	}
	if calledWith(r.Calls, "claude auth status") {
		t.Fatal("--no-auth must not run the auth probe")
	}
	if calledWith(r.Calls, loginCmd) {
		t.Fatal("--no-auth must not run login")
	}
	if !tr.launched {
		t.Fatal("must still launch after skipping auth")
	}
}

func TestWriteCollaboratorRole(t *testing.T) {
	f := &runner.Fake{}
	if err := writeCollaboratorRole(f, sshargs.Target{}, "you are the steward"); err != nil {
		t.Fatal(err)
	}
	var wrote bool
	for _, c := range f.Calls {
		if strings.Contains(strings.Join(c.Args, " "), collaboratorVMPath) && c.Stdin == "you are the steward" {
			wrote = true
		}
	}
	if !wrote {
		t.Fatalf("no ssh write of %s with the prompt; calls=%+v", collaboratorVMPath, f.Calls)
	}
}

func TestWriteCollaboratorRoleEmptyWritesPlaceholder(t *testing.T) {
	f := &runner.Fake{}
	if err := writeCollaboratorRole(f, sshargs.Target{}, ""); err != nil {
		t.Fatal(err)
	}
	if len(f.Calls) == 0 || f.Calls[0].Stdin == "" {
		t.Fatalf("empty prompt must still write a placeholder; calls=%+v", f.Calls)
	}
}

// callIndex returns the index of the first recorded call whose args contain s,
// or -1 if none does.
func callIndex(calls []runner.Call, s string) int {
	for i, c := range calls {
		for _, a := range c.Args {
			if strings.Contains(a, s) {
				return i
			}
		}
	}
	return -1
}

const seedMark = "cat > " + credsVMPath

// With a saved host credentials file, Connect seeds it into the VM before the
// auth probe (so a login from another sandbox is reused), pipes the exact saved
// bytes, and skips login when the seeded creds validate.
func TestConnectSeedsSavedCredentialsBeforeProbe(t *testing.T) {
	dir := t.TempDir()
	credsFile := filepath.Join(dir, "credentials.json")
	want := `{"claudeAiOauth":{"accessToken":"a","refreshToken":"r"}}`
	if err := os.WriteFile(credsFile, []byte(want), 0o600); err != nil {
		t.Fatal(err)
	}
	b := &fakeBackend{state: backend.StateRunning}
	tr := &fakeTransport{}
	r := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: "tok\n"}, {Stdout: "cove-authed\n"}}}
	o := opts(dir)
	o.CredentialsFile = credsFile
	if err := Connect(b, r, tr, &fakeInhibitor{r: &rec{}}, o); err != nil {
		t.Fatal(err)
	}
	seedIdx, probeIdx := callIndex(r.Calls, seedMark), callIndex(r.Calls, authProbe)
	if seedIdx == -1 {
		t.Fatalf("saved credentials were not seeded into the VM; calls=%+v", r.Calls)
	}
	if probeIdx == -1 || seedIdx > probeIdx {
		t.Fatalf("seed must precede the auth probe; seed=%d probe=%d", seedIdx, probeIdx)
	}
	if got := r.Calls[seedIdx].Stdin; got != want {
		t.Fatalf("seeded %q into the VM, want %q", got, want)
	}
	if calledWith(r.Calls, loginCmd) {
		t.Fatal("must not run login when seeded credentials validate")
	}
}

// On a first-ever login (no saved host file), Connect runs login and then saves
// the VM's fresh credentials back to the host at mode 0600.
func TestConnectSavesCredentialsAfterLogin(t *testing.T) {
	dir := t.TempDir()
	credsFile := filepath.Join(dir, "credentials.json") // does not exist yet
	creds := `{"claudeAiOauth":{"refreshToken":"fresh"}}`
	b := &fakeBackend{state: backend.StateRunning}
	tr := &fakeTransport{}
	// Outputs: [0] secret, [1] auth probe (noauth), [2] post-login cat, [3] session-end cat.
	r := &runner.Fake{Outputs: []runner.FakeResult{
		{Stdout: "tok\n"}, {Stdout: "cove-noauth\n"}, {Stdout: creds}, {Stdout: creds},
	}}
	o := opts(dir)
	o.CredentialsFile = credsFile
	if err := Connect(b, r, tr, &fakeInhibitor{r: &rec{}}, o); err != nil {
		t.Fatal(err)
	}
	if !calledWith(r.Calls, loginCmd) {
		t.Fatal("first session must run login")
	}
	got, err := os.ReadFile(credsFile)
	if err != nil {
		t.Fatalf("credentials not saved to host: %v", err)
	}
	if string(got) != creds {
		t.Fatalf("saved %q, want %q", got, creds)
	}
	if info, _ := os.Stat(credsFile); info.Mode().Perm() != 0o600 {
		t.Fatalf("saved credentials mode %v, want 0600", info.Mode().Perm())
	}
}

// When the session refreshes (rotates) the VM credentials, Connect persists the
// new copy to the host at session end even though no interactive login ran.
func TestConnectSavesRotatedCredentialsAtSessionEnd(t *testing.T) {
	dir := t.TempDir()
	credsFile := filepath.Join(dir, "credentials.json")
	old, rotated := `{"v":1}`, `{"v":2}`
	if err := os.WriteFile(credsFile, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	b := &fakeBackend{state: backend.StateRunning}
	tr := &fakeTransport{}
	// Outputs: [0] secret, [1] auth probe (authed), [2] session-end cat -> rotated.
	r := &runner.Fake{Outputs: []runner.FakeResult{
		{Stdout: "tok\n"}, {Stdout: "cove-authed\n"}, {Stdout: rotated},
	}}
	o := opts(dir)
	o.CredentialsFile = credsFile
	if err := Connect(b, r, tr, &fakeInhibitor{r: &rec{}}, o); err != nil {
		t.Fatal(err)
	}
	if calledWith(r.Calls, loginCmd) {
		t.Fatal("authed session must not run login")
	}
	got, _ := os.ReadFile(credsFile)
	if string(got) != rotated {
		t.Fatalf("session end must persist rotated credentials; got %q want %q", got, rotated)
	}
}

// --no-auth disables the whole credentials dance: no seed, no VM creds access.
func TestConnectSkipAuthSkipsCredentials(t *testing.T) {
	dir := t.TempDir()
	credsFile := filepath.Join(dir, "credentials.json")
	if err := os.WriteFile(credsFile, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	b := &fakeBackend{state: backend.StateRunning}
	tr := &fakeTransport{}
	r := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: "tok\n"}}}
	o := opts(dir)
	o.CredentialsFile = credsFile
	o.SkipAuth = true
	if err := Connect(b, r, tr, &fakeInhibitor{r: &rec{}}, o); err != nil {
		t.Fatal(err)
	}
	if calledWith(r.Calls, credsVMPath) {
		t.Fatal("--no-auth must not seed or read VM credentials")
	}
}

func TestConnectAuthProbeFailureAborts(t *testing.T) {
	b := &fakeBackend{state: backend.StateRunning}
	tr := &fakeTransport{}
	// Secret resolves, but the auth probe's ssh fails (e.g. connection error).
	r := &runner.Fake{
		Outputs: []runner.FakeResult{{Stdout: "tok\n"}, {Err: &runner.ExitError{Code: 255}}},
	}
	err := Connect(b, r, tr, &fakeInhibitor{r: &rec{}}, opts(t.TempDir()))
	if err == nil {
		t.Fatal("expected error when the auth probe fails")
	}
	if calledWith(r.Calls, loginCmd) || tr.launched {
		t.Fatal("must not attempt login or launch when the probe connection fails")
	}
}

func TestConnectSecretFailureAbortsBeforeDial(t *testing.T) {
	b := &fakeBackend{state: backend.StateRunning}
	tr := &fakeTransport{}
	r := &runner.Fake{Outputs: []runner.FakeResult{{Err: &runner.ExitError{Code: 1}}}}
	err := Connect(b, r, tr, &fakeInhibitor{r: &rec{}}, opts(t.TempDir()))
	if err == nil {
		t.Fatal("expected error")
	}
	if b.dialCalled || tr.launched {
		t.Fatal("must not Dial or Launch after secret failure")
	}
}

func TestConnectRequiresRunning(t *testing.T) {
	b := &fakeBackend{state: backend.StateStopped}
	r := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: "tok"}}}
	err := Connect(b, r, &fakeTransport{}, &fakeInhibitor{r: &rec{}}, opts(t.TempDir()))
	if err == nil || b.dialCalled {
		t.Fatalf("stopped VM should error before Dial; err=%v dial=%v", err, b.dialCalled)
	}
}

func TestConnectCreatesKnownHostsDir(t *testing.T) {
	dir := t.TempDir()
	b := &fakeBackend{state: backend.StateRunning}
	r := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: "tok"}, {Stdout: "cove-authed"}}}
	if err := Connect(b, r, &fakeTransport{}, &fakeInhibitor{r: &rec{}}, opts(dir)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "known_hosts.d")); err != nil {
		t.Fatalf("known_hosts dir not created: %v", err)
	}
}

func TestConnectInhibitsSleepAroundLaunch(t *testing.T) {
	r := &rec{}
	b := &fakeBackend{state: backend.StateRunning}
	tr := &fakeTransport{r: r}
	run := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: "tok\n"}, {Stdout: "cove-authed\n"}}}
	if err := Connect(b, run, tr, &fakeInhibitor{r: r}, opts(t.TempDir())); err != nil {
		t.Fatal(err)
	}
	want := []string{"inhibit", "launch", "release"}
	if !reflect.DeepEqual(r.ev, want) {
		t.Fatalf("event order = %v, want %v", r.ev, want)
	}
}

func TestConnect_VertexSeedsADCAndSkipsOAuth(t *testing.T) {
	r := &runner.Fake{}
	b := &fakeBackend{state: backend.StateRunning}
	tr := &fakeTransport{}
	err := Connect(b, r, tr, &fakeInhibitor{r: &rec{}}, Options{
		Container:     "c1",
		IdentityFile:  "id",
		KnownHostsDir: t.TempDir(),
		ExtraEnv:      map[string]string{"CLAUDE_CODE_USE_VERTEX": "1", "CLOUD_ML_REGION": "us-east5"},
		Vertex:        &VertexAuth{ADC: []byte(`{"type":"authorized_user"}`)},
	})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	// The ADC was seeded to the VM path via stdin (never argv).
	seeded := false
	for _, c := range r.Calls {
		if calledWith([]runner.Call{c}, gcpADCVMPath) && strings.Contains(c.Stdin, "authorized_user") {
			seeded = true
		}
	}
	if !seeded {
		t.Fatalf("ADC was not seeded to %s via stdin; calls: %+v", gcpADCVMPath, r.Calls)
	}
	// Anthropic OAuth was skipped entirely.
	if calledWith(r.Calls, "claude auth status") || calledWith(r.Calls, "claude auth login") {
		t.Fatalf("vertex connect must not run claude auth; calls: %+v", r.Calls)
	}
	// The launch env carries the provider env plus the ADC pointer.
	if tr.gotEnv["CLAUDE_CODE_USE_VERTEX"] != "1" || tr.gotEnv["CLOUD_ML_REGION"] != "us-east5" {
		t.Fatalf("launch env missing provider vars: %v", tr.gotEnv)
	}
	if tr.gotEnv["GOOGLE_APPLICATION_CREDENTIALS"] != gcpADCVMPath {
		t.Fatalf("GOOGLE_APPLICATION_CREDENTIALS = %q, want %q", tr.gotEnv["GOOGLE_APPLICATION_CREDENTIALS"], gcpADCVMPath)
	}
}

// --no-auth on a Vertex session ("I manage auth myself") must be coherent: the
// ADC file must not be seeded, and GOOGLE_APPLICATION_CREDENTIALS must not be
// set in the launch env — a set-but-unseeded pointer would be worse than unset.
func TestConnect_VertexSkipAuthDoesNotSeedOrSetEnv(t *testing.T) {
	r := &runner.Fake{}
	b := &fakeBackend{state: backend.StateRunning}
	tr := &fakeTransport{}
	err := Connect(b, r, tr, &fakeInhibitor{r: &rec{}}, Options{
		Container:     "c1",
		IdentityFile:  "id",
		KnownHostsDir: t.TempDir(),
		SkipAuth:      true,
		ExtraEnv:      map[string]string{"CLAUDE_CODE_USE_VERTEX": "1", "CLOUD_ML_REGION": "us-east5"},
		Vertex:        &VertexAuth{ADC: []byte(`{"type":"authorized_user"}`)},
	})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if calledWith(r.Calls, gcpADCVMPath) {
		t.Fatalf("--no-auth must not seed the ADC file; calls: %+v", r.Calls)
	}
	if v, set := tr.gotEnv["GOOGLE_APPLICATION_CREDENTIALS"]; set {
		t.Fatalf("--no-auth must not set GOOGLE_APPLICATION_CREDENTIALS in the launch env; got %q", v)
	}
	// The non-secret provider env is still applied — only the credential pointer
	// is suppressed.
	if tr.gotEnv["CLAUDE_CODE_USE_VERTEX"] != "1" || tr.gotEnv["CLOUD_ML_REGION"] != "us-east5" {
		t.Fatalf("launch env missing provider vars: %v", tr.gotEnv)
	}
}

func TestConnectInhibitFailureWarnsAndLaunches(t *testing.T) {
	b := &fakeBackend{state: backend.StateRunning}
	tr := &fakeTransport{}
	run := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: "tok\n"}, {Stdout: "cove-authed\n"}}}
	var errBuf bytes.Buffer
	o := opts(t.TempDir())
	o.Stderr = &errBuf
	if err := Connect(b, run, tr, &fakeInhibitor{err: errors.New("no caffeinate")}, o); err != nil {
		t.Fatal(err)
	}
	if !tr.launched {
		t.Fatal("must launch even when the sleep assertion fails")
	}
	if !strings.Contains(errBuf.String(), "could not prevent host sleep") {
		t.Fatalf("expected warning; stderr=%q", errBuf.String())
	}
}
