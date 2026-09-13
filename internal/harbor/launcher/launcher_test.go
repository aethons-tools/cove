package launcher

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/backend"
	"github.com/aethons-tools/cove/internal/harbor"
	"github.com/aethons-tools/cove/internal/runner"
)

type fakeOps struct {
	ran        bool
	runName    string
	runImage   string
	runAddHost []string
	removed    string
	status     backend.State
	statusErr  error
	runErr     error
}

func (f *fakeOps) RunEphemeral(image, digest, name, label string, dns, addHosts []string, docker bool) (backend.Instance, error) {
	f.ran, f.runName, f.runImage, f.runAddHost = true, name, image, addHosts
	if f.runErr != nil {
		return backend.Instance{}, f.runErr
	}
	return backend.Instance{Container: name, Image: image}, nil
}
func (f *fakeOps) Dial(container string) (backend.Endpoint, func(), error) {
	return backend.Endpoint{Host: "127.0.0.1", Port: 2222, User: "agent"}, func() {}, nil
}
func (f *fakeOps) RemoveContainer(name string) error { f.removed = name; return nil }
func (f *fakeOps) Pause(name string) error           { return nil }
func (f *fakeOps) Unpause(name string) error         { return nil }
func (f *fakeOps) ScavengeLabeled(label string, olderThan time.Duration, now time.Time) (int, error) {
	return 0, nil
}
func (f *fakeOps) GetStatus(container string) (backend.State, error) { return f.status, f.statusErr }

// failingRunner is a Runner whose Run/RunStdin always error, for exercising the
// Raise cleanup-on-launch-failure path (there is no built-in runner.Failing()).
type failingRunner struct{}

func (failingRunner) Run(name string, args ...string) error { return errors.New("boom") }
func (failingRunner) RunEnv(extraEnv []string, name string, args ...string) error {
	return errors.New("boom")
}
func (failingRunner) Output(name string, args ...string) (string, error) {
	return "", errors.New("boom")
}
func (failingRunner) OutputEnv(extraEnv []string, name string, args ...string) (string, error) {
	return "", errors.New("boom")
}
func (failingRunner) Probe(name string, args ...string) error { return errors.New("boom") }
func (failingRunner) RunStdin(stdin io.Reader, name string, args ...string) error {
	return errors.New("boom")
}
func (failingRunner) RunIO(stdin io.Reader, stdout, stderr io.Writer, name string, args ...string) error {
	return errors.New("boom")
}

func newLauncher(ops *fakeOps) *Launcher {
	return New(Config{
		Ops: ops, Runner: &runner.Fake{},
		Image: "atcove-worker", ImageDigest: "sha256:abc",
		HarborHost: "harbor.example.com", RuntimeAddr: "harbor.example.com:443",
		IdentityFile: "k", KnownHostsDir: "/kh",
		sleep: func(time.Duration) {}, // injected no-op wait-for-sshd
	})
}

func TestRaiseRunsDialsLaunches(t *testing.T) {
	ops := &fakeOps{}
	l := newLauncher(ops)
	loc, err := l.Raise(context.Background(), harbor.RaiseSpec{ActorID: "w1", Prompt: "go"}, harbor.LaunchCreds{IdentityToken: "t", LaunchSecret: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if !ops.ran || ops.runName != "atcove-cove-w1" || ops.runImage != "atcove-worker" {
		t.Fatalf("RunEphemeral not called correctly: %+v", ops)
	}
	if len(ops.runAddHost) != 1 || ops.runAddHost[0] != "harbor.example.com" {
		t.Fatalf("addHosts = %v, want [harbor.example.com]", ops.runAddHost)
	}
	if loc != "atcove-cove-w1" {
		t.Fatalf("location = %q, want atcove-cove-w1", loc)
	}
}

func TestRaiseRemovesContainerOnLaunchFailure(t *testing.T) {
	ops := &fakeOps{}
	l := New(Config{
		Ops: ops, Runner: failingRunner{}, // a Runner whose Run/RunStdin returns an error
		Image: "img", HarborHost: "h", RuntimeAddr: "h:443", IdentityFile: "k", KnownHostsDir: "/kh",
		sleep: func(time.Duration) {},
	})
	_, err := l.Raise(context.Background(), harbor.RaiseSpec{ActorID: "w1"}, harbor.LaunchCreds{})
	if err == nil {
		t.Fatal("want error on launch failure")
	}
	if ops.removed != "atcove-cove-w1" {
		t.Fatalf("container not cleaned up on failure: removed=%q", ops.removed)
	}
}

func TestTeardownRemoves(t *testing.T) {
	ops := &fakeOps{}
	if err := newLauncher(ops).Teardown(context.Background(), harbor.Instance{Location: "atcove-cove-w1"}); err != nil {
		t.Fatal(err)
	}
	if ops.removed != "atcove-cove-w1" {
		t.Fatalf("removed = %q", ops.removed)
	}
}

func TestProbeMapsState(t *testing.T) {
	cases := []struct {
		st   backend.State
		err  error
		want harbor.Liveness
	}{
		{backend.StateRunning, nil, harbor.LivenessAlive},
		{backend.StateStopped, nil, harbor.LivenessDead},
		{backend.StateAbsent, nil, harbor.LivenessDead},
		{backend.StateAbsent, errors.New("docker hiccup"), harbor.LivenessUnknown},
	}
	for _, c := range cases {
		ops := &fakeOps{status: c.st, statusErr: c.err}
		got, _ := newLauncher(ops).Probe(context.Background(), harbor.Instance{Location: "x"})
		if got != c.want {
			t.Fatalf("state %v err %v → %v, want %v", c.st, c.err, got, c.want)
		}
	}
}
