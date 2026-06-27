package keys

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/aethons-tools/at-sbx/internal/runner"
)

func TestEnsureUsesExistingKey(t *testing.T) {
	dir := t.TempDir()
	priv := filepath.Join(dir, "id_ed25519")
	os.WriteFile(priv, []byte("PRIV"), 0o600)
	os.WriteFile(priv+".pub", []byte("ssh-ed25519 AAAA test\n"), 0o644)

	f := &runner.Fake{}
	gotPriv, pub, err := Ensure(f, dir)
	if err != nil {
		t.Fatal(err)
	}
	if gotPriv != priv {
		t.Fatalf("priv = %q", gotPriv)
	}
	if string(pub) != "ssh-ed25519 AAAA test\n" {
		t.Fatalf("pub = %q", pub)
	}
	if len(f.Calls) != 0 {
		t.Fatalf("should not call ssh-keygen when key exists: %+v", f.Calls)
	}
}

func TestEnsureGeneratesWhenMissing(t *testing.T) {
	dir := t.TempDir()
	priv := filepath.Join(dir, "id_ed25519")
	f := &runner.Fake{}
	// Pre-place the .pub so Ensure's post-generate read succeeds.
	os.WriteFile(priv+".pub", []byte("ssh-ed25519 AAAA gen\n"), 0o644)

	_, pub, err := Ensure(f, dir)
	if err != nil {
		t.Fatal(err)
	}
	if string(pub) != "ssh-ed25519 AAAA gen\n" {
		t.Fatalf("pub = %q", pub)
	}
	if len(f.Calls) != 1 || f.Calls[0].Name != "ssh-keygen" {
		t.Fatalf("expected ssh-keygen call, got %+v", f.Calls)
	}
}
