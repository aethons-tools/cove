package studio

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
)

// ErrContextBase64 is a stored base.context that is not valid base64.
var ErrContextBase64 = errors.New("base.context: invalid base64")

// ContextEntry is one regular file of a stored context, as listed for display.
type ContextEntry struct {
	Path string      // the tar name, unmodified
	Mode fs.FileMode // the header's permission bits (& 0o7777)
	Size int64       // bytes actually streamed, not the header's claim
	Sum  [32]byte    // SHA-256 of the content
	Data []byte      // the first keep bytes of the wanted file; nil for every other
}

// ContextFiles lists a stored base64 context's regular files with ScanContextTar's
// checks (caps; no absolute or ".." names, links or irregular entries; a root
// Dockerfile) — but, unlike ContextDirFiles, without requiring that a
// context-dir reproduces it. Content is hashed as it streams; only the file
// whose path is want keeps its first keep bytes (the last entry of that name,
// as extraction keeps), so listing a context never holds more than that.
func ContextFiles(b64, want string, keep int) ([]ContextEntry, error) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, ErrContextBase64
	}
	var out []ContextEntry
	kept := -1 // the entry holding want's bytes: a later duplicate replaces it
	err = walkContextTar(raw, func(hdr *tar.Header, r io.Reader) error {
		e := ContextEntry{Path: hdr.Name, Mode: fs.FileMode(hdr.Mode & 0o7777)}
		h := sha256.New()
		if hdr.Name == want {
			var buf bytes.Buffer
			n, err := io.Copy(io.MultiWriter(&buf, h), io.LimitReader(r, int64(keep)))
			if err != nil {
				return fmt.Errorf("base.context: %w", err)
			}
			if kept >= 0 {
				out[kept].Data = nil
			}
			e.Data, e.Size, kept = bytes.Clone(buf.Bytes()), n, len(out)
			if e.Data == nil {
				e.Data = []byte{}
			}
		}
		n, err := io.Copy(h, r)
		if err != nil {
			return fmt.Errorf("base.context: %w", err)
		}
		e.Size += n
		copy(e.Sum[:], h.Sum(nil))
		out = append(out, e)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
