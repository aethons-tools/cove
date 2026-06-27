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
	for _, want := range []string{".build/", ".local/", ".state/"} {
		if !strings.Contains(string(b), want) {
			t.Errorf(".gitignore missing %q; got:\n%s", want, b)
		}
	}
}

func TestEnsureGitignoreAppendsMissingPreservingCustom(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".gitignore")
	// A pre-existing file that already ignores .build/ and .local/ but not .state/.
	if err := os.WriteFile(p, []byte("custom\n.build/\n.local/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := EnsureGitignore(dir); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	got := string(b)
	if !strings.Contains(got, "custom") {
		t.Errorf("dropped the owner's content:\n%s", got)
	}
	if !strings.Contains(got, ".state/") {
		t.Errorf("did not append missing .state/:\n%s", got)
	}
	// Already-present entries are not duplicated.
	if strings.Count(got, ".build/") != 1 {
		t.Errorf(".build/ duplicated:\n%s", got)
	}
}

func TestEnsureGitignoreIdempotent(t *testing.T) {
	dir := t.TempDir()
	if err := EnsureGitignore(dir); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err := EnsureGitignore(dir); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if string(first) != string(second) {
		t.Fatalf("second run changed the file:\n%s\n---\n%s", first, second)
	}
}
