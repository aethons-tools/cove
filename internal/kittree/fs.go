package kittree

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"time"

	"github.com/go-git/go-billy/v5"
)

// NewFS is a read-only billy view of t. Open, OpenFile (read-only), Stat,
// Lstat, ReadDir and Join read; every write returns billy.ErrReadOnly.
// Directories are implied by the file paths. A file's bytes are fetched from
// blobs when it is opened.
func NewFS(t Tree, blobs Blobs) billy.Filesystem {
	v := &treeFS{t: t, blobs: blobs, dirs: map[string][]string{"": nil}}
	for _, e := range t.Entries {
		// Add each path element to its parent directory's children, up to the root.
		for p := e.Path; ; {
			key := path.Dir(p)
			if key == "." {
				key = ""
			}
			if name := path.Base(p); !slices.Contains(v.dirs[key], name) {
				v.dirs[key] = append(v.dirs[key], name)
			}
			if key == "" {
				break
			}
			p = key
		}
	}
	for k := range v.dirs {
		slices.Sort(v.dirs[k])
	}
	return v
}

type treeFS struct {
	t     Tree
	blobs Blobs
	dirs  map[string][]string // dir ("" = root) → sorted child names
}

// clean maps "/a", "./a" and "a" to "a", the root to "".
func clean(name string) string {
	p := path.Clean("/" + filepath.ToSlash(name))
	return p[1:]
}

func (v *treeFS) Open(name string) (billy.File, error) { return v.OpenFile(name, os.O_RDONLY, 0) }

func (v *treeFS) OpenFile(name string, flag int, _ os.FileMode) (billy.File, error) {
	if flag&(os.O_WRONLY|os.O_RDWR|os.O_CREATE|os.O_APPEND|os.O_TRUNC) != 0 {
		return nil, billy.ErrReadOnly
	}
	p := clean(name)
	e, ok := v.t.Lookup(p)
	if !ok {
		if _, isDir := v.dirs[p]; isDir {
			return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
		}
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	b, ok := v.blobs(e.SHA256)
	if !ok {
		return nil, fmt.Errorf("%s: content missing from the store", p)
	}
	return &treeFile{name: name, Reader: bytes.NewReader(b)}, nil
}

func (v *treeFS) Stat(name string) (os.FileInfo, error) {
	p := clean(name)
	if e, ok := v.t.Lookup(p); ok {
		return fileInfo{name: path.Base(p), size: e.Size, mode: e.Mode}, nil
	}
	if _, ok := v.dirs[p]; ok {
		return fileInfo{name: path.Base("/" + p), mode: fs.ModeDir | 0o755}, nil
	}
	return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrNotExist}
}

func (v *treeFS) Lstat(name string) (os.FileInfo, error) { return v.Stat(name) }

func (v *treeFS) ReadDir(name string) ([]os.FileInfo, error) {
	p := clean(name)
	kids, ok := v.dirs[p]
	if !ok {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrNotExist}
	}
	out := make([]os.FileInfo, 0, len(kids))
	for _, k := range kids {
		fi, err := v.Stat(path.Join(p, k))
		if err != nil {
			return nil, err
		}
		out = append(out, fi)
	}
	return out, nil
}

func (v *treeFS) Join(elem ...string) string                  { return path.Join(elem...) }
func (v *treeFS) Create(string) (billy.File, error)           { return nil, billy.ErrReadOnly }
func (v *treeFS) Rename(string, string) error                 { return billy.ErrReadOnly }
func (v *treeFS) Remove(string) error                         { return billy.ErrReadOnly }
func (v *treeFS) TempFile(string, string) (billy.File, error) { return nil, billy.ErrReadOnly }
func (v *treeFS) MkdirAll(string, os.FileMode) error          { return billy.ErrReadOnly }
func (v *treeFS) Symlink(string, string) error                { return billy.ErrReadOnly }
func (v *treeFS) Readlink(name string) (string, error) {
	return "", &fs.PathError{Op: "readlink", Path: name, Err: fs.ErrInvalid}
}
func (v *treeFS) Chroot(string) (billy.Filesystem, error) { return nil, billy.ErrNotSupported }
func (v *treeFS) Root() string                            { return "/" }
func (v *treeFS) Capabilities() billy.Capability          { return billy.ReadCapability | billy.SeekCapability }

type treeFile struct {
	name string
	*bytes.Reader
}

func (f *treeFile) Name() string              { return f.name }
func (f *treeFile) Write([]byte) (int, error) { return 0, billy.ErrReadOnly }
func (f *treeFile) Close() error              { return nil }
func (f *treeFile) Lock() error               { return nil }
func (f *treeFile) Unlock() error             { return nil }
func (f *treeFile) Truncate(int64) error      { return billy.ErrReadOnly }

type fileInfo struct {
	name string
	size int64
	mode fs.FileMode
}

func (i fileInfo) Name() string       { return i.name }
func (i fileInfo) Size() int64        { return i.size }
func (i fileInfo) Mode() fs.FileMode  { return i.mode }
func (i fileInfo) ModTime() time.Time { return epoch }
func (i fileInfo) IsDir() bool        { return i.mode.IsDir() }
func (i fileInfo) Sys() any           { return nil }
