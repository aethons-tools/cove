package jam

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/kittree/kittreetest"
)

func TestPublishKitDraft(t *testing.T) {
	st := NewMemStore()
	pushApp(t, st, "kit.yml", "kind: studio\n")
	tag := openApp(t, st)
	s, err := PutKitDraftFile(st, nil, "app", "run.sh", 0o755, []byte("echo\n"), tag, "local", draftNow)
	if err != nil {
		t.Fatal(err)
	}
	res, err := PublishKitDraft(st, nil, "app", false, s.ETag)
	if err != nil || res.Kit != "app" || res.Version != 2 || res.Base != 1 || res.Drift || res.Warning != "" || len(res.Pinned) != 0 {
		t.Fatalf("publish = %+v %v", res, err)
	}
	k, _ := st.GetKit("app")
	if k.Current != 2 || k.Trees[2] != s.Tree || k.Meta[2].Origin != KitOriginDraft || k.Meta[1].Origin != "" {
		t.Fatalf("kit = %+v", k)
	}
	if _, ok := st.GetKitDraft("app"); ok {
		t.Fatal("publish left the draft open")
	}
	if v, _ := LoadKitVersion(st, "app", 2); v.Err != nil {
		t.Fatalf("v2 = %v", v.Err)
	}
}

func TestPublishRefusals(t *testing.T) {
	st := NewMemStore()
	pushApp(t, st, "kit.yml", "kind: studio\n")
	tag := openApp(t, st)
	_, err := PublishKitDraft(st, nil, "app", false, tag)
	wantErr(t, err, "draft app has no changes", 409)
	if _, ok := st.GetKitDraft("app"); !ok {
		t.Fatal("a refused publish closed the draft")
	}
	s, err := PutKitDraftFile(st, nil, "app", "kit.yml", 0, []byte("kind: nope\n"), tag, "local", draftNow)
	if err != nil {
		t.Fatal(err)
	}
	_, err = PublishKitDraft(st, nil, "app", false, s.ETag)
	wantErr(t, err, `draft app has problems: studio kit: kind must be "studio", got "nope"`, 400)
	_, err = PublishKitDraft(st, nil, "app", false, tag) // Review Focus 1: stale
	wantErr(t, err, "draft app changed since you loaded it (reload)", 412)
	_, err = PublishKitDraft(st, nil, "app", false, "")
	wantErr(t, err, "draft app: If-Match is required (send the etag from the draft status)", 428)
	if k, _ := st.GetKit("app"); k.Current != 1 || len(k.VersionNumbers()) != 1 {
		t.Fatal("a refused publish made a version")
	}
}

// TestPublishAfterBaseSuperseded: a push lands while the draft is open; the
// draft publishes as it is, nothing merged (Review Focus 2).
func TestPublishAfterBaseSuperseded(t *testing.T) {
	st := NewMemStore()
	pushApp(t, st, "kit.yml", "kind: studio\n")
	tag := openApp(t, st)
	s, err := PutKitDraftFile(st, nil, "app", "draft.txt", 0, []byte("d\n"), tag, "local", draftNow)
	if err != nil {
		t.Fatal(err)
	}
	pushApp(t, st, "kit.yml", "kind: studio\n", "pushed.txt", "p\n") // v2
	status, err := KitDraftStatusOf(st, "app")
	if err != nil || status.Base != 1 || status.Current != 2 || !slices.Equal(status.Changes, []DraftChange{{"draft.txt", "added"}}) {
		t.Fatalf("status = %+v %v (changes are vs the base)", status, err)
	}
	res, err := PublishKitDraft(st, nil, "app", false, s.ETag)
	if err != nil || res.Version != 3 || res.Base != 1 {
		t.Fatalf("publish = %+v %v", res, err)
	}
	v3, _ := LoadKitVersion(st, "app", 3)
	if _, ok := v3.Tree.Lookup("draft.txt"); !ok {
		t.Fatal("v3 lacks the draft's file")
	}
	if _, ok := v3.Tree.Lookup("pushed.txt"); ok {
		t.Fatal("publish merged v2's file")
	}
	if v2, _ := LoadKitVersion(st, "app", 2); v2.Err != nil {
		t.Fatal("v2 must stay in history")
	} else if _, ok := v2.Tree.Lookup("pushed.txt"); !ok {
		t.Fatal("v2 changed")
	}
}

