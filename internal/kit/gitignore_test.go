package kit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureGitignoreCreates(t *testing.T) {
	dir := t.TempDir()
	if err := EnsureGitignore(dir); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatalf(".gitignore not written: %v", err)
	}
	for _, want := range []string{".build/", ".local/"} {
		if !strings.Contains(string(b), want) {
			t.Errorf(".gitignore missing %q; got:\n%s", want, b)
		}
	}
}

func TestEnsureGitignorePreservesExisting(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".gitignore")
	if err := os.WriteFile(p, []byte("custom\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := EnsureGitignore(dir); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if string(b) != "custom\n" {
		t.Fatalf("clobbered an existing .gitignore: %q", b)
	}
}
