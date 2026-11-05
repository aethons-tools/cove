package baseimage

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/legacynames"
)

// fakeDocker canned-answers Build and Layers by ref, and records what it built.
type fakeDocker struct {
	built     []string            // contextDirs passed to Build, in order
	buildID   string              // image ID Build returns
	layers    map[string][]string // ref → diff_ids
	buildErr  error
	layersErr map[string]error
	asked     []string // refs passed to Layers, in order
}

func (f *fakeDocker) Build(contextDir string) (string, error) {
	f.built = append(f.built, contextDir)
	if f.buildErr != nil {
		return "", f.buildErr
	}
	return f.buildID, nil
}

func (f *fakeDocker) Layers(ref string) ([]string, error) {
	f.asked = append(f.asked, ref)
	if f.layersErr != nil {
		if err := f.layersErr[ref]; err != nil {
			return nil, err
		}
	}
	return f.layers[ref], nil
}

const (
	blessedRef = "ghcr.io/aethons-tools/jam-studio-base@sha256:blessed"
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
	const tag = "ghcr.io/acme/jam-studio@sha256:child"
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
	// A kit Dockerfile → the launcher builds it; the built ID is the base.
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

// A rejection must explain itself: name the base and each blessed ref, and show
// the descends verdict — so "is the kit bad or the gate bad?" is answered on
// screen, not via a multi-command investigation.
func TestResolveRejectionDiagnoses(t *testing.T) {
	const tag = "ubuntu@sha256:evil"
	d := &fakeDocker{layers: map[string][]string{
		blessedRef: {"sha256:a", "sha256:b"},
		tag:        {"sha256:a", "sha256:x"}, // shares only the first layer, then diverges
	}}
	_, err := Resolve(d, Spec{Base: tag}, []string{blessedRef}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected rejection")
	}
	for _, want := range []string{tag, blessedRef, "descends from it: false", "layers"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("rejection must mention %q; got:\n%s", want, err.Error())
		}
	}
}

// A blessed ref whose inspect yields no layers (e.g. a multi-arch index digest
// with no .RootFS) must be called out distinctly — not silently read as "not
// descended". This is the exact ambiguity that made the real-world reject a
// mystery.
func TestResolveEmptyBlessedLayersSurfaced(t *testing.T) {
	const tag = "ghcr.io/acme/img@sha256:child"
	d := &fakeDocker{layers: map[string][]string{
		blessedRef: {},                       // inspect returned nothing
		tag:        {"sha256:a", "sha256:b"}, // a real base, unrelated to the (unreadable) blessed
	}}
	_, err := Resolve(d, Spec{Base: tag}, []string{blessedRef}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected rejection (no readable blessed layers)")
	}
	if !strings.Contains(err.Error(), "no layers") {
		t.Fatalf("must flag the empty blessed layers distinctly; got:\n%s", err.Error())
	}
}

func TestResolveAcceptsRecutBlessedBase(t *testing.T) {
	const kitBase = "ghcr.io/aethons-tools/jam-studio-base@sha256:older"
	d := &fakeDocker{layers: map[string][]string{
		blessedRef: {"sha256:a", "sha256:b"},
		kitBase:    {"sha256:a", "sha256:b"},
	}}
	if ref, err := Resolve(d, Spec{Base: kitBase}, []string{blessedRef}, &bytes.Buffer{}); err != nil || ref != kitBase {
		t.Fatalf("Resolve = %q, %v; a re-cut blessed base must pass", ref, err)
	}
}

func TestResolveRefusesThePreRenameRepo(t *testing.T) {
	old := legacynames.BaseImageRepo + "@sha256:blessed"
	d := &fakeDocker{layers: map[string][]string{blessedRef: {"sha256:a"}, old: {"sha256:a"}}}
	_, err := Resolve(d, Spec{Base: old}, []string{blessedRef}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "was renamed ghcr.io/aethons-tools/jam-studio-base") {
		t.Fatalf("err = %v; the old repo must be refused, naming the new one", err)
	}
}

func TestResolveRefusesThePreRenameRepoEvenUnverified(t *testing.T) {
	old := legacynames.BaseImageRepo + ":latest"
	d := &fakeDocker{layers: map[string][]string{blessedRef: {"sha256:a"}, old: {"sha256:a"}}}
	var warn bytes.Buffer
	if _, err := Resolve(d, Spec{Base: old, AllowUnverified: true}, []string{blessedRef}, &warn); err == nil {
		t.Fatal("--allow-unverified-base downgrades provenance failures only, not the rename refusal")
	}
}

