package jam

import (
	"errors"
	"fmt"
	"io"
	iofs "io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/aethons-tools/cove/internal/kittree"
	"github.com/aethons-tools/cove/internal/studio"
)

// Writing a config tree into a checkout (jam config write, and a branch
// save: config_branches.go).

// WriteConfigTree writes exp.Files, then each exp.Kits dir's files at their
// modes, removes exp.Remove, and mirrors each kit dir (mirrorKitDir). Every
// path is checked before anything is written: Files and Remove stay inside
// dir, a kit dir is kits/<name>, its files have safe paths and mode 0644 or
// 0755, no path is written twice, and kits/ and each kits/<name> on disk,
// when present, is a real directory (never a symlink a write could follow).
func WriteConfigTree(dir string, exp ConfigTreeExport) error {
	for p := range exp.Files {
		if !filepath.IsLocal(p) {
			return fmt.Errorf("refusing to write %q outside %s", p, dir)
		}
	}
	for d, files := range exp.Kits {
		if !isKitDir(d) {
			return fmt.Errorf("refusing to write kit dir %q: not kits/<name>", d)
		}
		for p := range exp.Files {
			if c := path.Clean(p); c == d || strings.HasPrefix(c, d+"/") {
				return fmt.Errorf("refusing to write %q twice (a file and a kit dir's)", p)
			}
		}
		for _, p := range exp.Remove {
			if c := path.Clean(p); c == d || strings.HasPrefix(c, d+"/") {
				return fmt.Errorf("refusing to remove %q inside the mirrored kit dir %s", p, d)
			}
		}
		paths := make(map[string]bool, len(files))
		for _, f := range files {
			if kittree.CheckPath(f.Path) != nil {
				return fmt.Errorf("refusing to write an unsafe path under %s", d)
			}
			if f.Mode != 0o644 && f.Mode != 0o755 {
				return fmt.Errorf("refusing to write %s/%s with mode %o", d, f.Path, f.Mode)
			}
			if paths[f.Path] {
				return fmt.Errorf("refusing to write %q twice", d+"/"+f.Path)
			}
			paths[f.Path] = true
		}
		for p := range paths {
			for a := path.Dir(p); a != "."; a = path.Dir(a) {
				if paths[a] {
					return fmt.Errorf("refusing to write %q: it conflicts with %q", d+"/"+p, d+"/"+a)
				}
			}
		}
	}
	for _, p := range exp.Remove {
		if !filepath.IsLocal(p) {
			return fmt.Errorf("refusing to remove %q outside %s", p, dir)
		}
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, d := range append([]string{"kits"}, slices.Sorted(maps.Keys(exp.Kits))...) {
		fi, err := root.Lstat(filepath.FromSlash(d))
		if err == nil && !fi.IsDir() {
			return fmt.Errorf("refusing to write %s: it is not a directory", d)
		}
		if err != nil && !errors.Is(err, iofs.ErrNotExist) {
			return err
		}
	}
	for p := range exp.Files {
		if err := checkWritePath(root, path.Clean(p)); err != nil {
			return err
		}
	}
	for d, files := range exp.Kits {
		for _, f := range files {
			if err := checkWritePath(root, d+"/"+f.Path); err != nil {
				return err
			}
		}
	}
	for p, content := range exp.Files {
		if err := writeTreeFile(root, p, []byte(content), 0o644); err != nil {
			return err
		}
	}
	for d, files := range exp.Kits {
		for _, f := range files {
			if err := writeTreeFile(root, d+"/"+f.Path, f.Data, os.FileMode(f.Mode)); err != nil {
				return err
			}
		}
	}
	for _, p := range exp.Remove {
		if err := root.Remove(filepath.FromSlash(p)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	for _, d := range slices.Sorted(maps.Keys(exp.Kits)) {
		files := exp.Kits[d]
		keep := map[string]bool{}
		var ignore *[]byte // nil: nothing exported here, read the dir's own .dockerignore
		if len(files) > 0 {
			empty := []byte{}
			ignore = &empty // exported without a .dockerignore: nothing on disk is excluded
		}
		for _, f := range files {
			keep[d+"/"+f.Path] = true
			if f.Path == ".dockerignore" {
				data := f.Data
				ignore = &data
			}
		}
		if err := mirrorKitDir(root, d, keep, ignore); err != nil {
			return err
		}
	}
	return nil
}

// MaxDockerignoreBytes caps an on-disk .dockerignore Prune reads.
const MaxDockerignoreBytes = 64 << 10

// readTreeDockerignore reads the .dockerignore at p in root: nil, true when
// there is none; ok=false when it is not a regular file (never followed) or
// is over MaxDockerignoreBytes.
func readTreeDockerignore(root *os.Root, p string) (data []byte, ok bool) {
	name := filepath.FromSlash(p)
	fi, err := root.Lstat(name)
	switch {
	case errors.Is(err, iofs.ErrNotExist):
		return nil, true
	case err != nil || !fi.Mode().IsRegular() || fi.Size() > MaxDockerignoreBytes:
		return nil, false
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	data, err = io.ReadAll(io.LimitReader(f, MaxDockerignoreBytes+1))
	if err != nil || len(data) > MaxDockerignoreBytes {
		return nil, false
	}
	return data, true
}

// writeTreeFile writes data at p in root with mode, past the umask and over
// an existing file's mode; through root, nothing lands outside the tree.
func writeTreeFile(root *os.Root, p string, data []byte, mode os.FileMode) error {
	name := filepath.FromSlash(p)
	if err := root.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		return err
	}
	// os.Root follows a symlink that stays inside the tree: replace one (or
	// any other non-regular file) at p instead of writing through it.
	if fi, err := root.Lstat(name); err == nil && !fi.Mode().IsRegular() {
		if fi.IsDir() {
			return fmt.Errorf("refusing to write %q: it is a directory", p)
		}
		if err := root.Remove(name); err != nil {
			return err
		}
	}
	if err := root.WriteFile(name, data, mode); err != nil {
		return err
	}
	return root.Chmod(name, mode)
}

// checkWritePath refuses to write p (slash, clean, local) when a directory
// on its way is, on disk, a symlink or not a directory (os.Root would follow
// an in-tree symlink onto another file), or p itself is a directory. A
// symlink at p itself is replaced by writeTreeFile.
func checkWritePath(root *os.Root, p string) error {
	segs := strings.Split(p, "/")
	for i := range segs {
		q := strings.Join(segs[:i+1], "/")
		fi, err := root.Lstat(filepath.FromSlash(q))
		if errors.Is(err, iofs.ErrNotExist) {
			return nil // nothing deeper exists either
		}
		if err != nil {
			return fmt.Errorf("refusing to write %q: %q cannot be read", p, q)
		}
		last := i == len(segs)-1
		if !last && !fi.IsDir() {
			return fmt.Errorf("refusing to write %q: %q is not a directory", p, q)
		}
		if last && fi.IsDir() {
			return fmt.Errorf("refusing to write %q: it is a directory", p)
		}
	}
	return nil
}

// isKitDir reports whether p is kits/<name>, name a tag-safe kit name.
func isKitDir(p string) bool {
	segs := strings.Split(p, "/")
	return len(segs) == 2 && segs[0] == "kits" && studio.TagSafeName(segs[1])
}

// mirrorKitDir removes every file under the kit dir p (in root) that keep
// does not hold and the dir's .dockerignore (ignore, as kittree reads it)
// does not exclude, then the dirs left empty, p included (kits/ stays). It
// only descends real dirs: kits/ or a kit dir that is a symlink is refused,
// and a symlink inside p is removed, never followed. kit.yml, Dockerfile and
// .dockerignore are never excluded, so they go unless kept.
//
// ignore nil means no files were exported for p (its kit is now written by
// reference, or gone): p's own .dockerignore then says what Jam never
// stored, so those files — and it — stay; one that is not a regular file
// or is over MaxDockerignoreBytes leaves p as it is.
func mirrorKitDir(root *os.Root, p string, keep map[string]bool, ignore *[]byte) error {
	for _, d := range []string{"kits", p} {
		fi, err := root.Lstat(d)
		if errors.Is(err, iofs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if !fi.IsDir() {
			return fmt.Errorf("refusing to mirror %q: %s is not a directory", p, d)
		}
	}
	var rules []byte
	if ignore != nil {
		rules = *ignore
	} else {
		var ok bool
		if rules, ok = readTreeDockerignore(root, p+"/.dockerignore"); !ok {
			return nil
		}
		if rules != nil {
			keep = maps.Clone(keep)
			keep[p+"/.dockerignore"] = true
		}
	}
	ig, err := kittree.NewIgnorer(rules)
	if err != nil {
		return fmt.Errorf("refusing to mirror %q: its .dockerignore cannot be read", p)
	}
	var dirs []string
	err = iofs.WalkDir(root.FS(), p, func(q string, d iofs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if q != p {
			ignored, merr := ig.Ignored(strings.TrimPrefix(q, p+"/"))
			if merr != nil {
				return merr
			}
			if ignored && d.IsDir() && ig.Prunes() {
				return iofs.SkipDir
			}
			if ignored && !d.IsDir() {
				return nil
			}
		}
		if d.IsDir() {
			dirs = append(dirs, q)
			return nil
		}
		if keep[q] {
			return nil
		}
		return root.Remove(q)
	})
	if err != nil {
		return err
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		_ = root.Remove(dirs[i]) // only an empty dir goes
	}
	return nil
}
