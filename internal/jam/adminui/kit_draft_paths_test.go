package adminui_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
)

func TestDraftAddFile(t *testing.T) {
	store, h := draftFixture(t)
	if add := section(get(t, h, "/app/admin/kits/app/draft").Body.String(), "draft-add"); !strings.Contains(add, `hx-post="/app/admin/kits/app/draft/add"`) {
		t.Fatalf("no add form:\n%s", add)
	}
	rec := postMultipart(t, h, "/app/admin/kits/app/draft/add", map[string]string{"etag": draftETag(t, store), "path": " scripts/new.sh ", "executable": "1"}, nil)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "added scripts/new.sh") || editorValue(t, body) != "" {
		t.Fatalf("add empty = %d %s", rec.Code, body)
	}
	if data, e, _, err := jam.KitDraftFile(store, "app", "scripts/new.sh"); err != nil || len(data) != 0 || e.Mode != 0o755 {
		t.Fatalf("added %q %o %v", data, e.Mode, err)
	}
	rec = postMultipart(t, h, "/app/admin/kits/app/draft/add", map[string]string{"etag": draftETag(t, store), "path": "data/blob.bin"}, []byte{0, 1, 2})
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "evil") {
		t.Fatalf("add upload = %d %s", rec.Code, rec.Body)
	}
	if data, e, _, err := jam.KitDraftFile(store, "app", "data/blob.bin"); err != nil || string(data) != "\x00\x01\x02" || e.Mode != 0o644 {
		t.Fatalf("uploaded %q %o %v", data, e.Mode, err)
	}
	// An add never overwrites.
	rec = postMultipart(t, h, "/app/admin/kits/app/draft/add", map[string]string{"etag": draftETag(t, store), "path": "run.sh"}, []byte("clobber"))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "draft app already has a file run.sh") {
		t.Fatalf("add over a file = %d %s", rec.Code, rec.Body)
	}
	// jam takes the etag unquoted too: that is still the form's etag, so 409.
	if rec := postMultipart(t, h, "/app/admin/kits/app/draft/add", map[string]string{"etag": " " + strings.Trim(draftETag(t, store), `"`) + " ", "path": "run.sh"}, []byte("clobber")); rec.Code != http.StatusConflict {
		t.Fatalf("add over a file, unquoted etag = %d %s", rec.Code, rec.Body)
	}
	if data, _, _, _ := jam.KitDraftFile(store, "app", "run.sh"); string(data) != "echo\n" {
		t.Fatalf("run.sh = %q", data)
	}
}

// Ruling 21: an add from a form loaded before another save is stale (412),
// whether or not its path is taken by then, and writes nothing.
func TestDraftAddStale(t *testing.T) {
	store, h := draftFixture(t)
	old := draftETag(t, store)
	putDraft(t, store, "taken.txt", 0o644, "theirs\n")
	now := draftETag(t, store)
	for _, p := range []string{"fresh.txt", "taken.txt"} {
		rec := postMultipart(t, h, "/app/admin/kits/app/draft/add", map[string]string{"etag": old, "path": p}, []byte("mine\n"))
		if rec.Code != http.StatusPreconditionFailed || !strings.Contains(rec.Body.String(), "draft app changed since you loaded it (reload)") {
			t.Errorf("stale add %s = %d %s", p, rec.Code, rec.Body)
		}
	}
	if draftETag(t, store) != now {
		t.Fatal("a stale add changed the draft")
	}
	if data, _, _, _ := jam.KitDraftFile(store, "app", "taken.txt"); string(data) != "theirs\n" {
		t.Fatalf("taken.txt = %q", data)
	}
}

