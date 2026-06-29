package assemble

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/kit"
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

	// Local override: a benign file that does not collide with hardening.
	mustWrite(t, filepath.Join(kitDir, "image-files/home/agent/note.txt"), "local")

	if err := Assemble(kitDir, buildDir, []byte("ssh-ed25519 AAAA k\n"), kit.ImageConfig{}); err != nil {
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
	// Managed key injected.
	if got := read(t, filepath.Join(buildDir, "image-files/home/agent/.ssh/authorized_keys")); got != "ssh-ed25519 AAAA k\n" {
		t.Fatalf("authorized_keys = %q", got)
	}
}

func TestAssembleRejectsCollision(t *testing.T) {
	kitDir := t.TempDir()
	buildDir := filepath.Join(t.TempDir(), ".build")
	// etc/nftables.conf is shipped by the hardening layer; shadowing it must fail.
	mustWrite(t, filepath.Join(kitDir, "image-files/etc/nftables.conf"), "PWNED")

	err := Assemble(kitDir, buildDir, []byte("k\n"), kit.ImageConfig{})
	if err == nil {
		t.Fatal("expected a collision error, got nil")
	}
	if !strings.Contains(err.Error(), "etc/nftables.conf") {
		t.Fatalf("error should name the colliding path: %v", err)
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

func TestAssembleAllowedDomains(t *testing.T) {
	kitDir := t.TempDir()
	buildDir := filepath.Join(t.TempDir(), ".build")
	img := kit.ImageConfig{AllowedDomains: []string{".example.com", "pkg.go.dev"}}
	if err := Assemble(kitDir, buildDir, []byte("k\n"), img); err != nil {
		t.Fatal(err)
	}
	got := read(t, filepath.Join(buildDir, "image-files/etc/squid/allowed_domains.kit.txt"))
	if !strings.Contains(got, ".example.com") || !strings.Contains(got, "pkg.go.dev") {
		t.Fatalf("kit allow-list = %q", got)
	}
}

func TestAssembleAllowedDomainsAlwaysWritten(t *testing.T) {
	kitDir := t.TempDir()
	buildDir := filepath.Join(t.TempDir(), ".build")
	if err := Assemble(kitDir, buildDir, []byte("k\n"), kit.ImageConfig{}); err != nil {
		t.Fatal(err)
	}
	// File must exist even with no domains, so squid.conf never references a missing file.
	if _, err := os.Stat(filepath.Join(buildDir, "image-files/etc/squid/allowed_domains.kit.txt")); err != nil {
		t.Fatalf("kit allow-list must always be written: %v", err)
	}
}

func TestSquidConfReferencesKitFile(t *testing.T) {
	got := read(t, "hardening/image-files/etc/squid/squid.conf")
	if !strings.Contains(got, "allowed_domains.kit.txt") {
		t.Fatalf("squid.conf must reference the kit allow-list: %q", got)
	}
}
