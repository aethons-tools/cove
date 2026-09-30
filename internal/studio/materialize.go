package studio

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// MaterializeInto writes a context base (context-files tree or base64 zip) into
// dir as a docker build context whose root holds a Dockerfile. It is the only
// host input for the build (remote-build-tolerant). It re-validates first and is
// fail-closed on any unsafe entry (see materializeZip). It errors for the image
// and default forms, which the launcher resolves without a build context.
func (b Base) MaterializeInto(dir string) error {
	if err := b.validate(); err != nil {
		return err
	}
	kind, _ := b.Kind()
	switch kind {
	case BaseContextFiles:
		return b.materializeTree(dir)
	case BaseContextZip:
		return b.materializeZip(dir)
	default:
		return fmt.Errorf("base: MaterializeInto is only for context-files/context (got kind %d)", kind)
	}
}

// materializeTree writes the inline context tree: the top-level `dockerfile`
// becomes the context's Dockerfile, every other entry is written verbatim
// (a string is a file, a map is a subdirectory). Keys are single segments
// (validated), so no join can escape dir.
func (b Base) materializeTree(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("materialize context-files: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(*b.ContextFiles["dockerfile"].File), 0o644); err != nil {
		return fmt.Errorf("materialize context-files: %w", err)
	}
	for name, node := range b.ContextFiles {
		if name == "dockerfile" {
			continue // already written as Dockerfile
		}
		if err := writeContextNode(dir, name, node); err != nil {
			return fmt.Errorf("materialize context-files: %w", err)
		}
	}
	return nil
}

func writeContextNode(dir, name string, node ContextNode) error {
	dst := filepath.Join(dir, name)
	if node.Dir != nil {
		if err := os.MkdirAll(dst, 0o755); err != nil {
			return err
		}
		for k, child := range node.Dir {
			if err := writeContextNode(dst, k, child); err != nil {
				return err
			}
		}
		return nil
	}
	if node.File != nil {
		return os.WriteFile(dst, []byte(*node.File), 0o644)
	}
	return nil
}

// materializeZip decodes + unzips the base64 context into dir under hard caps:
// entry count, per-cumulative decompressed size, no symlink/irregular entries,
// no absolute or ".."-escaping names. Any violation is a loud error with no
// escaped write. It requires a root Dockerfile after extraction.
func (b Base) materializeZip(dir string) error {
	raw, err := base64.StdEncoding.DecodeString(b.Context)
	if err != nil {
		return fmt.Errorf("base.context: invalid base64: %w", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return fmt.Errorf("base.context: not a readable zip: %w", err)
	}
	if len(zr.File) > maxZipEntries {
		return fmt.Errorf("base.context: %d entries exceeds the %d-entry cap", len(zr.File), maxZipEntries)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("base.context: %w", err)
	}
	var total int64
	for _, f := range zr.File {
		if f.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("base.context: entry %q is a symlink (not allowed)", f.Name)
		}
		if filepath.IsAbs(f.Name) || hasDotDotComponent(f.Name) {
			return fmt.Errorf("base.context: entry %q escapes the build context", f.Name)
		}
		dst := filepath.Join(dir, filepath.FromSlash(f.Name))
		if !withinDir(dir, dst) {
			return fmt.Errorf("base.context: entry %q escapes the build context", f.Name)
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(dst, 0o755); err != nil {
				return fmt.Errorf("base.context: %w", err)
			}
			continue
		}
		if !f.Mode().IsRegular() {
			return fmt.Errorf("base.context: entry %q is not a regular file", f.Name)
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return fmt.Errorf("base.context: %w", err)
		}
		n, err := extractCapped(dst, f, int64(maxDecompressedZip)-total)
		if err != nil {
			return fmt.Errorf("base.context: %w", err)
		}
		total += n
	}
	if _, err := os.Stat(filepath.Join(dir, "Dockerfile")); err != nil {
		return fmt.Errorf("base.context: zip has no root Dockerfile")
	}
	return nil
}

// extractCapped copies one zip entry to dst, writing at most remaining+1 bytes
// and erroring if the entry would push the cumulative total past the cap (a
// decompression-bomb guard that trusts the stream, not the header).
func extractCapped(dst string, f *zip.File, remaining int64) (int64, error) {
	if remaining < 0 {
		remaining = 0
	}
	rc, err := f.Open()
	if err != nil {
		return 0, err
	}
	defer rc.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return 0, err
	}
	defer out.Close()
	n, err := io.Copy(out, io.LimitReader(rc, remaining+1))
	if err != nil {
		return n, err
	}
	if n > remaining {
		return n, fmt.Errorf("decompressed size exceeds the %d-byte cap", maxDecompressedZip)
	}
	return n, nil
}

// hasDotDotComponent reports a ".." path component in a zip entry name. Combined
// with the absolute-path check and withinDir, it blocks escape on at-jam's POSIX
// hosts; a backslash in a name is a literal filename here, not a separator.
func hasDotDotComponent(name string) bool {
	for _, part := range strings.Split(filepath.ToSlash(name), "/") {
		if part == ".." {
			return true
		}
	}
	return false
}

func withinDir(dir, dst string) bool {
	rel, err := filepath.Rel(dir, dst)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
