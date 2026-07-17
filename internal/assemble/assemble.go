package assemble

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/aethons-tools/cove/internal/attask"
	"github.com/aethons-tools/cove/internal/kit"
)

// Assemble builds the context in buildDir: the sealed hardening layer, the
// injected at-task, the kit's egress allow-list, and the managed public key. The
// kit's image/ is the Dockerfile build context (resolved elsewhere), not overlaid.
func Assemble(kitDir, buildDir string, pub []byte, img kit.ImageConfig) error {
	// Any path that assembles a build context (build/create/work) keeps the kit's
	// .gitignore current, so generated .build/.state artifacts never leak into git.
	if err := kit.EnsureGitignore(kitDir); err != nil {
		return err
	}
	if err := os.RemoveAll(buildDir); err != nil {
		return err
	}
	if err := os.MkdirAll(buildDir, 0o755); err != nil {
		return err
	}

	// The kit's image/ is only the Dockerfile build context now (COV-34) — it is
	// not overlaid, and the overridable defaults ship in cove-base-image. So the
	// build context is the sealed hardening layer plus the injected at-task, the
	// kit's egress allow-list, and the managed key.
	if err := copyEmbed(hardeningFS, "hardening", buildDir); err != nil {
		return err
	}

	if err := writeAtTask(buildDir); err != nil {
		return err
	}

	if err := writeAllowedDomains(buildDir, img.AllowedDomains); err != nil {
		return err
	}

	// Managed key injection.
	ak := filepath.Join(buildDir, "image-files/home/agent/.ssh/authorized_keys")
	if err := os.MkdirAll(filepath.Dir(ak), 0o700); err != nil {
		return err
	}
	return os.WriteFile(ak, pub, 0o600)
}

// writeAtTask stages the embedded linux at-task binaries into the build context
// (buildDir/attask/at-task-linux-<arch>), so the sealed hardening layer can
// install the arch-matching one — the at-cove that orchestrates a dispatch ships
// the exact at-task it expects (COV-34/COV-36). When the embed was not staged (a
// plain `go build` without scripts/build.sh — never the release path), a 0-byte
// placeholder is written instead; hardening installs the binary only when it is
// non-empty, so it falls back to the base image's at-task rather than a broken one.
func writeAtTask(buildDir string) error {
	dir := filepath.Join(buildDir, "attask")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, arch := range []string{"amd64", "arm64"} {
		b, err := attask.Binary(arch)
		if err != nil {
			b = nil // not staged → placeholder; hardening keeps the base's at-task
		}
		if err := os.WriteFile(filepath.Join(dir, "at-task-linux-"+arch), b, 0o755); err != nil {
			return err
		}
	}
	return nil
}

// copyEmbed copies efs under root into dst, stripping the root prefix.
func copyEmbed(efs fs.FS, root, dst string) error {
	return fs.WalkDir(efs, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := fs.ReadFile(efs, p)
		if err != nil {
			return err
		}
		mode := fs.FileMode(0o644)
		if filepath.Ext(p) == ".sh" || filepath.Base(p) == "entrypoint.sh" {
			mode = 0o755
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, b, mode)
	})
}

// writeAllowedDomains writes the kit's additive squid allow-list. Always written
// (empty list → header only) so the sealed squid.conf can reference it
// unconditionally without squid erroring on a missing ACL file.
func writeAllowedDomains(buildDir string, domains []string) error {
	dst := filepath.Join(buildDir, "image-files/etc/squid/allowed_domains.kit.txt")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString("# Kit-declared egress domains (config.yml image.allowed-domains).\n")
	b.WriteString("# Additive to the sealed base allowed_domains.txt; leading dot = subdomains.\n")
	for _, d := range domains {
		b.WriteString(d)
		b.WriteString("\n")
	}
	return os.WriteFile(dst, []byte(b.String()), 0o644)
}
