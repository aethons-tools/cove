package adminui_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// The unsaved-edit guard lives in the layout script: it marks the editor
// dirty on input, asks before another Draft-tab request or a link leaves
// it, warns on unload, and reads the dirty state back from each swapped
// #draft. The page carries the hooks it keys on.
func TestDraftEditorGuardHooks(t *testing.T) {
	_, h := draftFixture(t)
	body := get(t, h, "/app/admin/kits/app/draft?file=run.sh").Body.String()
	ed := section(body, "draft-editor")
	for _, want := range []string{
		`<form class="body" data-draft-save hx-post="/app/admin/kits/app/draft/save"`,
		`<textarea class="editor" name="content" data-draft-editor `,
		`<a class="sub" href="/app/admin/kits/app/draft/download?path=run.sh" download>Download</a>`,
	} {
		if !strings.Contains(ed, want) {
			t.Errorf("editor missing %q:\n%s", want, ed)
		}
	}
	if strings.Contains(ed, "data-unsaved") {
		t.Error("a freshly loaded editor is marked unsaved")
	}
	for _, want := range []string{
		`You have unsaved changes in the editor. Discard them?`,
		`'htmx:confirm'`, `'beforeunload'`, `textarea[data-draft-editor]`, `[data-draft-save]`, `[data-unsaved]`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("layout script missing %q", want)
		}
	}
}

// A stale save's editor holds unsaved text: it is rendered marked
// data-unsaved, so the guard stays armed after the swap.
func TestDraftStaleEditorMarkedUnsaved(t *testing.T) {
	store, h := draftFixture(t)
	stale := draftETag(t, store)
	putDraft(t, store, "run.sh", 0, "echo other\n")
	rec := post(t, h, "/app/admin/kits/app/draft/save", url.Values{"etag": {stale}, "path": {"run.sh"}, "content": {"echo typed\n"}})
	ed := section(rec.Body.String(), "draft-editor")
	if rec.Code != http.StatusOK || !strings.Contains(ed, `data-draft-editor data-unsaved`) {
		t.Fatalf("stale editor = %d:\n%s", rec.Code, ed)
	}
	if !strings.Contains(ed, "your text is still in the editor — copy it before reloading") {
		t.Errorf("stale banner:\n%s", ed)
	}
}
