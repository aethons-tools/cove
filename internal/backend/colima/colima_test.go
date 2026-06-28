package colima

import (
	"testing"

	"github.com/aethons-tools/cove/internal/backend"
	"github.com/aethons-tools/cove/internal/runner"
)

func TestCreateIsolated(t *testing.T) {
	f := &runner.Fake{}
	b := New(f)
	inst, err := b.Create(backend.CreateContext{
		Name: "box", BuildDir: "/b",
		Workspace: backend.WorkspaceMount{Mode: backend.Isolated},
	})
	if err != nil {
		t.Fatal(err)
	}
	if f.Calls[0].Name != "docker" || f.Calls[0].Args[0] != "build" || !contains(f.Calls[0].Args, "at-cove-for-box") {
		t.Fatalf("build call = %+v", f.Calls[0])
	}
	run := f.Calls[1].Args
	if !contains(run, "-v") || !contains(run, "box-workspace:/home/agent/workspace") {
		t.Fatalf("isolated workspace volume missing: %v", run)
	}
	if !contains(run, "box-state:/agent-data") {
		t.Fatalf("state volume missing: %v", run)
	}
	if inst.Container != "box" || inst.Image != "at-cove-for-box" || inst.Backend != "colima" {
		t.Fatalf("instance = %+v", inst)
	}
}

func TestCreateShared(t *testing.T) {
	f := &runner.Fake{}
	if _, err := New(f).Create(backend.CreateContext{
		Name: "box", BuildDir: "/b",
		Workspace: backend.WorkspaceMount{Mode: backend.Shared, HostPath: "/host/repo"},
	}); err != nil {
		t.Fatal(err)
	}
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

// TestDestroyKeepsVolumes guards the invariant that `recreate` relies on: Destroy
// force-removes the container but never its named volumes (no -v/--volumes), so
// /agent-data (saved login) and the workspace survive a recreate. It also removes
// the image to keep the namespace clean.
func TestDestroyKeepsVolumes(t *testing.T) {
	f := &runner.Fake{}
	err := New(f).Destroy(backend.Instance{Container: "box", Image: "at-cove-for-box"})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Calls) != 2 {
		t.Fatalf("expected rm + rmi, got %+v", f.Calls)
	}
	rm := f.Calls[0]
	if rm.Name != "docker" || rm.Args[0] != "rm" || !contains(rm.Args, "-f") || !contains(rm.Args, "box") {
		t.Fatalf("destroy should force-remove the container: %+v", rm)
	}
	for _, a := range rm.Args {
		if a == "-v" || a == "--volumes" {
			t.Fatalf("destroy must not remove volumes: %v", rm.Args)
		}
	}
	rmi := f.Calls[1]
	if rmi.Name != "docker" || rmi.Args[0] != "rmi" || !contains(rmi.Args, "at-cove-for-box") {
		t.Fatalf("destroy should remove the image: %+v", rmi)
	}
}

func TestCreateUsesProvidedImage(t *testing.T) {
	f := &runner.Fake{}
	c := New(f)
	_, err := c.Create(backend.CreateContext{
		Name:      "box-loop-foo",
		BuildDir:  "/b",
		Image:     "at-cove-for-box",
		Workspace: backend.WorkspaceMount{Mode: backend.Isolated},
	})
	if err != nil {
		t.Fatal(err)
	}
	// build tags the SHARED image, not one derived from the instance name.
	if f.Calls[0].Args[0] != "build" || !contains(f.Calls[0].Args, "at-cove-for-box") {
		t.Fatalf("build call = %+v", f.Calls[0])
	}
	if contains(f.Calls[0].Args, "at-cove-for-box-loop-foo") {
		t.Fatalf("must not derive a per-loop image tag: %+v", f.Calls[0])
	}
	// container and volumes still derive from Name.
	run := f.Calls[1].Args
	if !contains(run, "box-loop-foo") || !contains(run, "box-loop-foo-state:/agent-data") {
		t.Fatalf("container/volumes must derive from Name: %v", run)
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
