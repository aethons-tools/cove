package assemble

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func requireBash(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
}

func TestApplyEnvDFoldsFragmentsInOrder(t *testing.T) {
	requireBash(t)
	dir := t.TempDir()
	envD := filepath.Join(dir, "env.d")
	// 00-base first (with a comment + blank line to skip), then 50-kit.
	mustWrite(t, filepath.Join(envD, "00-base.env"), "# base\n\nPATH=/usr/local/bin:/usr/bin\nCLAUDE_CONFIG_DIR=/agent-data\n")
	mustWrite(t, filepath.Join(envD, "50-kit.env"), "GOROOT=/usr/local/go\n")
	envFile := filepath.Join(dir, "environment")
	mustWrite(t, envFile, "")

	cmd := exec.Command("bash", "hardening/image-files/usr/local/lib/cove/apply-env-d.sh")
	cmd.Env = append(os.Environ(), "COVE_ENV_D="+envD, "COVE_ENV_FILE="+envFile)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("apply-env-d.sh failed: %v\n%s", err, out)
	}
	got := read(t, envFile)

	want := "PATH=/usr/local/bin:/usr/bin\nCLAUDE_CONFIG_DIR=/agent-data\nGOROOT=/usr/local/go\n"
	if got != want {
		t.Fatalf("fold mismatch:\n got: %q\nwant: %q", got, want)
	}
	if strings.Contains(got, "# base") {
		t.Fatalf("comments must be skipped:\n%s", got)
	}
}
