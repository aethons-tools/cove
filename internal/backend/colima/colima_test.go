package colima

import (
	"errors"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/backend"
	"github.com/aethons-tools/cove/internal/runner"
)

func TestAddHostArgs(t *testing.T) {
	if a := addHostArgs(nil); a != nil {
		t.Fatalf("addHostArgs(nil) = %v, want nil", a)
	}
	got := strings.Join(addHostArgs([]string{"h.one", "h.two"}), " ")
	if got != "--add-host h.one:host-gateway --add-host h.two:host-gateway" {
		t.Fatalf("addHostArgs = %q", got)
	}
}

// sysboxRuntimesOutput is a `docker info -f '{{json .Runtimes}}'` payload that
// registers the sysbox-runc runtime, so a docker:true preflight passes.
const sysboxRuntimesOutput = `{"runc":{"path":"runc"},"sysbox-runc":{"path":"/usr/bin/sysbox-runc"}}`

// TestDockerArgs pins the pure arg-builder: the three Sysbox flags when on,
// nothing at all when off (so the run argv stays byte-for-byte unchanged).
func TestDockerArgs(t *testing.T) {
	if a := dockerArgs(false, "box-docker"); a != nil {
		t.Errorf("docker off must yield no flags; got %v", a)
	}
	got := strings.Join(dockerArgs(true, "box-docker"), " ")
	if got != "--runtime=sysbox-runc -e JAM_DOCKER=1 -v box-docker:/var/lib/docker" {
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

func TestDialParsesDockerPort(t *testing.T) {
	// preflight `info` is a Probe (no Output consumed); `port` is the only Output.
	f := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: "127.0.0.1:49153\n"}}}
	ep, cleanup, err := NewWithContext(f, "").Dial("box")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if ep.Host != "127.0.0.1" || ep.Port != 49153 || ep.User != "agent" {
		t.Fatalf("ep = %+v", ep)
	}
}

// TestPinsContext: every docker call carries --context colima, so cove never
// uses a different daemon than colima.
func TestPinsContext(t *testing.T) {
	f := &runner.Fake{}
	if _, err := NewWithContext(f, "").RunEphemeral("img:tag", "", "box", "jam.studio", nil, nil, false); err != nil {
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
	// RunEphemeral
	if _, err := NewWithContext(mkFail(), "").RunEphemeral("img:tag", "", "box", "jam.studio", nil, nil, false); err == nil || !strings.Contains(err.Error(), "colima start") {
		t.Fatalf("RunEphemeral should fail actionably; err=%v", err)
	}
	// Dial
	if _, _, err := NewWithContext(mkFail(), "").Dial("box"); err == nil || !strings.Contains(err.Error(), "colima start") {
		t.Fatalf("Dial should fail actionably; err=%v", err)
	}
	// RemoveContainer
	if err := NewWithContext(mkFail(), "").RemoveContainer("box"); err == nil || !strings.Contains(err.Error(), "colima start") {
		t.Fatalf("RemoveContainer should fail actionably; err=%v", err)
	}
	// GetStatus now surfaces the unreachable error instead of a misleading "absent".
	if _, err := NewWithContext(mkFail(), "").GetStatus("box"); err == nil || !strings.Contains(err.Error(), "colima start") {
		t.Fatalf("GetStatus should fail actionably; err=%v", err)
	}
}

// BuildKitImage (the Jam launcher's managed-kit build) is context-pinned and
// passes the caller's resolved base as the Dockerfile's BASE arg — the two things
// the bare-runner build in T2/T3 got wrong (COV-217).
func TestBuildKitImageContextPinnedWithBase(t *testing.T) {
	f := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: "sha256:img\n"}}} // the post-build inspect .Id
	digest, err := NewWithContext(f, "").BuildKitImage("/b", "jam-kit:managed-v1", "jam-studio-base@sha256:base", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if digest != "sha256:img" {
		t.Fatalf("digest = %q, want sha256:img", digest)
	}
	build := dockerCall(f.Calls, "build")
	if build == nil || !contains(build, "--build-arg") || !contains(build, "BASE=jam-studio-base@sha256:base") ||
		!contains(build, "-t") || !contains(build, "jam-kit:managed-v1") || !contains(build, "/b") ||
		!contains(build, "--progress=plain") {
		t.Fatalf("build call = %+v", f.Calls)
	}
	if !allPinned(f.Calls) {
		t.Fatalf("every docker call must pin --context colima: %+v", f.Calls)
	}
}

