//go:build integration

// Integration tests that exercise the real ssh client and a throwaway sshd on
// loopback — no Docker, no network egress, no VM. They validate the parts that
// the hermetic unit tests cannot: that the argv builders actually drive
// OpenSSH, that the sshd AcceptEnv/stdin paths really deliver secrets, that
// known_hosts TOFU writes a key, and that the connect auth flow logs in once.
//
// Excluded from the default suite (build tag). Run with:
//
//	go test -tags integration ./internal/connect/ -v
//
// Requires `ssh`, `sshd`, and `ssh-keygen` on PATH; tests Skip if absent or if
// a non-root sshd cannot start in this environment.
package connect

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

	"github.com/aethons-tools/cove/internal/backend"
	"github.com/aethons-tools/cove/internal/runner"
	"github.com/aethons-tools/cove/internal/sshargs"
)

// noopInhibitor is a stand-in awake.Inhibitor for the connect tests: it holds
// no OS assertion and its release is a safe no-op, keeping these tests off any
// platform-specific sleep-prevention machinery.
type noopInhibitor struct{}

func (noopInhibitor) Inhibit() (func(), error) { return func() {}, nil }

// sshdServer is a running throwaway sshd and the knobs the tests need.
type sshdServer struct {
	Target sshargs.Target // points the ssh client at this server
	Dir    string         // temp dir: fake claude, markers, env dumps live here
}

// startSSHD boots an unprivileged sshd on a free loopback port configured for
// key-only auth, with a controllable fake `claude` on the session PATH.
func startSSHD(t *testing.T) sshdServer {
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
	mustCopy(t, filepath.Join(dir, "clientkey.pub"), filepath.Join(dir, "authorized_keys"))

	fakebin := filepath.Join(dir, "bin")
	mustMkdir(t, fakebin)
	writeFakeClaude(t, filepath.Join(fakebin, "claude"), dir)

	u, err := user.Current()
	if err != nil {
		t.Skipf("cannot resolve current user: %v", err)
	}
	port := freePort(t)
	cfg := filepath.Join(dir, "sshd_config")
	mustWriteFile(t, cfg, fmt.Sprintf(`Port %d
ListenAddress 127.0.0.1
HostKey %s
PidFile %s
AuthorizedKeysFile %s
PasswordAuthentication no
PubkeyAuthentication yes
UsePAM no
StrictModes no
AcceptEnv *
SetEnv PATH=%s:/usr/bin:/bin
`, port, filepath.Join(dir, "hostkey"), filepath.Join(dir, "sshd.pid"),
		filepath.Join(dir, "authorized_keys"), fakebin))

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

	return sshdServer{
		Target: sshargs.Target{
			Host:           "127.0.0.1",
			User:           u.Username,
			Port:           port,
			IdentityFile:   filepath.Join(dir, "clientkey"),
			KnownHostsFile: filepath.Join(dir, "known_hosts"),
		},
		Dir: dir,
	}
}

// writeFakeClaude drops a stand-in `claude` that records what cove asks of it:
//   - `auth status`         -> exit 0 if <dir>/authed.marker exists, else 1
//   - `auth login ...`      -> create the marker, append to login.log
//   - (no args, i.e. agent) -> dump its environment to agent-env.txt
func writeFakeClaude(t *testing.T, path, dir string) {
	t.Helper()
	script := fmt.Sprintf(`#!/usr/bin/env bash
DIR=%q
case "$1 $2" in
  "auth status")
    [ -f "$DIR/authed.marker" ] && exit 0 || exit 1 ;;
  "auth login")
    touch "$DIR/authed.marker"; echo login >> "$DIR/login.log"; exit 0 ;;
  *)
    env > "$DIR/agent-env.txt"; echo launched >> "$DIR/launch.log"; exit 0 ;;
esac
`, dir)
	mustWriteFileMode(t, path, script, 0o755)
}

func TestRealSSHBaseConnects(t *testing.T) {
	s := startSSHD(t)
	out, err := runner.OS{}.Output("ssh", append(sshargs.Base(s.Target), "echo", "cove-ok")...)
	if err != nil {
		t.Fatalf("ssh connect via sshargs.Base failed: %v", err)
	}
	if !strings.Contains(out, "cove-ok") {
		t.Fatalf("unexpected remote output: %q", out)
	}
}

