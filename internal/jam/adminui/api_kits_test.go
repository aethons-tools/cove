package adminui_test

import (
	"net/url"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminui"
	"github.com/aethons-tools/cove/internal/kittree/kittreetest"
)

func TestKitsAPI(t *testing.T) {
	e := editOn(t, editSetup{})
	h := e.H
	var c adminui.Created
	if rec := apiDo(t, h, "POST", "/app/admin/api/kits", map[string]string{"name": "k", "config": "kind: studio\nprompt: one\n"}, &c); rec.Code != 201 || c.Path != "config/kits/k" {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	rec := apiDo(t, h, "POST", "/app/admin/api/kits", map[string]string{"name": "k", "config": "kind: studio\n"}, nil)
	wantErr(t, rec, 409, "conflict")
	if !strings.Contains(rec.Body.String(), "push a new version on its page") {
		t.Errorf("409 = %s", rec.Body)
	}
	wantErr(t, apiDo(t, h, "POST", "/app/admin/api/kits", map[string]string{"name": "x"}, nil), 400, "bad_request")
	var res adminui.KitResult
	apiDo(t, h, "POST", "/app/admin/api/kits/k/versions", map[string]string{"config": "kind: studio\nprompt: two\n"}, &res)
	if res.Message != "pushed k v2" || res.Kit.Current != 2 {
		t.Fatalf("push = %+v", res.Message)
	}
	apiDo(t, h, "POST", "/app/admin/api/kits/k/versions", map[string]string{"config": "kind: studio\nprompt: two\n"}, &res)
	if res.Message != "k unchanged (current v2)" {
		t.Errorf("unchanged push = %q", res.Message)
	}
	apiDo(t, h, "POST", "/app/admin/api/kits/k/pin", map[string]int{"version": 1}, &res)
	if res.Message != "k current is now v1" || res.Kit.Current != 1 {
		t.Errorf("pin = %q %d", res.Message, res.Kit.Current)
	}
	e.Edit(func(st jam.Store) {
		if _, _, err := jam.PushKitFiles(st, "k", kittreetest.Files("kit.yml", "kind: studio\nprompt: three\n", "run.sh", "echo hi\n", "a b%.txt", "spaced\n")); err != nil {
			t.Fatal(err)
		}
	})
	var d adminui.KitDetail
	apiDo(t, h, "GET", "/app/admin/api/kits/k?v=3&diff=2", nil, &d)
	if d.Diff == nil || d.FilesDiff == nil || len(d.FilesDiff.Rows) == 0 || d.View.N != 3 {
		t.Fatalf("diff = %+v / %+v", d.Diff, d.FilesDiff)
	}
	apiDo(t, h, "GET", "/app/admin/api/kits/k?v=3&file=run.sh", nil, &d)
	if d.File == nil || d.File.Content != "echo hi\n" {
		t.Errorf("file = %+v", d.File)
	}
	apiDo(t, h, "GET", "/app/admin/api/kits/k?v=3&file="+url.QueryEscape("a b%.txt"), nil, &d)
	if d.File == nil || d.File.Content != "spaced\n" {
		t.Errorf("spaced file = %+v", d.File)
	}
	found := false
	for _, r := range d.Files.Rows {
		found = found || r.Path == "a b%.txt"
	}
	if !found {
		t.Errorf("raw path missing in %+v", d.Files.Rows)
	}
	apiDo(t, h, "GET", "/app/admin/api/kits/k?v=3&file=nope", nil, &d)
	if d.File == nil || !d.File.Missing {
		t.Errorf("missing file = %+v", d.File)
	}
	var list adminui.KitsResponse
	apiDo(t, h, "GET", "/app/admin/api/kits", nil, &list)
	if list.CanSource || len(list.Kits) == 0 {
		t.Errorf("list = %+v", list)
	}
	if rec := apiDo(t, h, "DELETE", "/app/admin/api/kits/k", nil, nil); rec.Code != 204 {
		t.Errorf("delete = %d %s", rec.Code, rec.Body)
	}
	wantErr(t, apiDo(t, h, "GET", "/app/admin/api/kits/k", nil, nil), 404, "not_found")
	wantErr(t, apiDo(t, h, "POST", "/app/admin/api/kits/k/sync", map[string]string{}, nil), 404, "not_found")
}

func TestKitsAPIInvalid(t *testing.T) {
	h, store := adminHandler(t)
	b64 := packCtx(t, "Dockerfile", "FROM x\n")
	if _, err := store.PushKit("bad", "kind: studio\nbogus: 1\nbase:\n  context: "+b64+"\n"); err != nil {
		t.Fatal(err)
	}
	var list adminui.KitsResponse
	apiDo(t, h, "GET", "/app/admin/api/kits", nil, &list)
	if len(list.Kits) != 1 || !list.Kits[0].Invalid {
		t.Fatalf("list = %+v", list.Kits)
	}
	rec := apiDo(t, h, "GET", "/app/admin/api/kits/bad", nil, nil)
	if strings.Contains(rec.Body.String(), "H4sI") {
		t.Errorf("the packed context leaked: %s", rec.Body)
	}
	var d adminui.KitDetail
	apiDo(t, h, "GET", "/app/admin/api/kits/bad", nil, &d)
	if d.View.ParseErr == "" || d.View.RawShown == "" || !strings.Contains(d.View.RawShown, "«packed context, not shown»") || strings.Contains(d.View.PushText, "H4sI") {
		t.Errorf("view = %+v", d.View)
	}
}

func TestKitsAPISources(t *testing.T) {
	f := &srcFetcher{t: t, refs: map[string]string{"main": srcSHA1}, kits: map[string]string{srcSHA1: srcKitV1, srcSHA2: srcKitV2}}
	h, _ := sourceUI(t, f)
	var c adminui.Created
	if rec := apiDo(t, h, "POST", "/app/admin/api/kits/import", map[string]any{"source": "https://github.com/acme/kits@main", "name": "", "track": true}, &c); rec.Code != 201 ||
		c.Path != "config/kits/web-dev" || c.Message == "" {
		t.Fatalf("import = %d %+v %s", rec.Code, c, rec.Body)
	}
	var d adminui.KitDetail
	apiDo(t, h, "GET", "/app/admin/api/kits/web-dev", nil, &d)
	if d.Source == nil || !d.CanSource || d.Source.Ref != "main" {
		t.Fatalf("source = %+v", d.Source)
	}
	f.refs["main"] = srcSHA2
	var res adminui.KitResult
	apiDo(t, h, "POST", "/app/admin/api/kits/web-dev/sync", map[string]string{}, &res)
	if !strings.HasPrefix(res.Message, "synced: web-dev is now v2") {
		t.Errorf("sync = %q", res.Message)
	}
	apiDo(t, h, "DELETE", "/app/admin/api/kits/web-dev/source", nil, &res)
	if res.Kit.Source != nil || res.Message != "web-dev no longer tracks a source" {
		t.Errorf("untrack = %+v %q", res.Kit.Source, res.Message)
	}
	apiDo(t, h, "POST", "/app/admin/api/kits/web-dev/source", map[string]string{"source": "https://github.com/acme/kits@main"}, &res)
	if res.Kit.Source == nil || !strings.HasPrefix(res.Message, "tracking https://github.com/acme/kits@main") {
		t.Errorf("track = %q", res.Message)
	}
}
