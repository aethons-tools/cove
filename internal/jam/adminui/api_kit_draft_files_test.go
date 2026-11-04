package adminui_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminui"
	"github.com/aethons-tools/cove/internal/jam/appui"
	"github.com/aethons-tools/cove/internal/kittree/kittreetest"
)

const draftFilesAPI = "/app/admin/api/kits/app/draft/files"

func sha(s string) string {
	b := sha256.Sum256([]byte(s))
	return hex.EncodeToString(b[:])
}

// rawDo sends a raw-body request with headers as the extension does: an
// admin app token (no origin check).
func rawDo(t *testing.T, h http.Handler, method, path, body string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var rd io.Reader
	if body != "" || method == "PUT" {
		rd = bytes.NewReader([]byte(body))
	}
	r := httptest.NewRequest(method, path, rd)
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, appui.WithPrincipal(r, appui.Principal{Admin: true, Bearer: true, AdminAPI: true, Operator: jam.Operator{ID: "op-1"}}))
	return rec
}

func draftFiles(t *testing.T, h http.Handler) adminui.DraftFiles {
	t.Helper()
	var out adminui.DraftFiles
	rec := rawDo(t, h, "GET", draftFilesAPI, "", nil)
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
		t.Fatalf("list = %d %s", rec.Code, rec.Body)
	}
	return out
}

func fileOf(fs adminui.DraftFiles, p string) (adminui.DraftFile, bool) {
	for _, f := range fs.Files {
		if f.Path == p {
			return f, true
		}
	}
	return adminui.DraftFile{}, false
}

func TestDraftFilesListAndRead(t *testing.T) {
	_, h := draftFixture(t)
	fs := draftFiles(t, h)
	if fs.ETag == "" || fs.Base != 1 {
		t.Fatalf("listing %+v", fs)
	}
	ky, ok := fileOf(fs, "kit.yml")
	if !ok || ky.SHA256 != sha("kind: studio\n") || ky.Exec {
		t.Fatalf("kit.yml %+v", ky)
	}
	if run, _ := fileOf(fs, "run.sh"); !run.Exec {
		t.Fatalf("run.sh %+v", run)
	}
	rec := rawDo(t, h, "GET", draftFilesAPI+"/docs/a.md", "", nil)
	if rec.Code != 200 || rec.Body.String() != "# a\n" || rec.Header().Get("ETag") != `"`+sha("# a\n")+`"` {
		t.Fatalf("read = %d %v %q", rec.Code, rec.Header(), rec.Body)
	}
	wantErr(t, rawDo(t, h, "GET", draftFilesAPI+"/nope", "", nil), 404, "not_found")
}

// A save is conditional on the file it writes, not on the whole draft.
func TestDraftFilePutConditionalOnFile(t *testing.T) {
	store, h := draftFixture(t)
	putDraft(t, store, "run.sh", 0, "echo changed\n") // another tab
	rec := rawDo(t, h, "PUT", draftFilesAPI+"/kit.yml", "kind: studio\negress: [example.com]\n", map[string]string{"If-Match": `"` + sha("kind: studio\n") + `"`})
	if rec.Code != 200 {
		t.Fatalf("put = %d %s", rec.Code, rec.Body)
	}
	var fs adminui.DraftFiles
	_ = json.Unmarshal(rec.Body.Bytes(), &fs)
	if f, _ := fileOf(fs, "kit.yml"); f.SHA256 != sha("kind: studio\negress: [example.com]\n") {
		t.Fatalf("answer %+v", fs)
	}
	if got := rawDo(t, h, "GET", draftFilesAPI+"/kit.yml", "", nil).Body.String(); got != "kind: studio\negress: [example.com]\n" {
		t.Fatalf("read back %q", got)
	}
	wantErr(t, rawDo(t, h, "PUT", draftFilesAPI+"/run.sh", "x\n", map[string]string{"If-Match": `"` + sha("echo\n") + `"`}), 412, "changed")
	if got := rawDo(t, h, "GET", draftFilesAPI+"/run.sh", "", nil).Body.String(); got != "echo changed\n" {
		t.Fatalf("overwritten: %q", got)
	}
	wantErr(t, rawDo(t, h, "PUT", draftFilesAPI+"/run.sh", "x\n", nil), 428, "precondition_required")
}

