package agentrun

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/aethons-tools/cove/internal/jam/sessionctx"
)

func TestWriteContextReplacesWholesale(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "context")
	if err := os.MkdirAll(filepath.Join(dir, "old"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "old", "stale.md"), []byte("stale"), 0o644)
	b := sessionctx.Bundle{Core: "CORE", Files: map[string]string{"INDEX.md": "I", "kit/tools.md": "T"}}
	if err := writeContext(dir, b); err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]string{"CORE.md": "CORE", "INDEX.md": "I", "kit/tools.md": "T"} {
		got, err := os.ReadFile(filepath.Join(dir, p))
		if err != nil || string(got) != want {
			t.Errorf("%s: got %q, %v", p, got, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "old")); !os.IsNotExist(err) {
		t.Fatal("stale files from a previous bundle must be gone")
	}
	if _, err := os.Stat(dir + ".new"); !os.IsNotExist(err) {
		t.Fatal("staging dir must not linger")
	}
}

func TestWriteContextRefusesNonLocalPaths(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "context")
	for _, bad := range []string{"../escape.md", "/abs.md", "a/../../x.md", ""} {
		if err := writeContext(dir, sessionctx.Bundle{Core: "C", Files: map[string]string{bad: "x"}}); err == nil {
			t.Errorf("%q: want an error", bad)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "escape.md")); !os.IsNotExist(err) {
		t.Fatal("nothing may be written outside the dir")
	}
}
