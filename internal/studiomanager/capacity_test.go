package studiomanager

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/backend"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/launcher"
	"github.com/aethons-tools/cove/internal/runner"
)

// fakeBackend is a goroutine-safe launcher.Backend that also lists
// containers (backend.ContainerLister) and installs the SSH CA (backend.SSHCA).
// HasKitImage is always true, so every kit raise is admitted by the inventory.
type fakeBackend struct {
	mu         sync.Mutex
	containers map[string]map[string]string // name -> labels
	runs       int
	lists      int
	removed    []string
	paused     []string
	unpaused   []string
	caFor      map[string][]byte // container -> InstallSSHUserCA pub

	addOnRun bool          // RunEphemeral lists the container it ran, with its labels, as docker would
	entered  chan struct{} // if set, RunEphemeral sends on it (non-blocking)
	block    chan struct{} // if set, RunEphemeral waits for it to close
}

func (f *fakeBackend) RunEphemeral(image, digest, name string, labels []string, dns, addHosts []string, docker bool, mounts ...backend.Mount) (backend.Instance, error) {
	f.mu.Lock()
	f.runs++
	if f.addOnRun {
		if f.containers == nil {
			f.containers = map[string]map[string]string{}
		}
		ls := map[string]string{}
		for _, l := range labels {
			k, v, _ := strings.Cut(l, "=")
			ls[k] = v
		}
		f.containers[name] = ls
	}
	entered, block := f.entered, f.block
	f.mu.Unlock()
	if entered != nil {
		select {
		case entered <- struct{}{}:
		default:
		}
	}
	if block != nil {
		<-block
	}
	return backend.Instance{Container: name, Image: image}, nil
}
func (f *fakeBackend) Dial(string) (backend.Endpoint, func(), error) {
	return backend.Endpoint{Host: "127.0.0.1", Port: 2222, User: "agent"}, func() {}, nil
}
func (f *fakeBackend) RemoveContainer(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, name)
	return nil
}
func (f *fakeBackend) Pause(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.paused = append(f.paused, name)
	return nil
}
func (f *fakeBackend) Unpause(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unpaused = append(f.unpaused, name)
	return nil
}
func (f *fakeBackend) BuildKitImage(string, string, string, map[string]string, bool) (string, error) {
	return "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", nil
}
func (f *fakeBackend) HasKitImage(string) (bool, error)            { return true, nil }
func (f *fakeBackend) ResolveKitBase(string) (string, error)       { return "base", nil }
func (f *fakeBackend) ResolveKitBaseTar(io.Reader) (string, error) { return "base", nil }
func (f *fakeBackend) ResolveKitBaseTarOn(io.Reader, string, string) (string, error) {
	return "base", nil
}
func (f *fakeBackend) HasKitBase(string) (bool, error)                          { return true, nil }
func (f *fakeBackend) CreateVolume(string, ...string) error                     { return nil }
func (f *fakeBackend) RemoveVolumes(...string) error                            { return nil }
func (f *fakeBackend) ListVolumes(string, ...string) (map[string]string, error) { return nil, nil }
func (f *fakeBackend) GetStatus(string) (backend.State, error)                  { return backend.StateRunning, nil }
func (f *fakeBackend) VolumeExists(string) (bool, error)                        { return false, nil }
func (f *fakeBackend) CopyVolume(string, string, string) error                  { return nil }
func (f *fakeBackend) FindContainers(string) (map[string]backend.ContainerInfo, error) {
	return nil, nil
}

func (f *fakeBackend) ListContainers(key string, match ...string) (map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lists++
	out := map[string]string{}
next:
	for name, ls := range f.containers {
		v, ok := ls[key]
		if !ok {
			continue
		}
		for _, m := range match {
			k, want, _ := strings.Cut(m, "=")
			if got, has := ls[k]; !has || got != want {
				continue next
			}
		}
		out[name] = v
	}
	return out, nil
}

func (f *fakeBackend) InstallSSHUserCA(container string, pub []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.caFor == nil {
		f.caFor = map[string][]byte{}
	}
	f.caFor[container] = append([]byte(nil), pub...)
	return nil
}

func (f *fakeBackend) snapshot(read func(*fakeBackend)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	read(f)
}

var (
	_ launcher.Backend        = (*fakeBackend)(nil)
	_ backend.ContainerLister = (*fakeBackend)(nil)
	_ backend.SSHCA           = (*fakeBackend)(nil)
)

// studioOf is the labels of a studio container m1 raised for actor.
func studioOf(actor, manager string) map[string]string {
	return map[string]string{launcher.Label: "", launcher.ActorLabel: actor, launcher.ManagerLabel: manager}
}

// okRunner is a stateless Runner where every command succeeds: unlike
// runner.Fake it is safe for concurrent raises.
type okRunner struct{}

func (okRunner) Run(string, ...string) error                           { return nil }
func (okRunner) RunEnv([]string, string, ...string) error              { return nil }
func (okRunner) Output(string, ...string) (string, error)              { return "", nil }
func (okRunner) OutputEnv([]string, string, ...string) (string, error) { return "", nil }
func (okRunner) Probe(string, ...string) error                         { return nil }
func (okRunner) RunStdin(io.Reader, string, ...string) error           { return nil }
func (okRunner) RunIO(io.Reader, io.Writer, io.Writer, string, ...string) error {
	return nil
}

