package adminui_test

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/kittree"
)

// postMultipart posts fields and, when file is non-nil, a "file" part whose
// own name ("../evil<script>.txt") must be ignored, same-origin.
func postMultipart(t *testing.T, h http.Handler, path string, fields map[string]string, file []byte) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		if err := mw.WriteField(k, v); err != nil {
			t.Fatal(err)
		}
	}
	if file != nil {
		fw, err := mw.CreateFormFile("file", "../evil<script>.txt")
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fw.Write(file)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, path, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Origin", "http://"+req.Host)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestDraftReplaceUpload(t *testing.T) {
	store, h := draftFixture(t)
	putDraft(t, store, "blob.bin", 0, "\x00\x01")
	ed := section(get(t, h, "/app/admin/kits/app/draft?file=blob.bin").Body.String(), "draft-editor")
	if !strings.Contains(ed, `id="draft-replace"`) || !strings.Contains(ed, `hx-encoding="multipart/form-data"`) {
		t.Fatalf("no replace form:\n%s", ed)
	}
	rec := postMultipart(t, h, "/app/admin/kits/app/draft/replace", map[string]string{"etag": hiddenETag(t, ed), "path": "blob.bin", "executable": "1"}, []byte("\x00\x02\x03"))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "saved blob.bin") || strings.Contains(rec.Body.String(), "evil") {
		t.Fatalf("replace = %d %s", rec.Code, rec.Body)
	}
	if data, e, _, _ := jam.KitDraftFile(store, "app", "blob.bin"); string(data) != "\x00\x02\x03" || e.Mode != 0o755 {
		t.Fatalf("stored %q %o", data, e.Mode)
	}
	if st, _ := jam.KitDraftStatusOf(store, "app"); len(st.Entries) != 5 {
		t.Fatalf("the upload's own name must be ignored: entries %+v", st.Entries)
	}
	if rec := postMultipart(t, h, "/app/admin/kits/app/draft/replace", map[string]string{"etag": draftETag(t, store), "path": "blob.bin"}, nil); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "choose a file to upload") {
		t.Fatalf("no file = %d %s", rec.Code, rec.Body)
	}
	if rec := postMultipart(t, h, "/app/admin/kits/app/draft/replace", map[string]string{"etag": hiddenETag(t, ed), "path": "blob.bin"}, []byte("x")); rec.Code != http.StatusPreconditionFailed || !strings.Contains(rec.Body.String(), "draft app changed since you loaded it (reload)") {
		t.Fatalf("stale replace = %d %s", rec.Code, rec.Body)
	}
	if rec := post(t, h, "/app/admin/kits/app/draft/replace", url.Values{"etag": {draftETag(t, store)}, "path": {"blob.bin"}}); rec.Code != http.StatusBadRequest {
		t.Fatalf("a non-multipart replace = %d", rec.Code)
	}
}

// Over the per-file limit: jam's own 413 within the body cap; past the cap,
// a fixed 413 before the body is read. Nothing changes either way.
func TestDraftUploadOverLimit(t *testing.T) {
	store, h := draftFixture(t)
	old := kittree.MaxFile
	kittree.MaxFile = 1024
	t.Cleanup(func() { kittree.MaxFile = old })
	tag := draftETag(t, store)
	rec := postMultipart(t, h, "/app/admin/kits/app/draft/replace", map[string]string{"etag": tag, "path": "run.sh"}, bytes.Repeat([]byte("a"), 2000))
	if rec.Code != http.StatusRequestEntityTooLarge || !strings.Contains(rec.Body.String(), "draft app: run.sh: more than 1024 bytes (limit 1024 bytes per file)") {
		t.Fatalf("over the file limit = %d %s", rec.Code, rec.Body)
	}
	rec = postMultipart(t, h, "/app/admin/kits/app/draft/replace", map[string]string{"etag": tag, "path": "run.sh"}, bytes.Repeat([]byte("a"), 2<<20))
	if rec.Code != http.StatusRequestEntityTooLarge || !strings.Contains(rec.Body.String(), "draft app: the file is over 1024 bytes (limit 1024 bytes per file)") {
		t.Fatalf("past the body cap = %d %s", rec.Code, rec.Body)
	}
	rec = post(t, h, "/app/admin/kits/app/draft/save", url.Values{"etag": {tag}, "path": {"run.sh"}, "content": {strings.Repeat("b", 2000)}})
	if rec.Code != http.StatusRequestEntityTooLarge || !strings.Contains(rec.Body.String(), "draft app: run.sh: more than 1024 bytes") {
		t.Fatalf("an editor text over the limit = %d %s", rec.Code, rec.Body)
	}
	if draftETag(t, store) != tag {
		t.Fatal("a refused upload changed the draft")
	}
}

