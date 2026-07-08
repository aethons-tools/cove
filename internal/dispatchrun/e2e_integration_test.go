//go:build integration

package dispatchrun_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestE2EReferenceWorker runs the whole worker path against real infra: it shells
// `at-cove dispatch` on the reference kit and asserts a real PR was opened.
//
// Prerequisites (skipped without them): colima running, `gh auth` logged in, a
// seeded claude login (via a prior `at-cove connect` login), and a scratch repo.
// Set E2E_REPO=<org>/<repo> to enable. See kits/reference-worker/RUNBOOK.md.
func TestE2EReferenceWorker(t *testing.T) {
	repo := os.Getenv("E2E_REPO")
	if repo == "" {
		t.Skip("set E2E_REPO=<org>/<scratch-repo> to run the end-to-end dispatch")
	}
	dir := t.TempDir()
	in := filepath.Join(dir, "input.json")
	out := filepath.Join(dir, "output.json")

	input := `{"issue":{"key":"DEMO-1","title":"Add a greeting helper","work-class":"implement",` +
		`"brief":"Add Greet(name string) string returning \"Hello, <name>!\" with a test."},` +
		`"repo":{"name":"` + repo + `","source-branch":"main","work-branch":"implement/DEMO-1"}}`
	if err := os.WriteFile(in, []byte(input), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("at-cove", "dispatch", "kits/reference-worker", "--in", in, "--out", out, "--timeout", "20m")
	cmd.Dir = repoRoot(t)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("at-cove dispatch: %v", err)
	}

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read output.json: %v", err)
	}
	var res struct {
		Status string `json:"status"`
		Work   struct {
			PRURL string `json:"pr-url"`
		} `json:"work"`
	}
	if err := json.Unmarshal(data, &res); err != nil {
		t.Fatalf("parse output.json: %v\n%s", err, data)
	}
	if res.Status != "OK" {
		t.Fatalf("status = %q; want OK\n%s", res.Status, data)
	}
	if res.Work.PRURL == "" {
		t.Fatalf("no PR url in output\n%s", data)
	}
	t.Logf("opened PR: %s", res.Work.PRURL)
}

// repoRoot returns the module root (two levels up from this package).
func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(filepath.Join(wd, "..", ".."))
}
