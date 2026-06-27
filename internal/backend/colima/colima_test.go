package colima

import (
	"testing"

	"github.com/aethons-tools/at-sbx/internal/backend"
	"github.com/aethons-tools/at-sbx/internal/runner"
)

func TestCreateIsolated(t *testing.T) {
	f := &runner.Fake{}
	b := New(f)
	err := b.Create(backend.CreateContext{
		Name: "box", BuildDir: "/b",
		Workspace: backend.WorkspaceMount{Mode: backend.Isolated},
	})
	if err != nil {
		t.Fatal(err)
	}
	if f.Calls[0].Name != "docker" || f.Calls[0].Args[0] != "build" {
		t.Fatalf("build call = %+v", f.Calls[0])
	}
	run := f.Calls[1].Args
	if !contains(run, "-v") || !contains(run, "box-workspace:/home/agent/workspace") {
		t.Fatalf("isolated workspace volume missing: %v", run)
	}
	if !contains(run, "box-state:/agent-data") {
		t.Fatalf("state volume missing: %v", run)
	}
}

func TestCreateShared(t *testing.T) {
	f := &runner.Fake{}
	New(f).Create(backend.CreateContext{
		Name: "box", BuildDir: "/b",
		Workspace: backend.WorkspaceMount{Mode: backend.Shared, HostPath: "/host/repo"},
	})
	if !contains(f.Calls[1].Args, "/host/repo:/home/agent/workspace") {
		t.Fatalf("shared bind missing: %v", f.Calls[1].Args)
	}
}

func TestDialParsesDockerPort(t *testing.T) {
	f := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: "127.0.0.1:49153\n"}}}
	ep, cleanup, err := New(f).Dial("box")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if ep.Host != "127.0.0.1" || ep.Port != 49153 || ep.User != "agent" {
		t.Fatalf("ep = %+v", ep)
	}
}

func TestGetStatus(t *testing.T) {
	cases := []struct {
		out  string
		err  error
		want backend.State
	}{
		{out: "true\n", want: backend.StateRunning},
		{out: "false\n", want: backend.StateStopped},
		{out: "", err: &runner.ExitError{Code: 1}, want: backend.StateAbsent},
	}
	for _, c := range cases {
		f := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: c.out, Err: c.err}}}
		got, _ := New(f).GetStatus("box")
		if got != c.want {
			t.Fatalf("status(%q,%v) = %v want %v", c.out, c.err, got, c.want)
		}
	}
}

func TestRegistered(t *testing.T) {
	if _, err := backend.Get("colima"); err != nil {
		t.Fatalf("colima not registered: %v", err)
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