// The gate stops at the first blessed base the image descends from: reading a
// blessed ref may pull it, so the rest are not read (nor pulled).
func TestResolveStopsAtFirstMatchingBlessedBase(t *testing.T) {
	const tag = "ghcr.io/acme/img@sha256:child"
	const older = "ghcr.io/aethons-tools/jam-studio-base@sha256:older"
	d := &fakeDocker{layers: map[string][]string{
		blessedRef: {"sha256:a", "sha256:b"},
		older:      {"sha256:o"},
		tag:        {"sha256:a", "sha256:b", "sha256:c"},
	}}
	if ref, err := Resolve(d, Spec{Base: tag}, []string{blessedRef, older}, &bytes.Buffer{}); err != nil || ref != tag {
		t.Fatalf("Resolve = %q, %v", ref, err)
	}
	if want := []string{tag, blessedRef}; !slices.Equal(d.asked, want) {
		t.Fatalf("Layers asked %v, want %v", d.asked, want)
	}
}

// embedded is a Spec.BlessedLayers lookup over a fixed ref → diff_ids table.
func embedded(m map[string][]string) func(string) ([]string, bool) {
	return func(ref string) ([]string, bool) {
		l, ok := m[ref]
		return l, ok
	}
}

// A blessed ref whose layers were embedded at build time is compared without
// docker: Layers is asked only for the resolved base, never the blessed ref.
func TestResolveUsesEmbeddedBlessedLayers(t *testing.T) {
	const tag = "ghcr.io/acme/img@sha256:child"
	d := &fakeDocker{layers: map[string][]string{
		tag: {"sha256:a", "sha256:b", "sha256:c"},
	}}
	s := Spec{Base: tag, BlessedLayers: embedded(map[string][]string{blessedRef: {"sha256:a", "sha256:b"}})}
	if ref, err := Resolve(d, s, []string{blessedRef}, &bytes.Buffer{}); err != nil || ref != tag {
		t.Fatalf("Resolve = %q, %v", ref, err)
	}
	if want := []string{tag}; !slices.Equal(d.asked, want) {
		t.Fatalf("Layers asked %v, want %v (embedded blessed layers need no docker)", d.asked, want)
	}
}

// A blessed ref with no embedded layers (watermark-only/offline build, the
// local base) falls back to docker; an embedded one still never reaches it.
func TestResolveFallsBackToDockerForUnembeddedBlessedRef(t *testing.T) {
	const tag = "ghcr.io/acme/img@sha256:child"
	const local = "jam-studio-base:dev-0123"
	d := &fakeDocker{layers: map[string][]string{
		local: {"sha256:l"},
		tag:   {"sha256:a", "sha256:b", "sha256:c"},
	}}
	s := Spec{Base: tag, BlessedLayers: embedded(map[string][]string{blessedRef: {"sha256:a", "sha256:b"}})}
	if ref, err := Resolve(d, s, []string{local, blessedRef}, &bytes.Buffer{}); err != nil || ref != tag {
		t.Fatalf("Resolve = %q, %v", ref, err)
	}
	if want := []string{tag, local}; !slices.Equal(d.asked, want) {
		t.Fatalf("Layers asked %v, want %v", d.asked, want)
	}
}

// The reject diagnostic still explains itself with embedded layers, and says
// where they came from.
func TestResolveRejectionDiagnosesEmbedded(t *testing.T) {
	const tag = "ubuntu@sha256:evil"
	d := &fakeDocker{layers: map[string][]string{tag: {"sha256:a", "sha256:x"}}}
	s := Spec{Base: tag, BlessedLayers: embedded(map[string][]string{blessedRef: {"sha256:a", "sha256:b"}})}
	_, err := Resolve(d, s, []string{blessedRef}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected rejection")
	}
	for _, want := range []string{tag, blessedRef, "embedded", "2 layers", "descends from it: false"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("rejection must mention %q; got:\n%s", want, err.Error())
		}
	}
	if want := []string{tag}; !slices.Equal(d.asked, want) {
		t.Fatalf("Layers asked %v, want %v", d.asked, want)
	}
}
