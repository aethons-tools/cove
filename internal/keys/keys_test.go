package keys

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/aethons-tools/cove/internal/runner"
)

func TestEnsureFileUsesExistingKey(t *testing.T) {
	priv := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(priv, []byte("PRIV"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(priv+".pub", []byte("ssh-ed25519 AAAA test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &runner.Fake{}
	pub, err := EnsureFile(f, priv)
	if err != nil {
		t.Fatal(err)
	}
	if string(pub) != "ssh-ed25519 AAAA test\n" {
		t.Fatalf("pub = %q", pub)
	}
	if len(f.Calls) != 0 {
		t.Fatalf("should not call ssh-keygen when the key exists: %+v", f.Calls)
	}
}

// A fresh Jam host has no launcher key (it is not shipped): the
// launcher generates it at the configured path.
func TestEnsureFileGeneratesWhenMissing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "jam")
	priv := filepath.Join(dir, "id_ed25519")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// runner.Fake runs nothing, so pre-place the .pub the real ssh-keygen writes.
	if err := os.WriteFile(priv+".pub", []byte("ssh-ed25519 AAAA gen\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &runner.Fake{}
	pub, err := EnsureFile(f, priv)
	if err != nil {
		t.Fatal(err)
	}
	if string(pub) != "ssh-ed25519 AAAA gen\n" {
		t.Fatalf("pub = %q", pub)
	}
	if len(f.Calls) != 1 || f.Calls[0].Name != "ssh-keygen" {
		t.Fatalf("expected one ssh-keygen call, got %+v", f.Calls)
	}
	if args := f.Calls[0].Args; len(args) == 0 || args[len(args)-1] != priv {
		t.Fatalf("ssh-keygen must write %s, args = %q", priv, args)
	}
}

// stdinRunner fails a plain Run: ssh-keygen must get an explicit (empty) stdin,
// never the process's, so an overwrite prompt can't hang serve.
type stdinRunner struct{ *runner.Fake }

func (stdinRunner) Run(name string, _ ...string) error {
	return fmt.Errorf("%s run with the process's stdin", name)
}

// ssh-keygen runs quietly (-q: no randomart on serve's stdout) with an empty
// stdin, so an overwrite prompt reads EOF and fails instead of waiting on a
// terminal.
func TestEnsureFileRunsKeygenQuietWithEmptyStdin(t *testing.T) {
	dir := t.TempDir()
	priv := filepath.Join(dir, "id_ed25519")
	if err := os.WriteFile(priv+".pub", []byte("ssh-ed25519 AAAA gen\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &runner.Fake{}
	if _, err := EnsureFile(stdinRunner{f}, priv); err != nil {
		t.Fatal(err)
	}
	if len(f.Calls) != 1 || !slices.Contains(f.Calls[0].Args, "-q") || f.Calls[0].Stdin != "" {
		t.Fatalf("want one quiet ssh-keygen call with empty stdin, got %+v", f.Calls)
	}
}

func TestEnsureFileCreatesKeyDir0700(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "fresh", "jam")
	_, _ = EnsureFile(&runner.Fake{}, filepath.Join(dir, "id_ed25519")) // the .pub read fails: the fake generated nothing
	st, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("key dir not created: %v", err)
	}
	if st.Mode().Perm() != 0o700 {
		t.Fatalf("key dir mode = %v, want 0700", st.Mode().Perm())
	}
}
