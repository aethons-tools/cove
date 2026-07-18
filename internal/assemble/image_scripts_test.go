package assemble

import (
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

// apply-sshenv.sh bridges the image's Docker ENV into /etc/environment (pam_env →
// SSH sessions): PATH is intrinsic (with ~/.local/bin prepended); the vars named
// in COVE_SSHENV transfer their live values; then the sealed runtime env
// (CLAUDE_CONFIG_DIR + egress proxy) is written last so it always wins.
func TestApplySshEnv(t *testing.T) {
	requireBash(t)
	dir := t.TempDir()
	envFile := filepath.Join(dir, "environment")

	cmd := exec.Command("bash", "hardening/image-files/usr/local/lib/cove/apply-sshenv.sh")
	cmd.Env = []string{
		"PATH=/usr/local/go/bin:/usr/bin:/bin", // the image's live PATH (go already on it)
		"COVE_SSHENV=GOROOT:GOPATH",
		"GOROOT=/usr/local/go",
		"GOPATH=/home/agent/go",
		"FOO=nope", // set but NOT named in COVE_SSHENV → must not transfer
		"COVE_ENV_FILE=" + envFile,
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("apply-sshenv.sh failed: %v\n%s", err, out)
	}
	got := read(t, envFile)

	// PATH intrinsic: ~/.local/bin prepended to the image's live PATH (go included).
	if !strings.Contains(got, "PATH=/home/agent/.local/bin:/usr/local/go/bin:/usr/bin:/bin\n") {
		t.Fatalf("PATH must prepend ~/.local/bin to the live PATH; got:\n%s", got)
	}
	// COVE_SSHENV-named vars transfer with their live values.
	if !strings.Contains(got, "GOROOT=/usr/local/go\n") || !strings.Contains(got, "GOPATH=/home/agent/go\n") {
		t.Fatalf("COVE_SSHENV vars must transfer; got:\n%s", got)
	}
	// A var not named in COVE_SSHENV must NOT leak in.
	if strings.Contains(got, "FOO=") {
		t.Fatalf("only COVE_SSHENV-named vars transfer; got:\n%s", got)
	}
	// Sealed runtime env is written (last, so it wins) and is never image-settable.
	for _, want := range []string{"CLAUDE_CONFIG_DIR=/agent-data\n", "http_proxy=http://127.0.0.1:3128\n", "NO_PROXY=localhost,127.0.0.1,::1,.internal\n"} {
		if !strings.Contains(got, want) {
			t.Fatalf("sealed env must contain %q; got:\n%s", want, got)
		}
	}
}
