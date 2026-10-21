package jam

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/blobstore"
	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
)

func (f *sqFixture) upload(inst Instance, body string) string {
	f.t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/squawks/uploads", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer tok-"+inst.ActorID)
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("X-Filename", "log.txt")
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	var out struct {
		Ref string `json:"ref"`
	}
	if rec.Code != http.StatusCreated || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
		f.t.Fatalf("upload = %d %s", rec.Code, rec.Body)
	}
	return out.Ref
}

// A session uploads, sends it home with no text, and its channel's members
// (another session included) read it; one outside can't.
func TestSquawksAttachRoundTrip(t *testing.T) {
	f := newSqFixture(t)
	f.h.SetAttachments(NewAttachments(f.log, blobstore.NewMem(), f.ic, 1<<20))
	home := f.mustPlan(f.standing, "").Channel
	if err := f.ic.JoinChannel(f.alice.ID, home.ID); err != nil {
		t.Fatal(err)
	}
	ref := f.upload(f.standing, "line 1\nline 2\n")
	if code, _ := f.send(f.standing, `{"attachments":["`+ref+`"]}`); code != http.StatusOK {
		t.Fatalf("send with only an attachment = %d", code)
	}
	in := f.log.InboxSince(f.alice.ID, 0, 0)
	last := in[len(in)-1]
	if len(last.Parts) != 1 || last.Parts[0].Ref != ref || last.Parts[0].Name != "log.txt" || last.Body != "log.txt" {
		t.Fatalf("alice got %+v", last)
	}
	// The sender reads its own attachment back through /squawks/blobs.
	rec := f.do(http.MethodGet, "/squawks/blobs/"+ref, f.standing, "")
	if rec.Code != http.StatusOK || rec.Body.String() != "line 1\nline 2\n" {
		t.Fatalf("session reads = %d %q", rec.Code, rec.Body)
	}
	if rec := f.do(http.MethodGet, "/squawks/blobs/"+ref, f.ticket, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("a session outside the channel reads = %d", rec.Code)
	}
	// Re-sending the same ref, or someone else's, is refused.
	if code, _ := f.send(f.standing, `{"body":"again","attachments":["`+ref+`"]}`); code != http.StatusBadRequest {
		t.Fatalf("re-attach = %d", code)
	}
	if code, _ := f.send(f.ticket, `{"body":"x","attachments":["`+ref+`"]}`); code != http.StatusBadRequest {
		t.Fatalf("someone else's = %d", code)
	}
	if rec := f.do(http.MethodGet, "/squawks/uploads", f.standing, ""); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET uploads = %d", rec.Code)
	}
}

func TestSquawksAttachOff(t *testing.T) {
	f := newSqFixture(t)
	if rec := f.do(http.MethodPost, "/squawks/uploads", f.standing, "x"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("upload off = %d", rec.Code)
	}
	if code, _ := f.send(f.standing, `{"body":"x","attachments":["`+string(ident.New(ident.Blob))+`"]}`); code != http.StatusBadRequest {
		t.Fatalf("refs off = %d", code)
	}
	if code, _ := f.send(f.standing, `{"body":""}`); code != http.StatusBadRequest {
		t.Fatalf("empty send = %d", code)
	}
}

// A person attaches through /me/send.
func TestParticipantSendAttachments(t *testing.T) {
	f := newICFixture(t)
	att := NewAttachments(f.log, blobstore.NewMem(), f.ic, 1<<20)
	b, err := att.Upload(t.Context(), f.bob.ID, "shot.png", "image/png", strings.NewReader("png"))
	if err != nil {
		t.Fatal(err)
	}
	h := NewParticipantSendHandler(f.store, f.ic, nil)
	h.SetAttachments(att)
	if w := meSend(t, h, f.bob, `{"to":"`+string(f.room.ID)+`","attachments":["`+string(b.ID)+`"]}`); w.Code != http.StatusNoContent {
		t.Fatalf("send = %d %s", w.Code, w.Body)
	}
	got, _ := f.log.GetBlob(b.ID)
	if got.State != intercom.BlobAttached || got.Channel != f.room.ID {
		t.Fatalf("blob = %+v", got)
	}
	if w := meSend(t, h, f.bob, `{"to":"`+string(f.room.ID)+`","body":"x","attachments":["`+string(b.ID)+`"]}`); w.Code != http.StatusBadRequest {
		t.Fatalf("re-attach = %d", w.Code)
	}
}
