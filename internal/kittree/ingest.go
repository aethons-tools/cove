package kittree

import (
	"archive/tar"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/moby/patternmatcher"
	"github.com/moby/patternmatcher/ignorefile"
)

// dockerignoreFile is the ignore file at a tree's root.
const dockerignoreFile = ".dockerignore"

// DockerignoreMatcher parses .dockerignore rules exactly as docker build
// reads them.
func DockerignoreMatcher(r io.Reader) (*patternmatcher.PatternMatcher, error) {
	patterns, err := ignorefile.ReadAll(r)
	if err != nil {
		return nil, err
	}
	return patternmatcher.New(patterns)
}

// Ignorer is a root .dockerignore.
type Ignorer struct {
	pm *patternmatcher.PatternMatcher
}

// NewIgnorer parses rules; nil rules ignore nothing.
func NewIgnorer(rules []byte) (Ignorer, error) {
	pm, err := DockerignoreMatcher(bytes.NewReader(rules))
	if err != nil {
		return Ignorer{}, errors.New(".dockerignore: cannot be parsed")
	}
	return Ignorer{pm: pm}, nil
}

// Ignored reports whether rel (slash, relative to the root) is excluded.
// kit.yml, Dockerfile and .dockerignore at the root never are.
func (ig Ignorer) Ignored(rel string) (bool, error) {
	if rel == KitFile || rel == Dockerfile || rel == dockerignoreFile {
		return false, nil
	}
	ok, err := ig.pm.MatchesOrParentMatches(rel)
	if err != nil {
		return false, errors.New(".dockerignore: cannot be applied")
	}
	return ok, nil
}

// Prunes reports whether an ignored directory can be skipped whole: no `!`
// rule could re-include something beneath it.
func (ig Ignorer) Prunes() bool { return !ig.pm.Exclusions() }

// Ignore drops the files the .dockerignore among files excludes.
func Ignore(files []File) ([]File, error) {
	var rules []byte
	for _, f := range files {
		if f.Path == dockerignoreFile {
			rules = f.Data
		}
	}
	ig, err := NewIgnorer(rules)
	if err != nil {
		return nil, err
	}
	out := make([]File, 0, len(files))
	for _, f := range files {
		ignored, err := ig.Ignored(f.Path)
		if err != nil {
			return nil, err
		}
		if !ignored {
			out = append(out, f)
		}
	}
	return out, nil
}

// ReadDir reads the kit directory dir (slash, relative to root; "." for the
// root) of an untrusted tree into files. Its .dockerignore is applied as the
// walk goes, so an ignored file is never read nor counted. Symlinks (the kit
// directory and its parents included), hard links and irregular files are
// refused, modes are normalized and the tree limits hold, enforced as the walk
// reads. Errors name paths as dir/rel (the limit errors as "dir: …") and never
// echo content or an unsafe name.
func ReadDir(root *os.Root, dir string) ([]File, error) {
	dir = path.Clean(dir)
	if dir != "." {
		if err := CheckPath(dir); err != nil {
			return nil, err
		}
	}
	if err := lstatDirs(root, dir); err != nil {
		return nil, err
	}
	ig, err := readIgnorer(root, dir)
	if err != nil {
		return nil, err
	}
	var files []File
	var total int64
	err = fs.WalkDir(root.FS(), dir, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return fmt.Errorf("%s: cannot be read", dir)
		}
		if p == dir {
			return nil
		}
		rel := p
		if dir != "." {
			rel = strings.TrimPrefix(p, dir+"/")
		}
		if err := CheckPath(rel); err != nil {
			return inDir(dir, err)
		}
		ignored, err := ig.Ignored(rel)
		if err != nil {
			return ignoreErr(dir, err)
		}
		if ignored {
			if d.IsDir() && ig.Prunes() {
				return fs.SkipDir
			}
			return nil
		}
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			return fmt.Errorf("%s: symlinks are not allowed", p)
		case d.IsDir():
			return nil
		case !d.Type().IsRegular():
			return fmt.Errorf("%s: not a regular file", p)
		}
		if len(files) == MaxEntries {
			return inDir(dir, tooManyFiles(MaxEntries+1))
		}
		data, mode, err := readRegular(root, p)
		if err != nil {
			return err
		}
		if total += int64(len(data)); total > MaxTotal {
			return inDir(dir, treeTooLarge())
		}
		files = append(files, File{Path: rel, Mode: NormMode(mode), Data: data})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

// inDir locates err at dir: "dir: err", or err alone for the root.
func inDir(dir string, err error) error {
	if dir == "." {
		return err
	}
	return fmt.Errorf("%s: %w", dir, err)
}

// ignoreErr locates a ".dockerignore: …" error at dir's .dockerignore.
func ignoreErr(dir string, err error) error {
	if dir == "." {
		return err
	}
	return fmt.Errorf("%s/%w", dir, err)
}

// lstatDirs requires dir and each of its parents to be a real directory, not
// a symlink (which os.Root would otherwise follow within the root).
func lstatDirs(root *os.Root, dir string) error {
	if dir == "." {
		return nil
	}
	parts := strings.Split(dir, "/")
	for i := range parts {
		fi, err := root.Lstat(filepath.FromSlash(strings.Join(parts[:i+1], "/")))
		if err != nil || !fi.IsDir() {
			return fmt.Errorf("%s: not a directory", dir)
		}
	}
	return nil
}