// Review Focus 4: hostile paths are refused by CheckPath on add and rename,
// never echoed; a graphic name with markup is fine and shown escaped.
func TestDraftHostilePaths(t *testing.T) {
	store, h := draftFixture(t)
	tag := draftETag(t, store)
	for _, p := range []string{"‮txt.exe", "a/../b", "/etc/passwd", "a\x1b[31mb", "docs//x", "docs/<b>名前‮.md", " "} {
		for _, rec := range []*httptest.ResponseRecorder{
			postMultipart(t, h, "/app/admin/kits/app/draft/add", map[string]string{"etag": tag, "path": p}, nil),
			post(t, h, "/app/admin/kits/app/draft/rename", url.Values{"etag": {tag}, "from": {"run.sh"}, "to": {p}}),
		} {
			body := rec.Body.String()
			if rec.Code != http.StatusBadRequest || !strings.Contains(body, "unsafe path") || strings.Contains(body, "‮") || strings.Contains(body, "txt.exe") || strings.Contains(body, "passwd") || strings.Contains(body, "\x1b") || strings.Contains(body, "名前") {
				t.Errorf("%q = %d %s", p, rec.Code, body)
			}
		}
	}
	if draftETag(t, store) != tag {
		t.Fatal("a hostile path changed the draft")
	}
	// Ruling F1: no "/" inside the markup, so the tree's visible name is the
	// whole markup, asserted escaped as text (not only in data-path).
	rec := postMultipart(t, h, "/app/admin/kits/app/draft/add", map[string]string{"etag": tag, "path": "docs/<b>名前.md"}, []byte("x\n"))
	body := rec.Body.String()
	tree := section(body, "draft-tree")
	if rec.Code != http.StatusOK || strings.Contains(body, "<b>名前") || !strings.Contains(tree, `data-path="docs/&lt;b&gt;名前.md"`) || !strings.Contains(tree, `">&lt;b&gt;名前.md</a>`) || !strings.Contains(body, "added docs/&lt;b&gt;名前.md") {
		t.Fatalf("markup name = %d %s", rec.Code, body)
	}
}

func TestDraftRenameAndDelete(t *testing.T) {
	store, h := draftFixture(t)
	ed := section(get(t, h, "/app/admin/kits/app/draft?file=run.sh").Body.String(), "draft-editor")
	if !strings.Contains(ed, `hx-confirm="Remove run.sh from the draft?"`) || !strings.Contains(ed, `name="to" required value="run.sh"`) {
		t.Fatalf("path actions:\n%s", ed)
	}
	rec := post(t, h, "/app/admin/kits/app/draft/rename", url.Values{"etag": {hiddenETag(t, ed)}, "from": {"run.sh"}, "to": {" bin/run.sh "}})
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "renamed run.sh to bin/run.sh") || editorValue(t, body) != "echo\n" {
		t.Fatalf("rename = %d %s", rec.Code, body)
	}
	tree := section(body, "draft-tree")
	if !strings.Contains(tree, `data-path="bin/run.sh" data-change="added"`) || !strings.Contains(tree, `data-path="run.sh" data-change="removed"`) {
		t.Fatalf("tree after rename:\n%s", tree)
	}
	if _, e, _, err := jam.KitDraftFile(store, "app", "bin/run.sh"); err != nil || e.Mode != 0o755 {
		t.Fatalf("bin/run.sh %o %v", e.Mode, err)
	}
	if rec := post(t, h, "/app/admin/kits/app/draft/rename", url.Values{"etag": {draftETag(t, store)}, "from": {"bin/run.sh"}, "to": {"kit.yml"}}); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "draft app already has a file kit.yml") {
		t.Fatalf("rename onto a file = %d %s", rec.Code, rec.Body)
	}
	if rec := post(t, h, "/app/admin/kits/app/draft/rename", url.Values{"etag": {hiddenETag(t, ed)}, "from": {"bin/run.sh"}, "to": {"x.sh"}}); rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale rename = %d", rec.Code)
	}
	rec = post(t, h, "/app/admin/kits/app/draft/delete", url.Values{"etag": {draftETag(t, store)}, "path": {"docs/a.md"}})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "removed docs/a.md") {
		t.Fatalf("delete = %d %s", rec.Code, rec.Body)
	}
	if _, _, _, err := jam.KitDraftFile(store, "app", "docs/a.md"); jam.WriteStatus(err, 500) != http.StatusNotFound {
		t.Fatalf("docs/a.md still there: %v", err)
	}
	if rec := post(t, h, "/app/admin/kits/app/draft/delete", url.Values{"etag": {hiddenETag(t, ed)}, "path": {"kit.yml"}}); rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale delete = %d", rec.Code)
	}
	if rec := post(t, h, "/app/admin/kits/app/draft/delete", url.Values{"path": {"kit.yml"}}); rec.Code != http.StatusPreconditionRequired && rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("delete without an etag = %d", rec.Code)
	}
	if _, _, _, err := jam.KitDraftFile(store, "app", "kit.yml"); err != nil {
		t.Fatal("a stale delete removed kit.yml")
	}
}
