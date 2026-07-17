package baseimage

import (
	"bytes"
	"strings"
	"testing"
)

// fakeDocker canned-answers Build and Layers by ref, and records what it built.
type fakeDocker struct {
	built     []string            // contextDirs passed to Build, in order
	buildID   string              // image ID Build returns
	layers    map[string][]string // ref → diff_ids
	buildErr  error
	layersErr map[string]error
}

func (f *fakeDocker) Build(contextDir string) (string, error) {
	f.built = append(f.built, contextDir)
	if f.buildErr != nil {
		return "", f.buildErr
	}
	return f.buildID, nil
}

func (f *fakeDocker) Layers(ref string) ([]string, error) {
	if f.layersErr != nil {
		if err := f.layersErr[ref]; err != nil {
			return nil, err
		}
	}
	return f.layers[ref], nil
}

const (
	blessedRef = "ghcr.io/aethons-tools/cove-base-image@sha256:blessed"
	defaultRef = blessedRef
)

func TestResolveDefault(t *testing.T) {
	// No Dockerfile, no base → the default blessed ref, which trivially verifies.
	d := &fakeDocker{layers: map[string][]string{
		blessedRef: {"sha256:a", "sha256:b"},
	}}
	var warn bytes.Buffer
	ref, err := Resolve(d, Spec{DefaultRef: defaultRef}, []string{blessedRef}, &warn)
	if err != nil {
		t.Fatal(err)
	}
	if ref != defaultRef {
		t.Fatalf("ref = %q, want %q", ref, defaultRef)
	}
	if len(d.built) != 0 {
		t.Fatalf("default path must not docker build, built: %v", d.built)
	}
	if warn.Len() != 0 {
		t.Fatalf("verified base must not warn, got: %q", warn.String())
	}
}

func TestResolveBaseTag(t *testing.T) {
	// image.base names a descendant of the blessed image → used verbatim, verifies.
	const tag = "ghcr.io/acme/cove-image@sha256:child"
	d := &fakeDocker{layers: map[string][]string{
		blessedRef: {"sha256:a", "sha256:b"},
		tag:        {"sha256:a", "sha256:b", "sha256:c"}, // built FROM the blessed base
	}}
	ref, err := Resolve(d, Spec{Base: tag}, []string{blessedRef}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if ref != tag {
		t.Fatalf("ref = %q, want %q", ref, tag)
	}
	if len(d.built) != 0 {
		t.Fatalf("base-tag path must not docker build, built: %v", d.built)
	}
}

func TestResolveDockerfile(t *testing.T) {
	// A kit image/Dockerfile → at-cove builds it; the built ID is the base.
	const builtID = "sha256:builtlocal"
	d := &fakeDocker{
		buildID: builtID,
		layers: map[string][]string{
			blessedRef: {"sha256:a", "sha256:b"},
			builtID:    {"sha256:a", "sha256:b", "sha256:kit"}, // FROM a blessed descendant
		},
	}
	ref, err := Resolve(d, Spec{DockerfileDir: "/kit/image"}, []string{blessedRef}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if ref != builtID {
		t.Fatalf("ref = %q, want the built image ID %q", ref, builtID)
	}
	if len(d.built) != 1 || d.built[0] != "/kit/image" {
		t.Fatalf("expected one build of /kit/image, got %v", d.built)
	}
}

func TestResolveRejectsUnverifiedBase(t *testing.T) {
	// image.base that does NOT descend from any blessed image → rejected.
	const tag = "ubuntu@sha256:evil"
	d := &fakeDocker{layers: map[string][]string{
		blessedRef: {"sha256:a", "sha256:b"},
		tag:        {"sha256:x", "sha256:y"}, // unrelated
	}}
	var warn bytes.Buffer
	_, err := Resolve(d, Spec{Base: tag}, []string{blessedRef}, &warn)
	if err == nil {
		t.Fatal("an unverified base must be rejected")
	}
	if !strings.Contains(err.Error(), tag) {
		t.Fatalf("rejection should name the unverified ref, got: %v", err)
	}
	if warn.Len() != 0 {
		t.Fatalf("a hard rejection must not also warn, got: %q", warn.String())
	}
}

func TestResolveAllowUnverifiedWarnsAndProceeds(t *testing.T) {
	const tag = "ubuntu@sha256:evil"
	d := &fakeDocker{layers: map[string][]string{
		blessedRef: {"sha256:a", "sha256:b"},
		tag:        {"sha256:x", "sha256:y"},
	}}
	var warn bytes.Buffer
	ref, err := Resolve(d, Spec{Base: tag, AllowUnverified: true}, []string{blessedRef}, &warn)
	if err != nil {
		t.Fatalf("--allow-unverified-base must downgrade the rejection, got: %v", err)
	}
	if ref != tag {
		t.Fatalf("ref = %q, want %q", ref, tag)
	}
	if !strings.Contains(warn.String(), tag) {
		t.Fatalf("the warning must name the unverified ref, got: %q", warn.String())
	}
}
