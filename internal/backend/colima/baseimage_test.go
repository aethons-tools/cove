package colima

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/backend"
	"github.com/aethons-tools/cove/internal/basedigest"
	"github.com/aethons-tools/cove/internal/runner"
)

// The default path (no image.base, no image/Dockerfile) needs no gate: the ref
// is a blessed jam-studio-base by construction, so no docker inspect runs.
func TestResolveBaseDefaultSkipsGate(t *testing.T) {
	f := &runner.Fake{}
	c := &Colima{r: f}
	ref, err := c.resolveBase(backend.BaseSpec{})
	if err != nil {
		t.Fatal(err)
	}
	if ref != basedigest.DefaultRef() {
		t.Fatalf("ref = %q, want %q", ref, basedigest.DefaultRef())
	}
	for _, call := range f.Calls {
		if contains(call.Args, "inspect") {
			t.Fatalf("default path must not inspect: %+v", f.Calls)
		}
	}
}

// A kit-chosen base tag that descends from the blessed image passes the gate.
func TestResolveBaseTagVerified(t *testing.T) {
	f := &runner.Fake{Outputs: []runner.FakeResult{
		{Stdout: `["sha256:a","sha256:b","sha256:c"]`}, // the base tag's layers
		{Stdout: `["sha256:a","sha256:b"]`},            // the blessed image's layers (a prefix)
	}}
	c := &Colima{r: f}
	const tag = "ghcr.io/acme/jam-studio@sha256:child"
	ref, err := c.resolveBase(backend.BaseSpec{Base: tag})
	if err != nil {
		t.Fatal(err)
	}
	if ref != tag {
		t.Fatalf("ref = %q, want %q", ref, tag)
	}
	if !allPinned(f.Calls) {
		t.Fatalf("every docker call must pin --context colima: %+v", f.Calls)
	}
}

// A base that descends from no blessed image is rejected.
func TestResolveBaseTagRejected(t *testing.T) {
	f := &runner.Fake{Outputs: []runner.FakeResult{
		{Stdout: `["sha256:x","sha256:y"]`}, // unrelated base
		{Stdout: `["sha256:a","sha256:b"]`}, // blessed
	}}
	c := &Colima{r: f}
	_, err := c.resolveBase(backend.BaseSpec{Base: "ubuntu@sha256:evil"})
	if err == nil {
		t.Fatal("a non-descendant base must be rejected")
	}
}

// --allow-unverified-base downgrades the rejection and proceeds with the ref.
func TestResolveBaseAllowUnverified(t *testing.T) {
	f := &runner.Fake{Outputs: []runner.FakeResult{
		{Stdout: `["sha256:x","sha256:y"]`},
		{Stdout: `["sha256:a","sha256:b"]`},
	}}
	c := &Colima{r: f}
	const tag = "ubuntu@sha256:evil"
	ref, err := c.resolveBase(backend.BaseSpec{Base: tag, AllowUnverified: true})
	if err != nil {
		t.Fatalf("--allow-unverified-base must proceed, got: %v", err)
	}
	if ref != tag {
		t.Fatalf("ref = %q, want %q", ref, tag)
	}
}

