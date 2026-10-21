package intercom

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
)

// Attachments (docs/superpowers/specs/2026-10-21-intercom-attachments-design.md):
// a file is uploaded first — its bytes to the blob store, its metadata here
// as a pending Blob — and a send then references it as a part {type, ref,
// name, size}. The append binds each ref to the squawk atomically (FoldBlobs):
// only the sender's own pending blobs, each attached once.

// BlobState is a blob's lifecycle state.
type BlobState string

const (
	BlobPending  BlobState = "pending"  // uploaded, not yet sent
	BlobAttached BlobState = "attached" // bound to one squawk in one channel
)

// Blob is an attachment's metadata.
type Blob struct {
	ID       ident.ID  `json:"id"`
	Uploader ident.ID  `json:"uploader"`
	State    BlobState `json:"state"`
	Size     int64     `json:"size"`
	Type     string    `json:"type"`
	Name     string    `json:"name"`
	SHA256   string    `json:"sha256"`
	Channel  ident.ID  `json:"channel,omitempty"`
	Squawk   string    `json:"squawk,omitempty"`
	Created  time.Time `json:"created"`
}

// MaxAttachments is the most reference parts one squawk carries.
const MaxAttachments = 10

// ErrBlobNotYours refuses a send referencing a blob that isn't the sender's
// own pending upload (missing, someone else's, or already attached).
var ErrBlobNotYours = fmt.Errorf("%w: not your pending upload", ErrInvalid)

// ErrBlobAttached refuses deleting an attached blob.
var ErrBlobAttached = errors.New("intercom: blob is attached")

// validateRef checks a reference part's shape.
func validateRef(p Part) error {
	id, err := ident.Parse(p.Ref)
	switch {
	case err != nil || id.Kind() != ident.Blob:
		return fmt.Errorf("%w: ref %q is not a blob id", ErrInvalid, p.Ref)
	case p.Type == "":
		return fmt.Errorf("%w: ref %s has no type", ErrInvalid, p.Ref)
	case len(p.Data) > 0:
		return fmt.Errorf("%w: ref %s carries data", ErrInvalid, p.Ref)
	}
	return nil
}

// FoldBlobs is a store's step for appending m (Prepare'd, its ID set): each
// reference part must name the sender's own pending blob (get), and is
// rewritten from the blob's row. It returns m and the blobs to store as
// attached; on error, append nothing.
func FoldBlobs(m Squawk, get func(id ident.ID) (Blob, bool)) (Squawk, []Blob, error) {
	var bound []Blob
	parts := slices.Clone(m.Parts)
	for i, p := range parts {
		if p.Ref == "" {
			continue
		}
		b, ok := get(ident.ID(p.Ref))
		if !ok || b.State != BlobPending || b.Uploader != m.From || slices.ContainsFunc(bound, func(x Blob) bool { return x.ID == b.ID }) {
			return Squawk{}, nil, fmt.Errorf("%w: %s", ErrBlobNotYours, p.Ref)
		}
		parts[i] = Part{Type: b.Type, Ref: string(b.ID), Name: b.Name, Size: b.Size}
		b.State, b.Channel, b.Squawk = BlobAttached, m.Channel, m.ID
		bound = append(bound, b)
	}
	m.Parts = parts
	return m, bound, nil
}
