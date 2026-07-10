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
// `at-cove work` on the reference kit and asserts a real PR was opened.
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
	in := filepath.Join(dir, "task.json")
	out := filepath.Join(dir, "task-result.json")

	// The target repo comes from the reference kit's origin (config.yml's
	// origin.github.project), not from the task — the maintainer sets that to
	// their scratch repo before `just e2e`. E2E_REPO above only gates the test.
	input := `{"issue":{"key":"DEMO-1","title":"Add a greeting helper"},` +
		`"worker":{"class":"implement"},` +
		`"task":{"brief":"Add Greet(name string) string returning \"Hello, <name>!\" with a test."}}`
	if err := os.WriteFile(in, []byte(input), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("at-cove", "work", "kits/reference-worker", "--in", in, "--out", out, "--timeout", "20m")
	cmd.Dir = repoRoot(t)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("at-cove work: %v", err)
	}

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read task-result.json: %v", err)
	}
	var res struct {
		Status struct {
			OK *struct {
				PRURL string `json:"pr-url"`
			} `json:"ok"`
		} `json:"status"`
	}
	if err := json.Unmarshal(data, &res); err != nil {
		t.Fatalf("parse task-result.json: %v\n%s", err, data)
	}
	if res.Status.OK == nil {
		t.Fatalf("status is not ok\n%s", data)
	}
	if res.Status.OK.PRURL == "" {
		t.Fatalf("no pr-url in task-result\n%s", data)
	}
	t.Logf("opened PR: %s", res.Status.OK.PRURL)
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
