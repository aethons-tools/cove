package kittree

import (
	"archive/tar"
	"fmt"
	"io"
	"path"
	"slices"

	"github.com/go-git/go-billy/v5"
)

// ContextTar writes the build context in fsys (a kit version's billy view):
// every regular file but the root kit.yml, as a deterministic uncompressed
// tar (sorted by path, epoch mtime, uid/gid 0, the stored mode), for `docker
// build -`.
func ContextTar(w io.Writer, fsys billy.Filesystem) error {
	var files []string
	var walk func(dir string) error
	walk = func(dir string) error {
		ents, err := fsys.ReadDir(dir)
		if err != nil {
			return fmt.Errorf("build context: %s: cannot be read", "/"+dir)
		}
		for _, e := range ents {
			p := path.Join(dir, e.Name())
			switch {
			case e.IsDir():
				if err := walk(p); err != nil {
					return err
				}
			case p != KitFile:
				files = append(files, p)
			}
		}
		return nil
	}
	if err := walk(""); err != nil {
		return err
	}
	slices.Sort(files)
	tw := tar.NewWriter(w)
	for _, p := range files {
		fi, err := fsys.Stat(p)
		if err != nil {
			return fmt.Errorf("build context: %s: %w", p, err)
		}
		f, err := fsys.Open(p)
		if err != nil {
			return fmt.Errorf("build context: %w", err)
		}
		data, err := io.ReadAll(f)
		f.Close()
		if err != nil {
			return fmt.Errorf("build context: %s: cannot be read", p)
		}
		if err := writeEntry(tw, p, fi.Mode().Perm(), data); err != nil {
			return err
		}
	}
	return tw.Close()
}
