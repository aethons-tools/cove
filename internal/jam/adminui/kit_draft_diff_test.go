package adminui_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminui"
	"github.com/aethons-tools/cove/internal/kittree/kittreetest"
)

func TestDraftDiffVsBase(t *testing.T) {
	store, h := draftFixture(t)
	putDraft(t, store, "run.sh", 0, "echo two\n")
	putDraft(t, store, "kit.yml", 0, "kind: studio\nprompt: <hi>\n")
	if _, err := jam.RemoveKitDraftFile(store, nil, "app", "gone.txt", draftETag(t, store), "local", time.Unix(200, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	body := get(t, h, "/app/admin/kits/app/draft").Body.String()
	ch := section(body, "draft-changes")
	for _, want := range []string{
		"<h2>Changes vs v1</h2>",
		`<tr data-change="modified"><td>modified</td><td class="mono"><a href="/app/admin/kits/app/draft?file=run.sh&amp;diff=1">run.sh</a></td><td class="mono">0755</td><td>5 B → 9 B</td></tr>`,
		`<tr data-change="removed"><td>removed</td><td class="mono"><a href="/app/admin/kits/app/draft?file=gone.txt&amp;diff=1">gone.txt</a></td>`,
		"1 unchanged",
	} {
		if !strings.Contains(ch, want) {
			t.Errorf("changes missing %q:\n%s", want, ch)
		}
	}
	if y := section(body, "draft-yaml-diff"); !strings.Contains(y, `<span class="d-add">+ prompt: &lt;hi&gt;</span>`) {
		t.Errorf("kit.yml diff:\n%s", y)
	}
	f := get(t, h, "/app/admin/kits/app/draft?file=run.sh&diff=1").Body.String()
	if cf := section(f, "ctx-file"); !strings.Contains(cf, "v1 → draft") || !strings.Contains(cf, `<span class="d-del">- echo</span>`) || !strings.Contains(cf, `<span class="d-add">+ echo two</span>`) || strings.Contains(f, `name="content"`) {
		t.Errorf("file diff:\n%s", cf)
	}
	if cf := section(get(t, h, "/app/admin/kits/app/draft?file=gone.txt&diff=1").Body.String(), "ctx-file"); !strings.Contains(cf, `<span class="d-del">- bye</span>`) {
		t.Errorf("removed file diff:\n%s", cf)
	}
	rec := get(t, h, "/app/admin/kits/app/draft?diff=1&file="+url.QueryEscape("x\u202ey"))
	if rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "\u202e") || !strings.Contains(rec.Body.String(), "No such file in either version") {
		t.Errorf("hostile diff file = %d", rec.Code)
	}
}

// A draft equal to its base lists no change and shows no kit.yml diff.
func TestDraftDiffUnchanged(t *testing.T) {
	_, h := draftFixture(t)
	body := get(t, h, "/app/admin/kits/app/draft").Body.String()
	if ch := section(body, "draft-changes"); !strings.Contains(ch, "No file changed; 4 unchanged") || strings.Contains(body, `id="draft-yaml-diff"`) {
		t.Errorf("unchanged draft:\n%s", ch)
	}
}

// Ruling 25 / F10: a base whose kit.yml holds a packed context is masked on
// both sides of the diffs the same way, so the base64 is never shown and
// only the real edit is a change; the editor alone shows the raw text.
func TestDraftDiffMasksPackedContext(t *testing.T) {
	store := newStore(t)
	b64 := packCtx(t, "Dockerfile", "FROM x\n")
	text := "kind: studio\nbogus: 1\nbase:\n  context: " + b64 + "\n"
	if _, err := store.PushKit("bad", text); err != nil {
		t.Fatal(err)
	}
	if _, err := jam.OpenKitDraft(store, "bad", 0, "local", time.Unix(100, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	st, err := jam.KitDraftStatusOf(store, "bad")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := jam.PutKitDraftFile(store, nil, "bad", "kit.yml", 0, []byte(text+"prompt: hi\n"), st.ETag, "local", time.Unix(200, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	h := uiHandler(t, store)

	body := get(t, h, "/app/admin/kits/bad/draft").Body.String()
	y := section(body, "draft-yaml-diff")
	if strings.Contains(body, "H4sI") || !strings.Contains(y, `<span class="d-add">+ prompt: hi</span>`) || strings.Count(y, `class="d-add"`) != 1 || strings.Contains(y, `class="d-del"`) {
		t.Errorf("kit.yml diff:\n%s", y)
	}

	f := get(t, h, "/app/admin/kits/bad/draft?file=kit.yml&diff=1").Body.String()
	cf := section(f, "ctx-file")
	if strings.Contains(f, "H4sI") || !strings.Contains(cf, "«packed context, not shown»") || !strings.Contains(cf, `<span class="d-add">+ prompt: hi</span>`) || strings.Count(cf, `class="d-add"`) != 1 || strings.Contains(cf, `class="d-del"`) {
		t.Errorf("kit.yml file diff:\n%s", cf)
	}

	if strings.Contains(y+cf, "This kit.yml is not shown") {
		t.Errorf("a partly masked kit.yml has the hidden note")
	}

	e := get(t, h, "/app/admin/kits/bad/draft?file=kit.yml").Body.String()
	if ed := section(e, "draft-editor"); !strings.Contains(ed, b64) || strings.Contains(section(e, "draft-yaml-diff"), "H4sI") {
		t.Errorf("editor should show the raw text, the diff never:\n%s", ed)
	}
}

// F10: a base kit.yml that parses but that maskKitYML hides whole (an anchor,
// and the word context) is hidden on the base side too, so masking alone
// never shows as a change.
func TestDraftDiffMasksParsedBaseLikeDraft(t *testing.T) {
	store := newStore(t)
	text := "kind: studio\nprompt: &a my context\n"
	if _, _, err := jam.PushKitFiles(store, "anc", kittreetest.Files("kit.yml", text)); err != nil {
		t.Fatal(err)
	}
	if _, err := jam.OpenKitDraft(store, "anc", 0, "local", time.Unix(100, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	st, err := jam.KitDraftStatusOf(store, "anc")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := jam.PutKitDraftFile(store, nil, "anc", "kit.yml", 0, []byte(text+"egress: [x.example]\n"), st.ETag, "local", time.Unix(200, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	h := uiHandler(t, store)
	y := section(get(t, h, "/app/admin/kits/anc/draft").Body.String(), "draft-yaml-diff")
	cf := section(get(t, h, "/app/admin/kits/anc/draft?file=kit.yml&diff=1").Body.String(), "ctx-file")
	for name, s := range map[string]string{"kit.yml diff": y, "file diff": cf} {
		if !strings.Contains(s, "1 unchanged line") || !strings.Contains(s, "This kit.yml is not shown") || strings.Contains(s, `class="d-del"`) || strings.Contains(s, `class="d-add"`) || strings.Contains(s, "my context") {
			t.Errorf("%s:\n%s", name, s)
		}
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

func sum(s string) string {
	b := sha256.Sum256([]byte(s))
	return hex.EncodeToString(b[:])
}

// A store fault reading a file for its diff answers the fixed 500, logged;
// never a diff of a file read as empty.
func TestDraftDiffStoreFault(t *testing.T) {
	store, _ := draftFixture(t)
	putDraft(t, store, "run.sh", 0, "echo two\n")
	var logs bytes.Buffer
	h := adminui.Handler(missBlob{store, sum("echo two\n")}, slog.New(slog.NewTextHandler(&logs, nil)), nil, nil, anyCred, nil)
	rec := get(t, h, "/app/admin/kits/app/draft?file=run.sh&diff=1")
	if rec.Code != http.StatusInternalServerError || strings.TrimSpace(rec.Body.String()) != `<p class="error">internal error</p>` {
		t.Fatalf("store fault = %d %s", rec.Code, rec.Body)
	}
	if !strings.Contains(logs.String(), `msg="ui kit draft: store error"`) {
		t.Errorf("store fault not logged:\n%s", logs.String())
	}
}

// movedDraft is a store whose draft reads after the first answer the draft
// at another stored tree: the draft changes between the page's status read
// and its file read.
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

// A file the draft changed or removed while the page loaded shows the fixed
// note, never a diff of what was not read.
func TestDraftDiffChangedWhileLoading(t *testing.T) {
	for name, files := range map[string][]string{
		// v1's own tree: run.sh back to "echo\n".
		"modified": {"kit.yml", "kind: studio\n", "run.sh*", "echo\n", "docs/a.md", "# a\n", "gone.txt", "bye\n"},
		"removed":  {"kit.yml", "kind: studio\n", "docs/a.md", "# a\n", "gone.txt", "bye\n"},
	} {
		t.Run(name, func(t *testing.T) {
			store, _ := draftFixture(t)
			putDraft(t, store, "run.sh", 0, "echo two\n")
			reads := 0
			h := uiHandler(t, movedDraft{store, spareTree(t, store, files), &reads})
			rec := get(t, h, "/app/admin/kits/app/draft?file=run.sh&diff=1")
			cf := section(rec.Body.String(), "ctx-file")
			if rec.Code != http.StatusOK || !strings.Contains(cf, "the draft changed while loading (reload)") || strings.Contains(cf, `class="d-`) {
				t.Errorf("= %d:\n%s", rec.Code, cf)
			}
		})
	}
	store, _ := draftFixture(t)
	putDraft(t, store, "kit.yml", 0, "kind: studio\nprompt: a\n")
	reads := 0
	tree := spareTree(t, store, []string{"kit.yml", "kind: studio\n", "run.sh*", "echo\n", "docs/a.md", "# a\n", "gone.txt", "bye\n"})
	y := section(get(t, uiHandler(t, movedDraft{store, tree, &reads}), "/app/admin/kits/app/draft").Body.String(), "draft-yaml-diff")
	if !strings.Contains(y, "the draft changed while loading (reload)") || strings.Contains(y, `class="d-`) {
		t.Errorf("kit.yml diff:\n%s", y)
	}
}

// spareTree pushes files as kit spare and returns its tree hash: a stored
// tree the store keeps.
func spareTree(t *testing.T, store jam.Store, files []string) string {
	t.Helper()
	if _, _, err := jam.PushKitFiles(store, "spare", kittreetest.Files(files...)); err != nil {
		t.Fatal(err)
	}
	k, _ := store.GetKit("spare")
	return k.Trees[k.Current]
}

// countDraft counts the draft reads.
type countDraft struct {
	jam.Store
	reads *int
}

func (c countDraft) GetKitDraft(kit string) (jam.KitDraft, bool) {
	*c.reads++
	return c.Store.GetKitDraft(kit)
}

// ?file=kit.yml&diff=1 reads kit.yml once for both its diffs: the status
// read and one file read.
func TestDraftDiffReadsKitYMLOnce(t *testing.T) {
	store, _ := draftFixture(t)
	putDraft(t, store, "kit.yml", 0, "kind: studio\nprompt: a\n")
	reads := 0
	body := get(t, uiHandler(t, countDraft{store, &reads}), "/app/admin/kits/app/draft?file=kit.yml&diff=1").Body.String()
	if reads != 2 || !strings.Contains(section(body, "draft-yaml-diff"), "+ prompt: a") || !strings.Contains(section(body, "ctx-file"), "+ prompt: a") {
		t.Errorf("draft reads = %d, want 2", reads)
	}
}