func TestDraftFileCreateDeleteRename(t *testing.T) {
	_, h := draftFixture(t)
	wantErr(t, rawDo(t, h, "PUT", draftFilesAPI+"/gone.txt", "x", map[string]string{"If-None-Match": "*"}), 412, "changed")
	if rec := rawDo(t, h, "PUT", draftFilesAPI+"/bin/new.sh", "echo\n", map[string]string{"If-None-Match": "*"}); rec.Code != 200 {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	if f, ok := fileOf(draftFiles(t, h), "bin/new.sh"); !ok || f.Exec {
		t.Fatalf("new file %+v", f)
	}
	if rec := rawDo(t, h, "PUT", draftFilesAPI+"/bin/new.sh?exec=1", "echo\n", map[string]string{"If-Match": `"` + sha("echo\n") + `"`}); rec.Code != 200 {
		t.Fatalf("exec = %d %s", rec.Code, rec.Body)
	}
	if f, _ := fileOf(draftFiles(t, h), "bin/new.sh"); !f.Exec {
		t.Fatalf("not exec: %+v", f)
	}
	wantErr(t, rawDo(t, h, "DELETE", draftFilesAPI+"/gone.txt", "", map[string]string{"If-Match": `"` + sha("nope") + `"`}), 412, "changed")
	if rec := rawDo(t, h, "DELETE", draftFilesAPI+"/gone.txt", "", map[string]string{"If-Match": `"` + sha("bye\n") + `"`}); rec.Code != 200 {
		t.Fatalf("delete = %d %s", rec.Code, rec.Body)
	}
	if _, ok := fileOf(draftFiles(t, h), "gone.txt"); ok {
		t.Fatal("not deleted")
	}
	rename := func(from, to, s string) *httptest.ResponseRecorder {
		b, _ := json.Marshal(map[string]string{"from": from, "to": to, "sha256": s})
		return rawDo(t, h, "POST", "/app/admin/api/kits/app/draft/files-rename", string(b), map[string]string{"Content-Type": "application/json"})
	}
	wantErr(t, rename("docs/a.md", "kit.yml", sha("# a\n")), 409, "conflict")
	wantErr(t, rename("docs/a.md", "docs/b.md", sha("old")), 412, "changed")
	if rec := rename("docs/a.md", "docs/b.md", sha("# a\n")); rec.Code != 200 {
		t.Fatalf("rename = %d %s", rec.Code, rec.Body)
	}
	if _, ok := fileOf(draftFiles(t, h), "docs/b.md"); !ok {
		t.Fatal("not renamed")
	}
}

// With no draft, the file API says so in a way a client can act on.
func TestDraftFilesNoDraft(t *testing.T) {
	store, h := draftFixture(t)
	if _, err := jam.DiscardKitDraft(store, nil, "app", draftETag(t, store)); err != nil {
		t.Fatal(err)
	}
	wantErr(t, rawDo(t, h, "GET", draftFilesAPI, "", nil), 404, "no_draft")
	wantErr(t, rawDo(t, h, "PUT", draftFilesAPI+"/kit.yml", "x", map[string]string{"If-Match": `"` + sha("kind: studio\n") + `"`}), 404, "no_draft")
}

// A kit from its own git source is flagged in the list.
func TestKitRowTracked(t *testing.T) {
	store := newStore(t)
	if _, _, err := jam.PushKitFiles(store, "app", kittreetest.Files("kit.yml", "kind: studio\n")); err != nil {
		t.Fatal(err)
	}
	var out adminui.KitsResponse
	apiDo(t, uiHandler(t, store), "GET", "/app/admin/api/kits", nil, &out)
	for _, k := range out.Kits {
		if k.Name == "app" && k.Tracked {
			t.Fatal("a pushed kit is not tracked")
		}
	}
}
