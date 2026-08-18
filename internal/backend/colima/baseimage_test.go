package colima

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/aethons-tools/cove/internal/backend"
	"github.com/aethons-tools/cove/internal/basedigest"
	"github.com/aethons-tools/cove/internal/runner"
)

// The default path (no image.base, no image/Dockerfile) needs no gate: the ref
// is a blessed cove-base-image by construction, so no docker inspect runs.
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
	const tag = "ghcr.io/acme/cove-image@sha256:child"
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
	const wantRef = "cove-kit-base:builtlocal"
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
	// at-cove injects the blessed base as the kit Dockerfile's COVE_BASE_IMAGE
	// build arg so a kit's own image/Dockerfile builds on the blessed base
	// (its ARG default is only for a bare manual `docker build`).
	if !contains(build, "--build-arg") || !contains(build, "COVE_BASE_IMAGE="+basedigest.DefaultRef()) {
		t.Fatalf("build must inject COVE_BASE_IMAGE build-arg: %+v", f.Calls)
	}
	// The built ID must be tagged under wantRef so `FROM ${BASE}` can resolve it.
	tag := dockerCall(f.Calls, "tag")
	if tag == nil || !contains(tag, builtID) || !contains(tag, wantRef) {
		t.Fatalf("expected `docker tag %s %s`, calls: %+v", builtID, wantRef, f.Calls)
	}
}

// Install injects the resolved base as --build-arg BASE on the hardening build
// (the single build path — Create is run-only, COV-38).
func TestInstallInjectsBaseBuildArg(t *testing.T) {
	f := &runner.Fake{}
	if _, err := New(f).Install(backend.InstallContext{
		Kit: "box", BuildDir: "/b",
	}); err != nil {
		t.Fatal(err)
	}
	build := dockerCall(f.Calls, "build")
	if build == nil || !contains(build, "--build-arg") || !contains(build, "BASE="+basedigest.DefaultRef()) {
		t.Fatalf("build must inject BASE build-arg: %+v", f.Calls)
	}
}
