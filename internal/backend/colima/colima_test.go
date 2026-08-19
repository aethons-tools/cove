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
	// Plain progress: BuildKit's TTY progress renderer right-aligns each step's
	// duration and can overflow the terminal by a column (a stray wrapped "s").
	// Plain output is line-by-line and never wraps that way.
	if !contains(build, "--progress=plain") {
		t.Fatalf("build must pass --progress=plain: %v", build)
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

// TestCreateDNS: a configured image.dns pins the container's resolvers on the
// persistent create path, one --dns pair per IP; an empty image.dns emits no
// --dns flag so the container inherits Docker's default resolver (COV-106).
func TestCreateDNS(t *testing.T) {
	f := &runner.Fake{}
	if _, err := New(f).Create(backend.CreateContext{
		Name: "box", Image: "atcove-box",
		Workspace: backend.WorkspaceMount{Mode: backend.Isolated},
		DNS:       []string{"10.0.0.53"},
	}); err != nil {
		t.Fatal(err)
	}
	run := dockerCall(f.Calls, "run")
	if got := strings.Join(run, " "); !strings.Contains(got, "--dns 10.0.0.53") {
		t.Fatalf("create run must pin the configured resolver:\n%s", got)
	}

	f2 := &runner.Fake{}
	if _, err := New(f2).Create(backend.CreateContext{
		Name: "box", Image: "atcove-box",
		Workspace: backend.WorkspaceMount{Mode: backend.Isolated},
	}); err != nil {
		t.Fatal(err)
	}
	if contains(dockerCall(f2.Calls, "run"), "--dns") {
		t.Fatalf("create run with no image.dns must not pin a resolver: %+v", f2.Calls)
	}
}

// sysboxRuntimesOutput is a `docker info -f '{{json .Runtimes}}'` payload that
// registers the sysbox-runc runtime, so a docker:true preflight passes.
const sysboxRuntimesOutput = `{"runc":{"path":"runc"},"sysbox-runc":{"path":"/usr/bin/sysbox-runc"}}`

// TestCreateDocker: a docker:true kit runs the sandbox container under Sysbox —
// --runtime=sysbox-runc, -e COVE_DOCKER=1, and a persistent -docker cache volume
// mounted at the inner /var/lib/docker — recorded on the instance so destroy
// removes it (COV-117). No --privileged/socket/--device/--security-opt, ever.
func TestCreateDocker(t *testing.T) {
	f := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: sysboxRuntimesOutput}}}
	inst, err := New(f).Create(backend.CreateContext{
		Name: "box", Image: "atcove-box", Docker: true,
		Workspace: backend.WorkspaceMount{Mode: backend.Isolated},
	})
	if err != nil {
		t.Fatal(err)
	}
	run := dockerCall(f.Calls, "run")
	got := strings.Join(run, " ")
	for _, want := range []string{"--runtime=sysbox-runc", "-e COVE_DOCKER=1", "-v box-docker:/var/lib/docker"} {
		if !strings.Contains(got, want) {
			t.Fatalf("docker:true create must emit %q:\n%s", want, got)
		}
	}
	for _, banned := range []string{"--privileged", "docker.sock", "--device", "--security-opt"} {
		if strings.Contains(got, banned) {
			t.Fatalf("docker:true create must never emit %q:\n%s", banned, got)
		}
	}
	// docker:true boots systemd as PID 1 (the entrypoint execs /sbin/init under
	// Sysbox — COV-118), so tini (--init) must be omitted; running systemd under
	// tini would make it a non-PID-1 child and break it.
	if strings.Contains(got, "--init") {
		t.Fatalf("docker:true create must omit --init (systemd is PID 1):\n%s", got)
	}
	if inst.Volumes.Docker != "box-docker" {
		t.Fatalf("docker:true create must record the -docker volume; got %+v", inst.Volumes)
	}
}

// TestCreateNoDockerByteForByte: with docker unset the run argv is byte-for-byte
// what it was before COV-117 — no --runtime, no COVE_DOCKER, no -docker volume,
// and no docker-info runtimes probe.
func TestCreateNoDockerByteForByte(t *testing.T) {
	f := &runner.Fake{}
	inst, err := New(f).Create(backend.CreateContext{
		Name: "box", Image: "atcove-box",
		Workspace: backend.WorkspaceMount{Mode: backend.Isolated},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(dockerCall(f.Calls, "run"), " ")
	for _, banned := range []string{"--runtime", "COVE_DOCKER", "-docker"} {
		if strings.Contains(got, banned) {
			t.Fatalf("a non-docker create must not emit %q:\n%s", banned, got)
		}
	}
	// A non-docker sandbox keeps tini as PID 1 (only docker:true swaps to systemd).
	if !strings.Contains(got, "--init") {
		t.Fatalf("a non-docker create must keep --init (tini as PID 1):\n%s", got)
	}
	if inst.Volumes.Docker != "" {
		t.Fatalf("a non-docker create must record no -docker volume; got %+v", inst.Volumes)
	}
	// The Runtimes probe (an Output-consuming `docker info -f …`) only runs for
	// docker:true; a non-docker create consumes no queued Output.
	if f.Outputs != nil {
		t.Fatalf("test setup: this case queues no Outputs")
	}
}

// TestCreateDockerPreflightRequiresSysbox: docker:true fails fast with an
// actionable message when the colima VM's docker daemon does not register the
// sysbox-runc runtime — at-cove detects but does not install (COV-117).
func TestCreateDockerPreflightRequiresSysbox(t *testing.T) {
	f := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: `{"runc":{"path":"runc"}}`}}}
	_, err := New(f).Create(backend.CreateContext{
		Name: "box", Image: "atcove-box", Docker: true,
		Workspace: backend.WorkspaceMount{Mode: backend.Isolated},
	})
	if err == nil || !strings.Contains(err.Error(), "sysbox-runc") || !strings.Contains(err.Error(), "Sysbox") {
		t.Fatalf("docker:true must fail actionably when sysbox-runc is absent; err=%v", err)
	}
	if dockerCall(f.Calls, "run") != nil {
		t.Fatalf("preflight must fail before running the container: %+v", f.Calls)
	}
}

