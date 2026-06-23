package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/aethons-tools/at-sbx/internal/runner"
)

func noEnv(string) (string, bool)        { return "", false }
func okPath(f string) (string, error)    { return "/usr/bin/" + f, nil }
func badPath(string) (string, error)     { return "", errors.New("not found") }

func TestRunNoArgsPrintsUsage(t *testing.T) {
	var out, errb bytes.Buffer
	code := run(nil, &runner.Fake{}, noEnv, okPath, &out, &errb)
	if code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
	if !strings.Contains(errb.String(), "Usage:") {
		t.Fatalf("missing usage: %q", errb.String())
	}
}

func TestRunUnknownCommand(t *testing.T) {
	var out, errb bytes.Buffer
	code := run([]string{"frobnicate"}, &runner.Fake{}, noEnv, okPath, &out, &errb)
	if code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
	if !strings.Contains(errb.String(), "unknown command") {
		t.Fatalf("missing error: %q", errb.String())
	}
}

func TestRunMissingSbxFails(t *testing.T) {
	var out, errb bytes.Buffer
	code := run([]string{"run", "box"}, &runner.Fake{}, noEnv, badPath, &out, &errb)
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if !strings.Contains(errb.String(), "sbx not found") {
		t.Fatalf("missing PATH error: %q", errb.String())
	}
}

func TestRunDeletePassesThrough(t *testing.T) {
	var out, errb bytes.Buffer
	f := &runner.Fake{}
	code := run([]string{"delete", "box"}, f, noEnv, okPath, &out, &errb)
	if code != 0 {
		t.Fatalf("code = %d, stderr=%q", code, errb.String())
	}
	if len(f.Calls) != 1 || !equalArgs(f.Calls[0].Args, []string{"remove", "box"}) {
		t.Fatalf("calls = %+v", f.Calls)
	}
}

func TestRunDeleteDryRunDoesNotExecute(t *testing.T) {
	var out, errb bytes.Buffer
	f := &runner.Fake{}
	code := run([]string{"delete", "box", "--dry-run"}, f, noEnv, okPath, &out, &errb)
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	if len(f.Calls) != 0 {
		t.Fatalf("dry-run executed: %+v", f.Calls)
	}
	if !strings.Contains(out.String(), "would run: sbx remove box") {
		t.Fatalf("missing dry-run output: %q", out.String())
	}
}

func TestRunDryRunFlagBeforeSubcommand(t *testing.T) {
	var out, errb bytes.Buffer
	f := &runner.Fake{}
	code := run([]string{"--dry-run", "run", "box"}, f, noEnv, badPath, &out, &errb)
	if code != 0 {
		t.Fatalf("code = %d, stderr=%q", code, errb.String())
	}
	// dry-run skips the PATH check (badPath) entirely.
	if len(f.Calls) != 0 {
		t.Fatalf("dry-run executed: %+v", f.Calls)
	}
	if !strings.Contains(out.String(), "would run: sbx run box") {
		t.Fatalf("missing output: %q", out.String())
	}
}

func TestRunPropagatesExitCode(t *testing.T) {
	var out, errb bytes.Buffer
	f := &runner.Fake{Err: &runner.ExitError{Code: 7}}
	code := run([]string{"run", "box"}, f, noEnv, okPath, &out, &errb)
	if code != 7 {
		t.Fatalf("code = %d, want 7", code)
	}
}

func TestRunCreateRequiresTwoArgs(t *testing.T) {
	var out, errb bytes.Buffer
	code := run([]string{"create", "onlyname"}, &runner.Fake{}, noEnv, okPath, &out, &errb)
	if code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
}

func equalArgs(a, b []string) bool {
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