var _ runner.Runner = okRunner{}

var testKit = jam.KitRef{ID: "web", Version: 1, Digest: "d1"}

func newGated(ops *fakeBackend, capacity int) *gated {
	return &gated{Launcher: launcher.New(launcher.Config{
		Ops: ops, Runner: okRunner{}, Manager: "m1",
		JamHost: "jam.example.com", RuntimeAddr: "jam.example.com:443",
		IdentityFile: "k", KnownHostsDir: "/kh",
	}), capacity: capacity}
}

func TestGatedDeclinesAtCapacity(t *testing.T) {
	ops := &fakeBackend{containers: map[string]map[string]string{"cove-w1": studioOf("w1", "m1")}}
	g := newGated(ops, 1)
	_, err := g.Raise(context.Background(), jam.RaiseSpec{ActorID: "w2", Kit: testKit}, jam.LaunchCreds{})
	if err == nil || err.Error() != "at capacity (1/1 studios)" {
		t.Fatalf("err = %v, want at capacity (1/1 studios)", err)
	}
	ops.snapshot(func(f *fakeBackend) {
		if f.runs != 0 {
			t.Fatalf("RunEphemeral called %d times", f.runs)
		}
	})
}

func TestGatedCountsInflight(t *testing.T) {
	ops := &fakeBackend{entered: make(chan struct{}, 1), block: make(chan struct{})}
	g := newGated(ops, 1)
	first := make(chan error, 1)
	go func() {
		_, err := g.Raise(context.Background(), jam.RaiseSpec{ActorID: "w1", Kit: testKit}, jam.LaunchCreds{})
		first <- err
	}()
	select {
	case <-ops.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("first raise never reached RunEphemeral")
	}
	// Run the second raise off the test goroutine: an admitted one would block
	// in RunEphemeral, so a broken gate fails here rather than hanging.
	second := make(chan error, 1)
	go func() {
		_, err := g.Raise(context.Background(), jam.RaiseSpec{ActorID: "w2", Kit: testKit}, jam.LaunchCreds{})
		second <- err
	}()
	select {
	case err := <-second:
		if err == nil || err.Error() != "at capacity (1/1 studios)" {
			t.Fatalf("second raise err = %v, want at capacity (1/1 studios)", err)
		}
	case <-time.After(2 * time.Second):
		close(ops.block)
		t.Fatal("second raise was admitted while the first was in flight")
	}
	close(ops.block)
	select {
	case err := <-first:
		if err != nil {
			t.Fatalf("first raise: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first raise did not return")
	}
	if _, err := g.Raise(context.Background(), jam.RaiseSpec{ActorID: "w3", Kit: testKit}, jam.LaunchCreds{}); err != nil {
		t.Fatalf("third raise: %v", err)
	}
	ops.snapshot(func(f *fakeBackend) {
		if f.runs != 2 {
			t.Fatalf("RunEphemeral calls = %d, want 2", f.runs)
		}
	})
}

func TestGatedUnlimited(t *testing.T) {
	ops := &fakeBackend{containers: map[string]map[string]string{"cove-w1": studioOf("w1", "m1")}}
	g := newGated(ops, 0)
	if _, err := g.Raise(context.Background(), jam.RaiseSpec{ActorID: "w2", Kit: testKit}, jam.LaunchCreds{}); err != nil {
		t.Fatal(err)
	}
	ops.snapshot(func(f *fakeBackend) {
		if f.lists != 0 {
			t.Fatalf("Studios listed %d times with unlimited capacity", f.lists)
		}
	})
}

// A raise past RunEphemeral is both listed and in flight; it counts once, so
// the gate still admits up to capacity while it finishes its launch.
func TestGatedCountsListedInflightOnce(t *testing.T) {
	ops := &fakeBackend{addOnRun: true, entered: make(chan struct{}, 2), block: make(chan struct{})}
	g := newGated(ops, 2)
	raise := func(actor string) chan error {
		c := make(chan error, 1)
		go func() {
			_, err := g.Raise(context.Background(), jam.RaiseSpec{ActorID: actor, Kit: testKit}, jam.LaunchCreds{})
			c <- err
		}()
		return c
	}
	waitEntered := func(which string) {
		t.Helper()
		select {
		case <-ops.entered:
		case <-time.After(2 * time.Second):
			close(ops.block)
			t.Fatalf("%s raise was not admitted (never reached RunEphemeral)", which)
		}
	}
	first := raise("w1")
	waitEntered("first")
	second := raise("w2")
	waitEntered("second")

	select {
	case err := <-raise("w3"):
		if err == nil || err.Error() != "at capacity (2/2 studios)" {
			t.Fatalf("third raise err = %v, want at capacity (2/2 studios)", err)
		}
	case <-time.After(2 * time.Second):
		close(ops.block)
		t.Fatal("third raise was admitted over capacity")
	}
	close(ops.block)
	for i, c := range []chan error{first, second} {
		select {
		case err := <-c:
			if err != nil {
				t.Fatalf("raise %d: %v", i+1, err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("raise %d did not return", i+1)
		}
	}
}
