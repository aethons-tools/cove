package studio

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// maxRootFile caps one file read out of a root (a kit.yml). Generous: the
// encoded context cap is the largest thing a kit.yml can carry.
const maxRootFile = 4 << 20

// maxNoteFileBytes caps the note files ResolveInRoot reads for one kit, in
// total.
const maxNoteFileBytes = 1 << 20

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
	fsRoot, err := os.OpenRoot(realRoot)
	if err != nil {
		return errors.New("kit root cannot be opened")
	}
	defer fsRoot.Close()
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
	budget := int64(maxNoteFileBytes)
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
		raw, err := readRegular(fsRoot, realRoot, real, field, n.File, budget)
		if err != nil {
			if errors.Is(err, errTooLarge) {
				return fmt.Errorf("notes: note files exceed the %d-byte total cap (at note %q)", maxNoteFileBytes, n.Name)
			}
			return err
		}
		budget -= int64(len(raw))
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
	fsRoot, err := os.OpenRoot(realRoot)
	if err != nil {
		return nil, errors.New("root cannot be opened")
	}
	defer fsRoot.Close()
	real, err := resolveInRoot(realRoot, realRoot, rel, "file")
	if err != nil {
		return nil, err
	}
	raw, err := readRegular(fsRoot, realRoot, real, "file", rel, maxRootFile)
	if errors.Is(err, errTooLarge) {
		return nil, fmt.Errorf("file %q: larger than %d bytes", rel, maxRootFile)
	}
	return raw, err
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
	// PackContextDir stats the root Dockerfile and opens .dockerignore, both
	// following symlinks, before its walk refuses symlinks: refuse non-regular
	// ones here, so nothing outside root is read and no error depends on what
	// exists outside it.
	for _, name := range []string{"Dockerfile", ".dockerignore"} {
		if fi, err := os.Lstat(filepath.Join(real, name)); err == nil && !fi.Mode().IsRegular() {
			return "", fmt.Errorf("%s %q: %s is not a regular file", field, cd, name)
		}
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
	reason := "does not resolve to a path inside the checkout"
	if field == "file" { // ReadFileInRoot: a missing file reads the same as an escaping one
		reason = "is missing or resolves outside the checkout"
	}
	outside := fmt.Errorf("%s %q: %s", field, rel, reason)
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

// errTooLarge reports a file over readRegular's cap.
var errTooLarge = errors.New("file too large")

// readRegular reads real (already resolved and contained) through fsRoot, so
// a symlink swapped in after the check still cannot leave root. It opens
// non-blocking (a FIFO swapped in cannot hang it) and refuses anything but a
// regular file, judged on the opened file. Over max bytes it returns
// errTooLarge.
func readRegular(fsRoot *os.Root, realRoot, real, field, rel string, max int64) ([]byte, error) {
	inRoot, err := filepath.Rel(realRoot, real)
	if err != nil {
		return nil, fmt.Errorf("%s %q: cannot be read", field, rel)
	}
	f, err := fsRoot.OpenFile(inRoot, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("%s %q: cannot be read", field, rel)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s %q: not a regular file", field, rel)
	}
	var buf bytes.Buffer
	n, err := io.Copy(&buf, io.LimitReader(f, max+1))
	if err != nil {
		return nil, fmt.Errorf("%s %q: cannot be read", field, rel)
	}
	if n > max {
		return nil, errTooLarge
	}
	return buf.Bytes(), nil
}
