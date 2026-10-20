package assemble

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const testCAKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl jam-ssh-ca"

// sshCAEnv is a hermetic stage for install-ssh-ca.sh: a fake `id` on PATH
// reporting the uid in uidFile, and the CA file redirected into a temp dir.
type sshCAEnv struct{ bin, uidFile, caFile string }

func newSSHCAEnv(t *testing.T) sshCAEnv {
	t.Helper()
	requireBash(t)
	dir := t.TempDir()
	e := sshCAEnv{bin: filepath.Join(dir, "bin"), uidFile: filepath.Join(dir, "uid"), caFile: filepath.Join(dir, "jam_user_ca.pub")}
	if err := os.MkdirAll(e.bin, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(e.bin, "id"), "#!/bin/sh\ncat "+e.uidFile+"\n", 0o755)
	writeFile(t, e.uidFile, "0\n", 0o644)
	return e
}

// run feeds stdin to the helper; it returns the exit code and combined output.
func (e sshCAEnv) run(t *testing.T, stdin string) (int, string) {
	t.Helper()
	cmd := exec.Command("bash", "hardening/image-files/usr/local/lib/cove/install-ssh-ca.sh")
	cmd.Env = []string{"PATH=" + e.bin + ":/usr/bin:/bin", "COVE_SSH_USER_CA_FILE=" + e.caFile}
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), string(out)
	}
	if err != nil {
		t.Fatalf("run install-ssh-ca.sh: %v", err)
	}
	return 0, string(out)
}

// assertNoCAFile fails if the helper wrote the CA file.
func (e sshCAEnv) assertNoCAFile(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(e.caFile); !os.IsNotExist(err) {
		t.Errorf("CA file written (stat err %v)", err)
	}
}

// A valid ed25519 line is installed as the CA file, world-readable, one line.
func TestInstallSSHCA_Installs(t *testing.T) {
	e := newSSHCAEnv(t)
	if code, out := e.run(t, testCAKey+"\n"); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if got := read(t, e.caFile); got != testCAKey+"\n" {
		t.Fatalf("CA file = %q", got)
	}
	fi, err := os.Stat(e.caFile)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o644 {
		t.Errorf("CA file mode = %v, want 0644", fi.Mode().Perm())
	}
	if left, _ := filepath.Glob(e.caFile + ".*"); len(left) != 0 {
		t.Errorf("temp files left behind: %v", left)
	}
}

// Re-installing replaces the key; a missing trailing newline is fine.
func TestInstallSSHCA_Replaces(t *testing.T) {
	e := newSSHCAEnv(t)
	writeFile(t, e.caFile, "ssh-ed25519 AAAAold\n", 0o644)
	if code, out := e.run(t, testCAKey); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if got := read(t, e.caFile); got != testCAKey+"\n" {
		t.Fatalf("CA file = %q", got)
	}
}

// Anything but exactly one ssh-ed25519 line is refused (exit 2), writing nothing:
// a second line would smuggle in a second trusted CA.
func TestInstallSSHCA_RejectsBadInput(t *testing.T) {
	for name, in := range map[string]string{
		"empty":       "",
		"rsa":         "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQ jam\n",
		"garbage":     "hello\n",
		"two lines":   testCAKey + "\n" + testCAKey + "\n",
		"bad base64":  "ssh-ed25519 not*base64\n",
		"no key body": "ssh-ed25519 \n",
		"options":     `cert-authority,principals="root" ` + testCAKey + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			e := newSSHCAEnv(t)
			code, out := e.run(t, in)
			if code != 2 {
				t.Fatalf("exit %d, want 2:\n%s", code, out)
			}
			if !strings.Contains(out, "install-ssh-ca: ") {
				t.Errorf("no diagnostic:\n%s", out)
			}
			e.assertNoCAFile(t)
		})
	}
}

// Root-only: a non-root run fails and writes nothing.
func TestInstallSSHCA_NonRootRefused(t *testing.T) {
	e := newSSHCAEnv(t)
	writeFile(t, e.uidFile, "1000\n", 0o644)
	if code, out := e.run(t, testCAKey+"\n"); code == 0 {
		t.Fatalf("non-root run must fail:\n%s", out)
	}
	e.assertNoCAFile(t)
}
