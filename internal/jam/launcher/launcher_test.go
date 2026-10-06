package launcher

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/backend"
	"github.com/aethons-tools/cove/internal/connect"
	"github.com/aethons-tools/cove/internal/harnessinstall"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/sessionctx"
	"github.com/aethons-tools/cove/internal/runner"
	"github.com/aethons-tools/cove/internal/studio"
)

// testKitRef is the prepared studio kit the raise-path tests run from; its image
// tag is l.imageTag(ref) (cove-kit:<Digest>-<asm>).
var testKitRef = jam.KitRef{ID: "web", Version: 1, Digest: studio.BuildDigest(studio.StudioKit{Kind: studio.Kind}, harnessinstall.Default())}

// readyInv is an inventory with testKitRef already prepared.
func readyInv() *fakeInv {
	i := &fakeInv{}
	i.set(testKitRef, true)
	return i
}

type fakeOps struct {
	ran         bool
	runName     string
	runImage    string
	runDigest   string
	runAddHost  []string
	runMounts   []backend.Mount
	removed     string
	volsRemoved []string // RemoveVolumes names, across calls
	volsErr     error
	created     []string // CreateVolume "name label…" records, in order
	createdRan  bool     // RunEphemeral had run when a volume was created
	createErr   error
	listed      map[string]string // ListVolumes result
	paused      string
	unpaused    string
	status      backend.State
	statusErr   error
	runErr      error

	// KitImageBuilder scripting/recording.
	builds       int
	builtTag     string
	builtBase    string
	builtArgs    map[string]string
	buildErr     error
	hasImage     map[string]bool
	resolvedBase string // ResolveKitBase returns this (default "blessed-default")
	resolvedFrom string // the declaredBase ResolveKitBase was asked about

	resolvedTarBase string // ResolveKitBaseTar returns this (default "blessed-tar-base")
	resolvedTar     []byte // the context tar bytes ResolveKitBaseTar was streamed
	resolveTarErr   error  // when set, ResolveKitBaseTar fails (gate-fail simulation)
}

func (f *fakeOps) RunEphemeral(image, digest, name, label string, dns, addHosts []string, docker bool, mounts ...backend.Mount) (backend.Instance, error) {
	f.ran, f.runName, f.runImage, f.runDigest, f.runAddHost, f.runMounts = true, name, image, digest, addHosts, mounts
	if f.runErr != nil {
		return backend.Instance{}, f.runErr
	}
	return backend.Instance{Container: name, Image: image}, nil
}
func (f *fakeOps) BuildKitImage(buildDir, tag, base string, buildArgs map[string]string, noCache bool) (string, error) {
	f.builds++
	f.builtTag, f.builtBase, f.builtArgs = tag, base, buildArgs
	if f.buildErr != nil {
		return "", f.buildErr
	}
	return "sha256:built", nil
}
func (f *fakeOps) HasKitImage(tag string) (bool, error) { return f.hasImage[tag], nil }
func (f *fakeOps) ResolveKitBase(declaredBase string) (string, error) {
	f.resolvedFrom = declaredBase
	if f.resolvedBase != "" {
		return f.resolvedBase, nil
	}
	return "blessed-default", nil
}
func (f *fakeOps) ResolveKitBaseTar(ctx io.Reader) (string, error) {
	b, _ := io.ReadAll(ctx)
	f.resolvedTar = b
	if f.resolveTarErr != nil {
		return "", f.resolveTarErr
	}
	if f.resolvedTarBase != "" {
		return f.resolvedTarBase, nil
	}
	return "blessed-tar-base", nil
}
func (f *fakeOps) Dial(container string) (backend.Endpoint, func(), error) {
	return backend.Endpoint{Host: "127.0.0.1", Port: 2222, User: "agent"}, func() {}, nil
}
func (f *fakeOps) RemoveContainer(name string) error { f.removed = name; return nil }
func (f *fakeOps) CreateVolume(name string, labels ...string) error {
	f.created = append(f.created, strings.Join(append([]string{name}, labels...), " "))
	f.createdRan = f.createdRan || f.ran
	return f.createErr
}
func (f *fakeOps) ListVolumes(key string) (map[string]string, error) { return f.listed, nil }
func (f *fakeOps) RemoveVolumes(names ...string) error {
	f.volsRemoved = append(f.volsRemoved, names...)
	return f.volsErr
}
func (f *fakeOps) Pause(name string) error   { f.paused = name; return nil }
func (f *fakeOps) Unpause(name string) error { f.unpaused = name; return nil }
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
		JamHost: "jam.example.com", RuntimeAddr: "jam.example.com:443",
		IdentityFile: "k", KnownHostsDir: "/kh",
		Inventory: readyInv(),
		sleep:     func(time.Duration) {}, // injected no-op wait-for-sshd
	})
}

