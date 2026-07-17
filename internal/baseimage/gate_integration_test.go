//go:build integration

// Real-docker proof of the provenance gate's core claim: an image built FROM
// another carries the parent's OCI rootfs diff_ids as an exact prefix, so the
// pure DescendsFrom prefix test matches OCI reality.
//
// Run with: go test -tags integration ./internal/baseimage/ -run TestProvenance -v
// (needs a working docker daemon and network to pull alpine).
package baseimage_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/baseimage"
)

func TestProvenanceMatchesRealOCILayers(t *testing.T) {
	dir := t.TempDir()
	// base: alpine + one layer. child: FROM base + one more. unrelated: alpine only.
	base := buildImage(t, dir, "cove-prov-base", "FROM alpine:3.20\nRUN echo base > /base\n")
	child := buildImage(t, dir, "cove-prov-child", "FROM cove-prov-base\nRUN echo child > /child\n")
	unrelated := buildImage(t, dir, "cove-prov-unrelated", "FROM alpine:3.20\n")
	t.Cleanup(func() {
		for _, tag := range []string{"cove-prov-child", "cove-prov-base", "cove-prov-unrelated"} {
			_ = exec.Command("docker", "rmi", "-f", tag).Run()
		}
	})

	if !baseimage.DescendsFrom(child, base) {
		t.Fatalf("child (%v) must descend from base (%v)", child, base)
	}
	if baseimage.DescendsFrom(unrelated, base) {
		t.Fatalf("unrelated (%v) must NOT descend from base (%v)", unrelated, base)
	}
	if baseimage.DescendsFrom(base, child) {
		t.Fatal("base must not descend from its own child (parent longer than child)")
	}
}

func buildImage(t *testing.T, dir, tag, dockerfile string) []string {
	t.Helper()
	df := filepath.Join(dir, tag+".Dockerfile")
	if err := os.WriteFile(df, []byte(dockerfile), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("docker", "build", "-q", "-t", tag, "-f", df, dir).CombinedOutput(); err != nil {
		t.Fatalf("docker build %s: %v\n%s", tag, err, out)
	}
	out, err := exec.Command("docker", "inspect", "--format", "{{json .RootFS.Layers}}", tag).Output()
	if err != nil {
		t.Fatalf("docker inspect %s: %v", tag, err)
	}
	layers, err := baseimage.ParseLayers(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatal(err)
	}
	return layers
}