func TestPublishPinsFollowers(t *testing.T) {
	st := NewMemStore()
	pushApp(t, st, "kit.yml", "kind: studio\n")
	for name, yml := range map[string]string{"web": "kind: studio\nbase:\n  kit: app # follows\n", "ops": "kind: studio\nbase:\n  kit: app@v1\n"} {
		if _, _, err := PushKitFiles(st, name, kittreetest.Files("kit.yml", yml)); err != nil {
			t.Fatal(err)
		}
	}
	tag := openApp(t, st)
	s, err := PutKitDraftFile(st, nil, "app", "kit.yml", 0, []byte("kind: studio\nprompt: new\n"), tag, "local", draftNow)
	if err != nil {
		t.Fatal(err)
	}
	res, err := PublishKitDraft(st, nil, "app", true, s.ETag)
	if err != nil || res.Version != 2 || !slices.Equal(res.Pinned, []string{"web v2 (app@v1)"}) {
		t.Fatalf("publish --pin = %+v %v", res, err)
	}
	web, _ := LoadKitVersion(st, "web", 0)
	if web.Version != 2 || string(web.KitYML) != "kind: studio\nbase:\n  kit: app@v1 # follows\n" {
		t.Fatalf("web = v%d %q (base.kit rewritten in place)", web.Version, web.KitYML)
	}
	if ops, _ := st.GetKit("ops"); ops.Current != 1 {
		t.Fatal("an already pinned kit was rewritten")
	}
	follow, pinned := KitsReferencing(st, "app")
	if len(follow) != 0 || !slices.Equal(pinned, []string{"ops", "web"}) {
		t.Fatalf("follow %v pinned %v", follow, pinned)
	}
	// Without --pin a follower keeps following (and goes stale as usual).
	if _, _, err := PushKitFiles(st, "dev", kittreetest.Files("kit.yml", "kind: studio\nbase:\n  kit: app\n")); err != nil {
		t.Fatal(err)
	}
	tag = openApp(t, st)
	s, _ = PutKitDraftFile(st, nil, "app", "kit.yml", 0, []byte("kind: studio\nprompt: newer\n"), tag, "local", draftNow)
	if res, err := PublishKitDraft(st, nil, "app", false, s.ETag); err != nil || len(res.Pinned) != 0 {
		t.Fatalf("publish = %+v %v", res, err)
	}
	if follow, _ := KitsReferencing(st, "app"); !slices.Equal(follow, []string{"dev"}) {
		t.Fatalf("follow = %v", follow)
	}
}

func TestPublishManagedKitDrifts(t *testing.T) {
	st := NewMemStore()
	pushApp(t, st, "kit.yml", "kind: studio\n")
	if err := st.ApplyConfig(ConfigWrite{Marks: []ConfigMark{{Kind: ConfigKindKit, Name: "app", Commit: "abc1234", Digest: "d", AppliedAt: time.Unix(1, 0).UTC()}}}); err != nil {
		t.Fatal(err)
	}
	tag := openApp(t, st)
	s, _ := PutKitDraftFile(st, nil, "app", "notes.md", 0, []byte("n\n"), tag, "local", draftNow)
	res, err := PublishKitDraft(st, nil, "app", false, s.ETag)
	if err != nil || res.Version != 2 || !res.Drift || res.Warning != "this will drift from the config repo; config write it to keep the repo in sync" {
		t.Fatalf("publish = %+v %v", res, err)
	}
}

func TestPublishRoute(t *testing.T) {
	var logs bytes.Buffer
	st := NewMemStore()
	h := NewAdminHandler(st, nil, nil, LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(&logs, nil)), nil)
	pushApp(t, st, "kit.yml", "kind: studio\n")
	open := draftDo(t, h, "POST", "/admin/kits/app/draft", "application/json", "", nil)
	put := draftDo(t, h, "PUT", "/admin/kits/app/draft/files/run.sh", "application/octet-stream", open.Header().Get("ETag"), []byte("echo SECRET\n"))
	tag := put.Header().Get("ETag")
	if w := draftDo(t, h, "POST", "/admin/kits/app/draft/publish", "text/plain", tag, []byte(`{}`)); w.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("text/plain = %d", w.Code)
	}
	if w := draftDo(t, h, "POST", "/admin/kits/app/draft/publish", "application/json", "", []byte(`{}`)); w.Code != http.StatusPreconditionRequired {
		t.Fatalf("no If-Match = %d", w.Code)
	}
	w := draftDo(t, h, "POST", "/admin/kits/app/draft/publish", "application/json", tag, []byte(`{"pin":false}`))
	var res KitDraftPublish
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &res) != nil || res.Version != 2 {
		t.Fatalf("publish = %d %s", w.Code, w.Body)
	}
	var hist []KitVersionInfo
	getJSON(t, h, "/admin/kits/app/history", &hist)
	if len(hist) != 2 || hist[0].Origin != "" || hist[1].Origin != "draft" {
		t.Fatalf("history = %+v", hist)
	}
	out := logs.String()
	if !strings.Contains(out, "kit draft published") || !strings.Contains(out, "version=2") || strings.Contains(out, "SECRET") {
		t.Fatalf("audit log:\n%s", out)
	}
}

