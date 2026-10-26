//go:build integration

// Integration tests that drive the real ssh client against a throwaway sshd on
// loopback — no Docker, no network egress, no VM. They validate what the
// hermetic argv tests cannot: that Base actually drives OpenSSH and that
// known_hosts TOFU (accept-new) pins the host key. Moved here from
// internal/connect when at-cove's connect flow was deleted (jam-only slice 2).
//
// Run with:
//
//	go test -tags integration ./internal/sshargs/ -v
//
// Requires `ssh`, `sshd`, and `ssh-keygen` on PATH; tests Skip if absent or if
// a non-root sshd cannot start in this environment.
package sshargs

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// startSSHD boots an unprivileged sshd on a free loopback port configured for
// key-only auth and returns a Target pointing the ssh client at it.
func startSSHD(t *testing.T) Target {
	t.Helper()
	sshd, err := exec.LookPath("sshd")
	if err != nil {
		sshd = "/usr/sbin/sshd"
	}
	if _, err := os.Stat(sshd); err != nil {
		t.Skipf("sshd not available: %v", err)
	}
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skipf("ssh client not available: %v", err)
	}
	dir := t.TempDir()
	genKey(t, filepath.Join(dir, "hostkey"))
	genKey(t, filepath.Join(dir, "clientkey"))
	pub, err := os.ReadFile(filepath.Join(dir, "clientkey.pub"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "authorized_keys"), pub, 0o600); err != nil {
		t.Fatal(err)
	}
	u, err := user.Current()
	if err != nil {
		t.Skipf("cannot resolve current user: %v", err)
	}
	port := freePort(t)
	cfg := filepath.Join(dir, "sshd_config")
	conf := fmt.Sprintf(`Port %d
ListenAddress 127.0.0.1
HostKey %s
PidFile %s
AuthorizedKeysFile %s
PasswordAuthentication no
PubkeyAuthentication yes
UsePAM no
StrictModes no
`, port, filepath.Join(dir, "hostkey"), filepath.Join(dir, "sshd.pid"), filepath.Join(dir, "authorized_keys"))
	if err := os.WriteFile(cfg, []byte(conf), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(sshd, "-D", "-f", cfg, "-e")
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start sshd: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	waitPort(t, port)
	return Target{
		Host:           "127.0.0.1",
		User:           u.Username,
		Port:           port,
		IdentityFile:   filepath.Join(dir, "clientkey"),
		KnownHostsFile: filepath.Join(dir, "known_hosts"),
	}
}

func genKey(t *testing.T, path string) {
	t.Helper()
	cmd := exec.Command("ssh-keygen", "-t", "ed25519", "-f", path, "-N", "", "-q")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("ssh-keygen failed: %v\n%s", err, out)
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("cannot find a free port: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func waitPort(t *testing.T, port int) {
	t.Helper()
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	for i := 0; i < 100; i++ {
		c, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			c.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("sshd did not come up on %s", addr)
}

func TestRealSSHBaseConnects(t *testing.T) {
	tgt := startSSHD(t)
	out, err := exec.Command("ssh", append(Base(tgt), "echo", "cove-ok")...).Output()
	if err != nil {
		t.Fatalf("ssh connect via Base failed: %v", err)
	}
	if !strings.Contains(string(out), "cove-ok") {
		t.Fatalf("unexpected remote output: %q", out)
	}
}

func TestRealSSHTOFUWritesKnownHosts(t *testing.T) {
	tgt := startSSHD(t)
	if err := exec.Command("ssh", append(Base(tgt), "true")...).Run(); err != nil {
		t.Fatalf("ssh connect failed: %v", err)
	}
	b, err := os.ReadFile(tgt.KnownHostsFile)
	if err != nil {
		t.Fatalf("accept-new did not write known_hosts: %v", err)
	}
	if !strings.Contains(string(b), "ssh-ed25519") {
		t.Fatalf("known_hosts has no pinned host key:\n%s", b)
	}
}
