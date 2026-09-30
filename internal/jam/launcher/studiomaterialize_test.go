package launcher

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/aethons-tools/cove/internal/studio"
)

func TestMaterializeDockerfileBaseWritesTree(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "base")
	base := studio.Base{
		Dockerfile: "FROM ${COVE_BASE_IMAGE}",
		Context:    map[string]string{"a.txt": "A", "nested/b.txt": "B"},
	}
	if err := materializeDockerfileBase(dir, base); err != nil {
		t.Fatalf("materialize: %v", err)
	}
	for path, want := range map[string]string{
		"Dockerfile":   "FROM ${COVE_BASE_IMAGE}",
		"a.txt":        "A",
		"nested/b.txt": "B",
	} {
		b, err := os.ReadFile(filepath.Join(dir, path))
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if string(b) != want {
			t.Fatalf("%s = %q, want %q", path, b, want)
		}
	}
}

// A context path that escapes the build dir (absolute or via "..") is rejected,
// so an authored kit can never write outside its build context.
func TestMaterializeDockerfileBaseRejectsUnsafePaths(t *testing.T) {
	for _, bad := range []string{"../evil", "../../etc/passwd", "/abs/evil", "Dockerfile"} {
		dir := filepath.Join(t.TempDir(), "base")
		err := materializeDockerfileBase(dir, studio.Base{
			Dockerfile: "FROM scratch",
			Context:    map[string]string{bad: "x"},
		})
		if err == nil {
			t.Fatalf("context path %q must be rejected", bad)
		}
		// The escaping write must not have happened.
		if bad == "../evil" {
			if _, statErr := os.Stat(filepath.Join(filepath.Dir(dir), "evil")); statErr == nil {
				t.Fatalf("unsafe path %q escaped the build dir", bad)
			}
		}
	}
}
