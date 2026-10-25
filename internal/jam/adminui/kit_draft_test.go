package adminui_test

import (
	"bytes"
	"errors"
	"html"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminui"
	"github.com/aethons-tools/cove/internal/kittree/kittreetest"
)

// draftFixture pushes kit app (kit.yml, run.sh 0755, docs/a.md, gone.txt) as
// v1, opens its draft from v1 and returns the store and the UI.
func draftFixture(t *testing.T) (jam.Store, http.Handler) {
	t.Helper()
	store := newStore(t)
	if _, _, err := jam.PushKitFiles(store, "app", kittreetest.Files("kit.yml", "kind: studio\n", "run.sh*", "echo\n", "docs/a.md", "# a\n", "gone.txt", "bye\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := jam.OpenKitDraft(store, "app", 0, "local", time.Unix(100, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	return store, uiHandler(t, store)
}

// draftETag is kit app's draft ETag now.
func draftETag(t *testing.T, store jam.Store) string {
	t.Helper()
	st, err := jam.KitDraftStatusOf(store, "app")
	if err != nil {
		t.Fatal(err)
	}
	return st.ETag
}

// putDraft writes file p into app's draft (another tab, the CLI).
func putDraft(t *testing.T, store jam.Store, p string, mode fs.FileMode, data string) {
	t.Helper()
	if _, err := jam.PutKitDraftFile(store, nil, "app", p, mode, []byte(data), draftETag(t, store), "local", time.Unix(200, 0).UTC()); err != nil {
		t.Fatal(err)
	}
}

// hiddenETag is the first hidden etag field's value in s, unescaped.
func hiddenETag(t *testing.T, s string) string {
	t.Helper()
	const k = `name="etag" value="`
	i := strings.Index(s, k)
	if i < 0 {
		t.Fatalf("no etag field in:\n%s", s)
	}
	s = s[i+len(k):]
	return html.UnescapeString(s[:strings.Index(s, `"`)])
}

// draftWriteRoutes are the Draft tab's writes, under /app/admin/kits/app;
// each later task appends its own.
var draftWriteRoutes = []string{"/draft", "/draft/discard"}

// Every Draft-tab write refuses a cross-origin form POST (403) before it
// reads the form, and changes nothing.
func TestDraftWritesRefuseCrossOrigin(t *testing.T) {
	store, h := draftFixture(t)
	before := draftETag(t, store)
	form := url.Values{"etag": {before}, "path": {"run.sh"}, "from": {"run.sh"}, "to": {"x.sh"}, "content": {"pwned"}, "pin": {"1"}}.Encode()
	for _, route := range draftWriteRoutes {
		req := httptest.NewRequest(http.MethodPost, "/app/admin/kits/app"+route, strings.NewReader(form))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", "http://evil.example")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("cross-origin POST %s = %d, want 403", route, rec.Code)
		}
	}
	if draftETag(t, store) != before {
		t.Fatal("a cross-origin write changed the draft")
	}
}

func TestDraftTabNoDraft(t *testing.T) {
	store := newStore(t)
	for _, y := range []string{"kind: studio\n", "kind: studio\nprompt: two\n"} {
		if _, _, err := jam.PushKitFiles(store, "app", kittreetest.Files("kit.yml", y)); err != nil {
			t.Fatal(err)
		}
	}
	rec := get(t, uiHandler(t, store), "/app/admin/kits/app/draft")
	none := section(rec.Body.String(), "draft-none")
	for _, want := range []string{`<option value="1">v1</option>`, `<option value="2" selected>v2 (current)</option>`, `hx-post="/app/admin/kits/app/draft"`} {
		if rec.Code != http.StatusOK || !strings.Contains(none, want) {
			t.Errorf("no-draft card (%d) missing %q:\n%s", rec.Code, want, none)
		}
	}
	if !strings.Contains(rec.Body.String(), `<a href="/app/admin/kits/app/draft" aria-current="page">Draft</a>`) {
		t.Error("the Draft sub-tab is not current")
	}
}

func TestDraftTabUnknownKit(t *testing.T) {
	rec := get(t, uiHandler(t, newStore(t)), "/app/admin/kits/nope/draft")
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "Kit not found") {
		t.Fatalf("unknown kit = %d", rec.Code)
	}
}

