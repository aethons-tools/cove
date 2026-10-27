package colima

import (
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/backend"
	"github.com/aethons-tools/cove/internal/runner"
)

// The CA install execs the sealed install-ssh-ca.sh as root, context-pinned,
// with the public key on stdin — never on argv.
func TestInstallSSHUserCAExecsHelperWithStdin(t *testing.T) {
	f := &runner.Fake{}
	c := NewWithContext(f, "")
	key := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl\n"

	var ca backend.SSHCA = c
	if err := ca.InstallSSHUserCA("sbx-1", []byte(key)); err != nil {
		t.Fatalf("InstallSSHUserCA: %v", err)
	}
	call := f.Calls[len(f.Calls)-1]
	got := strings.Join(call.Args, " ")
	if call.Name != "docker" || !strings.Contains(got, "--context colima") ||
		!strings.HasSuffix(got, "exec -i -u root sbx-1 /usr/local/lib/jam/install-ssh-ca.sh") {
		t.Fatalf("exec = %s %s; want docker --context colima exec -i -u root sbx-1 /usr/local/lib/jam/install-ssh-ca.sh", call.Name, got)
	}
	if strings.Contains(got, "AAAAC3") {
		t.Fatalf("key leaked onto argv: %s", got)
	}
	if call.Stdin != key {
		t.Fatalf("stdin = %q; want the key", call.Stdin)
	}
}

// The helper's rejection message lands in the error.
func TestInstallSSHUserCAErrorCarriesHelperOutput(t *testing.T) {
	r := stderrRunner{Fake: &runner.Fake{}, stderr: "install-ssh-ca: stdin must be one ssh-ed25519 public key line\n"}
	c := NewWithContext(r, "")
	err := c.InstallSSHUserCA("sbx-1", []byte("ssh-rsa AAAA\n"))
	if err == nil || !strings.Contains(err.Error(), "install-ssh-ca: stdin must be one ssh-ed25519 public key line") {
		t.Fatalf("err = %v; want the helper's message", err)
	}
}