// Ruling 20: a chunked upload (no Content-Length) past the body cap is
// stopped by http.MaxBytesReader as it reads — the same fixed 413, the
// body never read past the cap, and nothing changes.
func TestDraftUploadChunkedOverCap(t *testing.T) {
	store, h := draftFixture(t)
	old := kittree.MaxFile
	kittree.MaxFile = 1024
	t.Cleanup(func() { kittree.MaxFile = old })
	tag := draftETag(t, store)
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("etag", tag)
	_ = mw.WriteField("path", "run.sh")
	fw, err := mw.CreateFormFile("file", "x")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fw.Write(bytes.Repeat([]byte("a"), 4<<20))
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	body := &countingReader{r: &buf}
	req := httptest.NewRequest(http.MethodPost, "/app/admin/kits/app/draft/replace", io.NopCloser(body))
	req.ContentLength = -1 // chunked: no length to check up front
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Origin", "http://"+req.Host)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge || !strings.Contains(rec.Body.String(), "draft app: the file is over 1024 bytes (limit 1024 bytes per file)") {
		t.Fatalf("chunked past the body cap = %d %s", rec.Code, rec.Body)
	}
	if limit := int64(1024 + 1<<20); body.n > limit+64<<10 {
		t.Fatalf("read %d bytes of a body capped at %d", body.n, limit)
	}
	if draftETag(t, store) != tag {
		t.Fatal("a refused upload changed the draft")
	}
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

func TestDraftModeToggle(t *testing.T) {
	store, h := draftFixture(t)
	putDraft(t, store, "blob.bin", 0, "\x00\x01")
	ed := section(get(t, h, "/app/admin/kits/app/draft?file=blob.bin").Body.String(), "draft-editor")
	if !strings.Contains(ed, `id="draft-mode"`) || strings.Contains(ed, `name="content"`) {
		t.Fatalf("mode form:\n%s", ed)
	}
	rec := post(t, h, "/app/admin/kits/app/draft/mode", url.Values{"etag": {hiddenETag(t, ed)}, "path": {"blob.bin"}, "executable": {"1"}})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "blob.bin is now 0755") {
		t.Fatalf("mode = %d %s", rec.Code, rec.Body)
	}
	if data, e, _, _ := jam.KitDraftFile(store, "app", "blob.bin"); string(data) != "\x00\x01" || e.Mode != 0o755 {
		t.Fatalf("stored %q %o", data, e.Mode)
	}
	if rec := post(t, h, "/app/admin/kits/app/draft/mode", url.Values{"etag": {hiddenETag(t, ed)}, "path": {"blob.bin"}}); rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale mode = %d", rec.Code)
	}
}

// The upload hints state the per-file limit as jam enforces it.
func TestDraftUploadHintsShowLimit(t *testing.T) {
	old := kittree.MaxFile
	kittree.MaxFile = 2 << 20
	t.Cleanup(func() { kittree.MaxFile = old })
	_, h := draftFixture(t)
	body := get(t, h, "/app/admin/kits/app/draft?file=run.sh").Body.String()
	if strings.Count(body, "up to 2 MiB") != 2 || strings.Contains(body, "4 MiB") {
		t.Errorf("hints:\n%s%s", section(body, "draft-replace"), section(body, "draft-add"))
	}
}
