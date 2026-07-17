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

// Assemble must write the kit's managed .gitignore, so a kit built by any path
// (build/create/work) never leaks its .build/.state artifacts into git.
func TestAssembleEnsuresGitignore(t *testing.T) {
	kitDir := t.TempDir()
	if err := Assemble(kitDir, filepath.Join(kitDir, ".build"), []byte("ssh-ed25519 AAAA"), kit.ImageConfig{}); err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	gi := read(t, filepath.Join(kitDir, ".gitignore"))
	if !strings.Contains(gi, ".build/") || !strings.Contains(gi, ".state/") {
		t.Fatalf(".gitignore missing managed entries:\n%s", gi)
	}
}

func TestAssembleLayersAndKey(t *testing.T) {
	buildDir := filepath.Join(t.TempDir(), ".build")

	if err := Assemble(t.TempDir(), buildDir, []byte("ssh-ed25519 AAAA k\n"), kit.ImageConfig{}); err != nil {
		t.Fatal(err)
	}

	// Dockerfile present (from the sealed hardening layer).
	if _, err := os.Stat(filepath.Join(buildDir, "Dockerfile")); err != nil {
		t.Fatalf("Dockerfile missing: %v", err)
	}
	// Managed key injected.
	if got := read(t, filepath.Join(buildDir, "image-files/home/agent/.ssh/authorized_keys")); got != "ssh-ed25519 AAAA k\n" {
		t.Fatalf("authorized_keys = %q", got)
	}
}

// Assemble stages the at-task binaries into the build context (buildDir/attask/)
// for the sealed layer to install. In hermetic tests the embed is unstaged, so
// the placeholders are 0-byte — hardening then keeps the base image's at-task.
func TestAssembleStagesAtTask(t *testing.T) {
	buildDir := filepath.Join(t.TempDir(), ".build")
	if err := Assemble(t.TempDir(), buildDir, []byte("k\n"), kit.ImageConfig{}); err != nil {
		t.Fatal(err)
	}
	for _, arch := range []string{"amd64", "arm64"} {
		if _, err := os.Stat(filepath.Join(buildDir, "attask", "at-task-linux-"+arch)); err != nil {
			t.Fatalf("at-task-linux-%s not staged into the context: %v", arch, err)
		}
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

func TestCollaboratorRoleFileSeeded(t *testing.T) {
	base := filepath.Join("hardening", "image-files", "home", "agent", ".init-agent-data")
	b, err := os.ReadFile(filepath.Join(base, "CLAUDE.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "@COLLABORATOR.md") {
		t.Fatalf("hardening CLAUDE.md must @-include COLLABORATOR.md:\n%s", b)
	}
	if _, err := os.Stat(filepath.Join(base, "COLLABORATOR.md")); err != nil {
		t.Fatalf("default COLLABORATOR.md missing from the hardening payload: %v", err)
	}
}
