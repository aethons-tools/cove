package wire

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-billy/v5/util"

	"github.com/aethons-tools/cove/internal/harnessinstall"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/studio"
)

// File is one entry of a link's file tree: a regular file (Link == "") or a
// symlink (Link is its target).
type File struct {
	Path string
	Mode uint32
	Data []byte
	Link string
}

// KitLink is the wire form of jam.KitLink. Files == nil means the link's
// Files is nil. BuildContext is carried explicitly because studio tags
// StudioKit.BuildContext json:"-".
type KitLink struct {
	Ref          jam.KitRef
	Kit          studio.StudioKit
	BuildContext string
	Follow       bool
	Files        []File
}

// KitDef is the wire form of jam.KitDefinition.
type KitDef struct {
	Ref          jam.KitRef
	Kit          studio.StudioKit
	BuildContext string
	Chain        []KitLink
	Harness      harnessinstall.Install
}

// EncodeKitDefinition flattens def, reading every link's files (symlinks are
// recorded, not followed).
func EncodeKitDefinition(def jam.KitDefinition) (KitDef, error) {
	out := KitDef{Ref: def.Ref, Kit: def.Kit, BuildContext: def.Kit.BuildContext, Harness: def.Harness}
	for _, l := range def.Chain {
		files, err := encodeFiles(l.Files)
		if err != nil {
			return KitDef{}, fmt.Errorf("kit %s: %w", l.Ref.ID, err)
		}
		out.Chain = append(out.Chain, KitLink{Ref: l.Ref, Kit: l.Kit, BuildContext: l.Kit.BuildContext, Follow: l.Follow, Files: files})
	}
	return out, nil
}

func encodeFiles(fsys billy.Filesystem) ([]File, error) {
	if fsys == nil {
		return nil, nil
	}
	files := []File{}
	err := util.Walk(fsys, "", func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if fi.IsDir() {
			return nil
		}
		f := File{Path: filepath.ToSlash(p), Mode: uint32(fi.Mode().Perm())}
		if fi.Mode()&os.ModeSymlink != 0 {
			t, err := fsys.Readlink(p)
			if err != nil {
				return err
			}
			f.Link = t
			f.Mode = 0
		} else {
			fh, err := fsys.Open(p)
			if err != nil {
				return err
			}
			defer fh.Close()
			if f.Data, err = io.ReadAll(fh); err != nil {
				return err
			}
		}
		files = append(files, f)
		return nil
	})
	return files, err
}

// Definition rebuilds the jam.KitDefinition, with each link's Files as a memfs
// tree and StudioKit.BuildContext restored.
func (d KitDef) Definition() (jam.KitDefinition, error) {
	out := jam.KitDefinition{Ref: d.Ref, Kit: d.Kit, Harness: d.Harness}
	out.Kit.BuildContext = d.BuildContext
	for _, l := range d.Chain {
		link := jam.KitLink{Ref: l.Ref, Kit: l.Kit, Follow: l.Follow}
		link.Kit.BuildContext = l.BuildContext
		if l.Files != nil {
			fsys, err := decodeFiles(l.Files)
			if err != nil {
				return jam.KitDefinition{}, fmt.Errorf("kit %s: %w", l.Ref.ID, err)
			}
			link.Files = fsys
		}
		out.Chain = append(out.Chain, link)
	}
	return out, nil
}

func decodeFiles(files []File) (billy.Filesystem, error) {
	fsys := memfs.New()
	for _, f := range files {
		if !fs.ValidPath(f.Path) || f.Path == "." {
			return nil, fmt.Errorf("invalid file path %q", f.Path)
		}
		if dir := path.Dir(f.Path); dir != "." {
			if err := fsys.MkdirAll(dir, 0o755); err != nil {
				return nil, err
			}
		}
		if f.Link != "" {
			if err := fsys.Symlink(f.Link, f.Path); err != nil {
				return nil, err
			}
			continue
		}
		fh, err := fsys.OpenFile(f.Path, os.O_CREATE|os.O_WRONLY, os.FileMode(f.Mode))
		if err != nil {
			return nil, err
		}
		_, werr := fh.Write(f.Data)
		if cerr := fh.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			return nil, werr
		}
	}
	return fsys, nil
}
