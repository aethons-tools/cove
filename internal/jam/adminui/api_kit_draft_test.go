package adminui_test

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminui"
	"github.com/aethons-tools/cove/internal/jam/appui"
	"github.com/aethons-tools/cove/internal/kittree"
)

// apiForm posts a multipart form (fields, plus file as "file" when non-nil)
// to h as a Local admin, same-origin.
func apiForm(t *testing.T, h http.Handler, path string, fields map[string]string, file []byte, out any) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for k, v := range fields {
		_ = mw.WriteField(k, v)
	}
	if file != nil {
		fw, _ := mw.CreateFormFile("file", "upload.bin")
		_, _ = fw.Write(file)
	}
	_ = mw.Close()
	r := httptest.NewRequest("POST", path, &body)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	r.Header.Set("Origin", "http://example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, appui.WithPrincipal(r, appui.Principal{Admin: true, Local: true}))
	if out != nil && rec.Code < 300 {
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			t.Fatalf("%s: %v in %s", path, err, rec.Body)
		}
	}
	return rec
}

const draftAPI = "/app/admin/api/kits/app/draft"

func TestDraftAPI(t *testing.T) {
	store, h := draftFixture(t)
	if _, err := jam.DiscardKitDraft(store, nil, "app", draftETag(t, store)); err != nil {
		t.Fatal(err)
	}
	var res adminui.DraftResult
	apiDo(t, h, "POST", draftAPI, map[string]int{"from": 1}, &res)
	if res.Message != "opened a draft of app from v1" || res.Page.Draft == nil {
		t.Fatalf("open = %+v", res)
	}
	var pg adminui.DraftPage
	apiDo(t, h, "GET", draftAPI, nil, &pg)
	paths := []string{}
	for _, r := range pg.Draft.Tree {
		if !r.Dir {
			paths = append(paths, r.Path)
		}
	}
	if strings.Join(paths, ",") != "docs/a.md,gone.txt,kit.yml,run.sh" {
		t.Fatalf("tree = %v", paths)
	}
	etag := pg.Draft.ETag
	apiDo(t, h, "POST", draftAPI+"/save", map[string]any{"etag": etag, "path": "run.sh", "content": "echo hi\n", "executable": true}, &res)
	if res.Message != "saved run.sh" || res.Page.Draft.Editor == nil || res.Page.Draft.Editor.Text != "echo hi\n" {
		t.Fatalf("save = %+v", res.Message)
	}
	rec := apiDo(t, h, "POST", draftAPI+"/save", map[string]any{"etag": etag, "path": "run.sh", "content": "echo stale\n"}, nil)
	wantErr(t, rec, 412, "stale")
	if !strings.Contains(rec.Body.String(), "your text is still in the editor") {
		t.Errorf("stale = %s", rec.Body)
	}
	etag = res.Page.Draft.ETag
	apiDo(t, h, "POST", draftAPI+"/rename", map[string]any{"etag": etag, "from": "run.sh", "to": "run2.sh"}, &res)
	if res.Message != "renamed run.sh to run2.sh" {
		t.Errorf("rename = %q", res.Message)
	}
	apiDo(t, h, "POST", draftAPI+"/mode", map[string]any{"etag": res.Page.Draft.ETag, "path": "run2.sh", "executable": false}, &res)
	if res.Message != "run2.sh is now 0644" {
		t.Errorf("mode = %q", res.Message)
	}
	apiDo(t, h, "POST", draftAPI+"/delete", map[string]any{"etag": res.Page.Draft.ETag, "path": "gone.txt"}, &res)
	if res.Message != "removed gone.txt" || len(res.Page.Draft.Removed) == 0 {
		t.Errorf("delete = %q %+v", res.Message, res.Page.Draft.Removed)
	}
	if rec := apiForm(t, h, draftAPI+"/add", map[string]string{"etag": res.Page.Draft.ETag, "path": "notes/new.md"}, nil, &res); rec.Code != 200 || res.Message != "added notes/new.md" {
		t.Fatalf("add = %d %s", rec.Code, rec.Body)
	}
	if rec := apiForm(t, h, draftAPI+"/replace", map[string]string{"etag": res.Page.Draft.ETag, "path": "notes/new.md"}, []byte("# new\n"), &res); rec.Code != 200 || res.Message != "saved notes/new.md" {
		t.Fatalf("replace = %d %s", rec.Code, rec.Body)
	}
	dl := httptest.NewRecorder()
	h.ServeHTTP(dl, appui.WithPrincipal(httptest.NewRequest("GET", draftAPI+"/download?path=notes/new.md", nil), appui.Principal{Admin: true, Local: true}))
	if dl.Body.String() != "# new\n" || !strings.HasPrefix(dl.Header().Get("Content-Disposition"), "attachment") {
		t.Errorf("download = %q %v", dl.Body, dl.Header())
	}
	apiDo(t, h, "POST", draftAPI+"/publish", map[string]any{"etag": res.Page.Draft.ETag}, &res)
	if res.Page.Published == nil || res.Page.Published.Version != 2 || res.Page.Draft != nil || res.Message != "published app v2" {
		t.Fatalf("publish = %+v", res)
	}
	apiDo(t, h, "GET", draftAPI+"?file=nope", nil, &pg)
	if pg.Draft != nil {
		t.Errorf("draft after publish = %+v", pg.Draft)
	}
	wantErr(t, apiDo(t, h, "GET", "/app/admin/api/kits/gone/draft", nil, nil), 404, "not_found")
}

