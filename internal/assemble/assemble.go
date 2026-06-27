package assemble

import (
	"io/fs"
	"os"
	"path/filepath"
)

// Assemble builds the context in buildDir from the layered overlays (last
// writer wins) and injects the managed public key.
func Assemble(kitDir, buildDir string, pub []byte) error {
	if err := os.RemoveAll(buildDir); err != nil {
		return err
	}
	if err := os.MkdirAll(buildDir, 0o755); err != nil {
		return err
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
