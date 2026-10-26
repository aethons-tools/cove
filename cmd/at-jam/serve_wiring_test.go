package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPlanCredentials_FailsClosedOnUnsupplied(t *testing.T) {
	dir := t.TempDir()
	credFile := filepath.Join(dir, "credentials.yml")
	if err := os.WriteFile(credFile, []byte("credentials:\n  git-pat: { value: sekret-pat-value }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := serveConfig{
		CredentialsFile: credFile,
		Credentials:     map[string]credSpec{"git-pat": {}, "missing": {}},
	}
	_, err := planCredentials(cfg)
	if err == nil {
		t.Fatal("want fail-closed error naming the unsupplied credential, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "missing") {
		t.Errorf("error should name the unsupplied credential %q: %v", "missing", err)
	}
	if !strings.Contains(msg, credFile) {
		t.Errorf("error should name the credentials file %q: %v", credFile, err)
	}
	if strings.Contains(msg, "sekret-pat-value") {
		t.Errorf("error must not leak a supplied secret value: %v", err)
	}
}

func TestPlanCredentials_ResolvesSpecsByName(t *testing.T) {
	dir := t.TempDir()
	credFile := filepath.Join(dir, "credentials.yml")
	if err := os.WriteFile(credFile, []byte("credentials:\n  jam-db: { value: pw }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := serveConfig{
		CredentialsFile: credFile,
		Credentials:     map[string]credSpec{"jam-db": {}},
	}
	specs, err := planCredentials(cfg)
	if err != nil {
		t.Fatalf("planCredentials: %v", err)
	}
	if s, ok := specs["jam-db"]; !ok || !s.Literal || s.Value != "pw" {
		t.Fatalf("jam-db spec wrong: %+v ok=%v", specs["jam-db"], ok)
	}
}

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
	for path, want := range map[string]os.FileMode{priv: 0o600, priv + ".pub": 0o644, filepath.Dir(priv): 0o700} {
		st, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := st.Mode().Perm(); got != want {
			t.Errorf("%s mode = %v, want %v", path, got, want)
		}
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