// TestDestroyPurgesDockerVolume: a real destroy removes the recorded -docker
// volume alongside -agent-data/-workspace (COV-117).
func TestDestroyPurgesDockerVolume(t *testing.T) {
	f := &runner.Fake{}
	err := New(f).Destroy(backend.Instance{
		Container: "box",
		Volumes:   backend.VolumeSet{State: "box-agent-data", Workspace: "box-workspace", Docker: "box-docker"},
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	vol := dockerCall(f.Calls, "volume")
	if vol == nil || !contains(vol, "box-docker") {
		t.Fatalf("destroy must remove the recorded -docker volume: %+v", f.Calls)
	}
}

// TestDockerArgs pins the pure arg-builder: the three Sysbox flags when on,
// nothing at all when off (so the run argv stays byte-for-byte unchanged).
func TestDockerArgs(t *testing.T) {
	if a := dockerArgs(false, "box-docker"); a != nil {
		t.Errorf("docker off must yield no flags; got %v", a)
	}
	got := strings.Join(dockerArgs(true, "box-docker"), " ")
	if got != "--runtime=sysbox-runc -e COVE_DOCKER=1 -v box-docker:/var/lib/docker" {
		t.Errorf("dockerArgs = %q", got)
	}
}

// TestDNSArgs pins the pure arg-builder: one --dns pair per IP, nothing for empty.
func TestDNSArgs(t *testing.T) {
	if a := dnsArgs(nil); a != nil {
		t.Errorf("empty dns must yield no flags; got %v", a)
	}
	got := strings.Join(dnsArgs([]string{"1.1.1.1", "8.8.8.8"}), " ")
	if got != "--dns 1.1.1.1 --dns 8.8.8.8" {
		t.Errorf("dnsArgs = %q", got)
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
// A shared workspace with shadow-dirs overmounts each dir with its own volume and
// signals the list to the entrypoint via COVE_SHADOW_DIRS (COV-130).
func TestCreateSharedShadowDirs(t *testing.T) {
	f := &runner.Fake{}
	inst, err := New(f).Create(backend.CreateContext{
		Name: "box", Image: "atcove-box",
		Workspace: backend.WorkspaceMount{
			Mode: backend.Shared, HostPath: "/host/repo",
			ShadowDirs: []string{".venv", "node_modules"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	run := dockerCall(f.Calls, "run")
	for _, want := range []string{
		"box-shadow-venv:/home/agent/workspace/.venv",
		"box-shadow-node_modules:/home/agent/workspace/node_modules",
		"COVE_SHADOW_DIRS=.venv node_modules",
	} {
		if !contains(run, want) {
			t.Errorf("run argv missing %q: %+v", want, run)
		}
	}
	if len(inst.Volumes.Shadow) != 2 || inst.Volumes.Shadow[0] != "box-shadow-venv" {
		t.Fatalf("shadow volume names not recorded: %+v", inst.Volumes.Shadow)
	}
}

// A shared workspace with no shadow-dirs emits no shadow flags (unchanged path).
func TestCreateSharedNoShadowDirs(t *testing.T) {
	f := &runner.Fake{}
	if _, err := New(f).Create(backend.CreateContext{
		Name: "box", Image: "atcove-box",
		Workspace: backend.WorkspaceMount{Mode: backend.Shared, HostPath: "/host/repo"},
	}); err != nil {
		t.Fatal(err)
	}
	if contains(dockerCall(f.Calls, "run"), "COVE_SHADOW_DIRS=") {
		t.Fatal("no shadow-dirs must emit no COVE_SHADOW_DIRS")
	}
}

// Destroy removes the recorded shadow volumes alongside the others (COV-130).
func TestDestroyRemovesShadowVolumes(t *testing.T) {
	f := &runner.Fake{}
	err := New(f).Destroy(backend.Instance{
		Backend: "colima", Container: "box",
		Volumes: backend.VolumeSet{State: "box-agent-data", Shadow: []string{"box-shadow-venv"}},
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	rm := dockerCall(f.Calls, "volume")
	if !contains(rm, "box-shadow-venv") {
		t.Fatalf("destroy must rm shadow volume: %+v", rm)
	}
}

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
