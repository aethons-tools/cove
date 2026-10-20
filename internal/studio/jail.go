package studio

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// maxRootFile caps one file read out of a root (a kit.yml or a note file).
// Generous: the encoded context cap is the largest thing a kit.yml can carry.
const maxRootFile = 4 << 20

// ResolveInRoot is ResolveContextDir plus ResolveNoteFiles for a kit that
// lives in an UNTRUSTED tree (a git checkout): dir is the kit's directory,
// relative to root, and every path the kit names — dir itself, base.context-dir
// and each note file — must resolve, after following symlinks, inside root.
// Absolute paths are refused. The paths that are packed or read are the
// resolved ones, so a symlink is never followed a second time. Errors name the
// kit field and its relative path, never a host path outside root or the
// contents of a file; an escape and a missing path read alike, so a refusal is
// no oracle for what exists outside root. On error sk is unchanged.
func (sk *StudioKit) ResolveInRoot(root, dir string) error {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return errors.New("kit root cannot be resolved")
	}
	realDir, err := resolveInRoot(realRoot, realRoot, dir, "kit dir")
	if err != nil {
		return err
	}
	if fi, err := os.Lstat(realDir); err != nil || !fi.IsDir() {
		return fmt.Errorf("kit dir %q: not a directory", dir)
	}

	// Resolve everything before touching sk, so a refusal leaves it as it was.
	var context string
	if cd := sk.Base.ContextDir; cd != "" {
		context, err = packInRoot(realRoot, realDir, cd)
		if err != nil {
			return err
		}
	}
	bodies := make([]string, len(sk.Notes))
	for i, n := range sk.Notes {
		if n.File == "" {
			continue
		}
		if n.Body != "" {
			return fmt.Errorf("note %q: set body or file, not both", n.Name)
		}
		// Same rule as ResolveNoteFiles: a note file stays inside the kit dir.
		if !filepath.IsLocal(n.File) {
			return fmt.Errorf("note %q: file must be a relative path inside the kit directory", n.Name)
		}
		field := fmt.Sprintf("note %q file", n.Name)
		real, err := resolveInRoot(realRoot, realDir, n.File, field)
		if err != nil {
			return err
		}
		raw, err := readRegular(real, field, n.File)
		if err != nil {
			return err
		}
		bodies[i] = string(raw)
	}

	if sk.Base.ContextDir != "" {
		sk.Base.Context, sk.Base.ContextDir = context, ""
	}
	for i := range sk.Notes {
		if sk.Notes[i].File != "" {
			sk.Notes[i].Body, sk.Notes[i].File = bodies[i], ""
		}
	}
	return nil
}

// ReadFileInRoot reads the regular file at rel (relative to root), refusing it
// unless it resolves, after following symlinks, inside root. Errors carry rel,
// never a path outside root or the file's contents.
func ReadFileInRoot(root, rel string) ([]byte, error) {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, errors.New("root cannot be resolved")
	}
	real, err := resolveInRoot(realRoot, realRoot, rel, "file")
	if err != nil {
		return nil, err
	}
	return readRegular(real, "file", rel)
}

// packInRoot packs the context-dir cd (relative to realDir) once it resolves
// inside realRoot, rewriting pack errors to root-relative paths.
func packInRoot(realRoot, realDir, cd string) (string, error) {
	const field = "base.context-dir"
	real, err := resolveInRoot(realRoot, realDir, cd, field)
	if err != nil {
		return "", err
	}
	if fi, err := os.Lstat(real); err != nil || !fi.IsDir() {
		return "", fmt.Errorf("%s %q: not a directory", field, cd)
	}
	// PackContextDir opens .dockerignore (following a symlink) before its walk
	// refuses symlinks; refuse a non-regular one here so nothing outside root
	// is read.
	if fi, err := os.Lstat(filepath.Join(real, ".dockerignore")); err == nil && !fi.Mode().IsRegular() {
		return "", fmt.Errorf("%s %q: .dockerignore is not a regular file", field, cd)
	}
	b64, err := PackContextDir(real)
	if err != nil {
		msg := strings.ReplaceAll(err.Error(), realRoot+string(filepath.Separator), "")
		return "", fmt.Errorf("%s %q: %s", field, cd, strings.ReplaceAll(msg, realRoot, "."))
	}
	return b64, nil
}

// resolveInRoot resolves rel against base (a real path inside realRoot) and
// returns the real path, or an error naming field and rel when rel is
// absolute or does not resolve to a path inside realRoot.
func resolveInRoot(realRoot, base, rel, field string) (string, error) {
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("%s: must be a relative path", field)
	}
	outside := fmt.Errorf("%s %q: does not resolve to a path inside the checkout", field, rel)
	p := filepath.Join(base, rel)
	if !within(realRoot, p) {
		return "", outside
	}
	real, err := filepath.EvalSymlinks(p)
	if err != nil || !within(realRoot, real) {
		return "", outside
	}
	return real, nil
}

// within reports whether p (clean, absolute) is root or lies beneath it.
func within(root, p string) bool {
	r, err := filepath.Rel(root, p)
	return err == nil && (r == "." || filepath.IsLocal(r))
}

// readRegular reads real (already resolved and contained), refusing anything
// but a regular file and checking the opened file is the one inspected, so a
// swap between check and open is caught.
func readRegular(real, field, rel string) ([]byte, error) {
	fi, err := os.Lstat(real)
	if err != nil || !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s %q: not a regular file", field, rel)
	}
	f, err := os.Open(real)
	if err != nil {
		return nil, fmt.Errorf("%s %q: cannot be read", field, rel)
	}
	defer f.Close()
	ofi, err := f.Stat()
	if err != nil || !os.SameFile(fi, ofi) {
		return nil, fmt.Errorf("%s %q: changed while being read", field, rel)
	}
	var buf bytes.Buffer
	n, err := io.Copy(&buf, io.LimitReader(f, maxRootFile+1))
	if err != nil {
		return nil, fmt.Errorf("%s %q: cannot be read", field, rel)
	}
	if n > maxRootFile {
		return nil, fmt.Errorf("%s %q: larger than %d bytes", field, rel, maxRootFile)
	}
	return buf.Bytes(), nil
}