// readIgnorer reads dir's .dockerignore (a regular file of at most MaxFile
// bytes; absent: nothing is ignored).
func readIgnorer(root *os.Root, dir string) (Ignorer, error) {
	p := path.Join(dir, dockerignoreFile)
	fi, err := root.Lstat(filepath.FromSlash(p))
	if errors.Is(err, fs.ErrNotExist) {
		return NewIgnorer(nil)
	}
	if err != nil || !fi.Mode().IsRegular() {
		return Ignorer{}, fmt.Errorf("%s: not a regular file", p)
	}
	data, _, err := readRegular(root, p)
	if err != nil {
		return Ignorer{}, err
	}
	ig, err := NewIgnorer(data)
	if err != nil {
		return Ignorer{}, ignoreErr(dir, err)
	}
	return ig, nil
}

// readRegular reads p through root, non-blocking (a FIFO swapped in cannot
// hang it) and, where the platform allows, without following a final symlink,
// judging the opened file: anything but a regular file, a hard-linked file, or
// over MaxFile, is refused. At most MaxFile+1 bytes are read.
func readRegular(root *os.Root, p string) ([]byte, fs.FileMode, error) {
	f, err := root.OpenFile(filepath.FromSlash(p), os.O_RDONLY|openFlags, 0)
	if err != nil {
		return nil, 0, fmt.Errorf("%s: cannot be read", p)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return nil, 0, fmt.Errorf("%s: not a regular file", p)
	}
	if hardLinked(fi) {
		return nil, 0, fmt.Errorf("%s: hard links are not allowed", p)
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxFile+1))
	if err != nil {
		return nil, 0, fmt.Errorf("%s: cannot be read", p)
	}
	if int64(len(data)) > MaxFile {
		return nil, 0, fileTooLarge(p)
	}
	return data, fi.Mode(), nil
}

// MaxUpload caps a push body: the files, a header block per file with room
// for PAX records, and the end blocks.
func MaxUpload() int64 { return MaxTotal + int64(MaxEntries)*2048 + 1<<20 }

// tarError is a push body that cannot be read as a tar. Its text is fixed;
// it unwraps to the cause (a *http.MaxBytesError, say).
type tarError struct{ err error }

func (e *tarError) Error() string { return "not a readable tar" }
func (e *tarError) Unwrap() error { return e.err }

// ReadTar reads a push body: an uncompressed tar of regular files. A leading
// "./" is dropped and directory entries are skipped; links, devices and
// unsafe names are refused; the tree limits hold as the body streams. A pax
// global header (git archive writes one) carries no file and is skipped. It
// does not apply
// .dockerignore (Ignore does).
func ReadTar(r io.Reader) ([]File, error) {
	tr := tar.NewReader(r)
	var files []File
	var total int64
	seen := map[string]bool{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, &tarError{err}
		}
		name := strings.TrimPrefix(hdr.Name, "./")
		if hdr.Typeflag == tar.TypeDir || hdr.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		if err := CheckPath(name); err != nil {
			return nil, err
		}
		switch hdr.Typeflag {
		case tar.TypeReg:
		case tar.TypeSymlink:
			return nil, fmt.Errorf("%s: symlinks are not allowed", name)
		case tar.TypeLink:
			return nil, fmt.Errorf("%s: hard links are not allowed", name)
		default:
			return nil, fmt.Errorf("%s: not a regular file", name)
		}
		if seen[name] {
			return nil, fmt.Errorf("%s: appears twice", name)
		}
		seen[name] = true
		if len(files) == MaxEntries {
			return nil, tooManyFiles(MaxEntries + 1)
		}
		data, err := io.ReadAll(io.LimitReader(tr, MaxFile+1))
		if err != nil {
			return nil, &tarError{err}
		}
		if int64(len(data)) > MaxFile {
			return nil, fileTooLarge(name)
		}
		if total += int64(len(data)); total > MaxTotal {
			return nil, treeTooLarge()
		}
		files = append(files, File{Path: name, Mode: NormMode(fs.FileMode(hdr.Mode)), Data: data})
	}
	return files, nil
}

// WriteTar writes files as a deterministic uncompressed tar: sorted by path,
// epoch mtime, uid/gid 0, no user or group names, normalized modes.
func WriteTar(w io.Writer, files []File) error {
	sorted := slices.Clone(files)
	slices.SortFunc(sorted, func(a, b File) int { return strings.Compare(a.Path, b.Path) })
	tw := tar.NewWriter(w)
	for _, f := range sorted {
		if err := writeEntry(tw, f.Path, NormMode(f.Mode), f.Data); err != nil {
			return err
		}
	}
	return tw.Close()
}

// epoch is the fixed mtime of every entry: USTAR-representable, leaks no host time.
var epoch = time.Unix(0, 0)

func writeEntry(tw *tar.Writer, name string, mode fs.FileMode, data []byte) error {
	hdr := &tar.Header{Name: name, Mode: int64(mode.Perm()), Size: int64(len(data)), Typeflag: tar.TypeReg, ModTime: epoch}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err := tw.Write(data)
	return err
}
