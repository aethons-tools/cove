package colima

import (
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/backend"
	"github.com/aethons-tools/cove/internal/runner"
)

// TestInstallBuildsGatesTags: Install is the sole docker-build path — it resolves
// the base, builds the assembled context FROM it, tags atcove-<kit>, and does
// NOT run a container. With no kit base, the base resolves to the blessed default
// (no gate inspect needed), which Install records as the InstalledImage.BaseDigest.
func TestInstallBuildsGatesTags(t *testing.T) {
	f := &runner.Fake{}
	installed, err := New(f).Install(backend.InstallContext{Kit: "box", BuildDir: "/b"})
	if err != nil {
		t.Fatal(err)
	}
	build := dockerCall(f.Calls, "build")
	if build == nil || !contains(build, "--build-arg") || !contains(build, "-t") ||
		!contains(build, "atcove-box") || !contains(build, "/b") {
		t.Fatalf("build call = %+v", f.Calls)
	}
	if dockerCall(f.Calls, "run") != nil {
		t.Fatalf("Install must not run a container: %+v", f.Calls)
	}
	if !allPinned(f.Calls) {
		t.Fatalf("every docker call must pin --context colima: %+v", f.Calls)
	}
	if installed.Ref != "atcove-box" || installed.BaseDigest == "" {
		t.Fatalf("installed = %+v", installed)
	}
	// The BASE build-arg carries the resolved base Install reports.
	if !contains(build, "BASE="+installed.BaseDigest) {
		t.Fatalf("build must pass the resolved base as BASE=; build=%v base=%q", build, installed.BaseDigest)
	}
}

// TestInstallCapturesBuiltImageDigest: after building + tagging, Install inspects
// the built tag for its own image ID and reports it as InstalledImage.Digest, so a
// run can pin the exact built image rather than the mutable tag (COV-78). This is
// the built-image digest, distinct from BaseDigest (the FROM-base digest).
func TestInstallCapturesBuiltImageDigest(t *testing.T) {
	f := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: "sha256:cafe\n"}}}
	installed, err := New(f).Install(backend.InstallContext{Kit: "box", BuildDir: "/b"})
	if err != nil {
		t.Fatal(err)
	}
	if installed.Digest != "sha256:cafe" {
		t.Fatalf("Install must capture the built image digest; got %q", installed.Digest)
	}
	insp := dockerCall(f.Calls, "inspect")
	if insp == nil || !contains(insp, "{{.Id}}") || !contains(insp, "atcove-box") {
		t.Fatalf("Install must inspect the built tag for its image ID: %+v", f.Calls)
	}
	if !allPinned(f.Calls) {
		t.Fatalf("every docker call must pin --context colima: %+v", f.Calls)
	}
}

// TestCreatePinsDigest: when Install captured a built-image digest, Create runs
// that digest (the immutable pin) instead of the mutable tag, while still
// recording the human-readable tag on the instance for display/diagnostics
// (COV-78). A create with no digest (legacy manifest) falls back to the tag —
// covered by TestCreateIsolated.
func TestCreatePinsDigest(t *testing.T) {
	f := &runner.Fake{}
	inst, err := New(f).Create(backend.CreateContext{
		Name: "box", Image: "atcove-box", Digest: "sha256:cafe",
		Workspace: backend.WorkspaceMount{Mode: backend.Isolated},
	})
	if err != nil {
		t.Fatal(err)
	}
	run := dockerCall(f.Calls, "run")
	if run == nil || !contains(run, "sha256:cafe") {
		t.Fatalf("Create must run the digest-pinned image: %+v", f.Calls)
	}
	if contains(run, "atcove-box") {
		t.Fatalf("Create must run the digest, not the mutable tag: %v", run)
	}
	if inst.Image != "atcove-box" || inst.ImageDigest != "sha256:cafe" {
		t.Fatalf("instance must keep the tag for display and record the digest: %+v", inst)
	}
}

// TestInstallPreflightFailsActionably: an unreachable colima surfaces the
// `colima start` guidance from Install, like every other op.
func TestInstallPreflightFailsActionably(t *testing.T) {
	f := &runner.Fake{Err: &runner.ExitError{Code: 1}}
	if _, err := New(f).Install(backend.InstallContext{Kit: "box", BuildDir: "/b"}); err == nil ||
		!strings.Contains(err.Error(), "colima start") {
		t.Fatalf("Install should fail actionably; err=%v", err)
	}
}

