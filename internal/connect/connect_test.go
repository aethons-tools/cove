package connect

import (
	"os"
	"path/filepath"
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

func (b *fakeBackend) Create(backend.CreateContext) error { return nil }
func (b *fakeBackend) Destroy(string) error               { return nil }
func (b *fakeBackend) GetStatus(string) (backend.State, error) {
	b.statusCalled = true
	return b.state, nil
}
func (b *fakeBackend) Dial(string) (backend.Endpoint, func(), error) {
	b.dialCalled = true
	return backend.Endpoint{Host: "h", Port: 22, User: "agent"}, func() { b.cleaned = true }, b.dialErr
}

type fakeTransport struct {
	launched bool
	gotEnv   map[string]string
}

func (t *fakeTransport) Launch(_ sshargs.Target, env map[string]string) error {
	t.launched = true
	t.gotEnv = env
	return nil
}

func opts(dir string) Options {
	return Options{
		Name:          "box",
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
	if err := Connect(b, r, tr, opts(t.TempDir())); err != nil {
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
	if err := Connect(b, r, tr, opts(t.TempDir())); err != nil {
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
	if err := Connect(b, r, tr, opts(t.TempDir())); err != nil {
		t.Fatal(err)
	}
	if calledWith(r.Calls, loginCmd) {
		t.Fatal("must skip login when credentials already exist")
	}
}

func TestConnectAuthProbeFailureAborts(t *testing.T) {
	b := &fakeBackend{state: backend.StateRunning}
	tr := &fakeTransport{}
	// Secret resolves, but the auth probe's ssh fails (e.g. connection error).
	r := &runner.Fake{
		Outputs: []runner.FakeResult{{Stdout: "tok\n"}, {Err: &runner.ExitError{Code: 255}}},
	}
	err := Connect(b, r, tr, opts(t.TempDir()))
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
	err := Connect(b, r, tr, opts(t.TempDir()))
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
	err := Connect(b, r, &fakeTransport{}, opts(t.TempDir()))
	if err == nil || b.dialCalled {
		t.Fatalf("stopped VM should error before Dial; err=%v dial=%v", err, b.dialCalled)
	}
}

func TestConnectCreatesKnownHostsDir(t *testing.T) {
	dir := t.TempDir()
	b := &fakeBackend{state: backend.StateRunning}
	r := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: "tok"}, {Stdout: "cove-authed"}}}
	if err := Connect(b, r, &fakeTransport{}, opts(dir)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "known_hosts.d")); err != nil {
		t.Fatalf("known_hosts dir not created: %v", err)
	}
}
