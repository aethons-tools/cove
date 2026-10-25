package adminui

import (
	"io"
	"io/fs"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/kittree"
)

// ctxEntry is one file of a version's tree as the kit page lists it.
type ctxEntry struct {
	Path string
	Mode fs.FileMode
	Size int64
	Sum  string   // hex sha256 of the content
	Data []byte   // the first maxFileShow+1 bytes of the wanted file; nil for every other
	Mask maskKind // how Data (an unparsable kit.yml) was masked
}

// treeRead lists tree version v's files, kit.yml included. With unpack, the
// file named want has its first maxFileShow+1 bytes read through v.Files; a
// kit.yml that does not parse is read as shown (maskKitYML), so no packed
// context is shown. v's tree is a stored one (the store validated it on
// write).
func treeRead(v jam.KitVersion, want string, unpack bool) *ctxRead {
	c := &ctxRead{size: sizeLabel(int(v.Tree.Size())), byPath: make(map[string]int, len(v.Tree.Entries))}
	for i, e := range v.Tree.Entries {
		ent := ctxEntry{Path: e.Path, Mode: e.Mode, Size: e.Size, Sum: e.SHA256}
		switch {
		case !unpack || e.Path != want:
		case e.Path == kittree.KitFile && v.Err != nil:
			text, how := maskKitYML(string(v.KitYML))
			shown := []byte(text)
			ent.Data, ent.Mask = shown[:min(len(shown), maxFileShow+1)], how
		case v.Files != nil:
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
