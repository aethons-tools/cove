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
)

// PackContextDir zips the build context rooted at dir into a base64 string
// suitable for Base.Context. It is the client-side mirror of the server's
// extractor (MaterializeInto): it requires a root Dockerfile, rejects symlinks
// and irregular files, and enforces the same caps (entry count, uncompressed
// size, and the encoded-size cap on the result). Only regular files are packed
// (their paths carry their directories); empty directories are dropped.
func PackContextDir(dir string) (string, error) {
	// Resolve a symlinked root to its real path: WalkDir does NOT descend into a
	// symlinked directory (it would yield an empty zip that only fails later at
	// build), and EvalSymlinks also gives a clear error for a missing dir.
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", fmt.Errorf("context-dir %q: %w", dir, err)
	}
	dir = real
	if fi, err := os.Stat(filepath.Join(dir, "Dockerfile")); err != nil || !fi.Mode().IsRegular() {
		return "", fmt.Errorf("context-dir %q: a root Dockerfile is required", dir)
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	var entries int
	var total int64
	var sawDockerfile bool
	walkErr := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == dir {
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("entry %q is a symlink (not allowed)", p)
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("entry %q is not a regular file", p)
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		relSlash := filepath.ToSlash(rel)
		if relSlash == "Dockerfile" {
			sawDockerfile = true
		}
		entries++
		if entries > maxZipEntries {
			return fmt.Errorf("context-dir has more than %d entries", maxZipEntries)
		}
		w, err := zw.Create(relSlash)
		if err != nil {
			return err
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		// Bound the copy to the remaining budget (the actual decompressed stream,
		// not the stat size — the file could grow mid-pack), cumulative across entries.
		before := total
		n, cerr := io.Copy(w, io.LimitReader(f, int64(maxDecompressedZip)-before+1))
		f.Close()
		if cerr != nil {
			return cerr
		}
		if n > int64(maxDecompressedZip)-before {
			return fmt.Errorf("context-dir exceeds the %d-byte uncompressed cap", maxDecompressedZip)
		}
		total = before + n
		return nil
	})
	if walkErr != nil {
		return "", fmt.Errorf("pack context-dir %q: %w", dir, walkErr)
	}
	if !sawDockerfile {
		return "", fmt.Errorf("pack context-dir %q: no root Dockerfile packed", dir)
	}
	if err := zw.Close(); err != nil {
		return "", fmt.Errorf("pack context-dir %q: %w", dir, err)
	}
	b64 := base64.StdEncoding.EncodeToString(buf.Bytes())
	if len(b64) > maxEncodedZip {
		return "", fmt.Errorf("packed context-dir %q is %d base64 bytes, exceeds the %d-byte cap", dir, len(b64), maxEncodedZip)
	}
	return b64, nil
}

// ResolveContextDir packs a client-only base.context-dir into base.context (a
// zip) and clears context-dir, so the resulting kit is an ordinary context-zip
// kit ready to serialize + push. A relative context-dir is resolved against
// baseDir (e.g. the kit file's directory). A no-op when context-dir is unset.
func (sk *StudioKit) ResolveContextDir(baseDir string) error {
	if sk.Base.ContextDir == "" {
		return nil
	}
	dir := sk.Base.ContextDir
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(baseDir, dir)
	}
	b64, err := PackContextDir(dir)
	if err != nil {
		return err
	}
	sk.Base.Context = b64
	sk.Base.ContextDir = ""
	return nil
}
