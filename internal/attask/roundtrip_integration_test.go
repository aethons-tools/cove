//go:build integration

package attask

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// TestEmbeddedAtTaskRuns proves the embed round-trip end to end: the embedded
// linux at-task for the host arch extracts and actually executes. Requires
// scripts/build.sh to have staged the binaries, and a linux host whose arch is
// one of the embedded ones. Behind the `integration` build tag because the
// hermetic gate checkout stages no binaries.
func TestEmbeddedAtTaskRuns(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skipf("embedded at-task is a linux binary; host GOOS=%s", runtime.GOOS)
	}
	b, err := Binary(runtime.GOARCH)
	if err != nil {
		t.Skipf("at-task not staged (run scripts/build.sh): %v", err)
	}
	path := filepath.Join(t.TempDir(), "at-task")
	if err := os.WriteFile(path, b, 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(path, "version").CombinedOutput()
	if err != nil {
		t.Fatalf("the embedded at-task failed to run: %v\n%s", err, out)
	}
	if len(out) == 0 {
		t.Fatal("the embedded at-task produced no output")
	}
	t.Logf("embedded at-task (linux/%s) ran: %s", runtime.GOARCH, out)
}
