package assemble

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/aethons-tools/cove/internal/kit"
)

// Assemble builds the context in buildDir from the layered overlays (last
// writer wins) and injects the managed public key.
func Assemble(kitDir, buildDir string, pub []byte, img kit.ImageConfig) error {
	if err := os.RemoveAll(buildDir); err != nil {
		return err
	}
	if err := os.MkdirAll(buildDir, 0o755); err != nil {
		return err
	}

	if hits, err := collisions(kitDir); err != nil {
		return err
	} else if len(hits) > 0 {
		return fmt.Errorf("kit image-files collide with the sealed hardening layer (these would be silently overwritten — rename or remove them): %s", strings.Join(hits, ", "))
	}

	// Layer 1: overridable defaults (strip the "overridable/" prefix).
	if err := copyEmbed(overridableFS, "overridable", buildDir); err != nil {
		return err
	}
	// Layer 2: kit's local image-files (if present).
	localIF := filepath.Join(kitDir, "image-files")
	if _, err := os.Stat(localIF); err == nil {
		if err := copyTree(localIF, filepath.Join(buildDir, "image-files")); err != nil {
			return err
		}
	}
	// Layer 3 (deferred): .local/image-files — intentionally not applied yet.
	// Layer 4: non-overridable hardening (Dockerfile + image-files), wins.
	if err := copyEmbed(hardeningFS, "hardening", buildDir); err != nil {
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

// collisions returns the image-files paths present in BOTH the kit overlay and
// the sealed hardening tree. Such a path would be silently overwritten by the
// winning hardening copy, so Assemble rejects the build instead of surprising
// the kit author.
func collisions(kitDir string) ([]string, error) {
	localIF := filepath.Join(kitDir, "image-files")
	if _, err := os.Stat(localIF); err != nil {
		return nil, nil // no kit overlay → nothing to collide
	}
	var hits []string
	err := filepath.WalkDir(localIF, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(localIF, p)
		if err != nil {
			return err
		}
		hp := path.Join("hardening/image-files", filepath.ToSlash(rel))
		if _, err := fs.Stat(hardeningFS, hp); err == nil {
			hits = append(hits, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(hits)
	return hits, nil
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

// copyTree copies a real directory tree from src to dst, preserving modes.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, b, info.Mode().Perm())
	})
}