// noCache=true rebuilds every layer (--no-cache); the default build reuses the
// cache.
func TestBuildKitImageNoCache(t *testing.T) {
	for _, noCache := range []bool{false, true} {
		f := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: "sha256:img\n"}}}
		if _, err := NewWithContext(f, "").BuildKitImage("/b", "jam-kit:managed-v1", "jam-studio-base@sha256:base", nil, noCache); err != nil {
			t.Fatal(err)
		}
		build := dockerCall(f.Calls, "build")
		if build == nil || contains(build, "--no-cache") != noCache {
			t.Fatalf("noCache=%v: build call = %+v", noCache, f.Calls)
		}
	}
}

// ResolveKitBase with no declared base resolves to the substrate's blessed
// default (no gate inspect needed for the default), so a role-named kit that
// doesn't pin its own base still builds.
func TestResolveKitBaseDefaultsToBlessed(t *testing.T) {
	f := &runner.Fake{}
	got, err := NewWithContext(f, "").ResolveKitBase("")
	if err != nil || got == "" {
		t.Fatalf("ResolveKitBase(\"\") = %q, %v; want the blessed default", got, err)
	}
}

// The Dockerfile is FROM ${BASE}; a blank base can't build.
func TestBuildKitImageRequiresBase(t *testing.T) {
	f := &runner.Fake{}
	if _, err := NewWithContext(f, "").BuildKitImage("/b", "jam-kit:managed-v1", "", nil, false); err == nil {
		t.Fatal("BuildKitImage must require a non-empty base")
	}
}

// BuildKitImage injects kit build-args as deterministic --build-arg pairs
// alongside the required BASE arg.
func TestBuildKitImageInjectsBuildArgs(t *testing.T) {
	f := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: "sha256:img\n"}}}
	if _, err := NewWithContext(f, "").BuildKitImage("/b", "jam-kit:deadbeef", "jam-studio-base@sha256:base",
		map[string]string{"NODE_VERSION": "20"}, false); err != nil {
		t.Fatal(err)
	}
	build := dockerCall(f.Calls, "build")
	if build == nil || !contains(build, "--build-arg") || !contains(build, "NODE_VERSION=20") {
		t.Fatalf("build must inject the kit build-arg: %+v", f.Calls)
	}
	if !contains(build, "BASE=jam-studio-base@sha256:base") {
		t.Fatalf("build must still inject the BASE arg: %+v", f.Calls)
	}
}