// failKitWrites is a store whose kit version writes naming kit fail.
type failKitWrites struct {
	Store
	kit string
}

func (f failKitWrites) PushKitVersion(name, tree string, objs KitObjects, meta KitVersionMeta, track *KitSource) (int, error) {
	if name == f.kit {
		return 0, errors.New("store down")
	}
	return f.Store.PushKitVersion(name, tree, objs, meta, track)
}

func (f failKitWrites) PutKitDocs(kits []Kit, objs KitObjects) error {
	if slices.ContainsFunc(kits, func(k Kit) bool { return k.Name == f.kit }) {
		return errors.New("store down")
	}
	return f.Store.PutKitDocs(kits, objs)
}

// pinnedFixture is kit app with a draft that changes kit.yml, and web
// following app; it answers the draft's ETag.
func pinnedFixture(t *testing.T, st Store, followers map[string]string) string {
	t.Helper()
	pushApp(t, st, "kit.yml", "kind: studio\n")
	for name, yml := range followers {
		if _, _, err := PushKitFiles(st, name, kittreetest.Files("kit.yml", yml)); err != nil {
			t.Fatal(err)
		}
	}
	tag := openApp(t, st)
	s, err := PutKitDraftFile(st, nil, "app", "kit.yml", 0, []byte("kind: studio\nprompt: new\n"), tag, "local", draftNow)
	if err != nil {
		t.Fatal(err)
	}
	return s.ETag
}

// wantNothingPublished checks a failed publish --pin left app at v1 with its
// draft open and every follower unpinned at v1.
func wantNothingPublished(t *testing.T, st Store, followers ...string) {
	t.Helper()
	if k, _ := st.GetKit("app"); k.Current != 1 || len(k.VersionNumbers()) != 1 {
		t.Fatalf("app = %+v: a failed publish made a version", k)
	}
	if _, ok := st.GetKitDraft("app"); !ok {
		t.Fatal("a failed publish closed the draft")
	}
	for _, f := range followers {
		if k, _ := st.GetKit(f); k.Current != 1 || len(k.VersionNumbers()) != 1 {
			t.Fatalf("%s = %+v: a failed publish pinned it", f, k)
		}
	}
}

// TestPublishPinIsAtomic: the follower pins and the draft's version are one
// write, so a failing write leaves nothing pinned and nothing published.
func TestPublishPinIsAtomic(t *testing.T) {
	mem := NewMemStore()
	st := failKitWrites{Store: mem, kit: "app"}
	tag := pinnedFixture(t, mem, map[string]string{"web": "kind: studio\nbase:\n  kit: app\n"})
	if _, err := PublishKitDraft(st, nil, "app", true, tag); err == nil {
		t.Fatal("publish --pin succeeded through a failing store")
	}
	wantNothingPublished(t, mem, "web")
	if follow, _ := KitsReferencing(mem, "app"); !slices.Equal(follow, []string{"web"}) {
		t.Fatalf("follow = %v", follow)
	}
}

// TestPublishPinRefusesUnrewritable: a follower whose base.kit cannot be
// rewritten in place refuses the publish before anything is written.
func TestPublishPinRefusesUnrewritable(t *testing.T) {
	st := NewMemStore()
	tag := pinnedFixture(t, st, map[string]string{
		"web": "kind: studio\nbase:\n  kit: app\n",
		"zed": "kind: studio\nbase:\n  kit: >-\n    app\n",
	})
	_, err := PublishKitDraft(st, nil, "app", true, tag)
	wantErr(t, err, "kit zed: cannot pin its base.kit to app@v1 (kit.yml: write base.kit as a one-line plain or quoted value, without escapes)", 409)
	wantNothingPublished(t, st, "web", "zed")
}

// TestPublishPinManagedFollowerDrifts: pinning a config-managed follower
// drifts it from the config repo, so the answer warns.
func TestPublishPinManagedFollowerDrifts(t *testing.T) {
	st := NewMemStore()
	tag := pinnedFixture(t, st, map[string]string{"web": "kind: studio\nbase:\n  kit: app\n"})
	if err := st.ApplyConfig(ConfigWrite{Marks: []ConfigMark{{Kind: ConfigKindKit, Name: "web", Commit: "abc1234", Digest: "d", AppliedAt: time.Unix(1, 0).UTC()}}}); err != nil {
		t.Fatal(err)
	}
	res, err := PublishKitDraft(st, nil, "app", true, tag)
	if err != nil || !res.Drift || res.Warning != DraftDriftWarning || !slices.Equal(res.Pinned, []string{"web v2 (app@v1)"}) {
		t.Fatalf("publish --pin = %+v %v", res, err)
	}
	if web, _ := st.GetKit("web"); web.Meta[2].Origin != "" {
		t.Fatalf("a pinned follower's version has origin %q", web.Meta[2].Origin)
	}
}
