package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/legacynames"
)

func legacyBlock(studio string) string {
	return legacynames.SSHBlockBegin + studio + " >>>\nHost jam-" + studio + "\n  ProxyCommand old ssh-proxy " + studio + "\nMatch all\n" +
		legacynames.SSHBlockEnd + studio + " <<<\n"
}

func TestStripLegacySSHBlocks(t *testing.T) {
	cfg := "# mine\n" + legacyBlock("s1") + "\n" + legacyBlock("s2") + "Host *\n  User me\n"
	got, studios := stripLegacySSHBlocks(cfg, "")
	if strings.Contains(got, legacynames.SSHBlockBegin) || strings.Contains(got, "jam-s1") || strings.Contains(got, "jam-s2") {
		t.Fatalf("legacy blocks survive:\n%s", got)
	}
	if !strings.Contains(got, "# mine\n") || !strings.Contains(got, "Host *\n  User me\n") {
		t.Fatalf("the user's own lines must be kept:\n%s", got)
	}
	if strings.Join(studios, ",") != "s1,s2" {
		t.Fatalf("studios = %v", studios)
	}
	if again, none := stripLegacySSHBlocks(got, ""); again != got || len(none) != 0 {
		t.Fatal("strip is not idempotent")
	}
}

// Only the named studio's pre-rename block goes when a studio is given; an
// unterminated block is left alone.
func TestStripLegacySSHBlocksOneStudio(t *testing.T) {
	cfg := legacyBlock("s1") + legacyBlock("s2") + legacynames.SSHBlockBegin + "s3 >>>\nHost jam-s3\n"
	got, studios := stripLegacySSHBlocks(cfg, "s2")
	if want := legacyBlock("s1") + legacynames.SSHBlockBegin + "s3 >>>\nHost jam-s3\n"; got != want || strings.Join(studios, ",") != "s2" {
		t.Fatalf("got %v\n%s", studios, got)
	}
	if again, none := stripLegacySSHBlocks(got, "s3"); again != got || len(none) != 0 {
		t.Fatal("an unterminated block must be left alone")
	}
}

// --write replaces a pre-rename block for the same studio rather than leaving
// a shadowing duplicate Host above the new one; other studios' pre-rename
// blocks stay until their own --write (or --prune).
func TestSSHConfigWriteReplacesPreRenameBlock(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".ssh", "config")
	writeFile(t, path, legacyBlock("s1")+legacyBlock("s2")+"Host *\n  User me\n", 0o600)
	begin, end := sshBlockMarkers("s1")
	if _, err := installSSHConfigBlock("s1", begin+"\nHost jam-s1\nMatch all\n"+end+"\n"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	got := string(b)
	if strings.Contains(got, legacynames.SSHBlockBegin+"s1 ") || strings.Count(got, "Host jam-s1") != 1 || !strings.HasPrefix(got, "# >>> jam s1 >>>") {
		t.Fatalf("config =\n%s", got)
	}
	if !strings.Contains(got, legacyBlock("s2")) || !strings.HasSuffix(got, "Host *\n  User me\n") {
		t.Fatalf("another studio's block or the user's lines went:\n%s", got)
	}
}

// --prune removes every pre-rename block (their ProxyCommand names a binary
// that no longer exists) and says so per studio; --dry-run only reports.
func TestSSHConfigPruneRemovesPreRenameBlocks(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".ssh", "config")
	orig := "# mine\n" + legacyBlock("s1") + "\n" + legacyBlock("s2") + "Host *\n  User me\n"
	writeFile(t, path, orig, 0o600)

	var out, errb bytes.Buffer
	if code := pruneSSHConfig(true, &out, &errb); code != 0 {
		t.Fatalf("dry exit = %d, stderr=%s", code, errb.String())
	}
	if b, _ := os.ReadFile(path); string(b) != orig {
		t.Fatalf("--dry-run changed the file:\n%s", b)
	}
	if !strings.Contains(out.String(), "would remove pre-rename block for jam-s1\n") || !strings.Contains(out.String(), "would remove pre-rename block for jam-s2\n") {
		t.Fatalf("dry stdout = %q", out.String())
	}

	out.Reset()
	if code := pruneSSHConfig(false, &out, &errb); code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errb.String())
	}
	b, _ := os.ReadFile(path)
	if string(b) != "# mine\nHost *\n  User me\n" {
		t.Fatalf("config =\n%s", b)
	}
	if !strings.Contains(out.String(), "removed pre-rename block for jam-s1\n") || !strings.Contains(out.String(), "removed pre-rename block for jam-s2\n") {
		t.Fatalf("stdout = %q", out.String())
	}
}