func TestDraftAPIPackedContext(t *testing.T) {
	store := newStore(t)
	b64 := packCtx(t, "Dockerfile", "FROM x\n")
	if _, err := store.PushKit("app", "kind: studio\nbogus: 1\nbase:\n  context: "+b64+"\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := jam.OpenKitDraft(store, "app", 0, "local", time.Unix(100, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := jam.PutKitDraftFile(store, nil, "app", "kit.yml", 0o644, []byte("kind: studio\nprompt: x\n"), draftETag(t, store), "local", time.Unix(200, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	h := uiHandler(t, store)
	for _, q := range []string{"", "?file=kit.yml", "?file=kit.yml&diff=1"} {
		rec := apiDo(t, h, "GET", draftAPI+q, nil, nil)
		if rec.Code != 200 || strings.Contains(rec.Body.String(), "H4sI") {
			t.Errorf("%s = %d, carries the packed context: %v", q, rec.Code, strings.Contains(rec.Body.String(), "H4sI"))
		}
	}
}

func TestDraftAPILimits(t *testing.T) {
	store, h := draftFixture(t)
	big := strings.Repeat("x", int(kittree.MaxFile)*3+1<<17)
	rec := apiDo(t, h, "POST", draftAPI+"/save", map[string]any{"etag": draftETag(t, store), "path": "run.sh", "content": big}, nil)
	wantErr(t, rec, 413, "too_large")
	rec = apiForm(t, h, draftAPI+"/add", map[string]string{"etag": draftETag(t, store), "path": "big.bin"}, bytes.Repeat([]byte("x"), int(kittree.MaxFile)+2<<20), nil)
	wantErr(t, rec, 413, "too_large")
}

func TestDraftAPIPublishManaged(t *testing.T) {
	store, h := draftFixture(t)
	markManaged(t, store, "app")
	putDraft(t, store, "docs/new.md", 0, "n\n")
	rec := apiDo(t, h, "POST", draftAPI+"/publish", map[string]any{"etag": draftETag(t, store)}, nil)
	wantErr(t, rec, 400, "bad_request")
	if !strings.Contains(rec.Body.String(), "kit app is managed by the config repo") {
		t.Errorf("unconfirmed = %s", rec.Body)
	}
	var res adminui.DraftResult
	apiDo(t, h, "POST", draftAPI+"/publish", map[string]any{"etag": draftETag(t, store), "drift": true}, &res)
	if res.Page.Published == nil || !res.Page.Published.Drift || len(res.Page.Published.ConfigFiles) == 0 {
		t.Errorf("published = %+v", res.Page.Published)
	}
}
