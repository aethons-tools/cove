package launcher

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/kit"
	"github.com/aethons-tools/cove/internal/runner"
	"github.com/aethons-tools/cove/internal/studio"
)

// untarNames reads a tar.gz into a name→content map (test-side assertion of the
// streamed context docker would extract).
func untarNames(t *testing.T, raw []byte) map[string]string {
	t.Helper()
	gr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("gzip: %v", err)
	}
	tr := tar.NewReader(gr)
	out := map[string]string{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar: %v", err)
		}
		var b bytes.Buffer
		if _, err := io.Copy(&b, tr); err != nil { //nolint:gosec // bounded test input
			t.Fatal(err)
		}
		out[hdr.Name] = b.String()
	}
	return out
}

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
// Digest is the build-digest, so l.imageTag(ref) == cove-kit:<digest>-<asm>.
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

	sk := studio.StudioKit{Kind: studio.Kind, Egress: []string{"github.com", ".anthropic.com"}, BuildArgs: map[string]string{"X": "1"}}
	ref := KitRef{ID: "web", Version: 1, Digest: studio.BuildDigest(sk)}
	st, err := l.PrepareKit(context.Background(), KitDefinition{Ref: ref, Kit: sk})
	if err != nil || st.State != KitReady {
		t.Fatalf("PrepareKit = %+v, %v", st, err)
	}
	if ops.builtTag != l.imageTag(ref) {
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

// A context-files base is produced as a context tar (Dockerfile + nested files),
// scanned, streamed to the backend's `docker build -`, and gated; the kit image
// is built FROM the resolved base.
func TestPrepareStudioKitContextFilesBase(t *testing.T) {
	ops := &fakeOps{resolvedTarBase: "blessed-df@sha256:aaa"}
	l := New(Config{
		Ops: ops, Runner: &runner.Fake{},
		JamHost: "h", RuntimeAddr: "h:443", IdentityFile: "k", KnownHostsDir: "/kh",
		BuildRoot: t.TempDir(), Inventory: &fakeInv{},
		assemble: func(KitDefinition, string) error { return nil },
		sleep:    func(time.Duration) {},
	})
	df := "FROM ${COVE_BASE_IMAGE}\nRUN echo hi"
	sk := studio.StudioKit{Kind: studio.Kind, Base: studio.Base{
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
	// The context tar streamed to the backend carries the authored tree (the
	// reserved `dockerfile` as a root Dockerfile, nested files under their paths).
	files := untarNames(t, ops.resolvedTar)
	if files["Dockerfile"] != df {
		t.Fatalf("streamed Dockerfile = %q, want the authored one", files["Dockerfile"])
	}
	if files["scripts/setup.sh"] != "echo setup" {
		t.Fatalf("streamed nested context file = %q", files["scripts/setup.sh"])
	}
	if ops.builtBase != "blessed-df@sha256:aaa" {
		t.Fatalf("kit image built FROM %q, want the gated context base", ops.builtBase)
	}
	if ops.builtTag != l.imageTag(ref) {
		t.Fatalf("image tag = %q, want the build-digest tag", ops.builtTag)
	}
}

// A stored base64 tar.gz context is decoded, scanned, and streamed to the
// backend's `docker build -`, then gated; the kit image is built FROM the base.
func TestPrepareStudioKitTarContextBase(t *testing.T) {
	ops := &fakeOps{resolvedTarBase: "blessed-tar@sha256:bbb"}
	l := New(Config{
		Ops: ops, Runner: &runner.Fake{},
		JamHost: "h", RuntimeAddr: "h:443", IdentityFile: "k", KnownHostsDir: "/kh",
		BuildRoot: t.TempDir(), Inventory: &fakeInv{},
		assemble: func(KitDefinition, string) error { return nil },
		sleep:    func(time.Duration) {},
	})
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	for _, e := range []struct{ name, body string }{
		{"Dockerfile", "FROM ${COVE_BASE_IMAGE}\n"},
		{"app/main.go", "package main\n"},
	} {
		if err := tw.WriteHeader(&tar.Header{Name: e.name, Mode: 0o644, Size: int64(len(e.body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	raw := buf.Bytes()
	sk := studio.StudioKit{Kind: studio.Kind, Base: studio.Base{
		Context: base64.StdEncoding.EncodeToString(raw),
	}}
	ref := KitRef{ID: "web", Version: 1, Digest: studio.BuildDigest(sk)}
	st, err := l.PrepareKit(context.Background(), KitDefinition{Ref: ref, Kit: sk})
	if err != nil || st.State != KitReady {
		t.Fatalf("PrepareKit = %+v, %v", st, err)
	}
	if !bytes.Equal(ops.resolvedTar, raw) {
		t.Fatalf("streamed context must be the stored tar bytes verbatim")
	}
	if got := untarNames(t, ops.resolvedTar)["app/main.go"]; got != "package main\n" {
		t.Fatalf("streamed nested file = %q", got)
	}
	if ops.builtBase != "blessed-tar@sha256:bbb" {
		t.Fatalf("kit image built FROM %q, want the gated tar base", ops.builtBase)
	}
}

// An unblessed context base fails the prepare loudly (gate ON, fail-closed): the
// kit image is never built.
func TestPrepareStudioKitContextBaseGateFails(t *testing.T) {
	ops := &fakeOps{resolveTarErr: errors.New("base does not descend from any blessed cove-base-image")}
	l := New(Config{
		Ops: ops, Runner: &runner.Fake{},
		JamHost: "h", RuntimeAddr: "h:443", IdentityFile: "k", KnownHostsDir: "/kh",
		BuildRoot: t.TempDir(), Inventory: &fakeInv{},
		assemble: func(KitDefinition, string) error { return nil },
		sleep:    func(time.Duration) {},
	})
	sk := studio.StudioKit{Kind: studio.Kind, Base: studio.Base{
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

func TestPrepareKitIdempotentWhenPresent(t *testing.T) {
	ops := &fakeOps{}
	inv := &fakeInv{}
	def := studioKitDef("web", 4, studio.StudioKit{Kind: studio.Kind})
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
	def := studioKitDef("web", 7, studio.StudioKit{Kind: studio.Kind, Egress: []string{"github.com"}})
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

	st, err := l.PrepareKit(context.Background(), studioKitDef("web", 2, studio.StudioKit{Kind: studio.Kind}))
	if err == nil {
		t.Fatal("PrepareKit must surface a build failure")
	}
	if st.State == KitReady {
		t.Fatalf("a failed build must not report KitReady: %+v", st)
	}
}

// The real assembler bakes the studio kit's mcp-servers into the image
// (COV-240), where cove-master's claude harness reads them.
func TestDefaultAssembleBakesKitMCPServers(t *testing.T) {
	sk := studio.StudioKit{Kind: studio.Kind, MCPServers: map[string]kit.MCPServer{
		"linear": {Type: "http", URL: "${LINEAR_MCP_URL}"},
	}}
	l := New(Config{PublicKey: []byte("k\n")})
	buildDir := filepath.Join(t.TempDir(), "b")
	if err := l.defaultAssemble(studioKitDef("web", 1, sk), buildDir); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(buildDir, "image-files", kit.MCPServersImagePath))
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"linear":{"type":"http","url":"${LINEAR_MCP_URL}"}}` + "\n"; string(got) != want {
		t.Fatalf("baked mcp-servers = %s, want %s", got, want)
	}
}
