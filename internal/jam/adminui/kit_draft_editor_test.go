package adminui_test

import (
	"bytes"
	"html"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminui"
)

// editorValue is the editor textarea's value as a browser holds it: the
// markup unescaped, less the one newline the HTML parser drops after the
// start tag.
func editorValue(t *testing.T, body string) string {
	t.Helper()
	i := strings.Index(body, `name="content"`)
	if i < 0 {
		t.Fatalf("no editor in:\n%s", body)
	}
	s := body[i:]
	s = s[strings.Index(s, ">")+1:]
	end := strings.Index(s, "</textarea>")
	if end < 0 {
		t.Fatal("unterminated textarea")
	}
	return strings.TrimPrefix(html.UnescapeString(s[:end]), "\n")
}

func TestDraftEditorSaves(t *testing.T) {
	store, h := draftFixture(t)
	body := get(t, h, "/app/admin/kits/app/draft?file=run.sh").Body.String()
	ed := section(body, "draft-editor")
	if editorValue(t, body) != "echo\n" || !strings.Contains(ed, `name="executable" value="1" checked`) || !strings.Contains(ed, `href="/app/admin/kits/app/draft/download?path=run.sh"`) {
		t.Fatalf("editor:\n%s", ed)
	}
	// The browser sends CRLF; unchecking Executable makes it 0644.
	rec := post(t, h, "/app/admin/kits/app/draft/save", url.Values{"etag": {hiddenETag(t, ed)}, "path": {"run.sh"}, "content": {"echo two\r\n"}})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "saved run.sh") {
		t.Fatalf("save = %d %s", rec.Code, rec.Body)
	}
	data, e, _, _ := jam.KitDraftFile(store, "app", "run.sh")
	if string(data) != "echo two\n" || e.Mode != 0o644 {
		t.Fatalf("stored %q %o", data, e.Mode)
	}
	// The answer re-opens the file at the draft's new ETag.
	if got := hiddenETag(t, section(rec.Body.String(), "draft-editor")); got != draftETag(t, store) {
		t.Fatalf("re-rendered editor etag = %q", got)
	}
	// An invalid kit.yml is saved; its problem shows.
	rec = post(t, h, "/app/admin/kits/app/draft/save", url.Values{"etag": {draftETag(t, store)}, "path": {"kit.yml"}, "content": {"kind: nope\n"}})
	if rec.Code != http.StatusOK || !strings.Contains(section(rec.Body.String(), "draft-publish"), "kind must be &#34;studio&#34;") {
		t.Fatalf("save kit.yml = %d %s", rec.Code, rec.Body)
	}
	if rec := post(t, h, "/app/admin/kits/app/draft/save", url.Values{"path": {"run.sh"}, "content": {"x"}}); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("no etag = %d", rec.Code)
	}
}

// Review Focus 1: two tabs. The second save is refused, keeps its typed
// text with the fixed message and a reload link, keeps its stale ETag and
// disables Save; the first tab's save stands.
func TestDraftStaleSaveKeepsText(t *testing.T) {
	store, h := draftFixture(t)
	tab1 := hiddenETag(t, section(get(t, h, "/app/admin/kits/app/draft?file=run.sh").Body.String(), "draft-editor"))
	tab2 := hiddenETag(t, section(get(t, h, "/app/admin/kits/app/draft?file=run.sh").Body.String(), "draft-editor"))
	if rec := post(t, h, "/app/admin/kits/app/draft/save", url.Values{"etag": {tab2}, "path": {"run.sh"}, "content": {"echo tab2\n"}, "executable": {"1"}}); rec.Code != http.StatusOK {
		t.Fatalf("tab 2 save = %d %s", rec.Code, rec.Body)
	}
	rec := post(t, h, "/app/admin/kits/app/draft/save", url.Values{"etag": {tab1}, "path": {"run.sh"}, "content": {"echo <tab1> typed\r\n"}, "executable": {"1"}})
	body := rec.Body.String()
	ed := section(body, "draft-editor")
	if rec.Code != http.StatusOK || !strings.Contains(ed, `draft app changed since you loaded it (reload) <a href="/app/admin/kits/app/draft?file=run.sh">reload</a>`) {
		t.Fatalf("stale save = %d:\n%s", rec.Code, ed)
	}
	if got := editorValue(t, body); got != "echo <tab1> typed\n" {
		t.Fatalf("typed text = %q", got)
	}
	if hiddenETag(t, ed) != tab1 || !strings.Contains(ed, `<button type="submit" class="primary" disabled>Save</button>`) {
		t.Fatalf("a stale editor must keep its etag and disable Save:\n%s", ed)
	}
	if data, _, _, _ := jam.KitDraftFile(store, "app", "run.sh"); string(data) != "echo tab2\n" {
		t.Fatalf("stored %q: a stale save overwrote tab 2", data)
	}
}

