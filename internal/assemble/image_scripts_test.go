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

func TestRunSetupScriptRunsInOrderInPlace(t *testing.T) {
	requireBash(t)
	dir := t.TempDir()
	// Two scripts in different dirs; each appends its name and cwd to a shared log.
	log := filepath.Join(dir, "log")
	aDir := filepath.Join(dir, "a")
	bDir := filepath.Join(dir, "b")
	mustWrite(t, filepath.Join(aDir, "one.sh"), "printf 'one %s\\n' \"$(pwd)\" >> '"+log+"'\n")
	mustWrite(t, filepath.Join(bDir, "two.sh"), "printf 'two %s\\n' \"$(pwd)\" >> '"+log+"'\n")
	manifest := filepath.Join(dir, "manifest")
	mustWrite(t, manifest, filepath.Join(aDir, "one.sh")+"\n"+filepath.Join(bDir, "two.sh")+"\n")

	cmd := exec.Command("bash", "hardening/image-files/usr/local/lib/cove/run-setup.sh")
	cmd.Env = append(os.Environ(), "COVE_SETUP_MANIFEST="+manifest)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("run-setup.sh failed: %v\n%s", err, out)
	}
	got := read(t, log)
	want := "one " + aDir + "\ntwo " + bDir + "\n"
	if got != want {
		t.Fatalf("setup ran wrong order/cwd:\n got %q\nwant %q", got, want)
	}
}

func TestApplyImageEnvMergesPathAndAppendsEnv(t *testing.T) {
	requireBash(t)
	dir := t.TempDir()
	coveDir := filepath.Join(dir, "cove")
	mustWrite(t, filepath.Join(coveDir, "paths"), "/usr/local/go/bin\n/home/agent/go/bin\n")
	mustWrite(t, filepath.Join(coveDir, "env"), "GOROOT=/usr/local/go\n")
	envFile := filepath.Join(dir, "environment")
	mustWrite(t, envFile, "PATH=/usr/local/bin:/usr/bin:/bin\nCLAUDE_CONFIG_DIR=/agent-data\n")

	cmd := exec.Command("bash", "hardening/image-files/usr/local/lib/cove/apply-image-env.sh")
	cmd.Env = append(os.Environ(), "COVE_DIR="+coveDir, "COVE_ENV_FILE="+envFile)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("apply-image-env.sh failed: %v\n%s", err, out)
	}
	got := read(t, envFile)

	// Exactly one PATH= line, with base preserved and kit segments appended.
	if n := strings.Count(got, "\nPATH=") + boolToInt(strings.HasPrefix(got, "PATH=")); n != 1 {
		t.Fatalf("must remain a single PATH= line, got %d in:\n%s", n, got)
	}
	if !strings.Contains(got, "PATH=/usr/local/bin:/usr/bin:/bin:/usr/local/go/bin:/home/agent/go/bin") {
		t.Fatalf("PATH not merged correctly:\n%s", got)
	}
	if !strings.Contains(got, "\nGOROOT=/usr/local/go\n") {
		t.Fatalf("env var not appended:\n%s", got)
	}
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
