package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// serve's launcher block takes the studio images' authorized_keys from
// launcherPublicKey: on a fresh Jam host (no key — at-cove used to make it) it
// generates the pair at runtime.launcher.identity-file.
func TestLauncherPublicKeyGeneratesOnFreshHost(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen not on PATH")
	}
	priv := filepath.Join(t.TempDir(), "fresh", "at-cove", "id_ed25519")
	pub, err := launcherPublicKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(pub), "ssh-ed25519 ") {
		t.Fatalf("pub = %q, want an ed25519 public key", pub)
	}
	// The private key and its dir are owner-only whatever the umask; the .pub is
	// ssh-keygen's 0644 less the umask, so assert only that it is owner-readable
	// and never writable or executable beyond its owner.
	for path, want := range map[string]os.FileMode{priv: 0o600, filepath.Dir(priv): 0o700} {
		st, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := st.Mode().Perm(); got != want {
			t.Errorf("%s mode = %v, want %v", path, got, want)
		}
	}
	st, err := os.Stat(priv + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	if got := st.Mode().Perm(); got&0o600 != 0o600 || got&^0o644 != 0 {
		t.Errorf("%s.pub mode = %v, want 0644 less the umask (owner rw)", priv, got)
	}
	onDisk, err := os.ReadFile(priv + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	if string(onDisk) != string(pub) {
		t.Fatalf("returned pub %q != %s.pub %q", pub, priv, onDisk)
	}
}

// An existing launcher key (an upgraded host) is used as-is, never regenerated.
func TestLauncherPublicKeyKeepsExistingKey(t *testing.T) {
	priv := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(priv, []byte("PRIV"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(priv+".pub", []byte("ssh-ed25519 AAAA existing\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pub, err := launcherPublicKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	if string(pub) != "ssh-ed25519 AAAA existing\n" {
		t.Fatalf("pub = %q", pub)
	}
	if b, _ := os.ReadFile(priv); string(b) != "PRIV" {
		t.Fatalf("private key rewritten: %q", b)
	}
}

// The launcher's ssh uses StrictHostKeyChecking=accept-new with a per-studio
// file under known-hosts-dir; ssh will not create the dir, so serve must.
func TestEnsureKnownHostsDir(t *testing.T) {
	fresh := filepath.Join(t.TempDir(), "a", "known_hosts.d")
	if err := ensureKnownHostsDir(fresh); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(fresh)
	if err != nil || !st.IsDir() || st.Mode().Perm() != 0o700 {
		t.Fatalf("fresh dir: %v, %v", st, err)
	}
	existing := t.TempDir()
	if err := os.Chmod(existing, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ensureKnownHostsDir(existing); err != nil {
		t.Fatal(err)
	}
	st, _ = os.Stat(existing)
	if st.Mode().Perm() != 0o755 {
		t.Errorf("existing dir mode changed to %v", st.Mode().Perm())
	}
}
