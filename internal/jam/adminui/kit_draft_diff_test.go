package adminui_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
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
		if !strings.Contains(s, "1 unchanged line") || strings.Contains(s, `class="d-del"`) || strings.Contains(s, `class="d-add"`) || strings.Contains(s, "my context") {
			t.Errorf("%s:\n%s", name, s)
		}
	}
}