// TestCreateIsolated: Create is run-only (COV-38) — it runs the pre-built image
// (ctx.Image) with the state + isolated-workspace volumes and does NOT build.
func TestCreateIsolated(t *testing.T) {
	f := &runner.Fake{}
	b := New(f)
	inst, err := b.Create(backend.CreateContext{
		Name: "box", Image: "atcove-box",
		Workspace: backend.WorkspaceMount{Mode: backend.Isolated},
	})
	if err != nil {
		t.Fatal(err)
	}
	if dockerCall(f.Calls, "build") != nil {
		t.Fatalf("Create must not build (run-only): %+v", f.Calls)
	}
	run := dockerCall(f.Calls, "run")
	if run == nil || !contains(run, "atcove-box") || !contains(run, "box-workspace:/home/agent/workspace") || !contains(run, "box-agent-data:/agent-data") {
		t.Fatalf("isolated run call = %+v", f.Calls)
	}
	if !allPinned(f.Calls) {
		t.Fatalf("every docker call must pin --context colima: %+v", f.Calls)
	}
	if inst.Container != "box" || inst.Image != "atcove-box" || inst.Backend != "colima" {
		t.Fatalf("instance = %+v", inst)
	}
}

func TestCreateShared(t *testing.T) {
	f := &runner.Fake{}
	if _, err := New(f).Create(backend.CreateContext{
		Name: "box", Image: "atcove-box",
		Workspace: backend.WorkspaceMount{Mode: backend.Shared, HostPath: "/host/repo"},
	}); err != nil {
		t.Fatal(err)
	}
	run := dockerCall(f.Calls, "run")
	if run == nil || !contains(run, "/host/repo:/home/agent/workspace") {
		t.Fatalf("shared bind missing: %+v", f.Calls)
	}
}

