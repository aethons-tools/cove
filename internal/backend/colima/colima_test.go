package colima

import (
	"strings"
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
	build := dockerCall(f.Calls, "build")
	if build == nil || !contains(build, "at-cove-for-box") {
		t.Fatalf("build call = %+v", f.Calls)
	}
	run := dockerCall(f.Calls, "run")
	if run == nil || !contains(run, "box-workspace:/home/agent/workspace") || !contains(run, "box-state:/agent-data") {
		t.Fatalf("isolated run call = %+v", f.Calls)
	}
	if !allPinned(f.Calls) {
		t.Fatalf("every docker call must pin --context colima: %+v", f.Calls)
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
	run := dockerCall(f.Calls, "run")
	if run == nil || !contains(run, "/host/repo:/home/agent/workspace") {
		t.Fatalf("shared bind missing: %+v", f.Calls)
	}
}

func TestDialParsesDockerPort(t *testing.T) {
	// preflight `info` is a Probe (no Output consumed); `port` is the only Output.
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
		// preflight `info` is a Probe (no Output consumed); `inspect` is the Output.
		f := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: c.out, Err: c.err}}}
		got, err := New(f).GetStatus("box")
		if err != nil {
			t.Fatalf("status(%q,%v) errored: %v", c.out, c.err, err)
		}
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

// TestDestroyKeepsVolumes guards the invariant that `recreate` relies on: with
// keepVolumes=true, Destroy force-removes the container but never its named
// volumes (no -v/--volumes, no `volume rm`), so /agent-data (saved login) and the
// workspace survive a recreate. It also removes the image to keep the namespace clean.
func TestDestroyKeepsVolumes(t *testing.T) {
	f := &runner.Fake{}
	err := New(f).Destroy(backend.Instance{Container: "box", Image: "at-cove-for-box"}, true)
	if err != nil {
		t.Fatal(err)
	}
	rm := dockerCall(f.Calls, "rm")
	if rm == nil || !contains(rm, "-f") || !contains(rm, "box") {
		t.Fatalf("destroy should force-remove the container: %+v", f.Calls)
	}
	for _, a := range rm {
		if a == "-v" || a == "--volumes" {
			t.Fatalf("destroy must not remove volumes: %v", rm)
		}
	}
	if vol := dockerCall(f.Calls, "volume"); vol != nil {
		t.Fatalf("keepVolumes must not issue `docker volume rm`: %v", vol)
	}
	rmi := dockerCall(f.Calls, "rmi")
	if rmi == nil || !contains(rmi, "at-cove-for-box") {
		t.Fatalf("destroy should remove the image: %+v", f.Calls)
	}
}

// TestDestroyPurgesVolumes: a real `destroy` (keepVolumes=false) removes the
// instance's named volumes — `<container>-state` (/agent-data, the saved login)
// and `<container>-workspace` — so nothing lingers. The container is still
// force-removed first (volumes can't be removed while in use).
func TestDestroyPurgesVolumes(t *testing.T) {
	f := &runner.Fake{}
	err := New(f).Destroy(backend.Instance{Container: "box", Image: "at-cove-for-box"}, false)
	if err != nil {
		t.Fatal(err)
	}
	rmIdx, volIdx := -1, -1
	for i, c := range f.Calls {
		if c.Name != "docker" {
			continue
		}
		a := c.Args[2:] // skip --context colima
		if a[0] == "rm" {
			rmIdx = i
		}
		if a[0] == "volume" {
			volIdx = i
		}
	}
	vol := dockerCall(f.Calls, "volume")
	if vol == nil || !contains(vol, "rm") || !contains(vol, "box-state") || !contains(vol, "box-workspace") {
		t.Fatalf("destroy must remove the -state and -workspace volumes: %+v", f.Calls)
	}
	if rmIdx == -1 || volIdx == -1 || rmIdx > volIdx {
		t.Fatalf("container rm must precede volume rm: %+v", f.Calls)
	}
	if !allPinned(f.Calls) {
		t.Fatalf("every docker call must pin --context colima: %+v", f.Calls)
	}
}

