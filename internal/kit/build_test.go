package kit

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aethons-tools/at-sbx/internal/runner"
)

func writeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func assertFile(t *testing.T, path, wantContent string, wantMode os.FileMode) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(data) != wantContent {
		t.Fatalf("%s content = %q, want %q", path, data, wantContent)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != wantMode {
		t.Fatalf("%s mode = %v, want %v", path, info.Mode().Perm(), wantMode)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestStageWritesContentStructureAndMode(t *testing.T) {
	kitDir := t.TempDir()
	writeFile(t, filepath.Join(kitDir, "config.txt"), "user=${USER}\n", 0o600)
	writeFile(t, filepath.Join(kitDir, "bin", "run.sh"), "echo $USER", 0o755)
	writeFile(t, filepath.Join(kitDir, ".hidden"), "tok=$TOKEN", 0o644)
	writeFile(t, filepath.Join(kitDir, ".build", "kit.zip"), "junk", 0o644)

	n, err := Stage(kitDir, env(map[string]string{"USER": "alice", "TOKEN": "s3cret"}))
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("staged %d files, want 3 (.build excluded)", n)
	}

	staging := StagingDir(kitDir)
	assertFile(t, filepath.Join(staging, "config.txt"), "user=alice\n", 0o600)
	assertFile(t, filepath.Join(staging, "bin", "run.sh"), "echo alice", 0o755)
	assertFile(t, filepath.Join(staging, ".hidden"), "tok=s3cret", 0o644)

	if _, err := os.Stat(filepath.Join(staging, ".build")); !os.IsNotExist(err) {
		t.Fatalf(".build subtree leaked into staging")
	}
}

func TestBuildPacksThenRemovesStaging(t *testing.T) {
	kitDir := t.TempDir()
	writeFile(t, filepath.Join(kitDir, "a.txt"), "${X}", 0o644)

	f := &runner.Fake{}
	if err := Build(kitDir, f, env(map[string]string{"X": "y"}), Options{Stdout: &bytes.Buffer{}}); err != nil {
		t.Fatal(err)
	}

	if len(f.Calls) != 1 {
		t.Fatalf("got %d runner calls, want 1", len(f.Calls))
	}
	want := []string{"kit", "pack", StagingDir(kitDir), "-o", ZipPath(kitDir)}
	if c := f.Calls[0]; c.Name != "sbx" || !equalStrings(c.Args, want) {
		t.Fatalf("pack call = %+v, want sbx %v", c, want)
	}
	if _, err := os.Stat(StagingDir(kitDir)); !os.IsNotExist(err) {
		t.Fatalf("staging dir not cleaned up after pack")
	}
}

func TestBuildDryRunPrintsAndDoesNotExecute(t *testing.T) {
	kitDir := t.TempDir()
	writeFile(t, filepath.Join(kitDir, "a.txt"), "hi", 0o644)
	writeFile(t, filepath.Join(kitDir, "b.txt"), "ho", 0o644)

	f := &runner.Fake{}
	var out bytes.Buffer
	if err := Build(kitDir, f, env(nil), Options{DryRun: true, Stdout: &out}); err != nil {
		t.Fatal(err)
	}

	if len(f.Calls) != 0 {
		t.Fatalf("dry-run executed %d calls, want 0", len(f.Calls))
	}
	s := out.String()
	if !strings.Contains(s, "would template 2 files") {
		t.Fatalf("missing file count in output: %q", s)
	}
	if !strings.Contains(s, "sbx kit pack") {
		t.Fatalf("missing pack command in output: %q", s)
	}
	if _, err := os.Stat(StagingDir(kitDir)); !os.IsNotExist(err) {
		t.Fatalf("dry-run created staging dir")
	}
}

func TestBuildRejectsMissingKitDir(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope")
	err := Build(missing, &runner.Fake{}, env(nil), Options{Stdout: &bytes.Buffer{}})
	if err == nil {
		t.Fatal("expected error for missing kitdir")
	}
}
