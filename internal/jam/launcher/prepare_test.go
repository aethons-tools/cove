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

// anyCallHasArgs reports whether some recorded call's [Name, Args...] contains
// the given tokens as a contiguous subsequence (e.g. "build","-t","cove-kit:..").
func anyCallHasArgs(f *runner.Fake, tokens ...string) bool {
	for _, c := range f.Calls {
		seq := append([]string{c.Name}, c.Args...)
		for start := 0; start+len(tokens) <= len(seq); start++ {
			match := true
			for j, tok := range tokens {
				if seq[start+j] != tok {
					match = false
					break
				}
			}
			if match {
				return true
			}
		}
	}
	return false
}

func dockerBuilds(f *runner.Fake) int {
	n := 0
	for _, c := range f.Calls {
		if c.Name == "docker" && len(c.Args) > 0 && c.Args[0] == "build" {
			n++
		}
	}
	return n
}

func newPrepareLauncher(f *runner.Fake, inv Inventory, asm func(KitDefinition, string) error) *Launcher {
	return New(Config{
		Ops:     &fakeOps{},
		Runner:  f,
		JamHost: "h", RuntimeAddr: "h:443", IdentityFile: "k", KnownHostsDir: "/kh",
		BuildRoot: "/tmp/cove-kit-builds-test",
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
	f := &runner.Fake{}
	inv := &fakeInv{}
	asm := func(def KitDefinition, buildDir string) error { return nil }
	l := newPrepareLauncher(f, inv, asm)

	st, err := l.PrepareKit(context.Background(), KitDefinition{
		Ref:    KitRef{ID: "managed", Version: 1},
		Config: minimalKitConfig(t),
	})
	if err != nil || st.State != KitReady {
		t.Fatalf("PrepareKit = %+v, %v; want KitReady, nil", st, err)
	}
	if !anyCallHasArgs(f, "build", "-t", "cove-kit:managed-v1") {
		t.Fatalf("no docker build for the kit tag; calls=%+v", f.Calls)
	}
}

func TestPrepareKitIdempotentWhenPresent(t *testing.T) {
	f := &runner.Fake{}
	inv := &fakeInv{}
	ref := KitRef{ID: "managed", Version: 4}
	inv.set(ref, true) // already prepared
	asmCalled := false
	asm := func(def KitDefinition, buildDir string) error { asmCalled = true; return nil }
	l := newPrepareLauncher(f, inv, asm)

	st, err := l.PrepareKit(context.Background(), KitDefinition{Ref: ref, Config: minimalKitConfig(t)})
	if err != nil || st.State != KitReady {
		t.Fatalf("PrepareKit(present) = %+v, %v; want KitReady, nil", st, err)
	}
	if asmCalled {
		t.Fatal("PrepareKit must not assemble a kit that is already present")
	}
	if dockerBuilds(f) != 0 {
		t.Fatalf("PrepareKit(present) issued %d builds; want 0", dockerBuilds(f))
	}
}

func TestPrepareKitDedupesConcurrentBuilds(t *testing.T) {
	f := &runner.Fake{}
	inv := &fakeInv{}
	ref := KitRef{ID: "managed", Version: 7}
	// The assembler simulates the build's effect (the tagged image now exists) so a
	// serialized later prepare short-circuits; the small sleep widens the race
	// window the per-ref lock must close.
	asm := func(def KitDefinition, buildDir string) error {
		time.Sleep(2 * time.Millisecond)
		inv.set(def.Ref, true)
		return nil
	}
	l := newPrepareLauncher(f, inv, asm)

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

	if got := dockerBuilds(f); got != 1 {
		t.Fatalf("concurrent PrepareKit built the image %d times; want exactly 1 (de-duped)", got)
	}
}

func TestPrepareKitBuildErrorSurfaces(t *testing.T) {
	f := &runner.Fake{Err: errors.New("docker build boom")}
	inv := &fakeInv{}
	asm := func(def KitDefinition, buildDir string) error { return nil }
	l := newPrepareLauncher(f, inv, asm)

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
