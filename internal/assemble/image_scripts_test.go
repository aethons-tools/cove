package assemble

import (
	"os"
	"os/exec"
	"path/filepath"
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