// HasKitImage inspects the tag on the pinned colima daemon; a non-zero inspect
// (absent image) is a (false, nil) miss, not an error.
func TestHasKitImage(t *testing.T) {
	f := &runner.Fake{}
	ok, err := NewWithContext(f, "").HasKitImage("jam-kit:managed-v2")
	if err != nil || !ok {
		t.Fatalf("present: Has = %v, %v", ok, err)
	}
	insp := dockerCall(f.Calls, "image")
	if insp == nil || !contains(insp, "inspect") || !contains(insp, "jam-kit:managed-v2") {
		t.Fatalf("HasKitImage did not inspect the tag: %+v", f.Calls)
	}
	if !allPinned(f.Calls) {
		t.Fatalf("HasKitImage must pin --context colima: %+v", f.Calls)
	}

	f2 := &runner.Fake{Err: &runner.ExitError{Code: 1}}
	if ok, _ := NewWithContext(f2, "").HasKitImage("jam-kit:missing-v9"); ok {
		t.Fatal("HasKitImage must be false when the image is absent")
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

// dockerCallFull returns the whole recorded Call for the first `docker <sub>`
// invocation (so a test can read its Stdin), or nil if none matched.
func dockerCallFull(calls []runner.Call, sub string) *runner.Call {
	for i := range calls {
		c := calls[i]
		if c.Name != "docker" {
			continue
		}
		a := c.Args
		if len(a) >= 2 && a[0] == "--context" {
			a = a[2:]
		}
		if len(a) > 0 && a[0] == sub {
			return &calls[i]
		}
	}
	return nil
}

// stdinOf returns a Call's recorded stdin bytes, tolerating a nil Call.
func stdinOf(c *runner.Call) string {
	if c == nil {
		return ""
	}
	return c.Stdin
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

// TestNewWithContextPinsEveryCall: a Colima built for another docker context
// (a non-default colima profile's colima-<profile>) pins every docker call —
// probe, run, port, exec, volume ops — to it instead of the default.
func TestNewWithContextPinsEveryCall(t *testing.T) {
	f := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: "127.0.0.1:49153\n"}}}
	c := NewWithContext(f, "colima-jam-b")
	if _, err := c.RunEphemeral("img", "", "jam-studio-x", "jam.studio", nil, nil, false); err != nil {
		t.Fatalf("RunEphemeral: %v", err)
	}
	if _, _, err := c.Dial("jam-studio-x"); err != nil {
		t.Fatalf("Dial: %v", err)
	}
	if err := c.CreateVolume("v", "k=v"); err != nil {
		t.Fatalf("CreateVolume: %v", err)
	}
	if err := c.RemoveVolumes("v"); err != nil {
		t.Fatalf("RemoveVolumes: %v", err)
	}
	if len(f.Calls) == 0 {
		t.Fatal("expected docker calls")
	}
	for _, call := range f.Calls {
		if call.Name != "docker" {
			continue
		}
		if len(call.Args) < 2 || call.Args[0] != "--context" || call.Args[1] != "colima-jam-b" {
			t.Fatalf("docker call not pinned to colima-jam-b: %v", call.Args)
		}
	}
}

// TestNewWithContextEmptyIsDefault: an empty context keeps the default colima
// context, so an unset config key changes nothing.
func TestNewWithContextEmptyIsDefault(t *testing.T) {
	f := &runner.Fake{}
	c := NewWithContext(f, "")
	if err := c.RemoveContainer("x"); err != nil {
		t.Fatalf("RemoveContainer: %v", err)
	}
	if !allPinned(f.Calls) {
		t.Fatalf("empty context must pin to %q; calls=%+v", dockerContext, f.Calls)
	}
}

// TestPreflightNamesTheContext: an unreachable non-default context's error
// names it and points at its profile, not the default `colima start`.
func TestPreflightNamesTheContext(t *testing.T) {
	f := &runner.Fake{Err: errors.New("down")}
	err := NewWithContext(f, "colima-jam-b").RemoveContainer("x")
	if err == nil || !strings.Contains(err.Error(), `"colima-jam-b"`) || !strings.Contains(err.Error(), "colima start --profile jam-b") {
		t.Fatalf("preflight error should name the context + its profile; got %v", err)
	}
}

func TestDockerArgsPinsTheContext(t *testing.T) {
	if got := NewWithContext(&runner.Fake{}, "colima-jam").DockerArgs("image", "inspect", "x"); strings.Join(got, " ") != "--context colima-jam image inspect x" {
		t.Fatalf("DockerArgs = %v", got)
	}
	if got := NewWithContext(&runner.Fake{}, "").DockerArgs("ps"); strings.Join(got, " ") != "--context colima ps" {
		t.Fatalf("default context = %v", got)
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
		got, err := NewWithContext(f, "").GetStatus("box")
		if err != nil {
			t.Fatalf("status(%q,%v) errored: %v", c.out, c.err, err)
		}
		if got != c.want {
			t.Fatalf("status(%q,%v) = %v want %v", c.out, c.err, got, c.want)
		}
	}
}

// Context names the docker context calls are pinned to: the configured one,
// else colima's default.
func TestContextIsThePinnedOne(t *testing.T) {
	if got := NewWithContext(nil, "").Context(); got != dockerContext {
		t.Fatalf("default context = %q, want %q", got, dockerContext)
	}
	if got := NewWithContext(nil, "colima-jam").Context(); got != "colima-jam" {
		t.Fatalf("context = %q", got)
	}
}