// The tree nests by directory, gives each file its mode and size, and marks
// it against the base; removed files follow it.
func TestDraftTabTree(t *testing.T) {
	store, h := draftFixture(t)
	putDraft(t, store, "docs/a.md", 0, "# changed\n")
	putDraft(t, store, "docs/new.md", 0, "new\n")
	if _, err := jam.RemoveKitDraftFile(store, nil, "app", "gone.txt", draftETag(t, store), "local", time.Unix(200, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	body := get(t, h, "/app/admin/kits/app/draft").Body.String()
	tree := section(body, "draft-tree")
	for _, want := range []string{
		`<tr class="dir"><td class="name mono">docs/</td>`,
		`<tr data-path="docs/a.md" data-change="modified"><td class="name mono">  <a href=`,
		`<tr data-path="docs/new.md" data-change="added">`,
		`<tr data-path="run.sh"><td class="name mono"><a href="/app/admin/kits/app/draft?file=run.sh">run.sh</a></td><td class="mono">0755</td><td>5 B</td>`,
		`<tr data-path="gone.txt" data-change="removed"><td class="name mono"><a href="/app/admin/kits/app/draft?file=gone.txt&amp;diff=1">gone.txt</a></td><td class="mono">0644</td><td>4 B</td>`,
	} {
		if !strings.Contains(tree, want) {
			t.Errorf("tree missing %q:\n%s", want, tree)
		}
	}
	if i, j := strings.Index(tree, "docs/</td>"), strings.Index(tree, `data-path="kit.yml"`); i < 0 || j < i {
		t.Error("the docs directory should come before kit.yml")
	}
	if strings.Contains(body, `id="draft-superseded"`) || !strings.Contains(body, `id="draft-no-problems"`) {
		t.Error("a fresh, valid draft shows neither the superseded notice nor problems")
	}
	if got := hiddenETag(t, section(body, "draft-status")); got != draftETag(t, store) {
		t.Fatalf("discard form etag = %q", got)
	}
}

// A draft opened before another version landed says so; a problem is listed
// escaped and through displayText.
func TestDraftTabSupersededAndProblems(t *testing.T) {
	store, h := draftFixture(t)
	if _, _, err := jam.PushKitFiles(store, "app", kittreetest.Files("kit.yml", "kind: studio\nprompt: v2\n")); err != nil {
		t.Fatal(err)
	}
	putDraft(t, store, "kit.yml", 0, "kind: \"<x>\"\n")
	body := get(t, h, "/app/admin/kits/app/draft").Body.String()
	if s := section(body, "draft-status"); !strings.Contains(s, `id="draft-superseded">base v1, current v2:`) {
		t.Errorf("superseded notice:\n%s", s)
	}
	pub := section(body, "draft-publish")
	if !strings.Contains(pub, `id="draft-problems"`) || !strings.Contains(pub, `got &#34;&lt;x&gt;&#34;`) || strings.Contains(pub, "<x>") {
		t.Errorf("problems panel:\n%s", pub)
	}
}

// The kit page opens a draft from the viewed version; the page, its header
// and the kits list then mark the kit.
func TestOpenDraftFromKitPage(t *testing.T) {
	store := newStore(t)
	if _, _, err := jam.PushKitFiles(store, "app", kittreetest.Files("kit.yml", "kind: studio\n")); err != nil {
		t.Fatal(err)
	}
	h := uiHandler(t, store)
	page := get(t, h, "/app/admin/kits/app").Body.String()
	for _, want := range []string{`id="kit-open-draft">Open draft from v1</button>`, `<input type="hidden" name="from" value="1">`, `<a href="/app/admin/kits/app/draft">Draft</a>`} {
		if !strings.Contains(page, want) {
			t.Errorf("kit page missing %q", want)
		}
	}
	rec := post(t, h, "/app/admin/kits/app/draft", url.Values{"from": {"1"}})
	if rec.Code != http.StatusOK || rec.Header().Get("HX-Redirect") != "/app/admin/kits/app/draft" {
		t.Fatalf("open = %d %q: %s", rec.Code, rec.Header().Get("HX-Redirect"), rec.Body)
	}
	if d, ok := store.GetKitDraft("app"); !ok || d.Base != 1 {
		t.Fatalf("draft = %+v %v", d, ok)
	}
	page = get(t, h, "/app/admin/kits/app").Body.String()
	if !strings.Contains(page, `id="kit-draft-chip" href="/app/admin/kits/app/draft">draft · base v1</a>`) || strings.Contains(page, `id="kit-open-draft"`) {
		t.Error("kit page should link the open draft instead of offering a new one")
	}
	if list := get(t, h, "/app/admin/kits").Body.String(); !strings.Contains(list, `<a class="chip" href="/app/admin/kits/app/draft" title="An open draft">draft</a>`) {
		t.Error("kits list should mark the kit with a draft")
	}
}

func TestOpenDraftRefusals(t *testing.T) {
	store, h := draftFixture(t)
	if rec := post(t, h, "/app/admin/kits/app/draft", url.Values{}); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "kit app already has a draft") {
		t.Fatalf("second open = %d %s", rec.Code, rec.Body)
	}
	if _, err := jam.DiscardKitDraft(store, nil, "app", draftETag(t, store)); err != nil {
		t.Fatal(err)
	}
	if rec := post(t, h, "/app/admin/kits/app/draft", url.Values{"from": {"abc"}}); rec.Code != http.StatusBadRequest {
		t.Fatalf("from=abc = %d", rec.Code)
	}
	if rec := post(t, h, "/app/admin/kits/app/draft", url.Values{"from": {"9"}}); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "kit app has no version v9") {
		t.Fatalf("from=9 = %d %s", rec.Code, rec.Body)
	}
	if rec := post(t, h, "/app/admin/kits/nope/draft", url.Values{}); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown kit = %d", rec.Code)
	}
}

