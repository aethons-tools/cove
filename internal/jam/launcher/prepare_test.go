package launcher

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/runner"
	"github.com/aethons-tools/cove/internal/studio"
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

func (c *countingOps) BuildKitImage(buildDir, tag, base string, buildArgs map[string]string, noCache bool) (string, error) {
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
		Inventory: inv,
		assemble:  asm,
		sleep:     func(time.Duration) {},
	})
}

// studioKitDef is a small studio KitDefinition for prepare tests: its ref's
// Digest is the build-digest, so imageTag(ref) == cove-kit:<digest>.
func studioKitDef(name string, version int, sk studio.StudioKit) KitDefinition {
	return KitDefinition{Ref: KitRef{ID: name, Version: version, Digest: studio.BuildDigest(sk)}, Kit: sk}
}

// A studio kit builds by its build-digest tag, FROM the substrate-resolved base
// (empty Base.Ref → blessed default), threading the kit's build-args; the
// Anthropic-excluded ceiling is applied at assemble.
func TestPrepareStudioKitBuildsByDigestWithCeiling(t *testing.T) {
	ops := &fakeOps{resolvedBase: "blessed@sha256:def"}
	inv := &fakeInv{}
	asm := func(def KitDefinition, buildDir string) error { return nil } // assemble seam
	l := newPrepareLauncher(ops, inv, asm)

	sk := studio.StudioKit{Kind: studio.Kind, Name: "web",
		Egress: []string{"github.com", ".anthropic.com"}, BuildArgs: map[string]string{"X": "1"}}
	ref := KitRef{ID: "web", Version: 1, Digest: studio.BuildDigest(sk)}
	st, err := l.PrepareKit(context.Background(), KitDefinition{Ref: ref, Kit: sk})
	if err != nil || st.State != KitReady {
		t.Fatalf("PrepareKit = %+v, %v", st, err)
	}
	if ops.builtTag != "cove-kit:"+ref.Digest {
		t.Fatalf("image tag must be the build-digest: %q", ops.builtTag)
	}
	if ops.resolvedFrom != "" { // Base.Ref == "" → blessed default
		t.Fatalf("ResolveKitBase called with %q, want empty (blessed default)", ops.resolvedFrom)
	}
	if ops.builtBase != "blessed@sha256:def" {
		t.Fatalf("build base = %q, want the resolved blessed base", ops.builtBase)
	}
	if ops.builtArgs["X"] != "1" {
		t.Fatalf("build-args not threaded: %+v", ops.builtArgs)
	}
}

// A Dockerfile-context base is accepted in the schema but its build is deferred.
func TestPrepareStudioKitDockerfileDeferred(t *testing.T) {
	l := newPrepareLauncher(&fakeOps{}, &fakeInv{}, func(KitDefinition, string) error { return nil })
	sk := studio.StudioKit{Kind: studio.Kind, Name: "web", Base: studio.Base{Dockerfile: "FROM x"}}
	ref := KitRef{ID: "web", Version: 1, Digest: studio.BuildDigest(sk)}
	_, err := l.PrepareKit(context.Background(), KitDefinition{Ref: ref, Kit: sk})
	if !errors.Is(err, ErrDockerfileContextUnsupported) {
		t.Fatalf("want ErrDockerfileContextUnsupported, got %v", err)
	}
}

func TestPrepareKitIdempotentWhenPresent(t *testing.T) {
	ops := &fakeOps{}
	inv := &fakeInv{}
	def := studioKitDef("web", 4, studio.StudioKit{Kind: studio.Kind, Name: "web"})
	inv.set(def.Ref, true) // already prepared
	asmCalled := false
	asm := func(def KitDefinition, buildDir string) error { asmCalled = true; return nil }
	l := newPrepareLauncher(ops, inv, asm)

	st, err := l.PrepareKit(context.Background(), def)
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
	def := studioKitDef("web", 7, studio.StudioKit{Kind: studio.Kind, Name: "web", Egress: []string{"github.com"}})
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
			_, _ = l.PrepareKit(context.Background(), def)
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

	st, err := l.PrepareKit(context.Background(), studioKitDef("web", 2, studio.StudioKit{Kind: studio.Kind, Name: "web"}))
	if err == nil {
		t.Fatal("PrepareKit must surface a build failure")
	}
	if st.State == KitReady {
		t.Fatalf("a failed build must not report KitReady: %+v", st)
	}
}
