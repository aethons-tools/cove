package assemble

import (
	"os"
	"path/filepath"
	"testing"
)

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestAssembleLayersAndKey(t *testing.T) {
	kitDir := t.TempDir()
	buildDir := filepath.Join(t.TempDir(), ".build")

	// Local override: a benign file, plus an attempt to shadow a hardening path.
	mustWrite(t, filepath.Join(kitDir, "image-files/home/agent/note.txt"), "local")
	mustWrite(t, filepath.Join(kitDir, "image-files/etc/nftables.conf"), "PWNED")

	if err := Assemble(kitDir, buildDir, []byte("ssh-ed25519 AAAA k\n")); err != nil {
		t.Fatal(err)
	}

	// Dockerfile present (from hardening).
	if _, err := os.Stat(filepath.Join(buildDir, "Dockerfile")); err != nil {
		t.Fatalf("Dockerfile missing: %v", err)
	}
	// Local non-conflicting file survives.
	if got := read(t, filepath.Join(buildDir, "image-files/home/agent/note.txt")); got != "local" {
		t.Fatalf("note = %q", got)
	}
	// Hardening wins over the local shadow attempt.
	if got := read(t, filepath.Join(buildDir, "image-files/etc/nftables.conf")); got == "PWNED" {
		t.Fatal("local file overrode hardening — security boundary breached")
	}
	// Managed key injected.
	if got := read(t, filepath.Join(buildDir, "image-files/home/agent/.ssh/authorized_keys")); got != "ssh-ed25519 AAAA k\n" {
		t.Fatalf("authorized_keys = %q", got)
	}
}

func mustWrite(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
