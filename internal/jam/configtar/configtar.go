// Package configtar packs a config repo tree for upload to Jam's admin API
// (POST /admin/config/diff and /apply) and unpacks it on the serve side,
// refusing anything but regular files and directories under the root and
// capping the upload, the unpacked bytes and the entry count.
package configtar

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// The upload limits (the kit context limits scaled to 16 MiB).
const (
	MaxUpload   = 16 << 20 // gzip body bytes
	MaxUnpacked = 16 << 20 // total file bytes once unpacked
	MaxEntries  = 2000     // tar entries, files and directories
	// MaxDecompressed bounds the decompressed tar stream: the file bytes,
	// plus 2 KiB of headers and padding per entry (a PAX long name
	// included), plus 1 MiB of slack. Special headers are not entries, so
	// without it a chain of them would decompress without bound.
	MaxDecompressed = MaxUnpacked + MaxEntries*2048 + 1<<20
)

// ErrTooLarge is an archive over one of the limits.
var ErrTooLarge = errors.New("config tree too large")

// Pack archives the tree at dir as a tar.gz: regular files and directories in
// lexical order, skipping any .git. A symlink or other irregular file is an
// error. The output is deterministic: zero times and ids, modes reduced to
// perm&0o755 with an owner read/write floor (0o644).
func Pack(dir string) ([]byte, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if d.Name() == ".git" {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		name := filepath.ToSlash(rel)
		if !d.IsDir() && !d.Type().IsRegular() {
			return fmt.Errorf("config tree: %s is a symlink or not a regular file", name)
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		h := &tar.Header{Name: name, Mode: int64(fi.Mode().Perm()&0o755 | 0o644), Format: tar.FormatPAX}
		if d.IsDir() {
			h.Typeflag, h.Name = tar.TypeDir, name+"/"
			return tw.WriteHeader(h)
		}
		h.Typeflag, h.Size = tar.TypeReg, fi.Size()
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		_, err = io.CopyN(tw, f, h.Size)
		return err
	})
	if err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Unpack extracts the tar.gz in r under dest (an existing directory), through
// an os.Root so nothing lands outside it. An absolute name, a name with a ..
// component, and any entry but a regular file or directory are refused; more
// than MaxEntries entries or MaxUnpacked file bytes is ErrTooLarge. On error
// dest may hold a partial tree; the caller discards it.
func Unpack(r io.Reader, dest string) error {
	zr, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("config tree: not a gzip archive: %w", err)
	}
	defer zr.Close()
	root, err := os.OpenRoot(dest)
	if err != nil {
		return err
	}
	defer root.Close()
	tr := tar.NewReader(&capReader{r: zr, left: MaxDecompressed})
	var entries int
	var left int64 = MaxUnpacked
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("config tree: bad archive: %w", err)
		}
		if entries++; entries > MaxEntries {
			return fmt.Errorf("%w: more than %d entries", ErrTooLarge, MaxEntries)
		}
		name, err := entryName(h.Name)
		if err != nil {
			return err
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if name == "." {
				continue
			}
			if err := root.MkdirAll(name, 0o755); err != nil {
				return fmt.Errorf("config tree: %s: %w", name, err)
			}
		case tar.TypeReg:
			if name == "." {
				return fmt.Errorf("config tree: an entry has an empty name")
			}
			if h.Size > left {
				return fmt.Errorf("%w: more than %d bytes unpacked", ErrTooLarge, MaxUnpacked)
			}
			n, err := writeFile(root, name, os.FileMode(h.Mode)&0o755|0o600, tr, left)
			if err != nil {
				return err
			}
			left -= n
		default:
			return fmt.Errorf("config tree: %q is a symlink or not a regular file", name)
		}
	}
}

// entryName validates a tar name and returns it as a local, slash-separated
// path ("." for the root).
func entryName(raw string) (string, error) {
	name := strings.TrimSuffix(raw, "/")
	if name == "" || name == "." || name == "./" {
		return ".", nil
	}
	if path.IsAbs(name) || strings.HasPrefix(raw, `\`) || slices.Contains(strings.Split(name, "/"), "..") ||
		!filepath.IsLocal(filepath.FromSlash(name)) {
		return "", fmt.Errorf("config tree: entry %q is not a path inside the tree", raw)
	}
	return path.Clean(name), nil
}

// writeFile writes r to name under root at mode (past the umask, over an
// existing file's mode), reading at most left+1 bytes so an
// entry larger than its header claims still trips the cap.
func writeFile(root *os.Root, name string, mode os.FileMode, r io.Reader, left int64) (int64, error) {
	if dir := path.Dir(name); dir != "." {
		if err := root.MkdirAll(dir, 0o755); err != nil {
			return 0, fmt.Errorf("config tree: %s: %w", name, err)
		}
	}
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return 0, fmt.Errorf("config tree: %s: %w", name, err)
	}
	n, err := io.Copy(f, io.LimitReader(r, left+1))
	if err == nil { // the mode past the umask: a kit's context-dir packs its files' modes
		err = f.Chmod(mode)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return n, fmt.Errorf("config tree: %s: %w", name, err)
	}
	if n > left {
		return n, fmt.Errorf("%w: more than %d bytes unpacked", ErrTooLarge, MaxUnpacked)
	}
	return n, nil
}

// capReader reads r until left bytes are read, then fails with ErrTooLarge
// (an io.LimitReader's EOF would read as a truncated archive).
type capReader struct {
	r    io.Reader
	left int64
}

func (c *capReader) Read(p []byte) (int, error) {
	if c.left <= 0 {
		return 0, fmt.Errorf("%w: more than %d bytes decompressed", ErrTooLarge, MaxDecompressed)
	}
	if int64(len(p)) > c.left {
		p = p[:c.left]
	}
	n, err := c.r.Read(p)
	c.left -= int64(n)
	return n, err
}
