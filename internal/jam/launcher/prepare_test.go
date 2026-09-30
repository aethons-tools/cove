package launcher

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
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
// (empty Base.Image → blessed default), threading the kit's build-args; the
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
	if ops.resolvedFrom != "" { // Base.Image == "" → blessed default
		t.Fatalf("ResolveKitBase called with %q, want empty (blessed default)", ops.resolvedFrom)
	}
	if ops.builtBase != "blessed@sha256:def" {
		t.Fatalf("build base = %q, want the resolved blessed base", ops.builtBase)
	}
	if ops.builtArgs["X"] != "1" {
		t.Fatalf("build-args not threaded: %+v", ops.builtArgs)
	}
}

func sp(s string) *string { return &s }

// A context-files base is materialized (Dockerfile + nested files, by value) into
// a per-ref base dir, built+gated via the backend, and the kit image is built
// FROM the resolved base.
func TestPrepareStudioKitContextFilesBase(t *testing.T) {
	root := t.TempDir()
	ops := &fakeOps{resolvedDockerfileBase: "blessed-df@sha256:aaa"}
	l := New(Config{
		Ops: ops, Runner: &runner.Fake{},
		JamHost: "h", RuntimeAddr: "h:443", IdentityFile: "k", KnownHostsDir: "/kh",
		BuildRoot: root, Inventory: &fakeInv{},
		assemble: func(KitDefinition, string) error { return nil },
		sleep:    func(time.Duration) {},
	})
	df := "FROM ${COVE_BASE_IMAGE}\nRUN echo hi"
	sk := studio.StudioKit{Kind: studio.Kind, Name: "web", Base: studio.Base{
		ContextFiles: studio.ContextTree{
			"dockerfile": {File: sp(df)},
			"scripts":    {Dir: studio.ContextTree{"setup.sh": {File: sp("echo setup")}}},
		},
	}}
	ref := KitRef{ID: "web", Version: 1, Digest: studio.BuildDigest(sk)}
	st, err := l.PrepareKit(context.Background(), KitDefinition{Ref: ref, Kit: sk})
	if err != nil || st.State != KitReady {
		t.Fatalf("PrepareKit = %+v, %v", st, err)
	}
	baseDir := filepath.Join(root, ref.Digest+"-base")
	if ops.resolvedDockerfileDir != baseDir {
		t.Fatalf("ResolveKitBaseDockerfile ctxDir = %q, want %q", ops.resolvedDockerfileDir, baseDir)
	}
	if got := readFile(t, filepath.Join(baseDir, "Dockerfile")); got != df {
		t.Fatalf("materialized Dockerfile = %q, want the authored one", got)
	}
	if got := readFile(t, filepath.Join(baseDir, "scripts/setup.sh")); got != "echo setup" {
		t.Fatalf("materialized nested context file = %q", got)
	}
	if ops.builtBase != "blessed-df@sha256:aaa" {
		t.Fatalf("kit image built FROM %q, want the gated Dockerfile base", ops.builtBase)
	}
	if ops.builtTag != "cove-kit:"+ref.Digest {
		t.Fatalf("image tag = %q, want the build-digest tag", ops.builtTag)
	}
}

// A base64 zip context is decoded + unzipped into the base dir, then built+gated;
// the kit image is built FROM the resolved base.
func TestPrepareStudioKitZipContextBase(t *testing.T) {
	root := t.TempDir()
	ops := &fakeOps{resolvedDockerfileBase: "blessed-zip@sha256:bbb"}
	l := New(Config{
		Ops: ops, Runner: &runner.Fake{},
		JamHost: "h", RuntimeAddr: "h:443", IdentityFile: "k", KnownHostsDir: "/kh",
		BuildRoot: root, Inventory: &fakeInv{},
		assemble: func(KitDefinition, string) error { return nil },
		sleep:    func(time.Duration) {},
	})
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range []struct{ name, body string }{
		{"Dockerfile", "FROM ${COVE_BASE_IMAGE}\n"},
		{"app/main.go", "package main\n"},
	} {
		w, err := zw.Create(e.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	sk := studio.StudioKit{Kind: studio.Kind, Name: "web", Base: studio.Base{
		Context: base64.StdEncoding.EncodeToString(buf.Bytes()),
	}}
	ref := KitRef{ID: "web", Version: 1, Digest: studio.BuildDigest(sk)}
	st, err := l.PrepareKit(context.Background(), KitDefinition{Ref: ref, Kit: sk})
	if err != nil || st.State != KitReady {
		t.Fatalf("PrepareKit = %+v, %v", st, err)
	}
	baseDir := filepath.Join(root, ref.Digest+"-base")
	if ops.resolvedDockerfileDir != baseDir {
		t.Fatalf("ResolveKitBaseDockerfile ctxDir = %q, want %q", ops.resolvedDockerfileDir, baseDir)
	}
	if got := readFile(t, filepath.Join(baseDir, "app/main.go")); got != "package main\n" {
		t.Fatalf("zip nested file = %q", got)
	}
	if ops.builtBase != "blessed-zip@sha256:bbb" {
		t.Fatalf("kit image built FROM %q, want the gated zip base", ops.builtBase)
	}
}

// An unblessed context base fails the prepare loudly (gate ON, fail-closed): the
// kit image is never built.
func TestPrepareStudioKitContextBaseGateFails(t *testing.T) {
	ops := &fakeOps{resolveDockerfileErr: errors.New("base does not descend from any blessed cove-base-image")}
	l := New(Config{
		Ops: ops, Runner: &runner.Fake{},
		JamHost: "h", RuntimeAddr: "h:443", IdentityFile: "k", KnownHostsDir: "/kh",
		BuildRoot: t.TempDir(), Inventory: &fakeInv{},
		assemble: func(KitDefinition, string) error { return nil },
		sleep:    func(time.Duration) {},
	})
	sk := studio.StudioKit{Kind: studio.Kind, Name: "web", Base: studio.Base{
		ContextFiles: studio.ContextTree{"dockerfile": {File: sp("FROM scratch")}},
	}}
	ref := KitRef{ID: "web", Version: 1, Digest: studio.BuildDigest(sk)}
	st, err := l.PrepareKit(context.Background(), KitDefinition{Ref: ref, Kit: sk})
	if err == nil || st.State == KitReady {
		t.Fatalf("want a fail-closed prepare, got st=%+v err=%v", st, err)
	}
	if ops.builds != 0 {
		t.Fatalf("kit image must not build when the base gate fails; builds=%d", ops.builds)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
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
