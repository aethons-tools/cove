package studio

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// maxRootFile caps one file read out of a root (a kit.yml).
const maxRootFile = 4 << 20

// maxNoteFileBytes caps the note files ParseKitFS reads for one kit, in
// total.
const maxNoteFileBytes = 1 << 20

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
