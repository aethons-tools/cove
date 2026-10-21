package jam

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/blobstore"
	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
)

func attFixture(t *testing.T, max int64) (*icFixture, *Attachments, *blobstore.Mem) {
	t.Helper()
	f := newICFixture(t)
	bytes := blobstore.NewMem()
	return f, NewAttachments(f.log, bytes, f.ic, max), bytes
}

func serveBlob(a *Attachments, viewer ident.ID, id ident.ID) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	a.Serve(rec, httptest.NewRequest("GET", "/blobs/"+string(id), nil), viewer, id)
	return rec
}

func TestAttachmentsUploadSendServe(t *testing.T) {
	f, a, bytes := attFixture(t, 1<<20)
	ctx := context.Background()
	b, err := a.Upload(ctx, f.alice.ID, "../../etc/shot.png", "image/png", strings.NewReader("PNGDATA"))
	if err != nil {
		t.Fatal(err)
	}
	if b.ID.Kind() != ident.Blob || b.Size != 7 || b.Name != "shot.png" || b.Type != "image/png" || b.SHA256 == "" || bytes.Len() != 1 {
		t.Fatalf("upload = %+v (objects %d)", b, bytes.Len())
	}
	// Pending: the uploader only.
	if rec := serveBlob(a, f.alice.ID, b.ID); rec.Code != http.StatusOK || rec.Body.String() != "PNGDATA" {
		t.Fatalf("uploader reads pending = %d %q", rec.Code, rec.Body)
	}
	if rec := serveBlob(a, f.bob.ID, b.ID); rec.Code != http.StatusNotFound {
		t.Fatalf("bob reads alice's pending = %d", rec.Code)
	}
	// Alice sends it into the room; a member and a session in it may read it.
	parts, err := a.RefParts([]string{string(b.ID)})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.JoinChannel(f.room.ID, f.bob.ID, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ic.PostTrusted(f.room, intercom.Squawk{From: f.alice.ID, Body: "see", Parts: parts}); err != nil {
		t.Fatal(err)
	}
	rec := serveBlob(a, f.bob.ID, b.ID)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" || !strings.HasPrefix(rec.Header().Get("Content-Disposition"), "inline") ||
		rec.Header().Get("X-Content-Type-Options") != "nosniff" || rec.Header().Get("Content-Security-Policy") != "sandbox" {
		t.Fatalf("member read = %d %v", rec.Code, rec.Header())
	}
	if rec := serveBlob(a, f.carol.ID, b.ID); rec.Code != http.StatusNotFound {
		t.Fatalf("carol (not in the project) = %d", rec.Code)
	}
	if rec := serveBlob(a, ident.ID(f.standing.ActorID), b.ID); rec.Code != http.StatusNotFound {
		t.Fatalf("a session not in the room = %d", rec.Code)
	}
	if rec := serveBlob(a, f.bob.ID, ident.New(ident.Blob)); rec.Code != http.StatusNotFound {
		t.Fatalf("missing = %d", rec.Code)
	}
	// Ranges work.
	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set("Range", "bytes=3-")
	rr := httptest.NewRecorder()
	a.Serve(rr, req, f.bob.ID, b.ID)
	if rr.Code != http.StatusPartialContent || rr.Body.String() != "DATA" {
		t.Fatalf("range = %d %q", rr.Code, rr.Body)
	}
}

func TestAttachmentsUploadTooLarge(t *testing.T) {
	f, a, bytes := attFixture(t, 4)
	if _, err := a.Upload(context.Background(), f.alice.ID, "big.bin", "", strings.NewReader("12345")); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("over the cap: %v", err)
	}
	if bytes.Len() != 0 || len(f.log.PendingBlobsBefore(time.Now().Add(time.Hour))) != 0 {
		t.Fatal("a refused upload left something behind")
	}
	if b, err := a.Upload(context.Background(), f.alice.ID, "ok.bin", "", strings.NewReader("1234")); err != nil || b.Type != "application/octet-stream" {
		t.Fatalf("at the cap = %+v, %v", b, err)
	}
}

func TestAttachmentsDangerousTypesDownload(t *testing.T) {
	f, a, _ := attFixture(t, 1<<20)
	for _, ct := range []string{"text/html", "image/svg+xml", "application/pdf"} {
		b, err := a.Upload(context.Background(), f.alice.ID, "x", ct, strings.NewReader("<script>1</script>"))
		if err != nil {
			t.Fatal(err)
		}
		if rec := serveBlob(a, f.alice.ID, b.ID); !strings.HasPrefix(rec.Header().Get("Content-Disposition"), "attachment") {
			t.Errorf("%s served %q, want attachment", ct, rec.Header().Get("Content-Disposition"))
		}
	}
}

func TestAttachmentsSweep(t *testing.T) {
	f, a, bytes := attFixture(t, 1<<20)
	ctx := context.Background()
	old, _ := a.Upload(ctx, f.alice.ID, "old", "", strings.NewReader("o"))
	kept, _ := a.Upload(ctx, f.alice.ID, "kept", "", strings.NewReader("k"))
	parts, _ := a.RefParts([]string{string(kept.ID)})
	if _, err := f.ic.PostTrusted(f.room, intercom.Squawk{From: f.alice.ID, Body: "x", Parts: parts}); err != nil {
		t.Fatal(err)
	}
	a.Sweep(ctx, time.Now().Add(25*time.Hour))
	if _, ok := f.log.GetBlob(old.ID); ok || bytes.Len() != 1 {
		t.Fatalf("sweep left the old pending blob (objects %d)", bytes.Len())
	}
	if _, ok := f.log.GetBlob(kept.ID); !ok {
		t.Fatal("sweep removed an attached blob")
	}
}

func TestAttachmentsOff(t *testing.T) {
	var a *Attachments
	if a.Enabled() {
		t.Fatal("nil attachments enabled")
	}
	if _, err := a.RefParts([]string{"blb_x"}); !errors.Is(err, ErrAttachmentsOff) {
		t.Fatalf("refs with attachments off: %v", err)
	}
	if p, err := a.RefParts(nil); err != nil || p != nil {
		t.Fatal("no refs is fine when off")
	}
}

// The HTTP upload: raw body, X-Filename, 201 {ref, size, type, name}; 413
// over the cap; 503 when off.
func TestAttachmentsHandleUpload(t *testing.T) {
	f, a, _ := attFixture(t, 8)
	up := func(a *Attachments, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/uploads", strings.NewReader(body))
		req.Header.Set("Content-Type", "text/plain")
		req.Header.Set("X-Filename", "notes.txt")
		rec := httptest.NewRecorder()
		a.HandleUpload(rec, req, f.alice.ID)
		return rec
	}
	rec := up(a, "hello")
	var out struct {
		Ref  string `json:"ref"`
		Size int64  `json:"size"`
		Type string `json:"type"`
		Name string `json:"name"`
	}
	if rec.Code != http.StatusCreated || json.Unmarshal(rec.Body.Bytes(), &out) != nil || out.Name != "notes.txt" || out.Size != 5 || !strings.HasPrefix(out.Ref, "blb_") {
		t.Fatalf("upload = %d %s", rec.Code, rec.Body)
	}
	if rec := up(a, "way too long"); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("over = %d", rec.Code)
	}
	var off *Attachments
	if rec := up(off, "x"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("off = %d", rec.Code)
	}
	_, _ = io.Discard.Write(nil)
}
