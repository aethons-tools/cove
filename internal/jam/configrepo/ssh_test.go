package configrepo

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam/gitsource/gittest"
)

func khFile(t *testing.T, line string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "host-keys")
	if err := os.WriteFile(p, []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func sshRepo(t *testing.T, url, pem, kh string) *Repo {
	return &Repo{URL: url, Branch: "main", Path: t.TempDir(),
		Auth: SSHAuth("git", func() (string, error) { return pem, nil }, kh)}
}

func TestSSHBranchesAndCreate(t *testing.T) {
	url, line, pem := gittest.ServeSSH(t, map[string]string{"a.txt": "a"})
	r := sshRepo(t, url, pem, khFile(t, line))
	bs, err := r.Branches(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(bs) != 1 || bs[0].Name != "main" {
		t.Fatalf("branches = %v", bs)
	}
	if _, err := r.CreateBranch(context.Background(), "jam/me"); err != nil {
		t.Fatal(err)
	}
	if gittest.SSHHead(t, url, "jam/me") == "" {
		t.Fatal("jam/me not pushed")
	}
}

func TestSSHUnknownHostKey(t *testing.T) {
	url, line, pem := gittest.ServeSSH(t, map[string]string{"a.txt": "a"})
	_, other, _ := gittest.ServeSSH(t, nil)
	other = strings.Fields(other)[1] + " " + strings.Fields(other)[2] // another key
	r := sshRepo(t, url, pem, khFile(t, "[127.0.0.1]:1 "+other))
	_, err := r.Branches(context.Background())
	if err == nil || !strings.Contains(err.Error(), "is not in host-keys") ||
		!strings.Contains(err.Error(), strings.Fields(line)[1]+" "+strings.Fields(line)[2]) {
		t.Fatalf("err = %v", err)
	}
	for _, l := range strings.Split(strings.TrimSpace(pem), "\n")[1:3] {
		if strings.Contains(err.Error(), l) {
			t.Fatal("error quotes the key")
		}
	}
}

func TestSSHBadKey(t *testing.T) {
	r := sshRepo(t, "ssh://git@127.0.0.1:1/r.git", "nope", khFile(t, "x ssh-ed25519 AAAA"))
	if _, err := r.Auth(); err == nil || err.Error() != "config.git.cred: not an OpenSSH or PEM private key" {
		t.Fatalf("Auth err = %v", err)
	}
	_, err := r.Branches(context.Background())
	if err == nil || !strings.HasSuffix(err.Error(), "config.git.cred: not an OpenSSH or PEM private key") {
		t.Fatalf("err = %v", err)
	}
}

func TestSSHUser(t *testing.T) {
	for in, want := range map[string]string{"ssh://bob@h/x": "bob", "git@h:x.git": "git", "ssh://h/x": "git", "alice@h:x": "alice"} {
		if got := SSHUser(in); got != want {
			t.Errorf("SSHUser(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSSHChangedHostKey(t *testing.T) {
	url, line, pem := gittest.ServeSSH(t, map[string]string{"a.txt": "a"})
	_, other, _ := gittest.ServeSSH(t, nil)
	host := strings.Fields(line)[0]
	r := sshRepo(t, url, pem, khFile(t, host+" "+strings.Fields(other)[1]+" "+strings.Fields(other)[2]))
	_, err := r.Branches(context.Background())
	if err == nil || !strings.Contains(err.Error(), "does not match host-keys") ||
		strings.Contains(err.Error(), "is not in host-keys") || !strings.Contains(err.Error(), strings.Fields(line)[2]) {
		t.Fatalf("err = %v", err)
	}
	for _, l := range strings.Split(strings.TrimSpace(pem), "\n")[1:3] {
		if strings.Contains(err.Error(), l) {
			t.Fatal("error quotes the key")
		}
	}
}

func TestSSHPinnedNonDefaultAlgorithm(t *testing.T) {
	url, line, pem := gittest.ServeSSH(t, map[string]string{"a.txt": "a"}, gittest.WithRSAHostKey())
	if !strings.Contains(line, "ssh-rsa") {
		t.Fatalf("line = %s", line)
	}
	r := sshRepo(t, url, pem, khFile(t, line))
	if _, err := r.Branches(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSSHPinnedEd25519WhenRSAOffered(t *testing.T) {
	url, line, pem := gittest.ServeSSH(t, map[string]string{"a.txt": "a"}, gittest.WithExtraRSAHostKey())
	if !strings.Contains(line, "ssh-ed25519") {
		t.Fatalf("line = %s", line)
	}
	r := sshRepo(t, url, pem, khFile(t, line))
	if _, err := r.Branches(context.Background()); err != nil {
		t.Fatal(err)
	}
}
