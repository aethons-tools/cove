package adminui_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminui"
	"github.com/aethons-tools/cove/internal/jam/appui"
	"github.com/aethons-tools/cove/internal/kittree"
	"github.com/aethons-tools/cove/internal/kittree/kittreetest"
)

func getDraft(t *testing.T, h http.Handler, query string) adminui.DraftPage {
	t.Helper()
	var pg adminui.DraftPage
	if rec := apiDo(t, h, "GET", draftAPI+query, nil, &pg); rec.Code != 200 {
		t.Fatalf("GET draft%s = %d %s", query, rec.Code, rec.Body)
	}
	return pg
}

func diffOps(lines []adminui.DiffLine) string {
	var out []string
	for _, l := range lines {
		if l.Op == "+" || l.Op == "-" {
			out = append(out, l.Op+" "+strings.TrimPrefix(strings.TrimPrefix(l.Text, "+ "), "- "))
		}
	}
	return strings.Join(out, "|")
}

// The draft view nests the tree by directory and marks each file against the
// base; removed files follow; the changes list and kit.yml's diff compare
// with the base; a file's diff shows its lines.
func TestDraftAPIViewVsBase(t *testing.T) {
	store, h := draftFixture(t)
	putDraft(t, store, "run.sh", 0, "echo two\n")
	putDraft(t, store, "docs/new.md", 0, "new\n")
	putDraft(t, store, "kit.yml", 0, "kind: studio\nprompt: <hi>\n")
	if _, err := jam.RemoveKitDraftFile(store, nil, "app", "gone.txt", draftETag(t, store), "local", time.Unix(200, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	d := getDraft(t, h, "").Draft
	if d == nil || d.Base != 1 || d.Superseded || d.ETag != draftETag(t, store) || len(d.Problems) != 0 || d.Editor != nil || d.FileMissing {
		t.Fatalf("draft = %+v", d)
	}
	var rows []string
	for _, r := range d.Tree {
		rows = append(rows, strings.Repeat(" ", r.Depth)+r.Name+":"+r.Change)
	}
	if strings.Join(rows, ",") != "docs:, a.md:, new.md:added,kit.yml:modified,run.sh:modified" {
		t.Errorf("tree = %q", rows)
	}
	if len(d.Removed) != 1 || d.Removed[0] != (adminui.DraftRow{Name: "gone.txt", Path: "gone.txt", Label: "gone.txt", Mode: "0644", Size: 4, Change: "removed"}) {
		t.Errorf("removed = %+v", d.Removed)
	}
	if c := d.Changes; c == nil || c.From != 1 || c.Unchanged != 1 || len(c.Rows) != 4 {
		t.Fatalf("changes = %+v", d.Changes)
	}
	if y := d.YAMLDiff; y == nil || y.From != 1 || diffOps(y.Lines) != "+ prompt: <hi>" || y.Hidden || y.Note != "" {
		t.Errorf("kit.yml diff = %+v", d.YAMLDiff)
	}
	f := getDraft(t, h, "?file=run.sh&diff=1").Draft
	if f.FileDiff == nil || f.FileDiff.From != 1 || diffOps(f.FileDiff.Lines) != "- echo|+ echo two" || f.Editor != nil {
		t.Errorf("file diff = %+v", f.FileDiff)
	}
	if g := getDraft(t, h, "?file=gone.txt&diff=1").Draft.FileDiff; g == nil || diffOps(g.Lines) != "- bye" {
		t.Errorf("removed file diff = %+v", g)
	}
	if x := getDraft(t, h, "?diff=1&file="+url.QueryEscape("x\u202ey")).Draft.FileDiff; x == nil || !x.Missing || len(x.Lines) != 0 {
		t.Errorf("hostile diff file = %+v", x)
	}
}

// A draft equal to its base changes nothing and has no kit.yml diff; one
// opened before another version landed is superseded; a kit.yml problem is
// listed through displayText.
func TestDraftAPIViewUnchangedSupersededProblems(t *testing.T) {
	store, h := draftFixture(t)
	d := getDraft(t, h, "").Draft
	if d.Changes == nil || len(d.Changes.Rows) != 0 || d.Changes.Unchanged != 4 || d.YAMLDiff != nil || d.Size == "" {
		t.Errorf("unchanged = %+v", d)
	}
	if _, _, err := jam.PushKitFiles(store, "app", kittreetest.Files("kit.yml", "kind: studio\nprompt: v2\n")); err != nil {
		t.Fatal(err)
	}
	putDraft(t, store, "kit.yml", 0, "kind: \"<x>\u202e\"\n")
	d = getDraft(t, h, "").Draft
	if !d.Superseded || d.Base != 1 || d.Current != 2 || len(d.Problems) == 0 || !strings.Contains(d.Problems[0], `"<x>`) || strings.Contains(d.Problems[0], "\u202e") {
		t.Errorf("superseded/problems = %+v %q", d, d.Problems)
	}
}

// A kit with no draft lists its versions to open one from; an unknown kit is
// a 404; a second open, an unknown version or a stale discard is refused.
func TestDraftAPIOpenAndDiscardRefusals(t *testing.T) {
	store := newStore(t)
	for _, y := range []string{"kind: studio\n", "kind: studio\nprompt: two\n"} {
		if _, _, err := jam.PushKitFiles(store, "app", kittreetest.Files("kit.yml", y)); err != nil {
			t.Fatal(err)
		}
	}
	h := draftHandler(store, testLogger())
	if pg := getDraft(t, h, ""); pg.Draft != nil || pg.Current != 2 || !slices.Equal(pg.Versions, []int{1, 2}) {
		t.Errorf("no draft = %+v", pg)
	}
	wantErr(t, apiDo(t, h, "GET", "/app/admin/api/kits/nope/draft", nil, nil), 404, "not_found")
	wantErr(t, apiDo(t, h, "POST", "/app/admin/api/kits/nope/draft", map[string]int{}, nil), 404, "not_found")
	var res adminui.DraftResult
	apiDo(t, h, "POST", draftAPI, map[string]int{}, &res)
	if res.Message != "opened a draft of app from v2" || res.Page.Draft.Base != 2 {
		t.Fatalf("open from current = %+v", res)
	}
	rec := apiDo(t, h, "POST", draftAPI, map[string]int{}, nil)
	wantErr(t, rec, 409, "conflict")
	if !strings.Contains(rec.Body.String(), "kit app already has a draft") {
		t.Errorf("second open = %s", rec.Body)
	}
	stale := draftETag(t, store)
	putDraft(t, store, "x.txt", 0, "x") // another tab saves meanwhile
	rec = apiDo(t, h, "POST", draftAPI+"/discard", map[string]string{"etag": stale}, nil)
	wantErr(t, rec, 412, "stale")
	if !strings.Contains(rec.Body.String(), "draft app changed since you loaded it (reload)") {
		t.Errorf("stale discard = %s", rec.Body)
	}
	if _, open := store.GetKitDraft("app"); !open {
		t.Fatal("a stale discard dropped the draft")
	}
	apiDo(t, h, "POST", draftAPI+"/discard", map[string]string{"etag": draftETag(t, store)}, &res)
	if res.Message != "discarded the draft of app" || res.Page.Draft != nil {
		t.Errorf("discard = %+v", res)
	}
	rec = apiDo(t, h, "POST", draftAPI, map[string]int{"from": 9}, nil)
	wantErr(t, rec, 404, "not_found")
	if !strings.Contains(rec.Body.String(), "kit app has no version v9") {
		t.Errorf("from 9 = %s", rec.Body)
	}
}

// The editor opens text of at most 1 MiB with no CR; anything else says why
// it isn't editable. An absent or unsafe file is missing, never named.
func TestDraftAPIEditor(t *testing.T) {
	store, h := draftFixture(t)
	putDraft(t, store, "max.txt", 0, strings.Repeat("a", 1<<20))
	putDraft(t, store, "over.txt", 0, strings.Repeat("a", 1<<20+1))
	putDraft(t, store, "win.txt", 0, "a\r\nb\r\n")
	putDraft(t, store, "blob.bin", 0, "\x00\x01")
	putDraft(t, store, "bidi.md", 0, "名前 ✓ \u202eevil\n")
	ed := getDraft(t, h, "?file=run.sh").Draft.Editor
	if ed == nil || !ed.Editable || ed.Text != "echo\n" || !ed.Exec || ed.Mode != "0755" || ed.Size != 5 || ed.ETag != draftETag(t, store) || ed.Invisible || ed.Path != "run.sh" {
		t.Fatalf("run.sh editor = %+v", ed)
	}
	if ed := getDraft(t, h, "?file=max.txt").Draft.Editor; !ed.Editable {
		t.Error("a 1 MiB text file should be editable")
	}
	if ed := getDraft(t, h, "?file=bidi.md").Draft.Editor; !ed.Editable || !ed.Invisible || ed.Text != "名前 ✓ \u202eevil\n" {
		t.Errorf("bidi editor = %+v", ed)
	}
	for p, note := range map[string]string{"over.txt": "over 1 MiB", "win.txt": "carriage returns", "blob.bin": "binary file"} {
		if ed := getDraft(t, h, "?file="+p).Draft.Editor; ed == nil || ed.Editable || ed.Text != "" || !strings.Contains(ed.Note, note) {
			t.Errorf("%s editor = %+v", p, ed)
		}
	}
	for _, p := range []string{"nope.txt", "a\u202eb", "../x"} {
		rec := apiDo(t, h, "GET", draftAPI+"?file="+url.QueryEscape(p), nil, nil)
		if rec.Code != 200 || strings.Contains(rec.Body.String(), "\u202e") || strings.Contains(rec.Body.String(), `"editor":{`) || !strings.Contains(rec.Body.String(), `"fileMissing":true`) {
			t.Errorf("%q = %d %s", p, rec.Code, rec.Body)
		}
	}
}

// A kit.yml holding a packed context never opens in the editor, and both
// sides of the diffs are masked alike: only the real edit is a change.
func TestDraftAPIPackedContextMasked(t *testing.T) {
	store := newStore(t)
	text := "kind: studio\nbogus: 1\nbase:\n  context: " + packCtx(t, "Dockerfile", "FROM x\n") + "\n"
	if _, err := store.PushKit("app", text); err != nil {
		t.Fatal(err)
	}
	if _, err := jam.OpenKitDraft(store, "app", 0, "local", time.Unix(100, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	putDraft(t, store, "kit.yml", 0, text+"prompt: hi\n")
	h := uiHandler(t, store)
	d := getDraft(t, h, "").Draft
	if y := d.YAMLDiff; y == nil || diffOps(y.Lines) != "+ prompt: hi" || y.Hidden {
		t.Errorf("kit.yml diff = %+v", d.YAMLDiff)
	}
	f := getDraft(t, h, "?file=kit.yml&diff=1").Draft.FileDiff
	if f == nil || diffOps(f.Lines) != "+ prompt: hi" {
		t.Errorf("file diff = %+v", f)
	}
	if ed := getDraft(t, h, "?file=kit.yml").Draft.Editor; ed == nil || ed.Editable || ed.Note != "This kit.yml holds a packed context; download, remove it, and replace" {
		t.Errorf("editor = %+v", ed)
	}
}

// A base kit.yml that parses but is hidden whole (an anchor and the word
// context) is hidden on both sides: masking alone never shows as a change.
func TestDraftAPIMasksParsedBaseLikeDraft(t *testing.T) {
	store := newStore(t)
	text := "kind: studio\nprompt: &a my context\n"
	if _, _, err := jam.PushKitFiles(store, "app", kittreetest.Files("kit.yml", text)); err != nil {
		t.Fatal(err)
	}
	if _, err := jam.OpenKitDraft(store, "app", 0, "local", time.Unix(100, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	putDraft(t, store, "kit.yml", 0, text+"egress: [x.example]\n")
	h := uiHandler(t, store)
	rec := apiDo(t, h, "GET", draftAPI+"?file=kit.yml&diff=1", nil, nil)
	if strings.Contains(rec.Body.String(), "my context") {
		t.Errorf("the hidden kit.yml leaked: %s", rec.Body)
	}
	d := getDraft(t, h, "?file=kit.yml&diff=1").Draft
	if y := d.YAMLDiff; y == nil || !y.Hidden || diffOps(y.Lines) != "" {
		t.Errorf("kit.yml diff = %+v", y)
	}
	if f := d.FileDiff; f == nil || !f.Hidden || diffOps(f.Lines) != "" {
		t.Errorf("file diff = %+v", f)
	}
}

// movedDraft is a store whose draft reads after the first answer the draft
// at another stored tree: the draft changes between the status read and the
// file read.
type movedDraft struct {
	jam.Store
	tree  string
	reads *int
}

func (m movedDraft) GetKitDraft(kit string) (jam.KitDraft, bool) {
	d, ok := m.Store.GetKitDraft(kit)
	if *m.reads++; *m.reads > 1 {
		d.Tree = m.tree
	}
	return d, ok
}

// spareTree pushes files as kit spare and returns its tree hash.
func spareTree(t *testing.T, store jam.Store, files []string) string {
	t.Helper()
	if _, _, err := jam.PushKitFiles(store, "spare", kittreetest.Files(files...)); err != nil {
		t.Fatal(err)
	}
	k, _ := store.GetKit("spare")
	return k.Trees[k.Current]
}

// A file the draft changed or removed while the view loaded carries the
// fixed note, never a diff of what was not read.
func TestDraftAPIChangedWhileLoading(t *testing.T) {
	for name, files := range map[string][]string{
		"modified": {"kit.yml", "kind: studio\n", "run.sh*", "echo\n", "docs/a.md", "# a\n", "gone.txt", "bye\n"},
		"removed":  {"kit.yml", "kind: studio\n", "docs/a.md", "# a\n", "gone.txt", "bye\n"},
	} {
		t.Run(name, func(t *testing.T) {
			store, _ := draftFixture(t)
			putDraft(t, store, "run.sh", 0, "echo two\n")
			reads := 0
			f := getDraft(t, uiHandler(t, movedDraft{store, spareTree(t, store, files), &reads}), "?file=run.sh&diff=1").Draft.FileDiff
			if f == nil || f.Note != "the draft changed while loading (reload)" || len(f.Lines) != 0 {
				t.Errorf("file diff = %+v", f)
			}
		})
	}
	store, _ := draftFixture(t)
	putDraft(t, store, "kit.yml", 0, "kind: studio\nprompt: a\n")
	reads := 0
	tree := spareTree(t, store, []string{"kit.yml", "kind: studio\n", "run.sh*", "echo\n", "docs/a.md", "# a\n", "gone.txt", "bye\n"})
	y := getDraft(t, uiHandler(t, movedDraft{store, tree, &reads}), "").Draft.YAMLDiff
	if y == nil || y.Note != "the draft changed while loading (reload)" || len(y.Lines) != 0 {
		t.Errorf("kit.yml diff = %+v", y)
	}
}

// missBlob is a store whose blob sha is lost.
type missBlob struct {
	jam.Store
	sha string
}

func (m missBlob) KitBlob(sha string) ([]byte, bool) {
	if sha == m.sha {
		return nil, false
	}
	return m.Store.KitBlob(sha)
}

// missTree is a store whose tree sha is lost.
type missTree struct {
	jam.Store
	sha string
}

func (m missTree) KitTree(sha string) (kittree.Tree, bool) {
	if sha == m.sha {
		return kittree.Tree{}, false
	}
	return m.Store.KitTree(sha)
}

func sum(s string) string {
	b := sha256.Sum256([]byte(s))
	return hex.EncodeToString(b[:])
}

// A store fault — a lost file blob for a diff or the editor, a lost base
// tree — answers the fixed 500 and logs it; never a view of a file read as
// empty.
func TestDraftAPIStoreFaults(t *testing.T) {
	store, _ := draftFixture(t)
	putDraft(t, store, "run.sh", 0, "echo two\n")
	k, _ := store.GetKit("app")
	for name, tc := range map[string]struct {
		st    jam.Store
		query string
	}{
		"diff blob":   {missBlob{store, sum("echo two\n")}, "?file=run.sh&diff=1"},
		"editor blob": {missBlob{store, sum("echo two\n")}, "?file=run.sh"},
		"base tree":   {missTree{store, k.Trees[1]}, ""},
	} {
		var logs bytes.Buffer
		h := adminui.Handler(tc.st, slog.New(slog.NewTextHandler(&logs, nil)), nil, nil, anyCred, nil)
		rec := apiDo(t, h, "GET", draftAPI+tc.query, nil, nil)
		wantErr(t, rec, 500, "internal")
		if !strings.Contains(rec.Body.String(), `"internal error"`) || !strings.Contains(logs.String(), `msg="ui kit draft: store error"`) {
			t.Errorf("%s = %s\n%s", name, rec.Body, logs.String())
		}
	}
}

// failDraftPut is a store whose PutKitDraft fails: a store fault.
type failDraftPut struct{ jam.Store }

func (failDraftPut) PutKitDraft(jam.KitDraft, jam.KitObjects) error {
	return errors.New("disk on fire")
}

// A save that changes the tree leaves an audit line (never content or a
// path); an unchanged save — CRLF folded to LF — leaves none and says so; a
// mode toggle to the same mode says unchanged too; a write's store fault is
// the fixed 500.
func TestDraftAPISaveAuditAndUnchanged(t *testing.T) {
	store, _ := draftFixture(t)
	var logs bytes.Buffer
	h := draftHandler(store, slog.New(slog.NewTextHandler(&logs, nil)))
	var res adminui.DraftResult
	apiDo(t, h, "POST", draftAPI+"/save", map[string]any{"etag": draftETag(t, store), "path": "run.sh", "content": "echo secret\r\n", "executable": true}, &res)
	d, _ := store.GetKitDraft("app")
	if want := `msg="ui kit draft saved" operator="" kit=app tree=` + d.Tree[:12] + " changed=1\n"; !strings.Contains(logs.String(), want) {
		t.Errorf("logs missing %q:\n%s", want, logs.String())
	}
	if strings.Contains(logs.String(), "secret") || strings.Contains(logs.String(), "run.sh") {
		t.Errorf("the audit line names content or a path:\n%s", logs.String())
	}
	if data, _, _, _ := jam.KitDraftFile(store, "app", "run.sh"); string(data) != "echo secret\n" {
		t.Errorf("stored %q", data)
	}
	logs.Reset()
	apiDo(t, h, "POST", draftAPI+"/save", map[string]any{"etag": draftETag(t, store), "path": "run.sh", "content": "echo secret\n", "executable": true}, &res)
	if res.Message != "run.sh unchanged" || strings.Contains(logs.String(), "ui kit draft saved") {
		t.Errorf("unchanged save = %q\n%s", res.Message, logs.String())
	}
	apiDo(t, h, "POST", draftAPI+"/mode", map[string]any{"etag": draftETag(t, store), "path": "run.sh", "executable": true}, &res)
	if res.Message != "run.sh unchanged" {
		t.Errorf("same-mode toggle = %q", res.Message)
	}
	wantErr(t, apiDo(t, h, "POST", draftAPI+"/save", map[string]any{"path": "run.sh", "content": "x"}, nil), 428, "internal")
	wantErr(t, apiDo(t, h, "POST", draftAPI+"/mode", map[string]any{"etag": draftETag(t, store), "path": "nope"}, nil), 404, "not_found")

	logs.Reset()
	bad := draftHandler(failDraftPut{store}, slog.New(slog.NewTextHandler(&logs, nil)))
	rec := apiDo(t, bad, "POST", draftAPI+"/save", map[string]any{"etag": draftETag(t, store), "path": "run.sh", "content": "echo other\n"}, nil)
	wantErr(t, rec, 500, "internal")
	if strings.Contains(rec.Body.String(), "disk on fire") || !strings.Contains(logs.String(), "disk on fire") {
		t.Errorf("store fault = %s\n%s", rec.Body, logs.String())
	}
}

// An upload's preamble: an unknown kit is a 404, a body that isn't
// multipart a 400, a chunked body past the cap a 413; Replace needs a file.
func TestDraftAPIUploadPreamble(t *testing.T) {
	store, h := draftFixture(t)
	wantErr(t, apiForm(t, h, "/app/admin/api/kits/nope/draft/add", map[string]string{"path": "x"}, nil, nil), 404, "not_found")
	r := httptest.NewRequest("POST", draftAPI+"/add", strings.NewReader("not multipart"))
	r.Header.Set("Content-Type", "multipart/form-data; boundary=x")
	r.Header.Set("Origin", "http://example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, appui.WithPrincipal(r, appui.Principal{Admin: true, Local: true}))
	wantErr(t, rec, 400, "bad_request")
	if !strings.Contains(rec.Body.String(), "invalid form") {
		t.Errorf("bad multipart = %s", rec.Body)
	}

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("etag", draftETag(t, store))
	_ = mw.WriteField("path", "big.bin")
	fw, _ := mw.CreateFormFile("file", "big.bin")
	_, _ = fw.Write(bytes.Repeat([]byte("x"), int(kittree.MaxFile)+2<<20))
	_ = mw.Close()
	r = httptest.NewRequest("POST", draftAPI+"/add", &body)
	r.ContentLength = -1 // chunked: the cap applies as it reads
	r.Header.Set("Content-Type", mw.FormDataContentType())
	r.Header.Set("Origin", "http://example.com")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, appui.WithPrincipal(r, appui.Principal{Admin: true, Local: true}))
	wantErr(t, rec, 413, "too_large")

	rec = apiForm(t, h, draftAPI+"/replace", map[string]string{"etag": draftETag(t, store), "path": "run.sh"}, nil, nil)
	wantErr(t, rec, 400, "bad_request")
	if !strings.Contains(rec.Body.String(), "choose a file to upload") {
		t.Errorf("replace without a file = %s", rec.Body)
	}
	var res adminui.DraftResult
	if rec := apiForm(t, h, draftAPI+"/replace", map[string]string{"etag": draftETag(t, store), "path": "run.sh", "executable": "1"}, []byte("echo\n"), &res); rec.Code != 200 || res.Message != "run.sh unchanged" {
		t.Errorf("same-bytes replace = %d %q", rec.Code, res.Message)
	}
	if rec := apiForm(t, h, draftAPI+"/replace", map[string]string{"etag": draftETag(t, store), "path": "run.sh"}, []byte("echo\n"), &res); rec.Code != 200 || res.Message != "saved run.sh" {
		t.Errorf("mode-dropping replace = %d %q", rec.Code, res.Message)
	}
	if _, e, _, _ := jam.KitDraftFile(store, "app", "run.sh"); e.Mode != 0o644 {
		t.Errorf("mode = %o, want 0644 without executable", e.Mode)
	}
}

// Publishing with Pin followers is refused (400) and publishes nothing: the
// draft is on a branch, and the config repo cannot hold a pinned follower.
func TestDraftAPIPublishPinRefused(t *testing.T) {
	store, h := draftFixture(t)
	if _, _, err := jam.PushKitFiles(store, "web", kittreetest.Files("kit.yml", "kind: studio\nbase:\n  kit: app\n")); err != nil {
		t.Fatal(err)
	}
	putDraft(t, store, "run.sh", 0, "echo two\n")
	before, _ := store.GetKit("app")
	rec := apiDo(t, h, "POST", draftAPI+"/publish", map[string]any{"etag": draftETag(t, store), "pin": true}, nil)
	wantErr(t, rec, 400, "bad_request")
	if !strings.Contains(rec.Body.String(), "Pin followers can't be committed") {
		t.Errorf("pin publish = %s", rec.Body)
	}
	if after, _ := store.GetKit("app"); after.Current != before.Current {
		t.Errorf("a refused publish made v%d", after.Current)
	}
}