func TestRealSSHTOFUWritesKnownHosts(t *testing.T) {
	s := startSSHD(t)
	if _, err := (runner.OS{}).Output("ssh", append(sshargs.Base(s.Target), "true")...); err != nil {
		t.Fatalf("ssh connect failed: %v", err)
	}
	b, err := os.ReadFile(s.Target.KnownHostsFile)
	if err != nil {
		t.Fatalf("accept-new did not write known_hosts: %v", err)
	}
	if !strings.Contains(string(b), "ssh-ed25519") {
		t.Fatalf("known_hosts has no pinned host key:\n%s", b)
	}
}

func TestRealSendEnvDeliversSecretViaAcceptEnv(t *testing.T) {
	s := startSSHD(t)
	if err := (SendEnv{R: runner.OS{}}).Launch(s.Target, map[string]string{"COVE_SECRET": "s3cr3t"}); err != nil {
		t.Fatalf("SendEnv.Launch failed: %v", err)
	}
	env := mustRead(t, filepath.Join(s.Dir, "agent-env.txt"))
	if !strings.Contains(env, "COVE_SECRET=s3cr3t") {
		t.Fatalf("secret not delivered to the remote env via SendEnv:\n%s", env)
	}
}

func TestRealStdinScriptDeliversSecretViaTmpfs(t *testing.T) {
	s := startSSHD(t)
	if err := (StdinScript{R: runner.OS{}}).Launch(s.Target, map[string]string{"COVE_SECRET": "v3ry"}); err != nil {
		t.Fatalf("StdinScript.Launch failed: %v", err)
	}
	env := mustRead(t, filepath.Join(s.Dir, "agent-env.txt"))
	if !strings.Contains(env, "COVE_SECRET=v3ry") {
		t.Fatalf("secret not delivered to the remote env via StdinScript:\n%s", env)
	}
}

// liveBackend dials the throwaway sshd instead of a real VM.
type liveBackend struct{ ep backend.Endpoint }

func (liveBackend) Install(backend.InstallContext) (backend.InstalledImage, error) {
	return backend.InstalledImage{}, nil
}
func (liveBackend) Create(backend.CreateContext) (backend.Instance, error) {
	return backend.Instance{}, nil
}
func (liveBackend) Destroy(backend.Instance, bool) error    { return nil }
func (liveBackend) GetStatus(string) (backend.State, error) { return backend.StateRunning, nil }
func (b liveBackend) Dial(string) (backend.Endpoint, func(), error) {
	return b.ep, func() {}, nil
}

func TestRealConnectFirstSessionLogsInThenLaunches(t *testing.T) {
	s := startSSHD(t)
	b := liveBackend{ep: backend.Endpoint{Host: s.Target.Host, Port: s.Target.Port, User: s.Target.User}}
	o := Options{Container: "box", IdentityFile: s.Target.IdentityFile, KnownHostsDir: filepath.Join(s.Dir, "kh")}
	if err := Connect(b, runner.OS{}, SendEnv{R: runner.OS{}}, noopInhibitor{}, o); err != nil {
		t.Fatalf("Connect (first session): %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.Dir, "authed.marker")); err != nil {
		t.Fatalf("first session must run the OAuth login (marker missing): %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.Dir, "agent-env.txt")); err != nil {
		t.Fatalf("agent must launch after login: %v", err)
	}
}

func TestRealConnectAuthedSkipsLogin(t *testing.T) {
	s := startSSHD(t)
	mustWriteFile(t, filepath.Join(s.Dir, "authed.marker"), "x") // already logged in
	b := liveBackend{ep: backend.Endpoint{Host: s.Target.Host, Port: s.Target.Port, User: s.Target.User}}
	o := Options{Container: "box", IdentityFile: s.Target.IdentityFile, KnownHostsDir: filepath.Join(s.Dir, "kh")}
	if err := Connect(b, runner.OS{}, SendEnv{R: runner.OS{}}, noopInhibitor{}, o); err != nil {
		t.Fatalf("Connect (authed): %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.Dir, "login.log")); err == nil {
		t.Fatal("must not log in again when already authenticated")
	}
	if _, err := os.Stat(filepath.Join(s.Dir, "agent-env.txt")); err != nil {
		t.Fatalf("agent must launch: %v", err)
	}
}

// --- small test helpers ---

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

func mustCopy(t *testing.T, src, dst string) {
	t.Helper()
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, dst, string(b))
}

func mustMkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	mustWriteFileMode(t, path, content, 0o644)
}

func mustWriteFileMode(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v (did the agent launch?)", path, err)
	}
	return string(b)
}