// Review Focus 2: what the editor shows, a browser submits back byte for byte.
func TestDraftEditorRoundTripsBytes(t *testing.T) {
	store, h := draftFixture(t)
	for p, content := range map[string]string{
		"bom.txt":     "\ufeffkey: value\n\tindented\n\nno final newline",
		"lead.txt":    "\n\nlead\n",
		"markup.html": "<script>alert(1)</script>\n&amp; </textarea>\n",
		"bidi.md":     "名前 ✓ \u202eevil\n",
	} {
		t.Run(p, func(t *testing.T) {
			putDraft(t, store, p, 0, content)
			body := get(t, h, "/app/admin/kits/app/draft?file="+url.QueryEscape(p)).Body.String()
			ed := section(body, "draft-editor")
			if strings.Contains(ed, "<script>alert") {
				t.Fatal("file content rendered as markup")
			}
			got := editorValue(t, body)
			if got != content {
				t.Fatalf("editor holds %q, want %q", got, content)
			}
			if invisible := strings.Contains(ed, `id="draft-invisible"`); invisible != (p == "bom.txt" || p == "bidi.md") {
				t.Errorf("invisible-characters note = %v", invisible)
			}
			// htmx 2 submits the textarea's text with LF; a native form
			// submission sends CRLF. Both must store the same bytes.
			for _, sent := range []string{got, strings.ReplaceAll(got, "\n", "\r\n")} {
				rec := post(t, h, "/app/admin/kits/app/draft/save", url.Values{"etag": {hiddenETag(t, ed)}, "path": {p}, "content": {sent}})
				if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), p+" unchanged") {
					t.Fatalf("save %q = %d %s", sent, rec.Code, rec.Body)
				}
				if data, _, _, _ := jam.KitDraftFile(store, "app", p); string(data) != content {
					t.Fatalf("stored %q, want %q", data, content)
				}
			}
		})
	}
	if body := get(t, h, "/app/admin/kits/app/draft?file=lead.txt").Body.String(); !strings.Contains(body, "wrap=\"off\">\n\n\nlead\n</textarea>") {
		t.Error("the template must write one newline after <textarea> so a leading newline survives")
	}
}

// Review Focus 3: only text of at most 1 MiB without CR is editable; any
// other file offers Download and Replace instead.
func TestDraftEditorNotEditable(t *testing.T) {
	store, h := draftFixture(t)
	putDraft(t, store, "max.txt", 0, strings.Repeat("a", 1<<20))
	putDraft(t, store, "over.txt", 0, strings.Repeat("a", 1<<20+1))
	putDraft(t, store, "win.txt", 0, "a\r\nb\r\n")
	putDraft(t, store, "blob.bin", 0, "\x00\x01")
	if ed := section(get(t, h, "/app/admin/kits/app/draft?file=max.txt").Body.String(), "draft-editor"); !strings.Contains(ed, `name="content"`) {
		t.Fatal("a 1 MiB text file should be editable")
	}
	for p, note := range map[string]string{"over.txt": "over 1 MiB", "win.txt": "carriage returns", "blob.bin": "binary file"} {
		ed := section(get(t, h, "/app/admin/kits/app/draft?file="+p).Body.String(), "draft-editor")
		if strings.Contains(ed, `name="content"`) || !strings.Contains(ed, `id="draft-noedit"`) || !strings.Contains(ed, note) || !strings.Contains(ed, `href="/app/admin/kits/app/draft/download?path=`+p+`"`) {
			t.Errorf("%s:\n%.600s", p, ed)
		}
	}
}

// An absent or unsafe ?file= is a 404 that never names it.
func TestDraftFileMissing(t *testing.T) {
	_, h := draftFixture(t)
	for _, p := range []string{"nope.txt", "a\u202eb", "../x"} {
		rec := get(t, h, "/app/admin/kits/app/draft?file="+url.QueryEscape(p))
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "No such file in the draft.") || strings.Contains(rec.Body.String(), "\u202e") || strings.Contains(rec.Body.String(), `name="content"`) {
			t.Errorf("%q = %d", p, rec.Code)
		}
	}
}

