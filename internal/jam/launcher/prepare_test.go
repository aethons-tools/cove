package launcher

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/kit"
	"github.com/aethons-tools/cove/internal/runner"
)

// fakeInv is a mutable prepared-kit inventory a PrepareKit test controls: Has
// reflects whatever the test (or a simulated build) has marked present.
type fakeInv struct {
	mu      sync.Mutex
	present map[string]bool
}

func (i *fakeInv) Has(r KitRef) (bool, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.present[r.String()], nil
}

func (i *fakeInv) set(r KitRef, v bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.present == nil {
		i.present = map[string]bool{}
	}
	i.present[r.String()] = v
}

// countingOps is a fakeOps whose BuildKitImage is safe to call concurrently and
// counts builds — the de-dupe test needs the count race-free.
type countingOps struct {
	fakeOps
	mu       sync.Mutex
	nbuilds  int
	onBuild  func(tag string)
	buildErr error
}

func (c *countingOps) BuildKitImage(buildDir, tag, base string, noCache bool) (string, error) {
	c.mu.Lock()
	c.nbuilds++
	c.mu.Unlock()
	if c.onBuild != nil {
		c.onBuild(tag)
	}
	if c.buildErr != nil {
		return "", c.buildErr
	}
	return "sha256:built", nil
}
func (c *countingOps) builds() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.nbuilds
}

func newPrepareLauncher(ops Backend, inv Inventory, asm func(KitDefinition, string) error) *Launcher {
	return New(Config{
		Ops:     ops,
		Runner:  &runner.Fake{},
		JamHost: "h", RuntimeAddr: "h:443", IdentityFile: "k", KnownHostsDir: "/kh",
		BuildRoot: "/tmp/cove-kit-builds-test",
		BaseImage: "cove-base@sha256:base", // the Dockerfile's FROM ${BASE}
		Inventory: inv,
		assemble:  asm,
		sleep:     func(time.Duration) {},
	})
}

func minimalKitConfig(t *testing.T) kit.Config {
	t.Helper()
	return kit.Config{Name: "managed"}
}

func TestPrepareKitBuildsTaggedImage(t *testing.T) {
	ops := &fakeOps{}
	inv := &fakeInv{}
	asm := func(def KitDefinition, buildDir string) error { return nil }
	l := newPrepareLauncher(ops, inv, asm)

	st, err := l.PrepareKit(context.Background(), KitDefinition{
		Ref:    KitRef{ID: "managed", Version: 1},
		Config: minimalKitConfig(t),
	})
	if err != nil || st.State != KitReady {
		t.Fatalf("PrepareKit = %+v, %v; want KitReady, nil", st, err)
	}
	if ops.builds != 1 || ops.builtTag != "cove-kit:managed-v1" {
		t.Fatalf("build not routed through the backend for the kit tag: builds=%d tag=%q", ops.builds, ops.builtTag)
	}
	if ops.builtBase != "cove-base@sha256:base" {
		t.Fatalf("build did not pass the resolved base: %q", ops.builtBase)
	}
}

func TestPrepareKitIdempotentWhenPresent(t *testing.T) {
	ops := &fakeOps{}
	inv := &fakeInv{}
	ref := KitRef{ID: "managed", Version: 4}
	inv.set(ref, true) // already prepared
	asmCalled := false
	asm := func(def KitDefinition, buildDir string) error { asmCalled = true; return nil }
	l := newPrepareLauncher(ops, inv, asm)

	st, err := l.PrepareKit(context.Background(), KitDefinition{Ref: ref, Config: minimalKitConfig(t)})
	if err != nil || st.State != KitReady {
		t.Fatalf("PrepareKit(present) = %+v, %v; want KitReady, nil", st, err)
	}
	if asmCalled {
		t.Fatal("PrepareKit must not assemble a kit that is already present")
	}
	if ops.builds != 0 {
		t.Fatalf("PrepareKit(present) issued %d builds; want 0", ops.builds)
	}
}

func TestPrepareKitDedupesConcurrentBuilds(t *testing.T) {
	inv := &fakeInv{}
	ref := KitRef{ID: "managed", Version: 7}
	ops := &countingOps{}
	// The assembler simulates the build's effect (the tagged image now exists) so a
	// serialized later prepare short-circuits; the small sleep widens the race
	// window the per-ref lock must close.
	asm := func(def KitDefinition, buildDir string) error {
		time.Sleep(2 * time.Millisecond)
		inv.set(def.Ref, true)
		return nil
	}
	l := newPrepareLauncher(ops, inv, asm)

	const n = 8
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			_, _ = l.PrepareKit(context.Background(), KitDefinition{Ref: ref, Config: minimalKitConfig(t)})
		}()
	}
	wg.Wait()

	if got := ops.builds(); got != 1 {
		t.Fatalf("concurrent PrepareKit built the image %d times; want exactly 1 (de-duped)", got)
	}
}

func TestPrepareKitBuildErrorSurfaces(t *testing.T) {
	ops := &fakeOps{buildErr: errors.New("docker build boom")}
	inv := &fakeInv{}
	asm := func(def KitDefinition, buildDir string) error { return nil }
	l := newPrepareLauncher(ops, inv, asm)

	st, err := l.PrepareKit(context.Background(), KitDefinition{
		Ref:    KitRef{ID: "managed", Version: 2},
		Config: minimalKitConfig(t),
	})
	if err == nil {
		t.Fatal("PrepareKit must surface a build failure")
	}
	if st.State == KitReady {
		t.Fatalf("a failed build must not report KitReady: %+v", st)
	}
}