func TestCreateSharesImageViaKit(t *testing.T) {
	f := &runner.Fake{}
	c := New(f)
	_, err := c.Create(backend.CreateContext{
		Name:      "box-loop-foo",
		BuildDir:  "/b",
		Kit:       "box",
		Workspace: backend.WorkspaceMount{Mode: backend.Isolated},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Build tags the SHARED kit image (derived from Kit), not from the instance Name.
	build := dockerCall(f.Calls, "build")
	if build == nil || !contains(build, "at-cove-for-box") {
		t.Fatalf("build call = %+v", f.Calls)
	}
	if contains(build, "at-cove-for-box-loop-foo") {
		t.Fatalf("must not derive a per-loop image tag: %v", build)
	}
	// Container and volumes still derive from Name.
	run := dockerCall(f.Calls, "run")
	if run == nil || !contains(run, "box-loop-foo") || !contains(run, "box-loop-foo-state:/agent-data") {
		t.Fatalf("container/volumes must derive from Name: %+v", f.Calls)
	}
}

// TestPinsContext: every docker call carries --context colima, so cove never
// uses a different daemon than colima.
func TestPinsContext(t *testing.T) {
	f := &runner.Fake{}
	if _, err := New(f).Create(backend.CreateContext{Name: "box", BuildDir: "/b"}); err != nil {
		t.Fatal(err)
	}
	if len(f.Calls) == 0 || !allPinned(f.Calls) {
		t.Fatalf("docker calls must pin --context colima: %+v", f.Calls)
	}
	// The very first call is the preflight `info`.
	if info := dockerCall(f.Calls, "info"); info == nil {
		t.Fatalf("expected a preflight `docker info`: %+v", f.Calls)
	}
}

// TestPreflightFailsActionably: when colima is unreachable, operations fail with
// a message that names `colima start`, instead of a cryptic docker error.
func TestPreflightFailsActionably(t *testing.T) {
	// preflight probes with `docker info`; an unreachable daemon makes that Probe
	// return Fake.Err, which every operation must surface actionably.
	mkFail := func() *runner.Fake {
		return &runner.Fake{Err: &runner.ExitError{Code: 1}}
	}
	// Create
	if _, err := New(mkFail()).Create(backend.CreateContext{Name: "box", BuildDir: "/b"}); err == nil || !strings.Contains(err.Error(), "colima start") {
		t.Fatalf("Create should fail actionably; err=%v", err)
	}
	// Dial
	if _, _, err := New(mkFail()).Dial("box"); err == nil || !strings.Contains(err.Error(), "colima start") {
		t.Fatalf("Dial should fail actionably; err=%v", err)
	}
	// Destroy
	if err := New(mkFail()).Destroy(backend.Instance{Container: "box"}, false); err == nil || !strings.Contains(err.Error(), "colima start") {
		t.Fatalf("Destroy should fail actionably; err=%v", err)
	}
	// GetStatus now surfaces the unreachable error instead of a misleading "absent".
	if _, err := New(mkFail()).GetStatus("box"); err == nil || !strings.Contains(err.Error(), "colima start") {
		t.Fatalf("GetStatus should fail actionably; err=%v", err)
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

// dockerCall returns the args of the first `docker <sub>` call, skipping the
// pinned `--context colima` prefix, or nil if absent.
func dockerCall(calls []runner.Call, sub string) []string {
	for _, c := range calls {
		if c.Name != "docker" {
			continue
		}
		a := c.Args
		if len(a) >= 2 && a[0] == "--context" {
			a = a[2:]
		}
		if len(a) > 0 && a[0] == sub {
			return a
		}
	}
	return nil
}

// allPinned reports whether every docker call begins with `--context colima`.
func allPinned(calls []runner.Call) bool {
	for _, c := range calls {
		if c.Name != "docker" {
			continue
		}
		if len(c.Args) < 2 || c.Args[0] != "--context" || c.Args[1] != dockerContext {
			return false
		}
	}
	return true
}
