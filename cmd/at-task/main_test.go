package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// jsonlRecords parses the unattended (JSON) stderr into decoded slog records.
func jsonlRecords(t *testing.T, s string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(s), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("stderr line is not JSON: %q (%v)", line, err)
		}
		out = append(out, m)
	}
	return out
}

// findByMsg returns the first record whose msg equals want, or fails.
func findByMsg(t *testing.T, recs []map[string]any, want string) map[string]any {
	t.Helper()
	for _, r := range recs {
		if r["msg"] == want {
			return r
		}
	}
	t.Fatalf("no record with msg %q among %v", want, recs)
	return nil
}

// TestPrepareEmitsStructuredStepAttr asserts prepare emits structured JSONL to
// stderr carrying step=prepare (spec §7): the failure path (no task.json) surfaces
// as one parseable slog error record that self-identifies its layer.
func TestPrepareEmitsStructuredStepAttr(t *testing.T) {
	t.Setenv("AT_LOG_MODE", "unattended")
	dir := t.TempDir() // no .at-task/task.json → ReadTask fails
	wd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(wd)

	var out, errOut bytes.Buffer
	if code := run([]string{"prepare"}, &out, &errOut); code != 1 {
		t.Fatalf("exit = %d; want 1\nstderr: %s", code, errOut.String())
	}
	rec := findByMsg(t, jsonlRecords(t, errOut.String()), "read task")
	if rec["step"] != "prepare" {
		t.Fatalf("step = %v; want prepare (full: %v)", rec["step"], rec)
	}
	if rec["level"] != "ERROR" {
		t.Fatalf("level = %v; want ERROR (full: %v)", rec["level"], rec)
	}
}

// TestCompleteEmitsStructuredStepAttr asserts complete emits structured JSONL to
// stderr carrying step=complete, even on the unreadable-task path (which still
// writes an error task-result).
func TestCompleteEmitsStructuredStepAttr(t *testing.T) {
	t.Setenv("AT_LOG_MODE", "unattended")
	dir := t.TempDir() // no .at-task/task.json
	wd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(wd)

	var out, errOut bytes.Buffer
	if code := run([]string{"complete"}, &out, &errOut); code != 0 {
		t.Fatalf("exit = %d; want 0 (a task-result was written)\nstderr: %s", code, errOut.String())
	}
	recs := jsonlRecords(t, errOut.String())
	if len(recs) == 0 {
		t.Fatalf("complete emitted no structured records; stderr: %q", errOut.String())
	}
	for _, r := range recs {
		if r["step"] != "complete" {
			t.Fatalf("every complete record must carry step=complete; got %v", r)
		}
	}
}

// TestAtTaskNeverLogsGitToken is the §10 secret-safety guard: a known resolved
// code-host token set in the environment must NEVER appear in any emitted record.
// Records are secret-free by construction (§6.2) and bypass the host's raw-line
// scrubber, so at-task itself must not let the token reach a record.
func TestAtTaskNeverLogsGitToken(t *testing.T) {
	t.Setenv("AT_LOG_MODE", "unattended")
	const token = "ghp-SUPER-SECRET-do-not-log-me"
	t.Setenv("AT_TASK_GIT_TOKEN", token)
	dir := t.TempDir()
	wd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(wd)

	for _, cmd := range []string{"prepare", "complete"} {
		var out, errOut bytes.Buffer
		run([]string{cmd}, &out, &errOut)
		if strings.Contains(errOut.String(), token) {
			t.Fatalf("SECRET LEAK: %s emitted the git token:\n%s", cmd, errOut.String())
		}
	}
}

// TestScrubErrRedactsGitToken proves the secret-free-by-construction boundary:
// scrubErr masks the code-host token from any error text before it enters a record.
func TestScrubErrRedactsGitToken(t *testing.T) {
	const token = "ghp-SUPER-SECRET-do-not-log-me"
	t.Setenv("AT_TASK_GIT_TOKEN", token)
	got := scrubErr(fmt.Errorf("git push failed: bearer %s rejected", token))
	if strings.Contains(got, token) {
		t.Fatalf("scrubErr leaked the token: %q", got)
	}
	if !strings.Contains(got, "«redacted»") {
		t.Fatalf("scrubErr did not mark the redaction: %q", got)
	}
}

func TestVersion(t *testing.T) {
	old := version
	defer func() { version = old }()
	version = "1.2.3"
	var out, errOut bytes.Buffer
	if code := run([]string{"version"}, &out, &errOut); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if strings.TrimSpace(out.String()) != "at-task 1.2.3" {
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
	dir := t.TempDir() // no .at-task/task.json
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
	data, err := os.ReadFile(filepath.Join(dir, ".at-task", "task-result.json"))
	if err != nil {
		t.Fatalf("task-result.json not written: %v", err)
	}
	if !strings.Contains(string(data), `"error"`) {
		t.Fatalf("expected an error status:\n%s", data)
	}
}
