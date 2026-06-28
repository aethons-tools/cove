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

func (b *fakeBackend) Create(backend.CreateContext) (backend.Instance, error) {
	return backend.Instance{}, nil
}
func (b *fakeBackend) Destroy(backend.Instance) error { return nil }
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

func TestConnectRunsSetupWhenConfigured(t *testing.T) {
	b := &fakeBackend{state: backend.StateRunning}
	tr := &fakeTransport{}
	// Outputs consumed in order: secret, authProbe, setup emptiness probe.
	r := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: "tok\n"}, {Stdout: "cove-authed\n"}, {Stdout: "cove-empty\n"}}}
	o := opts(t.TempDir())
	o.Setup = "git clone https://x ."
	if err := Connect(b, r, tr, &fakeInhibitor{r: &rec{}}, o); err != nil {
		t.Fatal(err)
	}
	if !calledWith(r.Calls, "git clone https://x .") {
		t.Fatal("setup must run when configured and the workspace is empty")
	}
	if !tr.launched {
		t.Fatal("must still launch after setup")
	}
}

func TestConnectNoSetupWhenUnset(t *testing.T) {
	b := &fakeBackend{state: backend.StateRunning}
	tr := &fakeTransport{}
	r := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: "tok\n"}, {Stdout: "cove-authed\n"}}}
	if err := Connect(b, r, tr, &fakeInhibitor{r: &rec{}}, opts(t.TempDir())); err != nil {
		t.Fatal(err)
	}
	if calledWith(r.Calls, "ls -A") {
		t.Fatal("must not probe the workspace when no setup is configured")
	}
}