func TestDiscardDraft(t *testing.T) {
	store, h := draftFixture(t)
	tag := hiddenETag(t, section(get(t, h, "/app/admin/kits/app/draft").Body.String(), "draft-status"))
	putDraft(t, store, "x.txt", 0, "x") // another tab saves meanwhile
	rec := post(t, h, "/app/admin/kits/app/draft/discard", url.Values{"etag": {tag}})
	if rec.Code != http.StatusPreconditionFailed || !strings.Contains(rec.Body.String(), "draft app changed since you loaded it (reload)") {
		t.Fatalf("stale discard = %d %s", rec.Code, rec.Body)
	}
	if _, open := store.GetKitDraft("app"); !open {
		t.Fatal("a stale discard dropped the draft")
	}
	rec = post(t, h, "/app/admin/kits/app/draft/discard", url.Values{"etag": {draftETag(t, store)}})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "discarded the draft of app") || !strings.Contains(rec.Body.String(), `id="draft-none"`) {
		t.Fatalf("discard = %d %s", rec.Code, rec.Body)
	}
	if _, open := store.GetKitDraft("app"); open {
		t.Fatal("discard left the draft open")
	}
}

// failDraftPut is a store whose PutKitDraft fails: a store fault, not a
// refusal.
type failDraftPut struct{ jam.Store }

func (failDraftPut) PutKitDraft(jam.KitDraft, jam.KitObjects) error {
	return errors.New("disk on fire")
}

// Open and discard leave an audit line naming the operator, the kit and a
// tree prefix; a store fault answers a fixed 500 and logs it.
func TestDraftAuditAndStoreFault(t *testing.T) {
	store := newStore(t)
	if _, _, err := jam.PushKitFiles(store, "app", kittreetest.Files("kit.yml", "kind: studio\n")); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	h := adminui.Handler(store, slog.New(slog.NewTextHandler(&logs, nil)), nil, nil, anyCred, nil)
	if rec := post(t, h, "/app/admin/kits/app/draft", url.Values{"from": {"1"}}); rec.Code != http.StatusOK {
		t.Fatalf("open = %d %s", rec.Code, rec.Body)
	}
	d, _ := store.GetKitDraft("app")
	if rec := post(t, h, "/app/admin/kits/app/draft/discard", url.Values{"etag": {jam.DraftETag(d.Tree)}}); rec.Code != http.StatusOK {
		t.Fatalf("discard = %d %s", rec.Code, rec.Body)
	}
	for _, want := range []string{
		`msg="ui kit draft opened" operator="" kit=app base=1 tree=` + d.Tree[:12] + "\n",
		`msg="ui kit draft discarded" operator="" kit=app tree=` + d.Tree[:12] + "\n",
	} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("logs missing %q:\n%s", want, logs.String())
		}
	}

	logs.Reset()
	h = adminui.Handler(failDraftPut{store}, slog.New(slog.NewTextHandler(&logs, nil)), nil, nil, anyCred, nil)
	rec := post(t, h, "/app/admin/kits/app/draft", url.Values{})
	if rec.Code != http.StatusInternalServerError || strings.TrimSpace(rec.Body.String()) != `<p class="error">internal error</p>` {
		t.Fatalf("store fault = %d %s", rec.Code, rec.Body)
	}
	if !strings.Contains(logs.String(), `msg="ui kit draft: store error"`) || !strings.Contains(logs.String(), "disk on fire") {
		t.Errorf("store fault not logged:\n%s", logs.String())
	}
	if strings.Contains(rec.Body.String(), "disk on fire") {
		t.Error("the 500 echoes the store error")
	}
}
