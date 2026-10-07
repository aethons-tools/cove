package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/runner"
)

// withUpdateRunner swaps the Runner `at-jam update` drives install.sh through.
func withUpdateRunner(t *testing.T, f *runner.Fake) {
	t.Helper()
	prev := updateRunner
	updateRunner = f
	t.Cleanup(func() { updateRunner = prev })
}

// TestUpdatePinnedRunsEmbeddedInstallScript: a pinned `at-jam update --version`
// drives the embedded install.sh (`bash <temp script>`) with COVE_VERSION pinned.
func TestUpdatePinnedRunsEmbeddedInstallScript(t *testing.T) {
	f := &runner.Fake{}
	withUpdateRunner(t, f)
	var out, errOut bytes.Buffer
	code := run([]string{"update", "--version", "9-0909"}, func(string) string { return "" }, &out, &errOut)
	if code != 0 {
		t.Fatalf("update exit=%d stderr=%s", code, errOut.String())
	}
	if len(f.Calls) != 1 || f.Calls[0].Name != "bash" || len(f.Calls[0].Args) != 1 {
		t.Fatalf("update must run `bash <script>` once; calls=%+v", f.Calls)
	}
	if !strings.Contains(strings.Join(f.Calls[0].Env, " "), "COVE_VERSION=9-0909") {
		t.Fatalf("update must pin COVE_VERSION; env=%v", f.Calls[0].Env)
	}
	if !strings.Contains(out.String(), "updating at-jam from "+version+" to 9-0909") {
		t.Fatalf("stdout=%q", out.String())
	}
}

// TestUpdateHonorsCoveVersionEnv: with no flag, COVE_VERSION (read via getenv)
// pins the release — the same knob install.sh and `at-cove update` honor.
func TestUpdateHonorsCoveVersionEnv(t *testing.T) {
	f := &runner.Fake{}
	withUpdateRunner(t, f)
	getenv := func(k string) string {
		if k == "COVE_VERSION" {
			return "7-0707"
		}
		return ""
	}
	var out, errOut bytes.Buffer
	if code := run([]string{"update"}, getenv, &out, &errOut); code != 0 {
		t.Fatalf("update exit=%d stderr=%s", code, errOut.String())
	}
	if len(f.Calls) != 1 || !strings.Contains(strings.Join(f.Calls[0].Env, " "), "COVE_VERSION=7-0707") {
		t.Fatalf("COVE_VERSION must pin without resolving latest; calls=%+v", f.Calls)
	}
}

// TestUpdateDryRunTouchesNothing: --dry-run resolves and replaces nothing.
func TestUpdateDryRunTouchesNothing(t *testing.T) {
	f := &runner.Fake{}
	withUpdateRunner(t, f)
	var out, errOut bytes.Buffer
	code := run([]string{"--dry-run", "update"}, func(string) string { return "" }, &out, &errOut)
	if code != 0 {
		t.Fatalf("update dry-run exit=%d stderr=%s", code, errOut.String())
	}
	if len(f.Calls) != 0 {
		t.Fatalf("dry-run must touch nothing; calls=%+v", f.Calls)
	}
	if !strings.Contains(out.String(), "would update at-jam") {
		t.Fatalf("stdout=%q", out.String())
	}
}

// TestUpdateRejectsPositionals: update takes flags only.
func TestUpdateRejectsPositionals(t *testing.T) {
	withUpdateRunner(t, &runner.Fake{})
	var out, errOut bytes.Buffer
	if code := run([]string{"update", "extra"}, func(string) string { return "" }, &out, &errOut); code != 2 {
		t.Fatalf("positional must be a usage error (2); got %d", code)
	}
}