func TestRaiseRunsDialsLaunches(t *testing.T) {
	ops := &fakeOps{}
	l := newLauncher(ops)
	loc, err := l.Raise(context.Background(), jam.RaiseSpec{ActorID: "w1", Kit: testKitRef, Prompt: "go"}, jam.LaunchCreds{IdentityToken: "t", LaunchSecret: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if !ops.ran || ops.runName != "atcove-cove-w1" || ops.runImage != l.imageTag(testKitRef) {
		t.Fatalf("RunEphemeral not called correctly: %+v", ops)
	}
	if len(ops.runAddHost) != 1 || ops.runAddHost[0] != "jam.example.com" {
		t.Fatalf("addHosts = %v, want [jam.example.com]", ops.runAddHost)
	}
	if loc != "atcove-cove-w1" {
		t.Fatalf("location = %q, want atcove-cove-w1", loc)
	}
}

// A kit-referenced raise whose kit is not in the launcher's inventory returns
// ErrKitNotReady and creates NO container — the supervisor prepares it and
// retries. l.imageTag(spec.Kit) is never run.
func TestRaiseKitNotReady(t *testing.T) {
	ops := &fakeOps{}
	l := New(Config{
		Ops: ops, Runner: &runner.Fake{},
		JamHost: "h", RuntimeAddr: "h:443", IdentityFile: "k", KnownHostsDir: "/kh",
		Inventory: &fakeInv{}, // empty: nothing prepared
		sleep:     func(time.Duration) {},
	})
	_, err := l.Raise(context.Background(), jam.RaiseSpec{ActorID: "a", Kit: testKitRef}, jam.LaunchCreds{})
	if !errors.Is(err, ErrKitNotReady) {
		t.Fatalf("want ErrKitNotReady, got %v", err)
	}
	if ops.ran {
		t.Fatal("no container may be created on a not-ready kit")
	}
}

// A studio raise with no kit (empty KitRef) errors and creates no container —
// studio raises always carry a kit.
func TestRaiseRequiresKit(t *testing.T) {
	ops := &fakeOps{}
	l := New(Config{
		Ops: ops, Runner: &runner.Fake{},
		JamHost: "h", RuntimeAddr: "h:443", IdentityFile: "k", KnownHostsDir: "/kh",
		Inventory: &fakeInv{},
		sleep:     func(time.Duration) {},
	})
	_, err := l.Raise(context.Background(), jam.RaiseSpec{ActorID: "a"}, jam.LaunchCreds{})
	if err == nil || !strings.Contains(err.Error(), "no kit") {
		t.Fatalf("want a no-kit error, got %v", err)
	}
	if ops.ran {
		t.Fatal("no container may be created without a kit")
	}
}

// A studio ref with an empty build-digest is rejected (it would yield the invalid
// image tag "cove-kit:") and creates no container.
func TestRaiseRejectsEmptyDigest(t *testing.T) {
	ops := &fakeOps{}
	l := New(Config{
		Ops: ops, Runner: &runner.Fake{},
		JamHost: "h", RuntimeAddr: "h:443", IdentityFile: "k", KnownHostsDir: "/kh",
		Inventory: &fakeInv{},
		sleep:     func(time.Duration) {},
	})
	_, err := l.Raise(context.Background(), jam.RaiseSpec{ActorID: "a", Kit: jam.KitRef{ID: "web", Version: 1}}, jam.LaunchCreds{})
	if err == nil || !strings.Contains(err.Error(), "no build-digest") {
		t.Fatalf("want a no-build-digest error, got %v", err)
	}
	if ops.ran {
		t.Fatal("no container may be created without a build-digest")
	}
}

// A kit-referenced raise whose kit is prepared runs the cove-kit:<build-digest>-<asm>
// image and launches cove-master as usual.
func TestRaiseFromPreparedKit(t *testing.T) {
	ops := &fakeOps{}
	inv := &fakeInv{}
	ref := jam.KitRef{ID: "web", Version: 3, Digest: "cafef00d"}
	inv.set(ref, true)
	l := New(Config{
		Ops: ops, Runner: &runner.Fake{},
		JamHost: "h", RuntimeAddr: "h:443", IdentityFile: "k", KnownHostsDir: "/kh",
		Inventory: inv,
		sleep:     func(time.Duration) {},
	})
	loc, err := l.Raise(context.Background(), jam.RaiseSpec{ActorID: "w1", Kit: ref, Prompt: "go"}, jam.LaunchCreds{IdentityToken: "t", LaunchSecret: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if !ops.ran || ops.runImage != l.imageTag(ref) {
		t.Fatalf("kit raise ran image %q, want %q (calls=%+v)", ops.runImage, l.imageTag(ref), ops)
	}
	// The kit path must run the tag with NO digest pin — the build-digest tag
	// already names the exact built image.
	if ops.runDigest != "" {
		t.Fatalf("kit raise pinned digest %q; want none (the tag already pins the build)", ops.runDigest)
	}
	if loc != "atcove-cove-w1" {
		t.Fatalf("location = %q, want atcove-cove-w1", loc)
	}
}

func TestRaiseRemovesContainerOnLaunchFailure(t *testing.T) {
	ops := &fakeOps{}
	l := New(Config{
		Ops: ops, Runner: failingRunner{}, // a Runner whose Run/RunStdin returns an error
		JamHost: "h", RuntimeAddr: "h:443", IdentityFile: "k", KnownHostsDir: "/kh",
		Inventory: readyInv(),
		sleep:     func(time.Duration) {},
	})
	_, err := l.Raise(context.Background(), jam.RaiseSpec{ActorID: "w1", Kit: testKitRef}, jam.LaunchCreds{})
	if err == nil {
		t.Fatal("want error on launch failure")
	}
	if ops.removed != "atcove-cove-w1" {
		t.Fatalf("container not cleaned up on failure: removed=%q", ops.removed)
	}
}

func TestTeardownRemoves(t *testing.T) {
	ops := &fakeOps{}
	if err := newLauncher(ops).Teardown(context.Background(), jam.Instance{Location: "atcove-cove-w1"}); err != nil {
		t.Fatal(err)
	}
	if ops.removed != "atcove-cove-w1" {
		t.Fatalf("removed = %q", ops.removed)
	}
}

// TestRaiseMountsStateOnlyForStanding: a standing raise first creates its
// <container>-agent-data and <container>-workspace volumes labeled with its
// actor id, then mounts them at /agent-data and the agent's WorkDir (so a
// re-raise under the same actor id re-attaches them); ephemeral and personal
// raises create and mount nothing (COV-249).
func TestRaiseMountsStateOnlyForStanding(t *testing.T) {
	for kind, want := range map[string][]backend.Mount{
		"":                      nil,
		"ephemeral":             nil,
		jam.SessionKindPersonal: nil,
		jam.SessionKindStanding: {
			{Volume: "atcove-cove-s1-agent-data", Target: "/agent-data"},
			{Volume: "atcove-cove-s1-workspace", Target: "/work/here"},
		},
	} {
		ops := &fakeOps{}
		l := New(Config{
			Ops: ops, Runner: &runner.Fake{}, JamHost: "h", RuntimeAddr: "h:443", IdentityFile: "k", KnownHostsDir: "/kh",
			Inventory: readyInv(), WorkDir: "/work/here", sleep: func(time.Duration) {},
		})
		if _, err := l.Raise(context.Background(), jam.RaiseSpec{ActorID: "s1", Kit: testKitRef, Prompt: "go", SessionKind: kind}, jam.LaunchCreds{}); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(ops.runMounts, want) {
			t.Errorf("kind %q: mounts = %v, want %v", kind, ops.runMounts, want)
		}
		var wantCreated []string
		for _, m := range want {
			wantCreated = append(wantCreated, m.Volume+" "+StateLabel+"=s1")
		}
		if !slices.Equal(ops.created, wantCreated) || ops.createdRan {
			t.Errorf("kind %q: created = %v (after run: %v), want %v before the run", kind, ops.created, ops.createdRan, wantCreated)
		}
	}
}

// A state volume that can't be created fails the raise before any container.
func TestRaiseStateVolumeCreateFailure(t *testing.T) {
	ops := &fakeOps{createErr: errors.New("disk full")}
	if _, err := newLauncher(ops).Raise(context.Background(), jam.RaiseSpec{ActorID: "s1", Kit: testKitRef, SessionKind: jam.SessionKindStanding}, jam.LaunchCreds{}); err == nil {
		t.Fatal("want the create error")
	}
	if ops.ran {
		t.Fatal("no container may run without its state volumes")
	}
}

// TestRaiseFailureKeepsStateVolumes: a failed standing raise removes its
// container but never its state volumes.
func TestRaiseFailureKeepsStateVolumes(t *testing.T) {
	ops := &fakeOps{}
	l := New(Config{
		Ops: ops, Runner: failingRunner{}, JamHost: "h", RuntimeAddr: "h:443", IdentityFile: "k", KnownHostsDir: "/kh",
		Inventory: readyInv(), sleep: func(time.Duration) {},
	})
	if _, err := l.Raise(context.Background(), jam.RaiseSpec{ActorID: "s1", Kit: testKitRef, SessionKind: jam.SessionKindStanding}, jam.LaunchCreds{}); err == nil {
		t.Fatal("want a launch failure")
	}
	if ops.removed != "atcove-cove-s1" || len(ops.volsRemoved) != 0 {
		t.Fatalf("removed=%q volsRemoved=%v; want the container only", ops.removed, ops.volsRemoved)
	}
}

// TestTeardownKeepsVolumes: Teardown removes the container only.
func TestTeardownKeepsVolumes(t *testing.T) {
	ops := &fakeOps{}
	if err := newLauncher(ops).Teardown(context.Background(), jam.Instance{ActorID: "s1", Location: "atcove-cove-s1"}); err != nil {
		t.Fatal(err)
	}
	if len(ops.volsRemoved) != 0 {
		t.Fatalf("Teardown removed volumes %v", ops.volsRemoved)
	}
}

// TestPurgeStateRemovesVolumes: PurgeState removes the actor's state
// volumes (the same stateVolumes the raise mounts).
func TestPurgeStateRemovesVolumes(t *testing.T) {
	ops := &fakeOps{}
	if err := newLauncher(ops).PurgeState(context.Background(), "s1"); err != nil {
		t.Fatal(err)
	}
	if want := []string{"atcove-cove-s1-agent-data", "atcove-cove-s1-workspace"}; !slices.Equal(ops.volsRemoved, want) {
		t.Fatalf("volsRemoved = %v, want %v", ops.volsRemoved, want)
	}
	ops = &fakeOps{volsErr: errors.New("in use")}
	if err := newLauncher(ops).PurgeState(context.Background(), "s1"); err == nil {
		t.Fatal("want the removal error")
	}
}

// TestStateOwners: the actor ids owning labeled state volumes, deduplicated
// and sorted.
func TestStateOwners(t *testing.T) {
	ops := &fakeOps{listed: map[string]string{"a-agent-data": "id-b", "a-workspace": "id-b", "c-agent-data": "id-a", "x": ""}}
	got, err := newLauncher(ops).StateOwners(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"id-a", "id-b"}; !slices.Equal(got, want) {
		t.Fatalf("owners = %v, want %v", got, want)
	}
}

func TestTeardownCapturesAgentLog(t *testing.T) {
	var buf bytes.Buffer
	ops := &fakeOps{}
	r := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: "panic: claude auth failed\n"}}}
	l := New(Config{
		Ops: ops, Runner: r, IdentityFile: "id", KnownHostsDir: "kh",
		Log: slog.New(slog.NewTextHandler(&buf, nil)),
	})

	if err := l.Teardown(context.Background(), jam.Instance{Location: "atcove-cove-w1", ActorID: "w1"}); err != nil {
		t.Fatal(err)
	}
	// Container is still removed.
	if ops.removed != "atcove-cove-w1" {
		t.Fatalf("removed = %q, want atcove-cove-w1", ops.removed)
	}
	// The captured tail was logged.
	if !strings.Contains(buf.String(), "claude auth failed") {
		t.Errorf("teardown log missing the captured agent log; got: %s", buf.String())
	}
	// It read the cove-master log over ssh before removal.
	var sawTail bool
	for _, c := range r.Calls {
		if c.Name == "ssh" && strings.Contains(strings.Join(c.Args, " "), connect.CoveMasterLogVMPath) {
			sawTail = true
		}
	}
	if !sawTail {
		t.Errorf("expected an ssh tail of %s; calls: %+v", connect.CoveMasterLogVMPath, r.Calls)
	}
}

func TestTeardownCaptureIsBestEffort(t *testing.T) {
	ops := &fakeOps{}
	r := &runner.Fake{Err: errors.New("ssh boom")} // Output errors → capture yields ""
	l := New(Config{Ops: ops, Runner: r, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})

	if err := l.Teardown(context.Background(), jam.Instance{Location: "c1", ActorID: "a1"}); err != nil {
		t.Fatalf("teardown must not fail when log capture fails: %v", err)
	}
	if ops.removed != "c1" {
		t.Fatalf("container must still be removed; removed = %q", ops.removed)
	}
}

func TestPauseUnpause(t *testing.T) {
	ops := &fakeOps{}
	l := newLauncher(ops)
	if err := l.Pause(context.Background(), jam.Instance{Location: "cove-1"}); err != nil {
		t.Fatal(err)
	}
	if ops.paused != "cove-1" {
		t.Fatalf("paused = %q, want cove-1", ops.paused)
	}
	if err := l.Unpause(context.Background(), jam.Instance{Location: "cove-1"}); err != nil {
		t.Fatal(err)
	}
	if ops.unpaused != "cove-1" {
		t.Fatalf("unpaused = %q, want cove-1", ops.unpaused)
	}
}

func TestProbeMapsState(t *testing.T) {
	cases := []struct {
		st   backend.State
		err  error
		want jam.Liveness
	}{
		{backend.StateRunning, nil, jam.LivenessAlive},
		{backend.StateStopped, nil, jam.LivenessDead},
		{backend.StateAbsent, nil, jam.LivenessDead},
		{backend.StateAbsent, errors.New("docker hiccup"), jam.LivenessUnknown},
	}
	for _, c := range cases {
		ops := &fakeOps{status: c.st, statusErr: c.err}
		got, _ := newLauncher(ops).Probe(context.Background(), jam.Instance{Location: "x"})
		if got != c.want {
			t.Fatalf("state %v err %v → %v, want %v", c.st, c.err, got, c.want)
		}
	}
}

// TestRaiseResidentKinds: the launcher asks cove-master for resident mode
// (AT_COVE_RESIDENT=1) only for resident kinds — personal and standing.
func TestRaiseResidentKinds(t *testing.T) {
	for kind, want := range map[string]bool{"": false, "ephemeral": false, jam.SessionKindPersonal: true, jam.SessionKindStanding: true} {
		ops := &fakeOps{}
		r := &runner.Fake{}
		l := New(Config{
			Ops: ops, Runner: r, JamHost: "h", RuntimeAddr: "h:443", IdentityFile: "k", KnownHostsDir: "/kh",
			Inventory: readyInv(),
			sleep:     func(time.Duration) {},
		})
		if _, err := l.Raise(context.Background(), jam.RaiseSpec{ActorID: "w1", Kit: testKitRef, Prompt: "go", SessionKind: kind}, jam.LaunchCreds{IdentityToken: "t", LaunchSecret: "s"}); err != nil {
			t.Fatal(err)
		}
		got := false
		for _, c := range r.Calls {
			if strings.Contains(c.Stdin, "AT_COVE_RESIDENT=1") {
				got = true
			}
		}
		if got != want {
			t.Fatalf("kind %q: AT_COVE_RESIDENT set = %v, want %v", kind, got, want)
		}
	}
}

// egressOps is a fakeOps that also implements backend.RoleEgress, recording the
// domains it was given and how many runner calls had happened by then (so a
// test can prove the policy lands before cove-master launches).
type egressOps struct {
	*fakeOps
	r          *runner.Fake
	applied    bool
	container  string
	domains    []string
	callsAtApp int
	err        error
	reset      string // container passed to ResetRoleEgress
}

func (e *egressOps) ApplyRoleEgress(container string, domains []string) error {
	e.applied, e.container, e.domains, e.callsAtApp = true, container, domains, len(e.r.Calls)
	return e.err
}

func (e *egressOps) ResetRoleEgress(container string) error {
	e.reset = container
	return e.err
}

var _ backend.RoleEgress = (*egressOps)(nil)

func newEgressLauncher(ops Backend, r *runner.Fake, logw io.Writer) *Launcher {
	return New(Config{
		Ops: ops, Runner: r, JamHost: "h", RuntimeAddr: "h:443", IdentityFile: "k", KnownHostsDir: "/kh",
		Inventory: readyInv(),
		Log:       slog.New(slog.NewJSONHandler(logw, nil)),
		sleep:     func(time.Duration) {},
	})
}

// isCoveMasterLaunch reports whether a runner call is part of the cove-master
// bootstrap (staging its env/prompt on stdin, or starting it), not the sshd probe.
func isCoveMasterLaunch(c runner.Call) bool {
	return c.Stdin != "" || strings.Contains(strings.Join(c.Args, " "), "cove-master")
}

// A role's policy is applied in-box after sshd answers and before cove-master
// (and so the agent) starts; the domains reach the op unchanged; the info log
// carries the count, never the list.
func TestRaiseAppliesRoleEgressBeforeCoveMaster(t *testing.T) {
	r := &runner.Fake{}
	ops := &egressOps{fakeOps: &fakeOps{}, r: r}
	var logs bytes.Buffer
	l := newEgressLauncher(ops, r, &logs)
	domains := []string{".b.org", "a.com"}
	spec := jam.RaiseSpec{ActorID: "w1", Kit: testKitRef, Project: "acme", Role: "fenced", Prompt: "go", Egress: &jam.EgressPolicy{Domains: domains}}
	if _, err := l.Raise(context.Background(), spec, jam.LaunchCreds{IdentityToken: "t", LaunchSecret: "s"}); err != nil {
		t.Fatal(err)
	}
	if !ops.applied || ops.container != "atcove-cove-w1" || !slices.Equal(ops.domains, domains) {
		t.Fatalf("ApplyRoleEgress = applied %v, %q, %v; want atcove-cove-w1 with %v", ops.applied, ops.container, ops.domains, domains)
	}
	if ops.callsAtApp == 0 {
		t.Fatal("egress applied before sshd answered")
	}
	for _, c := range r.Calls[:ops.callsAtApp] {
		if isCoveMasterLaunch(c) {
			t.Fatal("cove-master launched before the role egress was applied")
		}
	}
	launched := false
	for _, c := range r.Calls[ops.callsAtApp:] {
		launched = launched || isCoveMasterLaunch(c)
	}
	if !launched {
		t.Fatal("cove-master never launched after the egress apply")
	}
	out := logs.String()
	if !strings.Contains(out, `"msg":"role egress applied"`) || !strings.Contains(out, `"domains":2`) {
		t.Fatalf("log = %s; want role egress applied with domains=2", out)
	}
	if strings.Contains(out, "a.com") {
		t.Fatalf("log must carry the count, not the list: %s", out)
	}
}

// No policy (the kit default): no egress call at all.
func TestRaiseNilEgressSkipsApply(t *testing.T) {
	r := &runner.Fake{}
	ops := &egressOps{fakeOps: &fakeOps{}, r: r}
	l := newEgressLauncher(ops, r, io.Discard)
	if _, err := l.Raise(context.Background(), jam.RaiseSpec{ActorID: "w1", Kit: testKitRef, Role: "guest", Prompt: "go"}, jam.LaunchCreds{}); err != nil {
		t.Fatal(err)
	}
	if ops.applied {
		t.Fatal("ApplyRoleEgress called for a role with no policy")
	}
}

// An empty (set) policy is still applied — it narrows to base + infra.
func TestRaiseEmptyEgressStillApplies(t *testing.T) {
	r := &runner.Fake{}
	ops := &egressOps{fakeOps: &fakeOps{}, r: r}
	l := newEgressLauncher(ops, r, io.Discard)
	if _, err := l.Raise(context.Background(), jam.RaiseSpec{ActorID: "w1", Kit: testKitRef, Role: "r", Egress: &jam.EgressPolicy{Domains: []string{}}}, jam.LaunchCreds{}); err != nil {
		t.Fatal(err)
	}
	if !ops.applied || len(ops.domains) != 0 {
		t.Fatalf("empty policy: applied=%v domains=%v", ops.applied, ops.domains)
	}
}

// A failed apply (e.g. a domain outside the kit's ceiling) fails the raise,
// never launches cove-master, and removes the container.
func TestRaiseEgressApplyErrorFailsAndCleansUp(t *testing.T) {
	r := &runner.Fake{}
	ops := &egressOps{fakeOps: &fakeOps{}, r: r, err: errors.New("apply-role-egress: evil.example is outside the kit's egress ceiling")}
	l := newEgressLauncher(ops, r, io.Discard)
	_, err := l.Raise(context.Background(), jam.RaiseSpec{ActorID: "w1", Kit: testKitRef, Role: "r", Egress: &jam.EgressPolicy{Domains: []string{"evil.example"}}}, jam.LaunchCreds{})
	if err == nil || !strings.Contains(err.Error(), "apply role egress") || !strings.Contains(err.Error(), "evil.example") {
		t.Fatalf("err = %v; want apply role egress naming the domain", err)
	}
	if ops.removed != "atcove-cove-w1" {
		t.Fatalf("container not removed on egress failure: removed=%q", ops.removed)
	}
	for _, c := range r.Calls {
		if isCoveMasterLaunch(c) {
			t.Fatal("cove-master launched despite the egress failure")
		}
	}
}

// A backend without RoleEgress fails closed for a role with a policy: never
// falls back to the wider kit default.
func TestRaiseEgressFailsClosedWithoutOp(t *testing.T) {
	r := &runner.Fake{}
	ops := &fakeOps{}
	l := newEgressLauncher(ops, r, io.Discard)
	_, err := l.Raise(context.Background(), jam.RaiseSpec{ActorID: "w1", Kit: testKitRef, Project: "acme", Role: "fenced", Egress: &jam.EgressPolicy{Domains: []string{"a.com"}}}, jam.LaunchCreds{})
	if err == nil || !strings.Contains(err.Error(), "backend does not support role egress (required for role acme/fenced)") {
		t.Fatalf("err = %v; want fail-closed", err)
	}
	if ops.removed != "atcove-cove-w1" {
		t.Fatalf("container not removed: removed=%q", ops.removed)
	}
	for _, c := range r.Calls {
		if isCoveMasterLaunch(c) {
			t.Fatal("cove-master launched without the role egress")
		}
	}
}

// ApplyEgress on a running cove: a policy goes to ApplyRoleEgress on the cove's
// location with its domains; the log carries the count, not the list.
func TestApplyEgressPolicy(t *testing.T) {
	r := &runner.Fake{}
	ops := &egressOps{fakeOps: &fakeOps{}, r: r}
	var logs bytes.Buffer
	l := newEgressLauncher(ops, r, &logs)
	inst := jam.Instance{ActorID: "w1", Project: "acme", Role: "fenced", Location: "atcove-cove-w1"}
	domains := []string{".b.org", "a.com"}
	if err := l.ApplyEgress(context.Background(), inst, &jam.EgressPolicy{Domains: domains}); err != nil {
		t.Fatal(err)
	}
	if !ops.applied || ops.container != "atcove-cove-w1" || !slices.Equal(ops.domains, domains) {
		t.Fatalf("ApplyRoleEgress = applied %v, %q, %v", ops.applied, ops.container, ops.domains)
	}
	if ops.reset != "" {
		t.Fatal("a policy must not reset to the kit default")
	}
	if out := logs.String(); !strings.Contains(out, `"domains":2`) || strings.Contains(out, "a.com") {
		t.Fatalf("log = %s; want the count, never the list", out)
	}
}

// ApplyEgress with a nil policy restores the kit default via ResetRoleEgress.
func TestApplyEgressNilResets(t *testing.T) {
	r := &runner.Fake{}
	ops := &egressOps{fakeOps: &fakeOps{}, r: r}
	l := newEgressLauncher(ops, r, io.Discard)
	inst := jam.Instance{ActorID: "w1", Role: "r", Location: "atcove-cove-w1"}
	if err := l.ApplyEgress(context.Background(), inst, nil); err != nil {
		t.Fatal(err)
	}
	if ops.reset != "atcove-cove-w1" || ops.applied {
		t.Fatalf("reset=%q applied=%v; want a reset of atcove-cove-w1 only", ops.reset, ops.applied)
	}
}

// ApplyEgress surfaces the backend's error.
func TestApplyEgressError(t *testing.T) {
	r := &runner.Fake{}
	ops := &egressOps{fakeOps: &fakeOps{}, r: r, err: errors.New("exit status 3: evil.example is outside")}
	l := newEgressLauncher(ops, r, io.Discard)
	inst := jam.Instance{ActorID: "w1", Role: "r", Location: "atcove-cove-w1"}
	for _, p := range []*jam.EgressPolicy{nil, {Domains: []string{"evil.example"}}} {
		if err := l.ApplyEgress(context.Background(), inst, p); err == nil || !strings.Contains(err.Error(), "exit status 3") {
			t.Fatalf("policy %v: err = %v; want the backend error", p, err)
		}
	}
}

// A backend without RoleEgress can't re-apply or reset: an error, never a silent no-op.
func TestApplyEgressWithoutOpErrors(t *testing.T) {
	l := newEgressLauncher(&fakeOps{}, &runner.Fake{}, io.Discard)
	inst := jam.Instance{ActorID: "w1", Project: "acme", Role: "fenced", Location: "atcove-cove-w1"}
	for _, p := range []*jam.EgressPolicy{nil, {Domains: []string{"a.com"}}} {
		if err := l.ApplyEgress(context.Background(), inst, p); err == nil || !strings.Contains(err.Error(), "backend does not support role egress") {
			t.Fatalf("policy %v: err = %v; want fail-closed", p, err)
		}
	}
}

func TestTeardownUnpausesBeforeRemoving(t *testing.T) {
	ops := &fakeOps{}
	if err := newLauncher(ops).Teardown(context.Background(), jam.Instance{Location: "atcove-cove-w1", ActorID: "w1"}); err != nil {
		t.Fatal(err)
	}
	if ops.unpaused != "atcove-cove-w1" {
		t.Fatalf("Teardown must unpause first (an idled cove is paused; SSH into it hangs); unpaused=%q", ops.unpaused)
	}
	if ops.removed != "atcove-cove-w1" {
		t.Fatalf("Teardown must still remove the container; removed=%q", ops.removed)
	}
}

// The launcher stages the compiled context and the session kind for cove-master.
func TestRaisePassesContextAndKind(t *testing.T) {
	r := &runner.Fake{}
	l := New(Config{Ops: &fakeOps{}, Runner: r, JamHost: "h", RuntimeAddr: "h:443", IdentityFile: "k", KnownHostsDir: "/kh", Inventory: readyInv(), sleep: func(time.Duration) {}})
	b := &sessionctx.Bundle{Core: "CORE-TEXT", Fingerprint: "ab"}
	if _, err := l.Raise(context.Background(), jam.RaiseSpec{ActorID: "w1", Kit: testKitRef, Prompt: "go", SessionKind: jam.SessionKindStanding, Context: b}, jam.LaunchCreds{IdentityToken: "t", LaunchSecret: "s"}); err != nil {
		t.Fatal(err)
	}
	var kind, ctx bool
	for _, c := range r.Calls {
		kind = kind || strings.Contains(c.Stdin, "AT_COVE_SESSION_KIND='standing'")
		ctx = ctx || strings.Contains(c.Stdin, `"core":"CORE-TEXT"`)
	}
	if !kind || !ctx {
		t.Fatalf("kind staged = %v, context staged = %v", kind, ctx)
	}
}