// TestCreateRecordsVolumeNames: Create reports the named volumes it actually
// created, so teardown removes exactly those rather than re-deriving them from
// the container name (COV-76). An isolated workspace records both -agent-data and
// -workspace; a shared (bind-mount) workspace records only -agent-data.
func TestCreateRecordsVolumeNames(t *testing.T) {
	inst, err := New(&runner.Fake{}).Create(backend.CreateContext{
		Name: "box", Image: "atcove-box",
		Workspace: backend.WorkspaceMount{Mode: backend.Isolated},
	})
	if err != nil {
		t.Fatal(err)
	}
	if inst.Volumes.State != "box-agent-data" || inst.Volumes.Workspace != "box-workspace" {
		t.Fatalf("isolated create must record both volume names; got %+v", inst.Volumes)
	}

	inst, err = New(&runner.Fake{}).Create(backend.CreateContext{
		Name: "box", Image: "atcove-box",
		Workspace: backend.WorkspaceMount{Mode: backend.Shared, HostPath: "/host/repo"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if inst.Volumes.State != "box-agent-data" || inst.Volumes.Workspace != "" {
		t.Fatalf("shared create records only the -agent-data volume (no workspace volume); got %+v", inst.Volumes)
	}
}

// TestDestroyUsesRecordedVolumeNames: a real destroy removes the volume names
// recorded on the instance, not names re-derived from the container (COV-76) —
// the single-source-of-truth invariant. Only -state is removed when no workspace
// volume was recorded (a shared workspace).
func TestDestroyUsesRecordedVolumeNames(t *testing.T) {
	f := &runner.Fake{}
	err := New(f).Destroy(backend.Instance{
		Container: "box",
		Volumes:   backend.VolumeSet{State: "recorded-state", Workspace: "recorded-workspace"},
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	vol := dockerCall(f.Calls, "volume")
	if vol == nil || !contains(vol, "recorded-state") || !contains(vol, "recorded-workspace") {
		t.Fatalf("destroy must remove the recorded volume names: %+v", f.Calls)
	}
	if contains(vol, "box-state") || contains(vol, "box-workspace") {
		t.Fatalf("destroy must not re-derive volume names from the container: %v", vol)
	}
}

// TestDestroyFallsBackToContainerDerivedVolumes: a legacy instance that recorded
// no volume names (Volumes zero) falls back to the historical
// <container>-state/-workspace reconstruction, so sandboxes created before COV-76
// still tear down cleanly.
func TestDestroyFallsBackToContainerDerivedVolumes(t *testing.T) {
	f := &runner.Fake{}
	if err := New(f).Destroy(backend.Instance{Container: "box"}, false); err != nil {
		t.Fatal(err)
	}
	vol := dockerCall(f.Calls, "volume")
	if vol == nil || !contains(vol, "box-state") || !contains(vol, "box-workspace") {
		t.Fatalf("legacy destroy must fall back to container-derived volume names: %+v", f.Calls)
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
// workspace survive a recreate. It must also NOT remove the image: the image is
// an `install` artifact (COV-38), and recreate re-runs it without rebuilding —
// deleting it would leave `docker run` with no image to run (COV-63).
func TestDestroyKeepsVolumes(t *testing.T) {
	f := &runner.Fake{}
	err := New(f).Destroy(backend.Instance{Container: "box", Image: "atcove-box"}, true)
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
	if rmi := dockerCall(f.Calls, "rmi"); rmi != nil {
		t.Fatalf("destroy must NOT remove the image (it belongs to install; recreate re-runs it): %+v", f.Calls)
	}
}

// TestDestroyPurgesVolumes: a real `destroy` (keepVolumes=false) removes the
// instance's named volumes — `<container>-state` (/agent-data, the saved login)
// and `<container>-workspace` — so nothing lingers. The container is still
// force-removed first (volumes can't be removed while in use).
func TestDestroyPurgesVolumes(t *testing.T) {
	f := &runner.Fake{}
	err := New(f).Destroy(backend.Instance{Container: "box", Image: "atcove-box"}, false)
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
	// A full destroy tears down the instance (container + volumes), but the image
	// is an install artifact — leave it, so install.json stays consistent and a
	// later create can re-run it without a rebuild (COV-63).
	if rmi := dockerCall(f.Calls, "rmi"); rmi != nil {
		t.Fatalf("destroy must NOT remove the image: %+v", f.Calls)
	}
	if !allPinned(f.Calls) {
		t.Fatalf("every docker call must pin --context colima: %+v", f.Calls)
	}
}

// TestRemoveImage: RemoveImage is the inverse of Install — it `docker rmi`s the
// kit's compiled image (pinned to the colima context) and nothing else. It is the
// sole image-teardown path (COV-64); Destroy never touches the image (COV-63).
func TestRemoveImage(t *testing.T) {
	f := &runner.Fake{}
	if err := New(f).RemoveImage("atcove-box"); err != nil {
		t.Fatal(err)
	}
	rmi := dockerCall(f.Calls, "rmi")
	if rmi == nil || !contains(rmi, "atcove-box") {
		t.Fatalf("RemoveImage must docker rmi the image: %+v", f.Calls)
	}
	if dockerCall(f.Calls, "rm") != nil || dockerCall(f.Calls, "volume") != nil {
		t.Fatalf("RemoveImage must not touch containers or volumes: %+v", f.Calls)
	}
	if !allPinned(f.Calls) {
		t.Fatalf("every docker call must pin --context colima: %+v", f.Calls)
	}
}

// TestRemoveImagePreflightFailsActionably: an unreachable colima surfaces the
// `colima start` guidance from RemoveImage, like every other op.
func TestRemoveImagePreflightFailsActionably(t *testing.T) {
	f := &runner.Fake{Err: &runner.ExitError{Code: 1}}
	if err := New(f).RemoveImage("atcove-box"); err == nil || !strings.Contains(err.Error(), "colima start") {
		t.Fatalf("RemoveImage should fail actionably; err=%v", err)
	}
}

// TestCreateRunsGivenImageContainerFromName: Create runs the pre-built image it
// is handed (ctx.Image), while the container and its volumes still derive from
// Name — so one installed kit image can back several distinct instances.
func TestCreateRunsGivenImageContainerFromName(t *testing.T) {
	f := &runner.Fake{}
	c := New(f)
	_, err := c.Create(backend.CreateContext{
		Name:      "box-loop-foo",
		Image:     "atcove-box",
		Workspace: backend.WorkspaceMount{Mode: backend.Isolated},
	})
	if err != nil {
		t.Fatal(err)
	}
	if dockerCall(f.Calls, "build") != nil {
		t.Fatalf("Create must not build (run-only): %+v", f.Calls)
	}
	// run uses the shared kit image, while container + volumes derive from Name.
	run := dockerCall(f.Calls, "run")
	if run == nil || !contains(run, "atcove-box") || !contains(run, "box-loop-foo") || !contains(run, "box-loop-foo-agent-data:/agent-data") {
		t.Fatalf("container/volumes must derive from Name over the given image: %+v", f.Calls)
	}
}

// TestPinsContext: every docker call carries --context colima, so cove never
// uses a different daemon than colima.
func TestPinsContext(t *testing.T) {
	f := &runner.Fake{}
	if _, err := New(f).Create(backend.CreateContext{Name: "box", Image: "atcove-box"}); err != nil {
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
	if _, err := New(mkFail()).Create(backend.CreateContext{Name: "box", Image: "atcove-box"}); err == nil || !strings.Contains(err.Error(), "colima start") {
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
