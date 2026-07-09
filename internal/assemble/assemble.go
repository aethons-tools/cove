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

	if _, err := os.Stat(filepath.Join(kitDir, "image-files", ".cove")); err == nil {
		return fmt.Errorf("kit image-files/.cove is reserved for cove-generated build files; rename or remove it")
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

	if err := writeSetupManifest(kitDir, buildDir, img.SetupScripts); err != nil {
		return err
	}

	if err := writeImageEnv(buildDir, img.Paths, img.Env); err != nil {
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

// writeSetupManifest writes the ordered list of in-image absolute script paths
// for the build-time runner. Each entry is interpreted relative to the kit's
// image-files root: on disk kitDir/image-files/<entry>, in the image /<entry>
// (the file is placed there by `COPY image-files/. /.`). Always written (empty
// list → empty file) so the runner can read it unconditionally.
func writeSetupManifest(kitDir, buildDir string, scripts []string) error {
	var b strings.Builder
	for _, s := range scripts {
		onDisk := filepath.Join(kitDir, "image-files", filepath.FromSlash(s))
		info, err := os.Stat(onDisk)
		if err != nil {
			return fmt.Errorf("image.setup-scripts %q: %w", s, err)
		}
		if info.IsDir() {
			return fmt.Errorf("image.setup-scripts %q: is a directory, not a script", s)
		}
		b.WriteString(path.Clean("/" + filepath.ToSlash(s)))
		b.WriteString("\n")
	}
	dst := filepath.Join(buildDir, "image-files/.cove/setup-manifest")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, []byte(b.String()), 0o644)
}

// writeImageEnv writes the kit's additive PATH segments and env vars for the
// build-time apply helper. Both files are always written (empty when unset).
// Paths keep declaration order; env is sorted by key for a deterministic image.
func writeImageEnv(buildDir string, paths []string, env map[string]string) error {
	dir := filepath.Join(buildDir, "image-files/.cove")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	var pb strings.Builder
	for _, p := range paths {
		pb.WriteString(p)
		pb.WriteString("\n")
	}
	if err := os.WriteFile(filepath.Join(dir, "paths"), []byte(pb.String()), 0o644); err != nil {
		return err
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var eb strings.Builder
	for _, k := range keys {
		eb.WriteString(k)
		eb.WriteString("=")
		eb.WriteString(env[k])
		eb.WriteString("\n")
	}
	return os.WriteFile(filepath.Join(dir, "env"), []byte(eb.String()), 0o644)
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
