package adminui

import (
	"io"
	"io/fs"

	"github.com/aethons-tools/cove/internal/jam"
)

// ctxEntry is one file of a version as the kit page lists it: from a tree,
// or from a pre-tree packed context.
type ctxEntry struct {
	Path string
	Mode fs.FileMode
	Size int64
	Sum  string // hex sha256 of the content
	Data []byte // the first maxFileShow+1 bytes of the wanted file; nil for every other
}

// treeRead lists tree version v's files, kit.yml included. With unpack, the
// file named want has its first maxFileShow+1 bytes read through v.Files.
// v's tree is a stored one (the store validated it on write).
func treeRead(v jam.KitVersion, want string, unpack bool) *ctxRead {
	c := &ctxRead{tree: true, size: sizeLabel(int(v.Tree.Size())), read: true, byPath: make(map[string]int, len(v.Tree.Entries))}
	for i, e := range v.Tree.Entries {
		ent := ctxEntry{Path: e.Path, Mode: e.Mode, Size: e.Size, Sum: e.SHA256}
		if unpack && e.Path == want && v.Files != nil {
			if f, err := v.Files.Open(e.Path); err == nil {
				ent.Data, _ = io.ReadAll(io.LimitReader(f, maxFileShow+1))
				_ = f.Close()
			}
		}
		c.files = append(c.files, ent)
		c.byPath[e.Path] = i
	}
	return c
}
