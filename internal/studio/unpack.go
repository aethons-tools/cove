package studio

import (
	"archive/tar"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"testing/fstest"
	"unicode"
	"unicode/utf8"
)

// ContextFile is one regular file of a build context.
type ContextFile struct {
	Path string      // slash-separated, relative to the context root
	Mode fs.FileMode // permission bits: 0644 or 0755
	Data []byte
}

// ErrContextNotReproducible is a context that no context-dir packs back to
// byte for byte: a hand-made tar (directory entries, unsorted or unclean
// names, other metadata), a file a .dockerignore of its own would drop, or
// a mode other than 0644/0755 (the two git and the config upload keep).
var ErrContextNotReproducible = errors.New("base.context: not reproducible from a context-dir")

// ContextDirFiles unpacks a stored base64 context into the files of a
// context-dir that PackContextDir packs back to exactly that context. The
// tar is read with ScanContextTar's checks (caps; no absolute or ".." names,
// links or irregular entries; a root Dockerfile), and the files are then
// repacked in memory with the same packer and compared: a context whose
// repack differs is ErrContextNotReproducible, so a caller can keep it
// inline instead of writing files that would read back as a change.
func ContextDirFiles(b64 string) ([]ContextFile, error) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, errors.New("base.context: invalid base64")
	}
	var files []ContextFile
	err = walkContextTar(raw, func(hdr *tar.Header, r io.Reader) error {
		data, err := io.ReadAll(r)
		if err != nil {
			return fmt.Errorf("base.context: %w", err)
		}
		files = append(files, ContextFile{Path: hdr.Name, Mode: fs.FileMode(hdr.Mode), Data: data})
		return nil
	})
	if err != nil {
		return nil, err
	}
	fsys := fstest.MapFS{}
	for _, f := range files {
		if (f.Mode != 0o644 && f.Mode != 0o755) || !fs.ValidPath(f.Path) || unprintablePath(f.Path) || fsys[f.Path] != nil {
			return nil, ErrContextNotReproducible
		}
		fsys[f.Path] = &fstest.MapFile{Data: f.Data, Mode: f.Mode}
	}
	again, err := packContextFS(fsys, "context")
	if err != nil || again != b64 {
		return nil, ErrContextNotReproducible
	}
	return files, nil
}

// unprintablePath reports invalid UTF-8 or a control character in p: a name
// kept out of a written repo.
func unprintablePath(p string) bool {
	if !utf8.ValidString(p) {
		return true
	}
	for _, r := range p {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}
