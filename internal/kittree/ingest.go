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
// root) of an untrusted tree into files, in walk order (each directory's
// entries by name, a directory's files where its name sorts; Build sorts by
// path).
//
// Its .dockerignore is applied as the walk goes: an ignored path may be
// statted (a failed stat skips it), never opened or read, counted against the
// tree limits or name-checked, so an ignored unsafe name is not refused (with
// or without "!" rules). The one exception: an ignored directory is still
// walked when a "!" rule could re-include something beneath it. Symlinks (the
// kit directory and its parents included), hard links and irregular files are
// refused, modes are normalized and the tree limits hold, enforced as the
// walk reads. Directories may nest at most 64 deep, and one directory may hold
// at most 4×MaxEntries names (ignored ones included), read in batches.
//
// The walk holds each directory open and opens every name in it relative to
// that handle, comparing what it opened with what it listed (os.SameFile), so
// a symlink or other file swapped in after the check is refused and a
// directory swapped for a symlink is never followed.
//
// Errors name the failing path as dir/rel (when the name is safe; else the
// kit dir), the limit errors as "dir: …", and never echo content or an unsafe
// name.
func ReadDir(root *os.Root, dir string) ([]File, error) {
	dir = path.Clean(dir)
	if dir != "." {
		if err := CheckPath(dir); err != nil {
			return nil, err
		}
	}
	kit, err := openDirChain(root, dir)
	if err != nil {
		return nil, err
	}
	defer kit.Close()
	w := &dirWalk{dir: dir}
	ignoreData, ignoreMode, err := w.readIgnorer(kit)
	if err != nil {
		return nil, err
	}
	w.ignoreData, w.ignoreMode = ignoreData, ignoreMode
	if err := w.walk(kit, "", 0); err != nil {
		return nil, err
	}
	return w.files, nil
}

// dirWalk is one ReadDir in progress.
type dirWalk struct {
	dir        string
	ig         Ignorer
	ignoreData []byte // the root .dockerignore as read (nil: none)
	ignoreMode fs.FileMode
	files      []File
	total      int64
}

// show is rel's display path: dir/rel, or rel for the root.
func (w *dirWalk) show(rel string) string {
	if w.dir == "." {
		return rel
	}
	if rel == "" {
		return w.dir
	}
	return w.dir + "/" + rel
}

// at is where an error about rel is located: rel's display path when its
// name is safe, else the kit dir.
func (w *dirWalk) at(rel string) string {
	if rel != "" && CheckPath(rel) != nil {
		return w.dir
	}
	return w.show(rel)
}

// maxDepth caps how deep directories nest below the kit dir.
const maxDepth = 64

// walk reads the directory open as r, at rel ("" for the kit dir), depth
// directories below the kit dir.
func (w *dirWalk) walk(r *os.Root, rel string, depth int) error {
	if depth > maxDepth {
		return inDir(w.dir, fmt.Errorf("directories nested deeper than %d", maxDepth))
	}
	ents, err := listDir(r, 4*MaxEntries)
	if errors.Is(err, errTooManyNames) {
		return fmt.Errorf("%s: more than %d entries", w.at(rel), 4*MaxEntries)
	}
	if err != nil {
		return fmt.Errorf("%s: cannot be read", w.at(rel))
	}
	hook("listed", w.show(rel))
	for _, name := range ents {
		p := path.Join(rel, name)
		ignored, err := w.ig.Ignored(p)
		if err != nil {
			return ignoreErr(w.dir, err)
		}
		fi, err := r.Lstat(name)
		if err != nil {
			if ignored {
				continue
			}
			return fmt.Errorf("%s: cannot be read", w.at(p))
		}
		if ignored && (!fi.IsDir() || w.ig.Prunes()) {
			continue
		}
		if !ignored {
			if err := CheckPath(p); err != nil {
				return inDir(w.dir, err)
			}
		}
		switch {
		case fi.Mode()&fs.ModeSymlink != 0:
			return fmt.Errorf("%s: symlinks are not allowed", w.show(p))
		case fi.IsDir():
			// An ignored directory is still walked when a "!" rule could
			// re-include something beneath it.
			sub, err := openSub(r, name, fi, w.show(p))
			if err != nil {
				return fmt.Errorf("%s: %w", w.at(p), err)
			}
			err = w.walk(sub, p, depth+1)
			sub.Close()
			if err != nil {
				return err
			}
			continue
		case !fi.Mode().IsRegular():
			return fmt.Errorf("%s: not a regular file", w.show(p))
		}
		if len(w.files) == MaxEntries {
			return inDir(w.dir, tooManyFiles(MaxEntries+1))
		}
		var data []byte
		var mode fs.FileMode
		if p == dockerignoreFile {
			data, mode = w.ignoreData, w.ignoreMode
		} else if data, mode, err = readRegular(r, name, fi, w.show(p)); err != nil {
			return err
		}
		if w.total += int64(len(data)); w.total > MaxTotal {
			return inDir(w.dir, treeTooLarge())
		}
		w.files = append(w.files, File{Path: p, Mode: NormMode(mode), Data: data})
	}
	return nil
}