// A kit image/Dockerfile is built (docker build -q → image ID); that ID is
// tagged under a local, content-derived tag and the tag becomes the gated base.
// The bare `sha256:<id>` that `docker build -q` prints is NOT usable in `FROM` —
// Docker parses it as the repo/tag `docker.io/library/sha256:<id>` and fails to
// pull — and there is no way to reference the local build cache by digest in a
// FROM. Tagging the just-built image and handing back the tag gives `FROM ${BASE}`
// a name the local daemon resolves without a registry fetch.
func TestResolveBaseBuildsDockerfile(t *testing.T) {
	kitDir := t.TempDir()
	imageDir := filepath.Join(kitDir, "image")
	if err := os.MkdirAll(imageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(imageDir, "Dockerfile"), []byte("FROM x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	const builtID = "sha256:builtlocal"
	const wantRef = "jam-kit-base:builtlocal"
	f := &runner.Fake{Outputs: []runner.FakeResult{
		{Stdout: builtID + "\n"},                       // docker build -q
		{Stdout: `["sha256:a","sha256:b","sha256:k"]`}, // the built image's layers
		{Stdout: `["sha256:a","sha256:b"]`},            // blessed
	}}
	c := &Colima{r: f}
	ref, err := c.resolveBase(backend.BaseSpec{KitDir: kitDir})
	if err != nil {
		t.Fatal(err)
	}
	if ref != wantRef {
		t.Fatalf("ref = %q, want local tag ref %q", ref, wantRef)
	}
	build := dockerCall(f.Calls, "build")
	if build == nil {
		t.Fatalf("expected a docker build of the kit image/, calls: %+v", f.Calls)
	}
	// The launcher injects the blessed base as the kit Dockerfile's
	// JAM_STUDIO_BASE_IMAGE build arg so a kit's own image/Dockerfile builds on
	// the blessed base (its ARG default is only for a bare manual `docker build`).
	assertJamBuildArgs(t, build, basedigest.DefaultRef())
	// The built ID must be tagged under wantRef so `FROM ${BASE}` can resolve it.
	tag := dockerCall(f.Calls, "tag")
	if tag == nil || !contains(tag, builtID) || !contains(tag, wantRef) {
		t.Fatalf("expected `docker tag %s %s`, calls: %+v", builtID, wantRef, f.Calls)
	}
}

// ResolveKitBaseTar streams the context tar to `docker build -q … -` (the tar on
// stdin), tags the built id under jam-kit-base:<hex>, and runs the provenance
// gate. The built base must descend from a blessed jam-studio-base.
func TestResolveKitBaseTarStreamsAndGates(t *testing.T) {
	const builtID = "sha256:streamed"
	const wantRef = "jam-kit-base:streamed"
	const tarBytes = "FAKE_TAR_GZ_BYTES"
	f := &runner.Fake{Outputs: []runner.FakeResult{
		{Stdout: builtID + "\n"},                       // docker build -q - (streamed)
		{Stdout: `["sha256:a","sha256:b","sha256:k"]`}, // the built image's layers
		{Stdout: `["sha256:a","sha256:b"]`},            // blessed (a prefix → descends)
	}}
	c := &Colima{r: f}
	ref, err := c.ResolveKitBaseTar(strings.NewReader(tarBytes))
	if err != nil {
		t.Fatal(err)
	}
	if ref != wantRef {
		t.Fatalf("ref = %q, want %q", ref, wantRef)
	}
	build := dockerCall(f.Calls, "build")
	if build == nil {
		t.Fatalf("expected a streamed docker build, calls: %+v", f.Calls)
	}
	// The argv builds from stdin (`-`) with the two standard studio build args.
	if !contains(build, "-") {
		t.Fatalf("build must read the context from stdin (`-`): %+v", build)
	}
	assertJamBuildArgs(t, build, basedigest.DefaultRef())
	// The tar must be piped on stdin (Fake records RunIO's stdin bytes).
	buildCall := dockerCallFull(f.Calls, "build")
	if buildCall == nil || buildCall.Stdin != tarBytes {
		t.Fatalf("context tar must be streamed on stdin; got Stdin=%q", stdinOf(buildCall))
	}
	// The built id is tagged under the content-derived local tag for FROM/inspect.
	tag := dockerCall(f.Calls, "tag")
	if tag == nil || !contains(tag, builtID) || !contains(tag, wantRef) {
		t.Fatalf("expected `docker tag %s %s`, calls: %+v", builtID, wantRef, f.Calls)
	}
	if !allPinned(f.Calls) {
		t.Fatalf("every docker call must pin --context colima: %+v", f.Calls)
	}
}

// An unblessed streamed base is rejected (gate ON) and no tag is returned.
func TestResolveKitBaseTarGateRejects(t *testing.T) {
	f := &runner.Fake{Outputs: []runner.FakeResult{
		{Stdout: "sha256:streamed\n"},       // build
		{Stdout: `["sha256:x","sha256:y"]`}, // unrelated base layers
		{Stdout: `["sha256:a","sha256:b"]`}, // blessed
	}}
	c := &Colima{r: f}
	ref, err := c.ResolveKitBaseTar(strings.NewReader("tar"))
	if err == nil {
		t.Fatal("a non-descendant streamed base must be rejected")
	}
	if ref != "" {
		t.Fatalf("a rejected gate must return no tag, got %q", ref)
	}
}

// A chain stage builds its streamed context FROM the parent stage (both base
// build-args carry parentRef, never the blessed default), is gated against the
// blessed refs, and only then is tagged with its stage tag, which it returns.
func TestResolveKitBaseTarOnBuildsOnParentAndTagsStage(t *testing.T) {
	const parentRef = "jam-kit-base:parentstage"
	const stageTag = "jam-kit-base:0123456789abcdef0123456789abcdef"
	f := &runner.Fake{Outputs: []runner.FakeResult{
		{Stdout: "sha256:child\n"},                                // docker build -q - (streamed)
		{Stdout: `["sha256:a","sha256:b","sha256:p","sha256:c"]`}, // the built stage's layers
		{Stdout: `["sha256:a","sha256:b"]`},                       // blessed (a prefix → descends)
	}}
	c := &Colima{r: f}
	ref, err := c.ResolveKitBaseTarOn(strings.NewReader("CTX"), parentRef, stageTag)
	if err != nil {
		t.Fatal(err)
	}
	if ref != stageTag {
		t.Fatalf("ref = %q, want the stage tag %q", ref, stageTag)
	}
	build := dockerCall(f.Calls, "build")
	if build == nil || !contains(build, "-") {
		t.Fatalf("stage build must stream FROM the parent stage: %+v", build)
	}
	assertJamBuildArgs(t, build, parentRef)
	if contains(build, "JAM_STUDIO_BASE_IMAGE="+basedigest.DefaultRef()) {
		t.Fatalf("a stage must not build on the blessed default: %+v", build)
	}
	if bc := dockerCallFull(f.Calls, "build"); bc == nil || bc.Stdin != "CTX" {
		t.Fatalf("context must be streamed on stdin; got %q", stdinOf(bc))
	}
	// The gate inspected the blessed refs, and the stage tag is the LAST call
	// (applied only after the gate passed).
	if insp := dockerCall(f.Calls, "inspect"); insp == nil {
		t.Fatalf("the gate must inspect layers: %+v", f.Calls)
	}
	last := f.Calls[len(f.Calls)-1].Args[2:]
	if len(last) != 3 || last[0] != "tag" || last[1] != "jam-kit-base:child" || last[2] != stageTag {
		t.Fatalf("last call = %v, want `docker tag jam-kit-base:child %s`", last, stageTag)
	}
	if !allPinned(f.Calls) {
		t.Fatalf("every docker call must pin --context colima: %+v", f.Calls)
	}
}

// A stage whose built image descends from no blessed base is rejected, and its
// stage tag is never applied — so HasKitBase can never vouch for an ungated stage.
func TestResolveKitBaseTarOnGateRejectsWithoutStageTag(t *testing.T) {
	const stageTag = "jam-kit-base:0123456789abcdef0123456789abcdef"
	f := &runner.Fake{Outputs: []runner.FakeResult{
		{Stdout: "sha256:child\n"},
		{Stdout: `["sha256:x","sha256:y"]`}, // unrelated layers
		{Stdout: `["sha256:a","sha256:b"]`}, // blessed
	}}
	c := &Colima{r: f}
	ref, err := c.ResolveKitBaseTarOn(strings.NewReader("CTX"), "jam-kit-base:parent", stageTag)
	if err == nil || ref != "" {
		t.Fatalf("ResolveKitBaseTarOn = %q, %v; want a gate rejection", ref, err)
	}
	for _, call := range f.Calls {
		if contains(call.Args, stageTag) {
			t.Fatalf("a rejected stage must not be tagged %s: %+v", stageTag, f.Calls)
		}
	}
}

// A stage needs a parent and a stage tag; neither may be empty.
func TestResolveKitBaseTarOnRequiresParentAndTag(t *testing.T) {
	c := &Colima{r: &runner.Fake{}}
	if _, err := c.ResolveKitBaseTarOn(strings.NewReader("CTX"), "", "jam-kit-base:x"); err == nil {
		t.Fatal("an empty parentRef must be refused")
	}
	if _, err := c.ResolveKitBaseTarOn(strings.NewReader("CTX"), "jam-kit-base:p", ""); err == nil {
		t.Fatal("an empty stageTag must be refused")
	}
}

// HasKitBase inspects the stage tag on the pinned daemon; absence is a miss.
func TestHasKitBase(t *testing.T) {
	f := &runner.Fake{}
	ok, err := NewWithContext(f, "").HasKitBase("jam-kit-base:stage")
	if err != nil || !ok {
		t.Fatalf("present: HasKitBase = %v, %v", ok, err)
	}
	insp := dockerCall(f.Calls, "image")
	if insp == nil || !contains(insp, "inspect") || !contains(insp, "jam-kit-base:stage") || !allPinned(f.Calls) {
		t.Fatalf("HasKitBase did not inspect the pinned tag: %+v", f.Calls)
	}
	f2 := &runner.Fake{Err: &runner.ExitError{Code: 1}}
	if ok, err := NewWithContext(f2, "").HasKitBase("jam-kit-base:missing"); ok || err != nil {
		t.Fatalf("absent: HasKitBase = %v, %v; want (false, nil)", ok, err)
	}
}

// assertJamBuildArgs checks a kit/studio base build passes exactly the jam
// build args, JAM_STUDIO_BASE_IMAGE=base and JAM_STUDIO_TARGET_ARCH, each once,
// and no pre-rename build arg.
func assertJamBuildArgs(t *testing.T, build []string, base string) {
	t.Helper()
	argv := strings.Join(build, " ")
	for _, want := range []string{
		"--build-arg JAM_STUDIO_BASE_IMAGE=" + base,
		"--build-arg JAM_STUDIO_TARGET_ARCH=" + runtime.GOARCH,
	} {
		if !strings.Contains(argv, want) {
			t.Fatalf("build must pass %q:\n%s", want, argv)
		}
	}
	if n := strings.Count(argv, "--build-arg JAM_STUDIO_BASE_IMAGE="); n != 1 {
		t.Fatalf("JAM_STUDIO_BASE_IMAGE passed %d times; want once:\n%s", n, argv)
	}
	if n := strings.Count(argv, "--build-arg"); n != 2 {
		t.Fatalf("build passes %d build args; want exactly 2:\n%s", n, argv)
	}
	if strings.Contains(argv, "COVE_") || strings.Contains(argv, "AT_"+"JAM_") {
		t.Fatalf("pre-rename build arg passed:\n%s", argv)
	}
}
