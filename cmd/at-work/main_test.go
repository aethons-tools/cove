package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVersion(t *testing.T) {
	old := version
	defer func() { version = old }()
	version = "1.2.3"
	var out, errOut bytes.Buffer
	if code := run([]string{"version"}, &out, &errOut); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if strings.TrimSpace(out.String()) != "at-work 1.2.3" {
		t.Fatalf("stdout = %q", out.String())
	}
}

func TestUnknownCommand(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"bogus"}, &out, &errOut); code != 2 {
		t.Fatalf("exit = %d; want 2", code)
	}
	if !strings.Contains(errOut.String(), "usage:") {
		t.Fatalf("stderr = %q", errOut.String())
	}
}

func TestPrepareRejectsExtraArgs(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"prepare", "x"}, &out, &errOut); code != 2 {
		t.Fatalf("exit = %d; want 2 (prepare takes no arguments)", code)
	}
}

func TestCompleteRejectsExtraArgs(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"complete", "x"}, &out, &errOut); code != 2 {
		t.Fatalf("exit = %d; want 2 (complete takes no arguments)", code)
	}
}

// TestCompleteWritesResultWhenTaskUnreadable exercises doComplete's cwd-relative file
// I/O, so it changes directory rather than passing a path; it must run sequentially
// (no t.Parallel()) since os.Chdir is process-global.
func TestCompleteWritesResultWhenTaskUnreadable(t *testing.T) {
	dir := t.TempDir() // no .at-work/task.json
	wd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(wd)

	var stdout, stderr bytes.Buffer
	code := run([]string{"complete"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d; want 0 (a task-result was written)\nstderr: %s", code, stderr.String())
	}
	data, err := os.ReadFile(filepath.Join(dir, ".at-work", "task-result.json"))
	if err != nil {
		t.Fatalf("task-result.json not written: %v", err)
	}
	if !strings.Contains(string(data), `"error"`) {
		t.Fatalf("expected an error status:\n%s", data)
	}
}
