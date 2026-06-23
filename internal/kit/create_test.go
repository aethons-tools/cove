package kit

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aethons-tools/at-sbx/internal/runner"
)

func TestCreateBuildsBeforeRun(t *testing.T) {
	kitDir := t.TempDir()
	writeFile(t, filepath.Join(kitDir, "a.txt"), "${X}", 0o644)

	f := &runner.Fake{}
	err := Create("box", kitDir, []string{"/vol"}, f, env(map[string]string{"X": "y"}), Options{Stdout: &bytes.Buffer{}})
	if err != nil {
		t.Fatal(err)
	}

	if len(f.Calls) != 2 {
		t.Fatalf("got %d calls, want 2 (pack then run)", len(f.Calls))
	}
	if a := f.Calls[0].Args; len(a) < 2 || a[0] != "kit" || a[1] != "pack" {
		t.Fatalf("first call is not pack: %+v", f.Calls[0])
	}
	wantRun := []string{"run", "--name", "box", "--kit", ZipPath(kitDir), "claude", "/vol"}
	if !equalStrings(f.Calls[1].Args, wantRun) {
		t.Fatalf("run call = %v, want %v", f.Calls[1].Args, wantRun)
	}
}

func TestCreateDryRunPrintsBothCommandsNoExec(t *testing.T) {
	kitDir := t.TempDir()
	writeFile(t, filepath.Join(kitDir, "a.txt"), "hi", 0o644)

	f := &runner.Fake{}
	var out bytes.Buffer
	err := Create("box", kitDir, nil, f, env(nil), Options{DryRun: true, Stdout: &out})
	if err != nil {
		t.Fatal(err)
	}

	if len(f.Calls) != 0 {
		t.Fatalf("dry-run executed %d calls, want 0", len(f.Calls))
	}
	s := out.String()
	if !strings.Contains(s, "sbx kit pack") {
		t.Fatalf("missing pack command: %q", s)
	}
	if !strings.Contains(s, "sbx run --name box --kit") || !strings.Contains(s, "claude .") {
		t.Fatalf("missing run command with default volume: %q", s)
	}
}