var errTooManyNames = errors.New("too many names")

// listDir lists the directory open as r, by name, reading the names in
// batches and stopping with errTooManyNames past max (ignored names count
// too), so one huge directory cannot exhaust memory.
func listDir(r *os.Root, max int) ([]string, error) {
	f, err := r.Open(".")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var names []string
	for {
		batch, err := f.Readdirnames(256)
		names = append(names, batch...)
		if len(names) > max {
			return nil, errTooManyNames
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	slices.Sort(names)
	return names, nil
}

// openSub opens the directory name in r, which must be the directory want
// that r listed: a symlink or other directory swapped in is refused as "not a
// directory", an open failure is "cannot be read".
func openSub(r *os.Root, name string, want fs.FileInfo, p string) (*os.Root, error) {
	hook("opendir", p)
	sub, err := r.OpenRoot(name)
	if err != nil {
		return nil, errors.New("cannot be read")
	}
	got, err := sub.Stat(".")
	if err != nil || !os.SameFile(got, want) {
		sub.Close()
		return nil, errors.New("not a directory")
	}
	return sub, nil
}

// openDirChain opens dir under root one name at a time, each a real
// directory (not a symlink, which os.Root would follow within the root).
func openDirChain(root *os.Root, dir string) (*os.Root, error) {
	cur, err := root.OpenRoot(".")
	if err != nil {
		return nil, fmt.Errorf("%s: not a directory", dir)
	}
	if dir == "." {
		return cur, nil
	}
	for _, name := range strings.Split(dir, "/") {
		fi, err := cur.Lstat(name)
		if err != nil || !fi.IsDir() {
			cur.Close()
			return nil, fmt.Errorf("%s: not a directory", dir)
		}
		sub, err := openSub(cur, name, fi, dir)
		cur.Close()
		if err != nil {
			return nil, fmt.Errorf("%s: not a directory", dir)
		}
		cur = sub
	}
	return cur, nil
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

// readIgnorer reads the kit dir's .dockerignore (open as kit; a regular file
// of at most MaxFile bytes; absent: nothing is ignored) into w.ig, returning
// its bytes and mode for the walk to reuse.
func (w *dirWalk) readIgnorer(kit *os.Root) ([]byte, fs.FileMode, error) {
	fi, err := kit.Lstat(dockerignoreFile)
	if errors.Is(err, fs.ErrNotExist) {
		w.ig, err = NewIgnorer(nil)
		return nil, 0, err
	}
	p := w.show(dockerignoreFile)
	if err != nil || !fi.Mode().IsRegular() {
		return nil, 0, fmt.Errorf("%s: not a regular file", p)
	}
	data, mode, err := readRegular(kit, dockerignoreFile, fi, p)
	if err != nil {
		return nil, 0, err
	}
	if w.ig, err = NewIgnorer(data); err != nil {
		return nil, 0, ignoreErr(w.dir, err)
	}
	return data, mode, nil
}

// readRegular reads name in the directory open as r, which must be the file
// want that r listed: anything else (a symlink or file swapped in after the
// check), an irregular file, a hard-linked file, or over MaxFile, is refused.
// It opens non-blocking where the platform has FIFOs (a FIFO swapped in
// cannot hang it) and reads at most MaxFile+1 bytes. Errors name p.
func readRegular(r *os.Root, name string, want fs.FileInfo, p string) ([]byte, fs.FileMode, error) {
	hook("open", p)
	f, err := r.OpenFile(name, os.O_RDONLY|openFlags, 0)
	if err != nil {
		return nil, 0, fmt.Errorf("%s: cannot be read", p)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() || !os.SameFile(fi, want) {
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

// testHook, when set by a test, runs at the walk's race windows with the
// display path: "open" between a file's Lstat and its open, "opendir"
// between a directory's Lstat and its open, "listed" after a directory is
// listed.
var testHook func(at, p string)

func hook(at, p string) {
	if testHook != nil {
		testHook(at, p)
	}
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
// does not apply .dockerignore (Ignore does) and does not cap the body's
// bytes: the caller does, at MaxUpload (http.MaxBytesReader; its error is
// kept as the cause of "not a readable tar"). Files come in tar order.
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
