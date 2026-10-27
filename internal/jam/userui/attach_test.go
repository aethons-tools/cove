package userui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/blobstore"
	"github.com/aethons-tools/cove/internal/jam"
)

// /app/user uploads and serves attachments for the viewer.
func TestMeUploadAndBlob(t *testing.T) {
	e, p := fixture()
	e.Deps.Attachments = jam.NewAttachments(e.lg, blobstore.NewMem(), e.Intercom, 1<<20)
	h := Handler(e.Deps, nil)
	req := httptest.NewRequest("POST", "/app/user/uploads", strings.NewReader("PNG"))
	req.Header.Set("Content-Type", "image/png")
	req.Header.Set("X-Filename", "shot.png")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, jam.WithParticipant(req, p))
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"ref":"blb_`) {
		t.Fatalf("upload = %d %s", rec.Code, rec.Body)
	}
	ref := strings.Split(strings.Split(rec.Body.String(), `"ref":"`)[1], `"`)[0]
	get := httptest.NewRecorder()
	h.ServeHTTP(get, jam.WithParticipant(httptest.NewRequest("GET", "/app/user/blobs/"+ref, nil), p))
	if get.Code != http.StatusOK || get.Body.String() != "PNG" {
		t.Fatalf("blob = %d %q", get.Code, get.Body)
	}
}
