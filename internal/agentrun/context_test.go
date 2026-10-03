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

func TestWriteContextSwapsSymlink(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "context")
	// A pre-slice-4 plain directory is replaced.
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "stale.md"), []byte("x"), 0o644)
	b1 := sessionctx.Bundle{Core: "ONE", Files: map[string]string{"INDEX.md": "I"}, Fingerprint: "aaaaaaaaaaaa1111"}
	if err := writeContext(dir, b1); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(dir); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("context must be a symlink: %v %v", fi, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "stale.md")); !os.IsNotExist(err) {
		t.Fatal("the old plain directory's files must be gone")
	}
	held, _ := filepath.EvalSymlinks(dir) // a reader holding version 1
	b2 := sessionctx.Bundle{Core: "TWO", Files: map[string]string{"INDEX.md": "I2"}, Fingerprint: "bbbbbbbbbbbb2222"}
	if err := writeContext(dir, b2); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "CORE.md")); string(got) != "TWO" {
		t.Fatalf("CORE.md = %q", got)
	}
	if _, err := os.Stat(held); !os.IsNotExist(err) {
		t.Fatal("the superseded version dir must be removed after the swap")
	}
	entries, _ := os.ReadDir(filepath.Join(root, "context.d"))
	if len(entries) != 1 {
		t.Fatalf("exactly one version dir must remain, got %d", len(entries))
	}
	clearContext(dir)
	for _, p := range []string{dir, filepath.Join(root, "context.d")} {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Errorf("%s survives clearContext", p)
		}
	}
}

// The version dir name comes from Jam over the network: only hex is accepted,
// so a bad fingerprint can never escape context.d.
func TestWriteContextRefusesBadFingerprint(t *testing.T) {
	root := t.TempDir()
	keep := filepath.Join(root, "keep.txt")
	os.WriteFile(keep, []byte("x"), 0o644)
	for _, fp := range []string{"..", "../../x", "a/b", "ZZZZ"} {
		if err := writeContext(filepath.Join(root, "context"), sessionctx.Bundle{Core: "C", Fingerprint: fp}); err == nil {
			t.Errorf("fingerprint %q accepted", fp)
		}
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatal("a bad fingerprint deleted files outside context.d")
	}
}
