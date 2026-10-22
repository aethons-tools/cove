package userui

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/blobstore"
	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/jam"
)

func TestComposerAttachWiring(t *testing.T) {
	e, p := fixture()
	off := e.page(t, p, "/app/user/?c="+url.QueryEscape(engID))
	if strings.Contains(off, `class="attach-btn"`) {
		t.Error("attachments off: no 📎")
	}
	e.Deps.Attachments = jam.NewAttachments(e.lg, blobstore.NewMem(), e.Intercom, 25<<20)
	on := e.page(t, p, "/app/user/?c="+url.QueryEscape(engID))
	for _, want := range []string{`class="attach-btn"`, `type="file"`, "function meAttach(", "'/app/user/uploads'", "X-Filename", "attachments:", "clipboardData", "addEventListener('drop'", `class="att-chips"`} {
		if !strings.Contains(on, want) {
			t.Errorf("composer missing %q", want)
		}
	}
}

func TestMessageFilesRender(t *testing.T) {
	e, p := fixture()
	att := jam.NewAttachments(e.lg, blobstore.NewMem(), e.Intercom, 25<<20)
	e.Deps.Attachments = att
	img, _ := att.Upload(context.Background(), e.alice.ID, "shot.png", "image/png", strings.NewReader("png"), -1)
	pdf, _ := att.Upload(context.Background(), e.alice.ID, "spec.pdf", "application/pdf", strings.NewReader("%PDF"), -1)
	parts, _ := att.RefParts([]string{string(img.ID), string(pdf.ID)})
	if _, err := e.Intercom.PostTrusted(e.room, intercom.Squawk{From: e.alice.ID, Body: att.Names([]string{string(img.ID), string(pdf.ID)}), Parts: parts}); err != nil {
		t.Fatal(err)
	}
	body := e.page(t, p, "/app/user/?c="+url.QueryEscape(engID))
	for _, want := range []string{`<img loading="lazy" src="/app/user/blobs/` + string(img.ID) + `"`, `class="file-chip" href="/app/user/blobs/` + string(pdf.ID) + `"`, "spec.pdf", "4 B"} {
		if !strings.Contains(body, want) {
			t.Errorf("message missing %q", want)
		}
	}
	if strings.Contains(body, "shot.png, spec.pdf") {
		t.Error("a files-only message shouldn't repeat the names as its text")
	}
}

// Review fixes: a failed upload never leaves a chip "uploading"; a paste
// with text on the clipboard stays a text paste; copying a files-only
// bubble doesn't throw.
func TestComposerAttachRobustness(t *testing.T) {
	e, p := fixture()
	e.Deps.Attachments = jam.NewAttachments(e.lg, blobstore.NewMem(), e.Intercom, 25<<20)
	on := e.page(t, p, "/app/user/?c="+url.QueryEscape(engID))
	for _, want := range []string{"xhr.onerror=", "xhr.onabort=", "getData('text/plain')", "var b=m.querySelector('.body:not(.raw)')", "max 25 MB each"} {
		if !strings.Contains(on, want) {
			t.Errorf("page missing %q", want)
		}
	}
}
