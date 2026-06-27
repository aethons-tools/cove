package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aethons-tools/at-sbx/internal/runner"
)

func writeKit(t *testing.T, dir string) {
	t.Helper()
	atsbx := filepath.Join(dir, ".atsbx")
	if err := os.MkdirAll(atsbx, 0o755); err != nil {
		t.Fatal(err)
	}
	yml := "name: box\nbackend: colima\n"
	if err := os.WriteFile(filepath.Join(atsbx, "config.yml"), []byte(yml), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestStatusDispatchesToBackend(t *testing.T) {
	dir := t.TempDir()
	writeKit(t, dir)
	f := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: "true\n"}}}
	var out, errOut bytes.Buffer
	code := run([]string{"status", filepath.Join(dir, ".atsbx")}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "running") {
		t.Fatalf("status output = %q", out.String())
	}
}

func TestUnknownBackendErrors(t *testing.T) {
	dir := t.TempDir()
	atsbx := filepath.Join(dir, ".atsbx")
	os.MkdirAll(atsbx, 0o755)
	os.WriteFile(filepath.Join(atsbx, "config.yml"), []byte("name: box\nbackend: bogus\n"), 0o644)
	var out, errOut bytes.Buffer
	code := run([]string{"status", atsbx}, &runner.Fake{}, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code == 0 || !strings.Contains(errOut.String(), "bogus") {
		t.Fatalf("expected unknown-backend error, code=%d stderr=%q", code, errOut.String())
	}
}

func TestDryRunCreatePrintsNoExec(t *testing.T) {
	dir := t.TempDir()
	writeKit(t, dir)
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	code := run([]string{"--dry-run", "create", filepath.Join(dir, ".atsbx")}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, errOut.String())
	}
	if len(f.Calls) != 0 {
		t.Fatalf("dry-run executed commands: %+v", f.Calls)
	}
	if !strings.Contains(out.String(), "would") {
		t.Fatalf("dry-run should describe planned actions: %q", out.String())
	}
}

func dummyLookPath(string) (string, error) { return "/usr/bin/x", nil }