func TestDraftDownload(t *testing.T) {
	store, h := draftFixture(t)
	putDraft(t, store, "docs/名前.bin", 0, "\x00\x01<b>")
	rec := get(t, h, "/app/admin/kits/app/draft/download?path="+url.QueryEscape("docs/名前.bin"))
	hd := rec.Header()
	if rec.Code != http.StatusOK || rec.Body.String() != "\x00\x01<b>" || hd.Get("Content-Type") != "application/octet-stream" || hd.Get("X-Content-Type-Options") != "nosniff" || hd.Get("Cache-Control") != "no-store" {
		t.Fatalf("download = %d %v %q", rec.Code, hd, rec.Body)
	}
	if disp, params, err := mime.ParseMediaType(hd.Get("Content-Disposition")); err != nil || disp != "attachment" || params["filename"] != "名前.bin" {
		t.Fatalf("Content-Disposition = %q", hd.Get("Content-Disposition"))
	}
	if rec := get(t, h, "/app/admin/kits/app/draft/download?path=nope"); rec.Code != http.StatusNotFound {
		t.Fatalf("missing = %d", rec.Code)
	}
	if rec := get(t, h, "/app/admin/kits/app/draft/download?path="+url.QueryEscape("a\u202eb")); rec.Code != http.StatusBadRequest || strings.Contains(rec.Body.String(), "\u202e") {
		t.Fatalf("unsafe = %d", rec.Code)
	}
}

// draftGoneAfterRead is a store whose draft vanishes after its first read:
// the save reads it (and is refused stale), then a discard lands before the
// answer is built.
type draftGoneAfterRead struct {
	jam.Store
	reads int
}

func (s *draftGoneAfterRead) GetKitDraft(kit string) (jam.KitDraft, bool) {
	s.reads++
	if s.reads > 1 {
		return jam.KitDraft{}, false
	}
	return s.Store.GetKitDraft(kit)
}

// Ruling 17: when the draft is gone by the time a stale save's page is
// built, the 412 itself is answered (htmx leaves the typed text in place).
func TestDraftStaleSaveDraftGone(t *testing.T) {
	store, _ := draftFixture(t)
	stale := draftETag(t, store)
	putDraft(t, store, "run.sh", 0, "echo other\n")
	h := uiHandler(t, &draftGoneAfterRead{Store: store})
	rec := post(t, h, "/app/admin/kits/app/draft/save", url.Values{"etag": {stale}, "path": {"run.sh"}, "content": {"echo typed\n"}})
	if rec.Code != http.StatusPreconditionFailed || !strings.Contains(rec.Body.String(), "draft app changed since you loaded it (reload)") || strings.Contains(rec.Body.String(), "echo typed") {
		t.Fatalf("stale save, draft gone = %d %s", rec.Code, rec.Body)
	}
	if data, _, _, _ := jam.KitDraftFile(store, "app", "run.sh"); string(data) != "echo other\n" {
		t.Fatalf("stored %q", data)
	}
}

// Ruling 27: a save that changes the tree leaves an audit line (never
// content or a path); an unchanged save leaves none.
func TestDraftSaveAudit(t *testing.T) {
	store, _ := draftFixture(t)
	var logs bytes.Buffer
	h := adminui.Handler(store, slog.New(slog.NewTextHandler(&logs, nil)), nil, nil, anyCred, nil)
	if rec := post(t, h, "/app/admin/kits/app/draft/save", url.Values{"etag": {draftETag(t, store)}, "path": {"run.sh"}, "content": {"echo secret\n"}, "executable": {"1"}}); rec.Code != http.StatusOK {
		t.Fatalf("save = %d %s", rec.Code, rec.Body)
	}
	d, _ := store.GetKitDraft("app")
	if want := `msg="ui kit draft saved" operator="" kit=app tree=` + d.Tree[:12] + " changed=1\n"; !strings.Contains(logs.String(), want) {
		t.Errorf("logs missing %q:\n%s", want, logs.String())
	}
	if strings.Contains(logs.String(), "secret") || strings.Contains(logs.String(), "run.sh") {
		t.Errorf("the audit line names content or a path:\n%s", logs.String())
	}
	logs.Reset()
	if rec := post(t, h, "/app/admin/kits/app/draft/save", url.Values{"etag": {draftETag(t, store)}, "path": {"run.sh"}, "content": {"echo secret\n"}, "executable": {"1"}}); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "run.sh unchanged") {
		t.Fatalf("unchanged save = %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(logs.String(), "ui kit draft saved") {
		t.Errorf("an unchanged save was logged:\n%s", logs.String())
	}
}
