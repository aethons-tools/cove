package jam

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"
	"unicode"

	"github.com/aethons-tools/cove/internal/blobstore"
	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
)

// Attachments (docs/usage/jam/attachments.md): a file is uploaded first —
// streamed into the blob store, its metadata a pending intercom Blob — and a
// send references it; the channel log attaches it atomically. A download is
// access-checked against the blob's one channel. A nil *Attachments is off.
type Attachments struct {
	lg    intercom.Store
	bytes blobstore.Store
	ic    *Intercom
	max   int64
	log   *slog.Logger
}

// ErrTooLarge is an upload over the cap; ErrAttachmentsOff any attachment
// use on a Jam without a bucket.
var (
	ErrTooLarge       = errors.New("attachments: file too large")
	ErrAttachmentsOff = errors.New("attachments: not configured")
)

// pendingTTL is how long an upload waits to be sent before the sweep.
const pendingTTL = 24 * time.Hour

// NewAttachments returns attachments over lg's metadata and bytes, capped at
// max bytes per file. A nil bytes store returns nil: attachments off.
func NewAttachments(lg intercom.Store, bytes blobstore.Store, ic *Intercom, max int64) *Attachments {
	if bytes == nil {
		return nil
	}
	return &Attachments{lg: lg, bytes: bytes, ic: ic, max: max, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

// SetLogger sets where attachments log (ids and sizes, never names or bytes).
func (a *Attachments) SetLogger(l *slog.Logger) {
	if a != nil && l != nil {
		a.log = l
	}
}

// Enabled reports whether attachments are on.
func (a *Attachments) Enabled() bool { return a != nil && a.bytes != nil }

// MaxBytes is the per-file cap (0 when off).
func (a *Attachments) MaxBytes() int64 {
	if !a.Enabled() {
		return 0
	}
	return a.max
}

func key(id ident.ID) string { return "blobs/" + string(id) }

// cleanName is a safe display name: the base name, no control characters,
// at most 255 bytes.
func cleanName(name string) string {
	name = path.Base(strings.ReplaceAll(name, `\`, "/"))
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, name)
	if name == "." || name == "/" || name == "" {
		name = "file"
	}
	for len(name) > 255 {
		name = name[:len(name)-1]
	}
	return name
}

// countingHash counts and hashes what passes through it.
type countingHash struct {
	n int64
	h interface {
		io.Writer
		Sum([]byte) []byte
	}
}

func (c *countingHash) Write(p []byte) (int, error) {
	c.n += int64(len(p))
	return c.h.Write(p)
}

// Upload streams r into the bucket as uploader's pending blob: ErrTooLarge
// (nothing kept) past the cap.
func (a *Attachments) Upload(ctx context.Context, uploader ident.ID, name, contentType string, r io.Reader) (intercom.Blob, error) {
	if !a.Enabled() {
		return intercom.Blob{}, ErrAttachmentsOff
	}
	if mt, _, err := mime.ParseMediaType(contentType); err != nil || mt == "" {
		contentType = "application/octet-stream"
	} else {
		contentType = mt
	}
	id := ident.New(ident.Blob)
	sum := &countingHash{h: sha256.New()}
	body := io.TeeReader(io.LimitReader(r, a.max+1), sum)
	if err := a.bytes.Put(ctx, key(id), body, -1, contentType); err != nil {
		_ = a.bytes.Delete(context.WithoutCancel(ctx), key(id))
		return intercom.Blob{}, fmt.Errorf("attachments: storing: %w", err)
	}
	if sum.n > a.max {
		_ = a.bytes.Delete(context.WithoutCancel(ctx), key(id))
		return intercom.Blob{}, ErrTooLarge
	}
	b := intercom.Blob{ID: id, Uploader: uploader, Size: sum.n, Type: contentType, Name: cleanName(name),
		SHA256: hex.EncodeToString(sum.h.Sum(nil)), Created: time.Now().UTC()}
	if err := a.lg.CreateBlob(b); err != nil {
		_ = a.bytes.Delete(context.WithoutCancel(ctx), key(id))
		return intercom.Blob{}, err
	}
	b.State = intercom.BlobPending
	a.log.Info("attachments", "op", "upload", "blob", string(id), "uploader", string(uploader), "bytes", b.Size)
	return b, nil
}

// RefParts are a send's reference parts for refs (the store fills in each
// part from its blob, and refuses one that isn't the sender's pending
// upload). No refs: nil. Refs with attachments off: ErrAttachmentsOff.
func (a *Attachments) RefParts(refs []string) ([]intercom.Part, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	if !a.Enabled() {
		return nil, ErrAttachmentsOff
	}
	parts := make([]intercom.Part, len(refs))
	for i, r := range refs {
		parts[i] = intercom.Part{Type: "application/octet-stream", Ref: r}
	}
	return parts, nil
}

// Names is the body a files-only send falls back to: its files' names.
func (a *Attachments) Names(refs []string) string {
	var names []string
	for _, r := range refs {
		if b, ok := a.lg.GetBlob(ident.ID(r)); ok {
			names = append(names, b.Name)
		}
	}
	return strings.Join(names, ", ")
}

// mayRead reports whether viewer may read b: its uploader while pending;
// once attached, a person who can see its channel or a session in it.
func (a *Attachments) mayRead(viewer ident.ID, b intercom.Blob) bool {
	if b.State == intercom.BlobPending {
		return viewer == b.Uploader
	}
	ch, ok := a.ic.store.GetChannel(b.Channel)
	if !ok {
		return false
	}
	if isSessionID(viewer) {
		return a.ic.isMemberOf(ch, viewer)
	}
	return a.ic.CanSee(viewer, ch)
}

// inlineTypes render in the browser; anything else downloads.
var inlineTypes = map[string]bool{"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true}

// Serve streams blob id to viewer, or 404 alike for missing and forbidden.
// It never lets a file run as Jam's origin: nosniff, a sandbox CSP, and
// inline only for raster images.
func (a *Attachments) Serve(w http.ResponseWriter, r *http.Request, viewer, id ident.ID) {
	if !a.Enabled() {
		http.Error(w, "attachments are off", http.StatusServiceUnavailable)
		return
	}
	b, ok := a.lg.GetBlob(id)
	if !ok || !a.mayRead(viewer, b) {
		http.NotFound(w, r)
		return
	}
	obj, info, err := a.bytes.Open(r.Context(), key(id))
	if err != nil {
		if errors.Is(err, blobstore.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		a.log.Error("attachments: open failed", "blob", string(id), "err", err.Error())
		http.Error(w, "attachment unavailable", http.StatusBadGateway)
		return
	}
	defer obj.Close()
	disp := "attachment"
	if inlineTypes[b.Type] {
		disp = "inline"
	}
	h := w.Header()
	h.Set("Content-Type", b.Type)
	h.Set("Content-Disposition", mime.FormatMediaType(disp, map[string]string{"filename": b.Name}))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "sandbox")
	h.Set("Cache-Control", "private, max-age=3600")
	if b.SHA256 != "" {
		h.Set("ETag", `"`+b.SHA256+`"`)
	}
	http.ServeContent(w, r, "", info.Modified, obj)
}

// HandleUpload is the upload route: the raw body, its Content-Type, and the
// file name in X-Filename. 201 {ref, size, type, name}; 413 over the cap;
// 503 when off.
func (a *Attachments) HandleUpload(w http.ResponseWriter, r *http.Request, uploader ident.ID) {
	if !a.Enabled() {
		http.Error(w, "attachments are off", http.StatusServiceUnavailable)
		return
	}
	if r.ContentLength > a.max {
		http.Error(w, fmt.Sprintf("too large (max %d MB)", a.max>>20), http.StatusRequestEntityTooLarge)
		return
	}
	b, err := a.Upload(r.Context(), uploader, r.Header.Get("X-Filename"), r.Header.Get("Content-Type"), r.Body)
	switch {
	case errors.Is(err, ErrTooLarge):
		http.Error(w, fmt.Sprintf("too large (max %d MB)", a.max>>20), http.StatusRequestEntityTooLarge)
		return
	case err != nil:
		a.log.Error("attachments: upload failed", "uploader", string(uploader), "err", err.Error())
		http.Error(w, "upload failed", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(struct {
		Ref  string `json:"ref"`
		Size int64  `json:"size"`
		Type string `json:"type"`
		Name string `json:"name"`
	}{string(b.ID), b.Size, b.Type, b.Name})
}

// Sweep deletes pending uploads older than 24h before now: the row first —
// refused if a send attached it meanwhile, so an attached blob never loses
// its bytes — then the bytes (a crash between leaves only an orphan object).
func (a *Attachments) Sweep(ctx context.Context, now time.Time) {
	if !a.Enabled() {
		return
	}
	for _, b := range a.lg.PendingBlobsBefore(now.Add(-pendingTTL)) {
		if err := a.lg.DeleteBlob(b.ID); err != nil {
			if !errors.Is(err, intercom.ErrBlobAttached) {
				a.log.Warn("attachments: sweep row delete failed", "blob", string(b.ID), "err", err.Error())
			}
			continue
		}
		if err := a.bytes.Delete(ctx, key(b.ID)); err != nil {
			a.log.Warn("attachments: sweep delete failed (orphan object left)", "blob", string(b.ID), "err", err.Error())
		}
	}
}
